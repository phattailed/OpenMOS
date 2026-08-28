// Package messageid provides a durable MOS messageID sequence.
//
// MOS 4.0 §4.1.7 is unusually firm about this: "The sender in a MOS communication
// increments the messageID by one for each new request it sends, the last used messageID
// must be persistent. The messageID wraps to 1 when the limit of the data type is
// reached."
//
// The reason is in the same section. A messageID exists so a receiver can tell a retry
// from a new request: "when messageIDs are provided by the NCS the MOS Server can keep
// the messageIDs of the last received message(s) [...] it can see from the messageID
// whether or not it processed this message already". A process that restarts and reissues
// 1, 2, 3 is therefore not merely untidy -- a peer implementing that deduplication may
// answer those from its cache instead of processing them, and the sender will never know.
//
// Our reference NCS does exactly this, keeping a file per mosID under MOS\MESSAGEID\, so a
// file is both sufficient and precedented.
package messageid

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const (
	// maxID is the largest value the spec permits: "a 32-bit signed integer [...] with a
	// value larger than or equal to 1".
	maxID = int64(math.MaxInt32)

	// defaultBlock is how many identifiers are reserved on disk at a time.
	//
	// The sequence persists a high-water mark rather than every value, so a crash loses
	// the unused remainder of a block. That direction is deliberate: SKIPPING identifiers
	// is harmless, since the peer only cares whether it has seen a value before, while
	// REUSING one is the exact hazard the field exists to prevent. So the failure mode is
	// biased towards skipping.
	defaultBlock = int64(100)
)

// Sequence hands out MOS messageIDs and remembers where it got to.
//
// A zero-value Sequence is not usable; call Open or NewInMemory.
type Sequence struct {
	mu sync.Mutex

	// next is the value the next call to Next will return.
	next int64
	// reservedTo is the highest value durably recorded. Next may hand out values up to
	// and including it without touching the disk.
	reservedTo int64

	path  string
	block int64
	// degraded records that persistence has failed. The sequence keeps working from
	// memory, because refusing to send messages is worse than risking a repeated
	// identifier after a crash, but the condition is visible to callers.
	degraded bool
}

// NewInMemory returns a sequence that does not persist.
//
// Used when no directory is configured, and by tests. It satisfies the increment and wrap
// rules but not the persistence requirement, which Degraded reports.
func NewInMemory() *Sequence {
	return &Sequence{next: 1, reservedTo: maxID, block: defaultBlock, degraded: true}
}

// Open returns a sequence persisted to a file named after the owner, resuming after
// whatever value was last reserved.
//
// owner is normally the mosID. It is sanitised for use as a filename, since a MOS ID
// legitimately contains dots and a fully qualified one is dotted throughout.
func Open(dir, owner string) (*Sequence, error) {
	if strings.TrimSpace(dir) == "" {
		return NewInMemory(), nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create messageID directory %s: %w", dir, err)
	}

	s := &Sequence{
		path:  filepath.Join(dir, safeName(owner)),
		block: defaultBlock,
	}

	// Resume from the recorded high-water mark. A missing file is a first run, not an
	// error; anything unreadable is treated the same way rather than refusing to start,
	// but the value is never assumed to be lower than what was recorded.
	recorded := readMark(s.path)
	s.next = recorded + 1
	if s.next < 1 || s.next > maxID {
		s.next = 1
	}

	if err := s.reserve(s.next + s.block - 1); err != nil {
		// Keep going in memory. A device that cannot write a counter file is still more
		// useful than one that will not start.
		s.degraded = true
		s.reservedTo = maxID
		return s, fmt.Errorf("messageID sequence is not durable: %w", err)
	}
	return s, nil
}

// Next returns the next messageID as a decimal string, already spec-valid.
func (s *Sequence) Next() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.next > maxID {
		// "The messageID wraps to 1 when the limit of the data type is reached."
		s.next = 1
		s.reservedTo = 0
	}

	value := s.next
	s.next++

	if value > s.reservedTo {
		// Reserve the next block. On failure carry on from memory: the identifier being
		// returned is still correct for this process, and the alternative is refusing to
		// talk.
		if err := s.reserve(value + s.block - 1); err != nil {
			s.degraded = true
			s.reservedTo = maxID
		}
	}

	return strconv.FormatInt(value, 10)
}

// Peek reports the value Next would return, without consuming it. For tests and
// diagnostics.
func (s *Sequence) Peek() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next
}

// Degraded reports whether the sequence is running without durability, so a caller can
// say so once rather than guessing.
func (s *Sequence) Degraded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.degraded
}

// reserve records a high-water mark durably. Caller holds the lock.
func (s *Sequence) reserve(upTo int64) error {
	if s.path == "" {
		s.reservedTo = maxID
		return nil
	}
	if upTo > maxID {
		upTo = maxID
	}

	// Write and rename, so a crash mid-write leaves the previous mark rather than a
	// truncated file. A truncated mark would read low and reissue identifiers, which is
	// the one outcome worth engineering against.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(upTo, 10)), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	s.reservedTo = upTo
	s.degraded = false
	return nil
}

// readMark returns the recorded high-water mark, or 0 if there is not a usable one.
func readMark(path string) int64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	if value > maxID {
		return maxID
	}
	return value
}

// safeName turns an owner identifier into a filename.
//
// MOS IDs contain dots by convention and Profile 6 requires a fully qualified dotted
// form, so only path separators and characters Windows rejects are replaced. The reference
// NCS names these files after the mosID directly.
func safeName(owner string) string {
	if strings.TrimSpace(owner) == "" {
		owner = "default"
	}
	replacer := strings.NewReplacer(
		"/", "_", `\`, "_", ":", "_", "*", "_",
		"?", "_", `"`, "_", "<", "_", ">", "_", "|", "_",
	)
	return replacer.Replace(owner) + ".messageid"
}
