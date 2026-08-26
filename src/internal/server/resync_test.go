package server

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Pull recovery, end to end on the wire.
//
// MOS 3.8.4: "If a message references an unknown roID or storyID, the MOS device should
// treat this as lost synchronization, send roReq, and replace its local state from the
// returned full roList."
//
// This is the situation a live ENPS actually put us in: OpenMOS restarted with in-memory
// storage, lost its running orders, and the NCS carried on sending roStorySend because
// from its side nothing had changed. Refusing those is necessary but not sufficient --
// the NCS is not obliged to notice our amnesia, so a device that only complains stays
// broken.

const recoveryRO = `NCS-HOST;P_NEWS\W;AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE`

func envelopeFor(messageID, payload string) string {
	return `<mos><mosID>openmos.beltware.test</mosID><ncsID>beltware.test</ncsID>` +
		`<messageID>` + messageID + `</messageID>` + payload + `</mos>`
}

func TestPullRecoveryRebuildsLocalStateFromRoList(t *testing.T) {
	tcpServer, runningOrders, stories, items := startMOS28Server(t)
	conn := dialMOS28(t, tcpServer)

	// 1. The NCS sends a story for a running order we do not hold.
	writeMOS28ForTest(t, conn, envelopeFor("700",
		`<roStorySend><roID>`+recoveryRO+`</roID><storyID>S-1</storyID>`+
			`<storySlug>Orphan</storySlug><storyBody><p>text</p></storyBody></roStorySend>`))

	// 2. We refuse it, saying why.
	var ack struct {
		RoAck struct {
			RoStatus string `xml:"roStatus"`
		} `xml:"roAck"`
	}
	readMOS28XMLForTest(t, conn, &ack)
	if !strings.Contains(ack.RoAck.RoStatus, "NACK") {
		t.Fatalf("roStatus = %q, want a NACK", ack.RoAck.RoStatus)
	}

	// 3. And we ask for the running order.
	var pull struct {
		ROReq struct {
			ROID string `xml:"roID"`
		} `xml:"roReq"`
	}
	readMOS28XMLForTest(t, conn, &pull)
	if pull.ROReq.ROID != recoveryRO {
		t.Fatalf("roReq roID = %q, want %q", pull.ROReq.ROID, recoveryRO)
	}

	// 4. The NCS answers with the full build. Two stories, two items in the first, so
	//    ordering and nesting are both exercised.
	writeMOS28ForTest(t, conn, envelopeFor("701",
		`<roList><roID>`+recoveryRO+`</roID><roSlug>Recovered rundown</roSlug>`+
			`<roEdDur>1800</roEdDur>`+
			`<story><storyID>S-1</storyID><storySlug>First</storySlug>`+
			`<item><itemID>I-1</itemID><itemSlug>Item one</itemSlug><objID>OBJ-1</objID>`+
			`<mosID>openmos.beltware.test</mosID></item>`+
			`<item><itemID>I-2</itemID><itemSlug>Item two</itemSlug><objID>OBJ-2</objID>`+
			`<mosID>openmos.beltware.test</mosID></item>`+
			`</story>`+
			`<story><storyID>S-2</storyID><storySlug>Second</storySlug></story>`+
			`</roList>`))

	// roList defines no response, so nothing comes back. Give the server a moment to
	// apply it, then assert on stored state.
	deadline := time.Now().Add(2 * time.Second)
	ctx := context.Background()
	for {
		if _, err := runningOrders.Get(ctx, recoveryRO); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("running order was not rebuilt from the roList")
		}
		time.Sleep(10 * time.Millisecond)
	}

	ro, err := runningOrders.Get(ctx, recoveryRO)
	if err != nil {
		t.Fatalf("running order missing after recovery: %v", err)
	}
	if ro.Slug != "Recovered rundown" {
		t.Errorf("roSlug = %q, want the value from the roList", ro.Slug)
	}

	stored, err := stories.ListByRunningOrder(ctx, recoveryRO)
	if err != nil {
		t.Fatalf("list stories: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("recovered %d stories, want 2", len(stored))
	}
	// Order is significant, and the roList supplied it.
	if stored[0].RawID != "S-1" || stored[1].RawID != "S-2" {
		t.Errorf("story order not preserved: got %s then %s", stored[0].RawID, stored[1].RawID)
	}

	firstItems, err := items.ListByStory(ctx, stored[0].ID)
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	if len(firstItems) != 2 {
		t.Errorf("recovered %d items in the first story, want 2", len(firstItems))
	}

	// 5. The same story now applies cleanly, which is the point of recovering.
	writeMOS28ForTest(t, conn, envelopeFor("702",
		`<roStorySend><roID>`+recoveryRO+`</roID><storyID>S-1</storyID>`+
			`<storySlug>Orphan no more</storySlug><storyBody><p>text</p></storyBody></roStorySend>`))

	var second struct {
		RoAck struct {
			RoStatus string `xml:"roStatus"`
		} `xml:"roAck"`
	}
	readMOS28XMLForTest(t, conn, &second)
	if second.RoAck.RoStatus != "OK" {
		t.Errorf("after recovery roStatus = %q, want OK", second.RoAck.RoStatus)
	}
}

// TestRecoveryDoesNotLoop is the guard that matters operationally. A live ENPS sent ten
// roStorySend messages in a row for a running order we did not hold. If each refusal
// produced a roReq, and the NCS answered each with a NACK because the running order is
// gone on its side too, the pair would trade messages indefinitely. The spec warns about
// exactly this shape for heartbeat: "avoid an endless looping condition on response."
func TestRecoveryDoesNotLoop(t *testing.T) {
	tcpServer, _, _, _ := startMOS28Server(t)
	conn := dialMOS28(t, tcpServer)

	const burst = 5
	for i := 0; i < burst; i++ {
		writeMOS28ForTest(t, conn, envelopeFor("80"+string(rune('0'+i)),
			`<roStorySend><roID>`+recoveryRO+`</roID><storyID>S-`+string(rune('1'+i))+`</storyID>`+
				`<storySlug>Orphan</storySlug><storyBody><p>text</p></storyBody></roStorySend>`))
	}

	// Every message must be answered -- silence would leave the NCS retrying forever --
	// but only the first may trigger a request.
	nacks, requests := 0, 0
	for i := 0; i < burst+1; i++ {
		var frame struct {
			RoAck struct {
				RoStatus string `xml:"roStatus"`
			} `xml:"roAck"`
			ROReq struct {
				ROID string `xml:"roID"`
			} `xml:"roReq"`
		}
		readMOS28XMLForTest(t, conn, &frame)
		switch {
		case frame.ROReq.ROID != "":
			requests++
		case frame.RoAck.RoStatus != "":
			nacks++
		}
	}

	if nacks != burst {
		t.Errorf("answered %d of %d messages; every message must be acknowledged", nacks, burst)
	}
	if requests != 1 {
		t.Errorf("sent %d roReq for a burst of %d refusals, want exactly 1: recovery must not loop",
			requests, burst)
	}
}

func TestResyncGuardRateLimitsAndForgets(t *testing.T) {
	guard := newResyncGuard()

	if !guard.shouldRequest("RO-1") {
		t.Fatal("first request for a running order must be allowed")
	}
	if guard.shouldRequest("RO-1") {
		t.Error("second request within the interval must be suppressed")
	}
	if !guard.shouldRequest("RO-2") {
		t.Error("a different running order must not be blocked by another's rate limit")
	}

	// Once a roList has been applied the disagreement is resolved, so a later
	// divergence is new information and must be actionable immediately.
	guard.forget("RO-1")
	if !guard.shouldRequest("RO-1") {
		t.Error("after forget, a request must be allowed again")
	}

	// An empty identifier is never requestable.
	if guard.shouldRequest("") {
		t.Error("an empty roID must never produce a request")
	}

	// A nil guard is safe, so a server without one degrades to not requesting rather
	// than panicking.
	var nilGuard *resyncGuard
	if nilGuard.shouldRequest("RO-1") {
		t.Error("a nil guard must not authorise a request")
	}
	nilGuard.forget("RO-1")
}

func TestResyncGuardIsBounded(t *testing.T) {
	guard := newResyncGuard()
	guard.max = 4

	allowed := 0
	for i := 0; i < 50; i++ {
		if guard.shouldRequest(string(rune('A' + i))) {
			allowed++
		}
	}
	if allowed > guard.max {
		t.Errorf("authorised %d requests with a bound of %d; a peer sending endless unknown roIDs must not grow the set",
			allowed, guard.max)
	}
	if allowed == 0 {
		t.Error("the bound must not prevent all recovery")
	}
}
