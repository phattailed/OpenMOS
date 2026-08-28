package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mosxml "airshift/openmos/internal/xml"
)

// Durability, narrowly. Three pieces of state must survive a restart, and no more: the outbound
// messageID (internal/messageid, already covered), the deduplication receipts, and unfinished
// discovery work. Everything else can be rebuilt from the NCS by asking.
//
// The test that matters for each is the same shape: write, discard the object, reopen from the
// same directory, and prove the recovered state changes behaviour. Asserting a file exists proves
// almost nothing -- a file with the wrong contents also exists.

func TestDedupReceiptsSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	const scope, ncsID, msgID = "mos2-tcp", "NCS_001", "42"
	content := []byte(`<roCreate><roID>RO-1</roID></roCreate>`)
	response := []byte(`<roAck><roID>RO-1</roID><roStatus>OK</roStatus></roAck>`)

	// First run: a message arrives and is answered.
	first := OpenFileDedupStore(dir, 0)
	if first.Degraded() {
		t.Fatalf("store reported degraded with a usable directory %s", dir)
	}
	if got := first.Check(scope, ncsID, msgID, content); got != DedupNew {
		t.Fatalf("first delivery was %v, want DedupNew", got)
	}
	first.Remember(scope, ncsID, msgID, response)
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Restart.
	second := OpenFileDedupStore(dir, 0)
	defer second.Close()

	// The retry must be recognised, not treated as new. This is the whole point: applying a
	// roCreate or roElementAction twice is a real duplication, not a cosmetic problem.
	if got := second.Check(scope, ncsID, msgID, content); got != DedupDuplicate {
		t.Errorf("after a restart the retry was %v, want DedupDuplicate; the message would have "+
			"been applied a second time", got)
	}

	// And it must be answered with the same bytes.
	replayed, ok := second.Response(scope, ncsID, msgID)
	if !ok {
		t.Fatal("no stored response after a restart; the retry would get silence")
	}
	if string(replayed) != string(response) {
		t.Errorf("replayed response differs after a restart:\n got %q\nwant %q", replayed, response)
	}
}

// TestDedupDetectsAConflictAcrossARestart checks the harder half. Recognising a retry is not
// enough: the same messageID carrying DIFFERENT content is a protocol error and must still be
// caught after a restart, which requires the content hash to have been persisted too.
func TestDedupDetectsAConflictAcrossARestart(t *testing.T) {
	dir := t.TempDir()
	const scope, ncsID, msgID = "mos4-ws-ro", "NCS_001", "7"

	first := OpenFileDedupStore(dir, 0)
	first.Check(scope, ncsID, msgID, []byte(`<roDelete><roID>RO-1</roID></roDelete>`))
	first.Remember(scope, ncsID, msgID, []byte(`<roAck/>`))
	_ = first.Close()

	second := OpenFileDedupStore(dir, 0)
	defer second.Close()

	got := second.Check(scope, ncsID, msgID, []byte(`<roDelete><roID>RO-DIFFERENT</roID></roDelete>`))
	if got != DedupConflict {
		t.Errorf("a reused messageID with different content was %v after a restart, want "+
			"DedupConflict; the content hash was not persisted", got)
	}
}

// TestDedupScopesStayIndependentAcrossARestart guards the reason scoping exists. The two
// transports run concurrently and each sender keeps its own messageID sequence per channel, so
// the same value legitimately means different things. Persistence must not collapse that.
func TestDedupScopesStayIndependentAcrossARestart(t *testing.T) {
	dir := t.TempDir()
	content := []byte(`<roCreate><roID>RO-1</roID></roCreate>`)

	first := OpenFileDedupStore(dir, 0)
	first.Check("mos2-tcp", "NCS_001", "1", content)
	first.Remember("mos2-tcp", "NCS_001", "1", []byte(`<roAck>tcp</roAck>`))
	_ = first.Close()

	second := OpenFileDedupStore(dir, 0)
	defer second.Close()

	// Same messageID, same content, different channel: this is a new message.
	if got := second.Check("mos4-ws-ro", "NCS_001", "1", content); got != DedupNew {
		t.Errorf("messageID 1 on a different scope was %v after a restart, want DedupNew; "+
			"scopes were collapsed by persistence", got)
	}
}

// TestDedupLogCompactsRatherThanGrowingForever checks the log stays proportional to the bounded
// entry set rather than to total traffic, which is what makes an append-only log acceptable on a
// path written once per inbound message.
func TestDedupLogCompactsRatherThanGrowingForever(t *testing.T) {
	dir := t.TempDir()
	const capacity = 8
	store := OpenFileDedupStore(dir, capacity)
	defer store.Close()

	// Well past capacity and past the compaction threshold.
	for i := 0; i < capacity*12; i++ {
		id := fmt.Sprintf("%d", i)
		store.Check("mos2-tcp", "NCS_001", id, []byte("<roCreate><roID>RO-"+id+"</roID></roCreate>"))
		store.Remember("mos2-tcp", "NCS_001", id, []byte("<roAck>"+id+"</roAck>"))
	}

	if got := store.Len(); got > capacity {
		t.Errorf("in-memory entries grew to %d past the %d bound", got, capacity)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "dedup.log"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	// After compaction the log holds live entries plus whatever was appended since. It must not
	// hold a record for every one of the 192 operations.
	if lines > capacity*6 {
		t.Errorf("log holds %d records for %d live entries; compaction is not happening",
			lines, capacity)
	}
	if lines == 0 {
		t.Error("log is empty, so nothing would survive a restart")
	}
}

// TestDedupDegradesLoudlyWithoutADirectory pins the deliberate choice not to refuse to start. A
// storage problem should cost durability, not availability -- non-durable dedup is exactly the
// behaviour that shipped before this existed.
func TestDedupDegradesLoudlyWithoutADirectory(t *testing.T) {
	store := OpenFileDedupStore("", 0)
	defer store.Close()

	if !store.Degraded() {
		t.Error("an empty state directory should report degraded")
	}
	// It must still work as a dedup store.
	content := []byte(`<roCreate><roID>RO-1</roID></roCreate>`)
	if got := store.Check("mos2-tcp", "NCS_001", "1", content); got != DedupNew {
		t.Fatalf("degraded store did not function: %v", got)
	}
	if got := store.Check("mos2-tcp", "NCS_001", "1", content); got != DedupDuplicate {
		t.Errorf("degraded store lost in-memory dedup: %v", got)
	}
}

// TestUnfinishedDiscoveryWorkSurvivesARestart is the walk's half of the same story.
//
// The NCS states which running orders it holds once, in a roListAll. If the process stops halfway
// through fetching them, nothing repeats that statement, so the remainder stays divergent --
// present on the NCS, absent locally, with no error anywhere.
func TestUnfinishedDiscoveryWorkSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// First run: three advertised, one fetched.
	svc, _, _, _ := newDispatchService(t)
	walk := openDiscoveryWalk(dir)
	deps := roDeps{service: svc, resync: newResyncGuard(), walk: walk, mosID: "openmos.example.mos"}
	r := &recordingResponder{label: "peer"}

	if _, err := dispatchRunningOrder(ctx, deps, r, listAllOf("RO-1", "RO-2", "RO-3")); err != nil {
		t.Fatalf("dispatch roListAll: %v", err)
	}
	if _, err := dispatchRunningOrder(ctx, deps, r, mosxml.ROList{ID: "RO-1", Slug: "First"}); err != nil {
		t.Fatalf("dispatch roList: %v", err)
	}
	// RO-2 is now in flight, RO-3 queued. The process stops here.
	if got := walk.inFlightID(); got != "RO-2" {
		t.Fatalf("expected RO-2 in flight before the restart, got %q", got)
	}

	// Restart: a new walk from the same directory.
	resumed := openDiscoveryWalk(dir)
	if resumed.degraded {
		t.Fatalf("resumed walk reported degraded with a usable directory %s", dir)
	}

	// Both the in-flight and the queued running order must come back. The in-flight one
	// especially: its answer was never going to arrive, so it has to be requested again.
	deps2 := roDeps{service: svc, resync: newResyncGuard(), walk: resumed, mosID: "openmos.example.mos"}
	r2 := &recordingResponder{label: "peer-after-restart"}

	// Any inbound traffic restarts the walk.
	if _, err := dispatchRunningOrder(ctx, deps2, r2,
		mosxml.ROElementStat{ROID: "RO-9", Element: "RO"}); err != nil {
		t.Fatalf("dispatch nudge: %v", err)
	}

	requested := roIDsRequested(r2)
	if len(requested) != 1 || requested[0] != "RO-2" {
		t.Fatalf("after a restart the walk requested %v, want a single RO-2: the interrupted "+
			"request must be reissued because no answer is coming for it", requested)
	}

	// And completing it must move on to the one that was still queued.
	if _, err := dispatchRunningOrder(ctx, deps2, r2, mosxml.ROList{ID: "RO-2", Slug: "Second"}); err != nil {
		t.Fatalf("dispatch roList: %v", err)
	}
	requested = roIDsRequested(r2)
	if len(requested) != 2 || requested[1] != "RO-3" {
		t.Errorf("walk did not continue to the queued running order after a restart: %v", requested)
	}
}

// TestCompletedDiscoveryWorkIsNotRepeatedAfterARestart is the other side: a finished walk must
// not be refetched on every start, which would make restarts progressively more expensive.
func TestCompletedDiscoveryWorkIsNotRepeatedAfterARestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	svc, _, _, _ := newDispatchService(t)

	walk := openDiscoveryWalk(dir)
	deps := roDeps{service: svc, resync: newResyncGuard(), walk: walk, mosID: "openmos.example.mos"}
	r := &recordingResponder{label: "peer"}

	if _, err := dispatchRunningOrder(ctx, deps, r, listAllOf("RO-1")); err != nil {
		t.Fatalf("dispatch roListAll: %v", err)
	}
	if _, err := dispatchRunningOrder(ctx, deps, r, mosxml.ROList{ID: "RO-1", Slug: "Only"}); err != nil {
		t.Fatalf("dispatch roList: %v", err)
	}
	if walk.remaining() != 0 || walk.inFlightID() != "" {
		t.Fatalf("walk did not complete: %d queued, %q in flight", walk.remaining(), walk.inFlightID())
	}

	resumed := openDiscoveryWalk(dir)
	deps2 := roDeps{service: svc, resync: newResyncGuard(), walk: resumed, mosID: "openmos.example.mos"}
	r2 := &recordingResponder{label: "peer-after-restart"}
	if _, err := dispatchRunningOrder(ctx, deps2, r2,
		mosxml.ROElementStat{ROID: "RO-9", Element: "RO"}); err != nil {
		t.Fatalf("dispatch nudge: %v", err)
	}
	if got := roIDsRequested(r2); len(got) != 0 {
		t.Errorf("a completed walk was repeated after a restart: %v", got)
	}
}

// TestStateSubdirKeepsDurabilityOptional guards the config gotcha that has bitten this project
// repeatedly: an absent value is the zero value, and "" must mean disabled rather than the
// current directory.
func TestStateSubdirKeepsDurabilityOptional(t *testing.T) {
	if got := stateSubdir("", "mos2"); got != "" {
		t.Errorf("stateSubdir(\"\", ...) = %q, want \"\": an unset state directory must disable "+
			"durability, not write to ./mos2", got)
	}
	if got := stateSubdir("state", "mos2"); got != filepath.Join("state", "mos2") {
		t.Errorf("stateSubdir = %q, want state/mos2", got)
	}
}
