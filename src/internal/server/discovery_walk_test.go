package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	mosxml "airshift/openmos/internal/xml"
)

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
