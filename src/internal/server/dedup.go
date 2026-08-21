package server

import (
	"crypto/sha256"
	"fmt"
	"sync"
)

// DedupResult describes the outcome of a dedup check.
type DedupResult int

const (
	// DedupNew means this messageID has not been seen before.
	DedupNew DedupResult = iota
	// DedupDuplicate means same messageID with identical content (re-delivery).
	DedupDuplicate
	// DedupConflict means same messageID with different content (conflict).
	DedupConflict
)

// DedupStore tracks (ncsID, messageID) -> content hash for deduplication.
type DedupStore interface {
	// Check returns the dedup status for a given ncsID+messageID combination.
	// If the combination is new, it records it and returns DedupNew.
	// If the combination exists with the same hash, returns DedupDuplicate.
	// If the combination exists with a different hash, returns DedupConflict.
	Check(ncsID, messageID string, content []byte) DedupResult
}

// MemoryDedupStore is an in-memory implementation of DedupStore.
// Suitable for testing and single-instance deployments.
type MemoryDedupStore struct {
	mu     sync.RWMutex
	hashes map[string]string // key = "ncsID:messageID", value = hex content hash
}

// NewMemoryDedupStore creates a new in-memory dedup store.
func NewMemoryDedupStore() *MemoryDedupStore {
	return &MemoryDedupStore{
		hashes: make(map[string]string),
	}
}

func (d *MemoryDedupStore) Check(ncsID, messageID string, content []byte) DedupResult {
	key := ncsID + ":" + messageID
	hash := contentHash(content)

	d.mu.Lock()
	defer d.mu.Unlock()

	existing, exists := d.hashes[key]
	if !exists {
		d.hashes[key] = hash
		return DedupNew
	}
	if existing == hash {
		return DedupDuplicate
	}
	return DedupConflict
}

// contentHash computes a SHA-256 hex digest of the content.
func contentHash(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h)
}
