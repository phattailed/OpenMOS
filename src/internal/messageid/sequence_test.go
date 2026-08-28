package messageid

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// The property that matters is negative: a restarted sequence must never hand out an
// identifier it has handed out before.
//
// MOS 4.0 §4.1.7 exists so a receiver can tell a retry from a new request. If a restarted
// sender reissues 1, 2, 3, a peer implementing that deduplication may answer them from its
// cache instead of processing them -- and the sender cannot tell the difference between
// "processed" and "mistaken for a retry". Skipping identifiers is harmless by comparison,
// so the implementation is biased that way.

func TestSequenceNeverReusesAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	first, err := Open(dir, "openmos.example.mos")
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	var issued []string
	for i := 0; i < 5; i++ {
		issued = append(issued, first.Next())
	}
	if issued[0] != "1" {
		t.Errorf("first identifier = %q, want 1", issued[0])
	}

	// Restart. The new sequence must not repeat anything the old one issued.
	second, err := Open(dir, "openmos.example.mos")
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}

	seen := map[string]bool{}
	for _, id := range issued {
		seen[id] = true
	}
	for i := 0; i < 5; i++ {
		id := second.Next()
		if seen[id] {
			t.Fatalf("restarted sequence reissued %q, which the previous process already used", id)
		}
		seen[id] = true
	}
}

// TestSequenceSkipsRatherThanRepeats pins the direction of the failure. A block is
// reserved on disk, so a crash loses the unused remainder -- the next process resumes above
// it rather than inside it.
func TestSequenceSkipsRatherThanRepeats(t *testing.T) {
	dir := t.TempDir()

	first, err := Open(dir, "dev")
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	// Consume one identifier, then abandon the process without a clean shutdown.
	got := first.Next()
	if got != "1" {
		t.Fatalf("first identifier = %q, want 1", got)
	}

	second, err := Open(dir, "dev")
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	resumed, err := strconv.ParseInt(second.Next(), 10, 64)
	if err != nil {
		t.Fatalf("unparseable identifier: %v", err)
	}
	if resumed <= 1 {
		t.Errorf("resumed at %d, which repeats or reuses; it must resume above the reserved block", resumed)
	}
}

func TestSequenceWrapsAtTheSpecLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wrap.messageid")

	// Pretend a very long-lived sender reached the boundary.
	if err := os.WriteFile(path, []byte(strconv.FormatInt(int64(math.MaxInt32), 10)), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, "wrap")
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	// "The messageID wraps to 1 when the limit of the data type is reached."
	if got := s.Next(); got != "1" {
		t.Errorf("after the boundary the sequence returned %q, want 1", got)
	}
}

func TestSequenceValuesAreAlwaysSpecValid(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "valid")
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	for i := 0; i < 250; i++ {
		raw := s.Next()
		value, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			t.Fatalf("identifier %q is not a 32-bit signed integer: %v", raw, err)
		}
		if value < 1 {
			t.Fatalf("identifier %d is below the spec floor of 1", value)
		}
	}
}

// TestSequenceIsSafeUnderConcurrency matters because both a heartbeat timer and a request
// path can allocate at once, and a duplicate would be indistinguishable from a retry.
func TestSequenceIsSafeUnderConcurrency(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "concurrent")
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}

	const workers, each = 8, 60
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[string]bool{}
	dupes := 0

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				id := s.Next()
				mu.Lock()
				if seen[id] {
					dupes++
				}
				seen[id] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if dupes != 0 {
		t.Errorf("%d duplicate identifiers issued concurrently", dupes)
	}
	if len(seen) != workers*each {
		t.Errorf("issued %d distinct identifiers, want %d", len(seen), workers*each)
	}
}

// TestUnwritableDirectoryDegradesRatherThanFailing records the availability decision. A
// device that cannot write a counter file is still more useful than one that refuses to
// start, so the sequence keeps working from memory and says so.
func TestUnwritableDirectoryDegradesRatherThanFailing(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Opening under a path that is a file cannot create the directory.
	s, err := Open(filepath.Join(blocked, "sub"), "dev")
	if err == nil {
		t.Skip("this platform allowed the directory; the degraded path is covered by the in-memory case")
	}
	if s != nil && !s.Degraded() {
		t.Error("a sequence returned alongside an error must report itself degraded")
	}

	// The in-memory form is the same situation reached deliberately.
	mem := NewInMemory()
	if !mem.Degraded() {
		t.Error("an in-memory sequence is not durable and must say so")
	}
	if got := mem.Next(); got != "1" {
		t.Errorf("in-memory sequence started at %q, want 1", got)
	}
}

func TestSafeNameKeepsDottedMosIDsReadable(t *testing.T) {
	// Profile 6 requires fully qualified dotted mosIDs, and the reference NCS names these
	// files after the mosID directly, so dots must survive.
	if got := safeName("openmos.machine.site.enterprise.mos"); got != "openmos.machine.site.enterprise.mos.messageid" {
		t.Errorf("safeName mangled a fully qualified mosID: %q", got)
	}
	// Path separators must not survive, or the file escapes its directory.
	for _, bad := range []string{"a/b", `a\b`, "a:b", "a|b"} {
		got := safeName(bad)
		for _, ch := range []string{"/", `\`, ":", "|"} {
			if contains(got, ch) {
				t.Errorf("safeName(%q) = %q, which still contains %q", bad, got, ch)
			}
		}
	}
	if got := safeName("   "); got != "default.messageid" {
		t.Errorf("safeName(blank) = %q, want a default", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
