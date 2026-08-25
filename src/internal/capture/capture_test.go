package capture

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Capture must be off unless a directory is configured, and a nil Recorder must be
// safe to use so call sites need no conditionals.
func TestDisabledByDefault(t *testing.T) {
	rec, err := New("")
	if err != nil {
		t.Fatalf("New(\"\") returned an error: %v", err)
	}
	if rec != nil {
		t.Fatal("New(\"\") should return a nil Recorder -- capture must be opt-in")
	}

	// Every method must tolerate a nil receiver.
	if err := rec.Record("mos2-tcp", Inbound, "peer", []byte("<mos/>"), 12, "UCS-2BE"); err != nil {
		t.Errorf("Record on a nil Recorder returned an error: %v", err)
	}
	if got := rec.Count(); got != 0 {
		t.Errorf("Count on a nil Recorder = %d, want 0", got)
	}
	if got := rec.Dir(); got != "" {
		t.Errorf("Dir on a nil Recorder = %q, want empty", got)
	}
	if err := rec.Close(); err != nil {
		t.Errorf("Close on a nil Recorder returned an error: %v", err)
	}
}

func TestRecordWritesFrameAndManifest(t *testing.T) {
	dir := t.TempDir()
	rec, err := New(dir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer rec.Close()

	frame := []byte(`<mos><mosID>m</mosID><ncsID>n</ncsID><messageID>25</messageID><roCreate><roID>RO</roID></roCreate></mos>`)
	if err := rec.Record("mos2-tcp", Inbound, "127.0.0.1:1234", frame, len(frame)*2, "UCS-2BE"); err != nil {
		t.Fatalf("Record failed: %v", err)
	}
	if got := rec.Count(); got != 1 {
		t.Errorf("Count = %d, want 1", got)
	}

	// The frame is stored verbatim, so it can be used directly as a fixture.
	content, err := os.ReadFile(filepath.Join(dir, "0001-mos2-tcp-in.xml"))
	if err != nil {
		t.Fatalf("captured frame missing: %v", err)
	}
	if string(content) != string(frame) {
		t.Errorf("captured frame was altered:\n got  %s\n want %s", content, frame)
	}

	entries := readManifest(t, dir)
	if len(entries) != 1 {
		t.Fatalf("manifest has %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.Transport != "mos2-tcp" || e.Direction != "in" || e.Peer != "127.0.0.1:1234" {
		t.Errorf("unexpected manifest entry: %+v", e)
	}
	// wireBytes records the size before decoding, which is what makes the encoding
	// visible in the record rather than merely asserted.
	if e.WireBytes != len(frame)*2 {
		t.Errorf("wireBytes = %d, want %d", e.WireBytes, len(frame)*2)
	}
	if e.Encoding != "UCS-2BE" {
		t.Errorf("encoding = %q, want UCS-2BE", e.Encoding)
	}
	if e.Time == "" {
		t.Error("manifest entry has no timestamp")
	}
}

func TestRecordSeparatesDirectionsAndTransports(t *testing.T) {
	dir := t.TempDir()
	rec, _ := New(dir)
	defer rec.Close()

	_ = rec.Record("mos2-tcp", Inbound, "p", []byte("<a/>"), 8, "UCS-2BE")
	_ = rec.Record("mos2-tcp", Outbound, "p", []byte("<b/>"), 8, "UCS-2BE")
	_ = rec.Record("mos4-ws-ro", Inbound, "p", []byte("<c/>"), 8, "UCS-2BE")

	for _, name := range []string{"0001-mos2-tcp-in.xml", "0002-mos2-tcp-out.xml", "0003-mos4-ws-ro-in.xml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s: %v", name, err)
		}
	}
}

// An enabled capture runs for the life of the process, so it must not be able to
// fill a disk.
func TestRecordIsBounded(t *testing.T) {
	dir := t.TempDir()
	rec, _ := New(dir)
	defer rec.Close()
	rec.maxFrames = 3

	var stopErr error
	for i := 0; i < 10; i++ {
		if err := rec.Record("mos2-tcp", Inbound, "p", []byte("<x/>"), 8, "UCS-2BE"); err != nil {
			stopErr = err
			break
		}
	}

	if stopErr == nil {
		t.Fatal("capture should report when it stops at the frame limit")
	}
	if !strings.Contains(stopErr.Error(), "capture stopped") {
		t.Errorf("unexpected error: %v", stopErr)
	}
	if got := rec.Count(); got != 3 {
		t.Errorf("Count = %d, want 3", got)
	}

	// Once stopped it stays quiet rather than erroring on every subsequent frame,
	// so a bounded capture cannot turn into an unbounded log.
	if err := rec.Record("mos2-tcp", Inbound, "p", []byte("<y/>"), 8, "UCS-2BE"); err != nil {
		t.Errorf("Record after stopping should be silent, got: %v", err)
	}
}

func TestManifestAppendsAcrossRecorders(t *testing.T) {
	dir := t.TempDir()

	first, _ := New(dir)
	_ = first.Record("mos2-tcp", Inbound, "p", []byte("<a/>"), 8, "UCS-2BE")
	first.Close()

	second, _ := New(dir)
	_ = second.Record("mos2-tcp", Inbound, "p", []byte("<b/>"), 8, "UCS-2BE")
	second.Close()

	if got := len(readManifest(t, dir)); got != 2 {
		t.Errorf("manifest has %d entries across two runs, want 2", got)
	}
}

func readManifest(t *testing.T, dir string) []Entry {
	t.Helper()
	file, err := os.Open(filepath.Join(dir, "manifest.jsonl"))
	if err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	defer file.Close()

	var entries []Entry
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("bad manifest line %q: %v", line, err)
		}
		entries = append(entries, e)
	}
	return entries
}
