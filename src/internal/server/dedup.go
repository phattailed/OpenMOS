package server

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// Deduplication of retried messages.
//
// MOS 4.0 §4.1.6 explains why this exists. An NCS that gets no response within
// its timeout resets the connection and sends the same request again with the
// same messageID:
//
//	"The NCS cannot really know if the first sent message was processed by the
//	MOS Server. [...] Therefore the MOS Server would be forced to process the
//	repeated message, which will lead to an unwanted result in many cases."
//
// Retries are routine, not exceptional: "Message transmissions which do not
// receive a response will be retried at intervals until a response is received."
//
// A retry must therefore be answered with the original response and must not be
// applied twice. Answering with silence is not an option -- it guarantees the
// peer keeps retrying.

// DedupResult describes the outcome of a dedup check.
type DedupResult int

const (
	// DedupNew means this messageID has not been seen in this scope before.
	DedupNew DedupResult = iota
	// DedupDuplicate means the same messageID arrived with identical content,
	// i.e. a re-delivery. The original response should be replayed and the
	// operation must not be applied again.
	DedupDuplicate
	// DedupConflict means the same messageID arrived with different content.
	// This is a protocol error on the sender's part and must be rejected.
	DedupConflict
)

func (r DedupResult) String() string {
	switch r {
	case DedupNew:
		return "new"
	case DedupDuplicate:
		return "duplicate"
	case DedupConflict:
		return "conflict"
	default:
		return "unknown"
	}
}

// defaultDedupCapacity bounds how many messages are remembered. The store is
// in-memory, so it must not grow without limit on a long-lived connection.
// Eviction is oldest-first: retries follow closely after the original, so the
// entries that matter are always the newest.
const defaultDedupCapacity = 4096

// DedupStore tracks (scope, ncsID, messageID) against a content hash and the
// response that was sent, so a retry can be answered identically.
//
// scope separates traffic that could legitimately reuse a messageID -- the two
// transports run concurrently, and MOS 4 additionally multiplexes mom, ro and aux
// channels over separate connections. Each sender increments its own messageID
// sequence per channel, so the same value can mean different things on different
// channels.
type DedupStore interface {
	// Check records or matches this message. content must be the inner operation
	// only, never the whole envelope, so that re-deliveries differing in envelope
	// whitespace or field order are recognised as duplicates rather than
	// conflicts.
	Check(scope, ncsID, messageID string, content []byte) DedupResult

	// Remember stores the response sent for a message so a later retry can be
	// answered with exactly the same bytes.
	Remember(scope, ncsID, messageID string, response []byte)

	// Response returns the response previously stored for a message.
	Response(scope, ncsID, messageID string) ([]byte, bool)
}

type dedupEntry struct {
	key      string
	hash     string
	response []byte
}

// MemoryDedupStore is a bounded in-memory DedupStore.
//
// Not durable: a process restart loses all history, so the first retry after a
// restart is treated as new and re-applied. Durable dedup needs the state kept
// alongside the running orders themselves.
type MemoryDedupStore struct {
	mu       sync.Mutex
	capacity int
	entries  map[string]*list.Element
	order    *list.List // front = oldest
}

// NewMemoryDedupStore creates a store with the default capacity.
func NewMemoryDedupStore() *MemoryDedupStore {
	return NewMemoryDedupStoreWithCapacity(defaultDedupCapacity)
}

// NewMemoryDedupStoreWithCapacity creates a store bounded to capacity entries.
func NewMemoryDedupStoreWithCapacity(capacity int) *MemoryDedupStore {
	if capacity <= 0 {
		capacity = defaultDedupCapacity
	}
	return &MemoryDedupStore{
		capacity: capacity,
		entries:  make(map[string]*list.Element, capacity),
		order:    list.New(),
	}
}

func dedupKey(scope, ncsID, messageID string) string {
	return scope + "\x00" + ncsID + "\x00" + messageID
}

func (d *MemoryDedupStore) Check(scope, ncsID, messageID string, content []byte) DedupResult {
	key := dedupKey(scope, ncsID, messageID)
	hash := contentHash(content)

	d.mu.Lock()
	defer d.mu.Unlock()

	if element, exists := d.entries[key]; exists {
		entry := element.Value.(*dedupEntry)
		if entry.hash == hash {
			return DedupDuplicate
		}
		return DedupConflict
	}

	d.insertLocked(&dedupEntry{key: key, hash: hash})
	return DedupNew
}

func (d *MemoryDedupStore) Remember(scope, ncsID, messageID string, response []byte) {
	key := dedupKey(scope, ncsID, messageID)

	d.mu.Lock()
	defer d.mu.Unlock()

	stored := make([]byte, len(response))
	copy(stored, response)

	if element, exists := d.entries[key]; exists {
		element.Value.(*dedupEntry).response = stored
		return
	}
	// Check was evicted or never ran; keep the response anyway.
	d.insertLocked(&dedupEntry{key: key, response: stored})
}

func (d *MemoryDedupStore) Response(scope, ncsID, messageID string) ([]byte, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	element, exists := d.entries[dedupKey(scope, ncsID, messageID)]
	if !exists {
		return nil, false
	}
	entry := element.Value.(*dedupEntry)
	if entry.response == nil {
		return nil, false
	}
	out := make([]byte, len(entry.response))
	copy(out, entry.response)
	return out, true
}

// Len reports how many messages are currently remembered.
func (d *MemoryDedupStore) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.entries)
}

// insertLocked adds an entry, evicting the oldest if at capacity.
// The caller must hold d.mu.
func (d *MemoryDedupStore) insertLocked(entry *dedupEntry) {
	for len(d.entries) >= d.capacity {
		oldest := d.order.Front()
		if oldest == nil {
			break
		}
		d.order.Remove(oldest)
		delete(d.entries, oldest.Value.(*dedupEntry).key)
	}
	d.entries[entry.key] = d.order.PushBack(entry)
}

// contentHash computes a SHA-256 hex digest of the content.
func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// The three helpers below exist for FileDedupStore, which needs to persist what the in-memory
// store derives internally and to rebuild that state on startup. They are deliberately narrow:
// the durable store composes the memory store rather than reimplementing eviction, so the LRU
// bound and the conflict rules stay in one place.

// keyAndHash exposes the derived key and content hash so a durable store can record them
// without recomputing the hashing convention and risking divergence.
func (d *MemoryDedupStore) keyAndHash(scope, ncsID, messageID string, content []byte) (string, string) {
	return dedupKey(scope, ncsID, messageID), contentHash(content)
}

// hashFor returns the recorded hash for a key, so a response can be persisted alongside the
// hash that Check already established.
func (d *MemoryDedupStore) hashFor(key string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	element, exists := d.entries[key]
	if !exists {
		return "", false
	}
	return element.Value.(*dedupEntry).hash, true
}

// restore reinstates an entry read back from durable storage. It goes through insertLocked so a
// recovered log longer than the capacity is bounded exactly as live traffic would be, keeping the
// most recent entries.
func (d *MemoryDedupStore) restore(key, hash string, response []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if element, exists := d.entries[key]; exists {
		entry := element.Value.(*dedupEntry)
		if hash != "" {
			entry.hash = hash
		}
		if len(response) > 0 {
			entry.response = response
		}
		return
	}
	d.insertLocked(&dedupEntry{key: key, hash: hash, response: response})
}

// snapshot copies live entries in eviction order, oldest first, for log compaction.
func (d *MemoryDedupStore) snapshot() []dedupEntry {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([]dedupEntry, 0, len(d.entries))
	for element := d.order.Front(); element != nil; element = element.Next() {
		entry := element.Value.(*dedupEntry)
		copied := dedupEntry{key: entry.key, hash: entry.hash}
		if entry.response != nil {
			copied.response = make([]byte, len(entry.response))
			copy(copied.response, entry.response)
		}
		out = append(out, copied)
	}
	return out
}
