package server

import (
	"fmt"
	"testing"
)

// The same messageID can legitimately appear on different transports and on
// different MOS 4 channels, because each sender increments its own sequence per
// channel. Scoping keeps those apart.
func TestDedupScopesAreIndependent(t *testing.T) {
	store := NewMemoryDedupStore()
	content := []byte("<roCreate><roID>RO-1</roID></roCreate>")

	if got := store.Check("tcp:ro", "NCS", "41", content); got != DedupNew {
		t.Fatalf("first check in tcp:ro = %v, want new", got)
	}
	if got := store.Check("ws:ro", "NCS", "41", content); got != DedupNew {
		t.Errorf("same id in ws:ro = %v, want new -- scopes must not collide", got)
	}
	if got := store.Check("ws:mom", "NCS", "41", content); got != DedupNew {
		t.Errorf("same id in ws:mom = %v, want new -- channels must not collide", got)
	}
	if got := store.Check("tcp:ro", "NCS", "41", content); got != DedupDuplicate {
		t.Errorf("repeat within tcp:ro = %v, want duplicate", got)
	}
}

func TestDedupSeparatesSenders(t *testing.T) {
	store := NewMemoryDedupStore()
	content := []byte("<roCreate><roID>RO-1</roID></roCreate>")

	store.Check("tcp:ro", "NCS-A", "41", content)
	if got := store.Check("tcp:ro", "NCS-B", "41", content); got != DedupNew {
		t.Errorf("same id from a different ncsID = %v, want new", got)
	}
}

// A retry must be answerable with the original bytes.
func TestDedupReplaysTheStoredResponse(t *testing.T) {
	store := NewMemoryDedupStore()
	content := []byte("<roCreate><roID>RO-1</roID></roCreate>")
	ack := []byte("<mos><roAck><roID>RO-1</roID><roStatus>OK</roStatus></roAck></mos>")

	store.Check("tcp:ro", "NCS", "41", content)
	if _, ok := store.Response("tcp:ro", "NCS", "41"); ok {
		t.Fatal("a response was reported before any was remembered")
	}

	store.Remember("tcp:ro", "NCS", "41", ack)

	got, ok := store.Response("tcp:ro", "NCS", "41")
	if !ok {
		t.Fatal("no response stored after Remember")
	}
	if string(got) != string(ack) {
		t.Errorf("replayed response = %q, want %q", got, ack)
	}

	// The store must not hand out an alias callers could mutate.
	got[0] = 'X'
	again, _ := store.Response("tcp:ro", "NCS", "41")
	if string(again) != string(ack) {
		t.Error("stored response was mutated through the returned slice")
	}
}

// The store is in-memory and lives as long as the process, so it must not grow
// without bound on a long-lived connection.
func TestDedupStoreIsBounded(t *testing.T) {
	const capacity = 8
	store := NewMemoryDedupStoreWithCapacity(capacity)

	for i := 0; i < capacity*4; i++ {
		store.Check("tcp:ro", "NCS", fmt.Sprintf("%d", i), []byte("content"))
	}

	if got := store.Len(); got > capacity {
		t.Errorf("store holds %d entries, capacity is %d", got, capacity)
	}

	// The newest entry survives; retries follow closely after the original, so
	// oldest-first eviction keeps the entries that matter.
	newest := fmt.Sprintf("%d", capacity*4-1)
	if got := store.Check("tcp:ro", "NCS", newest, []byte("content")); got != DedupDuplicate {
		t.Errorf("newest entry was evicted: check returned %v, want duplicate", got)
	}
}

func TestDedupDistinguishesDuplicateFromConflict(t *testing.T) {
	store := NewMemoryDedupStore()

	store.Check("tcp:ro", "NCS", "41", []byte("<roCreate><roID>A</roID></roCreate>"))

	if got := store.Check("tcp:ro", "NCS", "41", []byte("<roCreate><roID>A</roID></roCreate>")); got != DedupDuplicate {
		t.Errorf("identical content = %v, want duplicate", got)
	}
	if got := store.Check("tcp:ro", "NCS", "41", []byte("<roCreate><roID>B</roID></roCreate>")); got != DedupConflict {
		t.Errorf("different content under the same id = %v, want conflict", got)
	}
}
