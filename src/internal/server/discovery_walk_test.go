package server

import (
	"context"
	"encoding/json"
	stdxml "encoding/xml"
	"fmt"
	"testing"
	"time"

	"airshift/openmos/internal/repository"
	"airshift/openmos/internal/service"
	mosxml "airshift/openmos/internal/xml"
)

type catalogueOnlyResponder struct{ recordingResponder }

func (*catalogueOnlyResponder) encodeSourceReply(context.Context, mosxml.MOSMessage) ([]byte, error) {
	return nil, fmt.Errorf("catalogue must not produce a MOS acknowledgement")
}

func (*catalogueOnlyResponder) sendSourceReply(context.Context, []byte) error {
	return fmt.Errorf("catalogue must not produce a MOS acknowledgement")
}

func TestCatalogueCapacityIsCheckedBeforeSourceAuthority(t *testing.T) {
	ctx := context.Background()
	binding := repository.SourceBinding{SourceID: "source", MosID: "device", NCSID: "newsroom", Transport: "ws-client", Destination: "http://127.0.0.1:1234/v1/openmos-snapshots"}
	catalogue, err := repository.OpenCatalogue(t.TempDir(), service.SourceCatalogueBinding(binding), true)
	if err != nil {
		t.Fatal(err)
	}
	defer catalogue.Close()
	source, err := service.NewCommittedSourceSet(ctx, nil, catalogue, "synthetic-token", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	input := service.SourceInput{Transport: "ws-client", NCSID: "newsroom", Scope: "ws-client:ro:standard", Session: "session", MessageID: "prior", Content: []byte(`<roListAll><ro><roID>retained</roID></ro></roListAll>`)}
	if err := source.Observe(ctx, input); err != nil {
		t.Fatal(err)
	}
	responder := &catalogueOnlyResponder{}
	if _, err := source.Apply(ctx, input, listAllOf("retained"), func(msg mosxml.MOSMessage) ([]byte, error) { return stdxml.Marshal(msg) }); err != nil {
		t.Fatal(err)
	}
	deps, walk := walkDeps(t)
	deps.service.Source = source
	walk.max = 1
	walk.requestCatalogue()
	walk.registerRequest("", input.Scope, input.Session, "current", false)
	input.MessageID = "current"
	msg := listAllOf("new-one", "new-two")
	input.Content, _ = stdxml.Marshal(msg)
	if _, err := dispatchRunningOrder(context.WithValue(ctx, sourceInputKey{}, input), deps, responder, msg); err == nil {
		t.Fatal("discovery capacity failure was not exposed")
	}
	cp, err := catalogue.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot service.SourceCatalogue
	if err := json.Unmarshal(cp.Pending, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Complete || len(snapshot.Rundowns) != 1 || snapshot.Rundowns[0].ID != "retained" || source.RetainsRundown("new-one") || source.RetainsRundown("new-two") || walk.remaining() != 0 {
		t.Fatal("capacity failure enrolled or queued a partial set, removed retained membership, or published complete authority")
	}
}

// The discovery walk: roReqAll -> roListAll -> one roReq per advertised running order -> apply
// each returned roList.
//
// The behaviour worth pinning is that it is SEQUENTIAL. Firing one roReq per advertised running
// order at once would pass a naive "did it request them all" test while violating MOS 4.0 §4.1,
// which requires a sender not to send another message on the same port until the previous is
// acknowledged. So these tests assert the shape of the conversation, not just its contents.

// walkDeps builds dependencies with a live walk and returns them alongside it.
func walkDeps(t *testing.T) (roDeps, *discoveryWalk) {
	t.Helper()
	svc, _, _, _ := newDispatchService(t)
	w := newDiscoveryWalk()
	return roDeps{service: svc, resync: newResyncGuard(), walk: w, mosID: "openmos.example.mos"}, w
}

// roIDsRequested extracts the running orders a responder was asked to request.
func roIDsRequested(r *recordingResponder) []string {
	var ids []string
	for _, msg := range r.sent {
		if req, ok := msg.(mosxml.ROReq); ok {
			ids = append(ids, req.ROID)
		}
	}
	return ids
}

func listAllOf(roIDs ...string) mosxml.ROListAll {
	items := make([]mosxml.ROListAllItem, 0, len(roIDs))
	for _, id := range roIDs {
		items = append(items, mosxml.ROListAllItem{ID: id, Slug: "Slug " + id})
	}
	return mosxml.ROListAll{ROs: items}
}

// TestDiscoveryWalkRequestsOneRunningOrderAtATime is the central assertion. An advertised list
// of three must produce exactly ONE roReq, not three.
func TestDiscoveryWalkRequestsOneRunningOrderAtATime(t *testing.T) {
	deps, walk := walkDeps(t)
	r := &recordingResponder{label: "peer"}

	handled, err := dispatchRunningOrder(context.Background(), deps, r, listAllOf("RO-1", "RO-2", "RO-3"))
	if err != nil {
		t.Fatalf("dispatch roListAll: %v", err)
	}
	if !handled {
		t.Fatal("roListAll was not recognised by the shared dispatcher")
	}

	got := roIDsRequested(r)
	if len(got) != 1 {
		t.Fatalf("roListAll advertising 3 running orders produced %d roReq(s) %v; the walk must "+
			"be sequential, because MOS 4.0 §4.1 forbids sending another message on the same "+
			"port before the previous is acknowledged", len(got), got)
	}
	if got[0] != "RO-1" {
		t.Errorf("first request was for %s, want RO-1: the NCS's ordering should be preserved",
			got[0])
	}
	if walk.remaining() != 2 {
		t.Errorf("walk has %d queued, want 2", walk.remaining())
	}
}

// TestDiscoveryWalkAdvancesOnEachAppliedROList proves the whole sequence completes, one step per
// answer, and that each returned running order is actually persisted rather than merely counted.
func TestDiscoveryWalkAdvancesOnEachAppliedROList(t *testing.T) {
	deps, walk := walkDeps(t)
	r := &recordingResponder{label: "peer"}
	ctx := context.Background()

	advertised := []string{"RO-A", "RO-B", "RO-C"}
	if _, err := dispatchRunningOrder(ctx, deps, r, listAllOf(advertised...)); err != nil {
		t.Fatalf("dispatch roListAll: %v", err)
	}

	// Answer each request in turn, exactly as an NCS would.
	for i, want := range advertised {
		got := roIDsRequested(r)
		if len(got) != i+1 {
			t.Fatalf("after %d answers there were %d requests %v, want %d", i, len(got), got, i+1)
		}
		if got[i] != want {
			t.Fatalf("request %d was for %s, want %s", i, got[i], want)
		}

		list := mosxml.ROList{
			ID:   want,
			Slug: "Slug " + want,
			Stories: []mosxml.StoryInfo{
				{ID: want + "-STORY-1", Slug: "First"},
			},
		}
		if _, err := dispatchRunningOrder(ctx, deps, r, list); err != nil {
			t.Fatalf("dispatch roList for %s: %v", want, err)
		}
	}

	if got := roIDsRequested(r); len(got) != len(advertised) {
		t.Errorf("walk issued %d requests %v, want %d", len(got), got, len(advertised))
	}
	if walk.remaining() != 0 || walk.inFlightID() != "" {
		t.Errorf("walk did not finish: %d queued, in flight %q",
			walk.remaining(), walk.inFlightID())
	}

	// Each discovered running order must actually be in the store. A walk that requests
	// everything and persists nothing is the exact failure this work set out to fix.
	for _, id := range advertised {
		ro, stories, err := deps.service.GetRunningOrderWithStories(ctx, id)
		if err != nil {
			t.Errorf("running order %s was requested but not persisted: %v", id, err)
			continue
		}
		if ro.Slug != "Slug "+id {
			t.Errorf("running order %s persisted with slug %q", id, ro.Slug)
		}
		if len(stories) != 1 {
			t.Errorf("running order %s persisted with %d stories, want 1: the roList content "+
				"must be applied, not just acknowledged", id, len(stories))
		}
	}
}

// TestDiscoveryWalkContinuesWhenAnAnswerNeverArrives covers the stall hazard. roReq may be
// answered with a NACK-bearing roAck rather than a roList -- a real ENPS buddy server NACKs
// everything -- and without a deadline one refusal would leave every later running order
// unrequested and the divergence silent.
func TestDiscoveryWalkContinuesWhenAnAnswerNeverArrives(t *testing.T) {
	deps, walk := walkDeps(t)
	walk.timeout = 20 * time.Millisecond
	r := &recordingResponder{label: "peer"}
	ctx := context.Background()

	if _, err := dispatchRunningOrder(ctx, deps, r, listAllOf("RO-1", "RO-2")); err != nil {
		t.Fatalf("dispatch roListAll: %v", err)
	}
	if got := roIDsRequested(r); len(got) != 1 || got[0] != "RO-1" {
		t.Fatalf("expected one request for RO-1, got %v", got)
	}

	// RO-1 is never answered. Let its deadline pass, then let any traffic arrive.
	time.Sleep(40 * time.Millisecond)
	if _, err := dispatchRunningOrder(ctx, deps, r, mosxml.ROElementStat{ROID: "RO-9", Element: "RO"}); err != nil {
		t.Fatalf("dispatch nudge message: %v", err)
	}

	got := roIDsRequested(r)
	if len(got) != 2 || got[1] != "RO-2" {
		t.Fatalf("walk did not continue past an unanswered request: %v. A roReq answered with "+
			"a NACK must not stall the remainder of the walk.", got)
	}
}

// TestDiscoveryWalkIgnoresUnsolicitedROList checks that a roList nobody asked for cannot advance
// somebody else's queue. Unsolicited roLists are legal, and treating one as an answer would skip
// a running order without ever requesting it.
func TestDiscoveryWalkIgnoresUnsolicitedROList(t *testing.T) {
	deps, _ := walkDeps(t)
	r := &recordingResponder{label: "peer"}
	ctx := context.Background()

	if _, err := dispatchRunningOrder(ctx, deps, r, listAllOf("RO-1", "RO-2")); err != nil {
		t.Fatalf("dispatch roListAll: %v", err)
	}

	// A roList for something else entirely arrives.
	unsolicited := mosxml.ROList{ID: "RO-OTHER", Slug: "Elsewhere"}
	if _, err := dispatchRunningOrder(ctx, deps, r, unsolicited); err != nil {
		t.Fatalf("dispatch unsolicited roList: %v", err)
	}

	got := roIDsRequested(r)
	if len(got) != 1 {
		t.Errorf("an unsolicited roList advanced the walk: requests %v. RO-1 is still "+
			"outstanding and must not be skipped.", got)
	}
}

// TestDiscoveryWalkDeduplicatesAndBounds covers a malformed or hostile list.
func TestDiscoveryWalkDeduplicatesAndBounds(t *testing.T) {
	t.Run("duplicates produce one request each", func(t *testing.T) {
		deps, walk := walkDeps(t)
		r := &recordingResponder{label: "peer"}
		if _, err := dispatchRunningOrder(context.Background(), deps, r,
			listAllOf("RO-1", "RO-1", "RO-2", "RO-1")); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		// One in flight plus one queued, not four.
		if walk.remaining() != 1 {
			t.Errorf("duplicates were queued: %d remaining, want 1", walk.remaining())
		}
	})

	t.Run("an implausible list is bounded", func(t *testing.T) {
		deps, walk := walkDeps(t)
		walk.max = 5
		r := &recordingResponder{label: "peer"}

		ids := make([]string, 0, 20)
		for i := 0; i < 20; i++ {
			ids = append(ids, fmt.Sprintf("RO-%02d", i))
		}
		if _, err := dispatchRunningOrder(context.Background(), deps, r, listAllOf(ids...)); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		// max applies to the queue; one of those is immediately in flight.
		if got := walk.remaining(); got != 4 {
			t.Errorf("bound not applied: %d queued, want 4 (5 accepted, 1 in flight)", got)
		}
	})

	t.Run("an empty roListAll is a valid answer", func(t *testing.T) {
		deps, walk := walkDeps(t)
		r := &recordingResponder{label: "peer"}
		handled, err := dispatchRunningOrder(context.Background(), deps, r, mosxml.ROListAll{})
		if err != nil || !handled {
			t.Fatalf("empty roListAll: handled=%v err=%v", handled, err)
		}
		if len(roIDsRequested(r)) != 0 || walk.inFlightID() != "" {
			t.Error("an empty roListAll should request nothing")
		}
	})
}

// TestDiscoveryWalkIsSharedByBothTransports pins the seam. The walk lives behind
// dispatchRunningOrder, so it cannot be present on one transport and missing on the other --
// which is how roElementStat diverged (doc/interop §14).
func TestDiscoveryWalkIsSharedByBothTransports(t *testing.T) {
	deps, _ := walkDeps(t)
	ctx := context.Background()

	for _, label := range []string{"mos2-tcp", "mos4-ws"} {
		r := &recordingResponder{label: label}
		if _, err := dispatchRunningOrder(ctx, deps, r, listAllOf("RO-"+label)); err != nil {
			t.Fatalf("%s: dispatch roListAll: %v", label, err)
		}
		if got := roIDsRequested(r); len(got) != 1 {
			t.Errorf("%s issued %d requests %v, want 1", label, len(got), got)
		}
		// Complete it so the next transport starts from an idle walk.
		if _, err := dispatchRunningOrder(ctx, deps, r,
			mosxml.ROList{ID: "RO-" + label, Slug: "S"}); err != nil {
			t.Fatalf("%s: dispatch roList: %v", label, err)
		}
	}
}

// TestOnlyOneROReqOutstandingPerLane pins MOS 4.0 §4.1 for the request family OpenMOS actually
// originates in volume: "a sender must not send another message on the same port until the previous
// message is acknowledged".
//
// Recovery used to send roReq directly while the discovery walk could have one outstanding, so a
// divergence arriving mid-walk produced two concurrent requests on the same lane. Recovery now
// routes through the walk.
func TestOnlyOneROReqOutstandingPerLane(t *testing.T) {
	deps, walk := walkDeps(t)
	ctx := context.Background()
	r := &recordingResponder{label: "peer"}

	// A walk is under way, so RO-1 is in flight and RO-2 and RO-3 are queued.
	if _, err := dispatchRunningOrder(ctx, deps, r, listAllOf("RO-1", "RO-2", "RO-3")); err != nil {
		t.Fatalf("dispatch roListAll: %v", err)
	}
	if got := roIDsRequested(r); len(got) != 1 {
		t.Fatalf("expected one request after roListAll, got %v", got)
	}

	// Now a peer sends content for a running order we do not hold, which triggers recovery.
	if _, err := dispatchRunningOrder(ctx, deps, r, mosxml.ROStorySend{
		ROID: "RO-DIVERGED", StoryID: "S-1",
	}); err != nil {
		t.Fatalf("dispatch roStorySend: %v", err)
	}

	// Still exactly one roReq outstanding. The recovery request must have been queued, not sent.
	if got := roIDsRequested(r); len(got) != 1 {
		t.Errorf("recovery sent a second concurrent roReq: %v. MOS 4.0 §4.1 forbids another "+
			"message on the same port before the previous is acknowledged.", got)
	}

	// And it must be next, ahead of the remaining discovery work, because recovery is the more
	// urgent of the two.
	if _, err := dispatchRunningOrder(ctx, deps, r, mosxml.ROList{ID: "RO-1", Slug: "First"}); err != nil {
		t.Fatalf("dispatch roList: %v", err)
	}
	got := roIDsRequested(r)
	if len(got) != 2 {
		t.Fatalf("expected a second request once the first completed, got %v", got)
	}
	if got[1] != "RO-DIVERGED" {
		t.Errorf("next request was %s, want RO-DIVERGED: recovery should jump the discovery queue",
			got[1])
	}
	if walk.remaining() != 2 {
		t.Errorf("walk has %d queued, want 2 (RO-2 and RO-3)", walk.remaining())
	}
}

func TestCatalogueDiscoverySharesTheRequestQueueAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	walk := openDiscoveryWalk(dir)
	if id, ok := walk.enqueueUrgent("first"); !ok || id != "first" {
		t.Fatal("initial rundown request did not start")
	}
	if _, ok := walk.requestCatalogue(); ok {
		t.Fatal("catalogue overlapped an outstanding rundown request")
	}
	walk = openDiscoveryWalk(dir)
	if id, ok := walk.nudge(); !ok || id != "" {
		t.Fatal("restart lost the pending catalogue request")
	}
	if _, ok := walk.requestCatalogue(); ok {
		t.Fatal("repeated recovery sent another catalogue request")
	}
	if _, ok := walk.enqueueUrgent("second"); ok {
		t.Fatal("rundown recovery overlapped the catalogue request")
	}
	if id, ok, _ := walk.begin([]string{"first", "second"}); !ok || id != "first" {
		t.Fatal("catalogue response did not start sequential rundown discovery")
	}
	if _, ok := walk.requestCatalogue(); ok {
		t.Fatal("catalogue refresh bypassed an outstanding rundown")
	}
	if id, ok := walk.resolved("first"); !ok || id != "" {
		t.Fatal("queued catalogue refresh did not take the next request slot")
	}
	if walk.registerRequest("", "ws:ro", "session", "request-a", false) == nil {
		t.Fatal("catalogue request did not retain its actual wire identity")
	}
	walk.mu.Lock()
	walk.deadline = time.Now().Add(-time.Second)
	walk.mu.Unlock()
	if id, expired := walk.timedOut(); !expired || id != "" {
		t.Fatal("unanswered catalogue request has no bounded timeout")
	}
	if id, ok := walk.nudge(); !ok || id != "" {
		t.Fatal("expired catalogue request was never retried")
	}
	if _, ok, _ := walk.begin(nil); ok {
		t.Fatal("authoritative empty catalogue retained stale discovery work")
	}
	walk = openDiscoveryWalk(dir)
	if _, ok := walk.nudge(); ok {
		t.Fatal("restart repeated completed empty catalogue work")
	}
}

func TestCatalogueRepliesRequireTheCurrentScopeSessionAndRequest(t *testing.T) {
	walk := newDiscoveryWalk()
	walk.requestCatalogue()
	first := walk.registerRequest("", "ws:ro", "request-session", "first", false)
	walk.mu.Lock()
	walk.deadline = time.Now().Add(-time.Second)
	walk.mu.Unlock()
	walk.nudge()
	if walk.claimRequest("", "ws:ro", "request-session", "first") != nil {
		t.Fatal("expired request claimed the reserved successor before it was written")
	}
	second := walk.registerRequest("", "ws:ro", "request-session", "second", false)
	for _, input := range []struct{ scope, session, id string }{
		{"ws:ro", "request-session", "first"},
		{"ws:ro", "passive-session", "second"},
		{"ws:obj", "request-session", "second"},
	} {
		if walk.claimRequest("", input.scope, input.session, input.id) != nil {
			t.Fatal("a late answer or a different lane claimed the current catalogue request")
		}
	}
	if walk.failedRequest(first) {
		t.Fatal("old failure cancelled the current request")
	}
	if walk.claimRequest("", "ws:ro", "request-session", "second") != second || second == nil {
		t.Fatal("matching catalogue response could not claim its own slot")
	}
}

func TestCatalogueAndRosterNativeTimeoutRequireAnotherConnection(t *testing.T) {
	for _, rundown := range []string{"", "rundown"} {
		t.Run("target-"+rundown, func(t *testing.T) {
			walk := newDiscoveryWalk()
			if rundown == "" {
				walk.requestCatalogue()
			} else {
				walk.enqueueUrgent(rundown)
			}
			walk.registerRequest(rundown, "tcp:ro", "old-session", "", true)
			walk.mu.Lock()
			walk.deadline = time.Now().Add(-time.Second)
			walk.mu.Unlock()
			walk.nudge()
			if walk.registerRequest(rundown, "tcp:ro", "old-session", "", true) != nil || walk.claimRequest(rundown, "tcp:ro", "old-session", "") != nil {
				t.Fatal("ambiguous native timeout allowed a second request or late reply on the same connection")
			}
			if _, ok := walk.enqueueUrgent("other"); ok {
				t.Fatal("rundown recovery bypassed the ambiguous native request")
			}
			if walk.registerRequest(rundown, "tcp:ro", "new-session", "", true) == nil {
				t.Fatal("a fresh native connection could not recover discovery")
			}
			if walk.claimRequest(rundown, "tcp:ro", "old-session", "") != nil || walk.claimRequest(rundown, "tcp:ro", "new-session", "") == nil {
				t.Fatal("native discovery response was not correlated to its sole outstanding session")
			}
		})
	}
}
