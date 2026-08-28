package server

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"airshift/openmos/pkg/logger"
)

// FileDedupStore is a DedupStore that survives a restart.
//
// Why this matters is specific, not general. The protocol's retry rule is that a sender repeats
// a message with the SAME messageID until it gets a response, and the receiver must answer a
// repeat with the response it already sent rather than applying the message twice. In memory
// that works. Across a restart it does not: history is lost, the retry looks new, and the
// message is applied a second time. For a roStorySend that means a duplicated story; for a
// roElementAction it means an operation performed twice.
//
// The design follows the durability already used for messageID in internal/messageid: write
// small, write often, and degrade loudly to memory rather than refusing to start.
//
// Storage is an append-only log rather than a rewritten snapshot, because dedup is written on
// every inbound message and rewriting the whole set each time would make the hot path
// proportional to history. The log is compacted from live state once it grows past a multiple of
// the capacity, so it cannot grow without bound.
//
// What is deliberately NOT done: there is no fsync per record. A hard power loss can therefore
// lose the last few appends, and those few messages would be re-applied on retry. That is the
// same failure this store exists to fix, reduced from "every message since startup" to "the last
// few before the crash", at a cost the message path can afford. Buying the remainder means an
// fsync per message, which is not worth it for a single-process deployment.
type FileDedupStore struct {
	mem *MemoryDedupStore

	mu       sync.Mutex
	file     *os.File
	writer   *bufio.Writer
	path     string
	appends  int
	compactA int
	degraded bool
}

// dedupRecord is one line of the log. Field names are short because there is one per message.
type dedupRecord struct {
	Key      string `json:"k"`
	Hash     string `json:"h"`
	Response string `json:"r,omitempty"` // base64, absent until Remember is called
}

// OpenFileDedupStore replays any existing log from dir and returns a store that appends to it.
//
// An unusable directory is reported and downgraded to memory-only. Refusing to start would turn
// a storage problem into an outage, and the protocol tolerates lost dedup history -- it is what
// happens today on every restart.
func OpenFileDedupStore(dir string, capacity int) *FileDedupStore {
	mem := NewMemoryDedupStoreWithCapacity(capacity)
	s := &FileDedupStore{mem: mem, compactA: mem.capacity * 4}

	if dir == "" {
		s.degraded = true
		return s
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.Warningf("Deduplication history will NOT survive a restart: cannot create state "+
			"directory %s: %v. A retry arriving after a restart will be applied a second time.",
			dir, err)
		s.degraded = true
		return s
	}

	s.path = filepath.Join(dir, "dedup.log")
	replayed := s.replay()

	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logger.Warningf("Deduplication history will NOT survive a restart: cannot append to %s: "+
			"%v. A retry arriving after a restart will be applied a second time.", s.path, err)
		s.degraded = true
		return s
	}
	s.file = file
	s.writer = bufio.NewWriter(file)

	if replayed > 0 {
		logger.Infof("Recovered %d deduplication receipts from %s; retries that predate this "+
			"restart will be answered from history rather than re-applied", replayed, s.path)
	}
	return s
}

// replay loads the log into memory. Later records win, which is what makes a Remember following
// a Check produce the right final state.
func (s *FileDedupStore) replay() int {
	f, err := os.Open(s.path)
	if err != nil {
		return 0 // no history yet is the normal first-run case
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	loaded, malformed := 0, 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec dedupRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil || rec.Key == "" {
			malformed++
			continue
		}
		var response []byte
		if rec.Response != "" {
			if decoded, err := base64.StdEncoding.DecodeString(rec.Response); err == nil {
				response = decoded
			} else {
				malformed++
				continue
			}
		}
		s.mem.restore(rec.Key, rec.Hash, response)
		loaded++
	}

	if malformed > 0 {
		// A truncated final line is the expected shape of a crash mid-append, so this is
		// reported rather than treated as corruption.
		logger.Warningf("Skipped %d unreadable deduplication records in %s, most likely a "+
			"partial write from an unclean shutdown", malformed, s.path)
	}
	return loaded
}

// Check delegates to memory and records a new key so it is known after a restart.
func (s *FileDedupStore) Check(scope, ncsID, messageID string, content []byte) DedupResult {
	result := s.mem.Check(scope, ncsID, messageID, content)
	if result == DedupNew {
		key, hash := s.mem.keyAndHash(scope, ncsID, messageID, content)
		s.append(dedupRecord{Key: key, Hash: hash})
	}
	return result
}

// Remember stores the response both in memory and on disk, so a retry after a restart is
// answered with the same bytes rather than re-applied.
func (s *FileDedupStore) Remember(scope, ncsID, messageID string, response []byte) {
	s.mem.Remember(scope, ncsID, messageID, response)
	key := dedupKey(scope, ncsID, messageID)
	hash, _ := s.mem.hashFor(key)
	s.append(dedupRecord{
		Key:      key,
		Hash:     hash,
		Response: base64.StdEncoding.EncodeToString(response),
	})
}

func (s *FileDedupStore) Response(scope, ncsID, messageID string) ([]byte, bool) {
	return s.mem.Response(scope, ncsID, messageID)
}

// Len reports the in-memory entry count, for tests.
func (s *FileDedupStore) Len() int { return s.mem.Len() }

// Degraded reports that history is not being persisted.
func (s *FileDedupStore) Degraded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.degraded
}

// Close flushes buffered records. Called on shutdown; a missed Close costs at most the buffered
// tail, which is the same window as a crash.
func (s *FileDedupStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writer != nil {
		if err := s.writer.Flush(); err != nil {
			return err
		}
	}
	if s.file != nil {
		return s.file.Close()
	}
	return nil
}

func (s *FileDedupStore) append(rec dedupRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.degraded || s.writer == nil {
		return
	}

	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if _, err := s.writer.Write(append(line, '\n')); err != nil {
		logger.Warningf("Failed to append a deduplication receipt to %s: %v. History from this "+
			"point will not survive a restart.", s.path, err)
		s.degraded = true
		return
	}
	// Flush every record. The buffer exists to coalesce the syscall, not to defer it: holding
	// receipts in userspace across a crash is the case this store is meant to remove.
	if err := s.writer.Flush(); err != nil {
		logger.Warningf("Failed to flush deduplication receipts to %s: %v", s.path, err)
		s.degraded = true
		return
	}

	s.appends++
	if s.appends >= s.compactA {
		s.compactLocked()
	}
}

// compactLocked rewrites the log from live in-memory state, so it stays proportional to the
// bounded entry set rather than to total traffic. Caller holds the lock.
func (s *FileDedupStore) compactLocked() {
	entries := s.mem.snapshot()

	tmp := s.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		logger.Warningf("Cannot compact the deduplication log at %s: %v", s.path, err)
		s.appends = 0 // do not retry on every subsequent append
		return
	}

	w := bufio.NewWriter(f)
	for _, e := range entries {
		rec := dedupRecord{Key: e.key, Hash: e.hash}
		if len(e.response) > 0 {
			rec.Response = base64.StdEncoding.EncodeToString(e.response)
		}
		line, err := json.Marshal(rec)
		if err != nil {
			continue
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			break
		}
	}
	flushErr := w.Flush()
	closeErr := f.Close()
	if flushErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		logger.Warningf("Cannot compact the deduplication log at %s: %v",
			s.path, fmt.Errorf("flush %v, close %v", flushErr, closeErr))
		s.appends = 0
		return
	}

	// Close the current handle before replacing the file, so the rename cannot leave the old
	// descriptor writing into an unlinked inode.
	if s.file != nil {
		_ = s.writer.Flush()
		_ = s.file.Close()
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		logger.Warningf("Cannot replace the deduplication log at %s: %v", s.path, err)
		s.degraded = true
		return
	}

	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logger.Warningf("Deduplication history stopped persisting after compaction: %v", err)
		s.degraded = true
		return
	}
	s.file = file
	s.writer = bufio.NewWriter(file)
	s.appends = 0
	logger.Infof("Compacted the deduplication log at %s to %d live receipts", s.path, len(entries))
}
