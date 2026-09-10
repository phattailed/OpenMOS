package service

import (
	"context"
	stdxml "encoding/xml"
	"testing"

	"airshift/openmos/internal/xml"
)

// A MOVE arriving from a live NCS, using the frame captured when OpenMOS asked ENPS to reorder a
// rundown and ENPS pushed the change back (doc/interop §42).
//
// This is the whole reason the bug went unnoticed: the operation was acknowledged OK and did nothing.
// The roElementAction family used bare wire identifiers as storage keys while the roCreate and
// roStorySend family used the composite storyPersistenceID, so the move set never matched any stored
// story, the order was rebuilt unchanged, and success was reported.
const liveMoveFrame = `<mos>
<mosID>openmos.example.mos</mosID>
<ncsID>NCS-HOST</ncsID>
<messageID>148</messageID>
<roElementAction operation="MOVE"><roID>NCS-HOST;P_STORYTELLING\W;2D526A13</roID>
<element_target>
<storyID>NCS-HOST;P_STORYTELLING\W\R_2D526A13;A0CEE368</storyID>
</element_target>
<element_source>
<storyID>NCS-HOST;P_STORYTELLING\W\R_2D526A13;593BEF12</storyID>
</element_source>
</roElementAction>
</mos>`

const (
	moveROID    = `NCS-HOST;P_STORYTELLING\W;2D526A13`
	moveTargetS = `NCS-HOST;P_STORYTELLING\W\R_2D526A13;A0CEE368`
	moveSourceS = `NCS-HOST;P_STORYTELLING\W\R_2D526A13;593BEF12`
	moveThirdS  = `NCS-HOST;P_STORYTELLING\W\R_2D526A13;6BC5B1F7`
)

// seedThreeStories builds a running order through the ordinary ingest path, so the stories are keyed
// exactly as a live rundown's are. Using the repositories directly would key them however the test
// chose and would not have caught this.
func seedThreeStories(t *testing.T, svc *MOSService, ctx context.Context) {
	t.Helper()
	if err := svc.ProcessRunningOrderInfo(ctx, xml.RunningOrderInfo{
		ID:   moveROID,
		Slug: "Tacit-test",
		Stories: []xml.StoryInfo{
			{ID: moveTargetS, Slug: "first"},
			{ID: moveSourceS, Slug: "has the graphics item"},
			{ID: moveThirdS, Slug: "third"},
		},
	}, "openmos.example.mos"); err != nil {
		t.Fatalf("seed running order: %v", err)
	}
}

func slugOrder(t *testing.T, ctx context.Context, svc *MOSService, roID string) []string {
	t.Helper()
	stories, err := svc.storyRepo.ListByRunningOrder(ctx, roID)
	if err != nil {
		t.Fatalf("list stories: %v", err)
	}
	out := make([]string, 0, len(stories))
	for _, s := range stories {
		out = append(out, s.Slug)
	}
	return out
}

func TestLiveMoveActuallyReordersStories(t *testing.T) {
	svc, _, _ := newStoryTestService(t)
	ctx := context.Background()
	seedThreeStories(t, svc, ctx)

	before := slugOrder(t, ctx, svc, moveROID)
	if len(before) != 3 {
		t.Fatalf("expected 3 seeded stories, got %d: %v", len(before), before)
	}
	if before[0] != "first" {
		t.Fatalf("seed order unexpected: %v", before)
	}

	var env xml.Envelope
	if err := stdxml.Unmarshal([]byte(liveMoveFrame), &env); err != nil {
		t.Fatalf("unmarshal captured MOVE: %v", err)
	}
	msg, err := env.Message()
	if err != nil {
		t.Fatalf("parse captured MOVE: %v", err)
	}
	action, ok := msg.(xml.ROElementAction)
	if !ok {
		t.Fatalf("parsed as %T, want ROElementAction", msg)
	}

	if err := svc.ProcessElementAction(ctx, action); err != nil {
		t.Fatalf("ProcessElementAction: %v", err)
	}

	after := slugOrder(t, ctx, svc, moveROID)
	if len(after) != 3 {
		t.Fatalf("story count changed: %v", after)
	}
	// The moved story must now sit ahead of the target it was moved before.
	if after[0] != "has the graphics item" {
		t.Errorf("MOVE did not reorder. order = %v, want the moved story first.\n"+
			"An unchanged order with no error is the original defect: wire identifiers were compared "+
			"against composite storage keys, so nothing matched and OK was returned.", after)
	}
	if after[1] != "first" {
		t.Errorf("target story should follow the moved one; order = %v", after)
	}
}

// Refusing is required, not optional. Applying part of a MOVE leaves our sequence disagreeing with
// the NCS's, and the spec is explicit that a missed message guarantees every later one compounds the
// divergence. The caller turns this error into a NACK and a roReq resync.
func TestMoveRefusesStoriesNotHeld(t *testing.T) {
	svc, _, _ := newStoryTestService(t)
	ctx := context.Background()
	seedThreeStories(t, svc, ctx)

	err := svc.MoveStories(ctx, moveROID,
		&xml.ElementTarget{StoryID: moveTargetS},
		[]string{moveSourceS, `NCS-HOST;P_STORYTELLING\W\R_2D526A13;NOT-HELD`})
	if err == nil {
		t.Fatal("a move naming a story we do not hold must be refused, not partially applied")
	}
	if order := slugOrder(t, ctx, svc, moveROID); order[0] != "first" {
		t.Errorf("a refused move must not have reordered anything; order = %v", order)
	}
}

// Ingest must not mint a second record for a story it already holds. Two records for one story under
// different identity conventions is what produced phantom stories and gapped ordering in the live
// rundown.
func TestElementActionDoesNotDuplicateStories(t *testing.T) {
	svc, _, _ := newStoryTestService(t)
	ctx := context.Background()
	seedThreeStories(t, svc, ctx)

	// REPLACE the middle story with itself, which is what an editing session produces.
	err := svc.ReplaceStories(ctx, moveROID,
		&xml.ElementTarget{StoryID: moveSourceS},
		[]xml.StoryInfo{{ID: moveSourceS, Slug: "has the graphics item"}})
	if err != nil {
		t.Fatalf("ReplaceStories: %v", err)
	}

	stories, err := svc.storyRepo.ListByRunningOrder(ctx, moveROID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(stories) != 3 {
		t.Fatalf("replacing a story in place produced %d stories, want 3: a second record was minted "+
			"under a different identity convention", len(stories))
	}
	seen := map[string]int{}
	for _, s := range stories {
		seen[s.RawID]++
	}
	for raw, n := range seen {
		if n > 1 {
			t.Errorf("story %s has %d records", raw, n)
		}
	}
}
