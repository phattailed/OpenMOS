package service

import (
	"context"
	stdxml "encoding/xml"
	"errors"
	"testing"

	"airshift/openmos/internal/model"
	"airshift/openmos/internal/xml"
)

// On-air tracking, using the roElementStat frames a live ENPS emits when an operator drags the timing
// bar. Captured verbatim; only identifiers are replaced.
//
// Each bar move produces a PAIR: STOP on the story being left, PLAY on the story being entered.
const (
	statPlayFrame = `<mos><mosID>openmos.example.mos</mosID><ncsID>NCS-HOST</ncsID>
<messageID>328</messageID><roElementStat element="STORY">
<roID>NCS-HOST;P_STORYTELLING\W;2D526A13</roID>
<storyID>NCS-HOST;P_STORYTELLING\W\R_2D526A13;593BEF12</storyID>
<status>PLAY</status><time>2026-09-10T20:20:30</time></roElementStat></mos>`

	statStopFrame = `<mos><mosID>openmos.example.mos</mosID><ncsID>NCS-HOST</ncsID>
<messageID>327</messageID><roElementStat element="STORY">
<roID>NCS-HOST;P_STORYTELLING\W;2D526A13</roID>
<storyID>NCS-HOST;P_STORYTELLING\W\R_2D526A13;A0CEE368</storyID>
<status>STOP</status><time>2026-09-10T20:20:28</time></roElementStat></mos>`
)

func parseStat(t *testing.T, frame string) xml.ROElementStat {
	t.Helper()
	var env xml.Envelope
	if err := stdxml.Unmarshal([]byte(frame), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msg, err := env.Message()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	stat, ok := msg.(xml.ROElementStat)
	if !ok {
		t.Fatalf("parsed as %T, want ROElementStat", msg)
	}
	return stat
}

func TestTimingBarSetsOnAirStory(t *testing.T) {
	svc, _, _ := newStoryTestService(t)
	ctx := context.Background()
	seedThreeStories(t, svc, ctx)

	story, since, err := svc.OnAirStory(ctx, moveROID)
	if err != nil {
		t.Fatalf("OnAirStory: %v", err)
	}
	if story != nil {
		t.Fatalf("nothing should be on air before any report; got %q", story.Slug)
	}

	if err := svc.ProcessElementStatus(ctx, parseStat(t, statPlayFrame)); err != nil {
		t.Fatalf("PLAY: %v", err)
	}

	story, since, err = svc.OnAirStory(ctx, moveROID)
	if err != nil {
		t.Fatalf("OnAirStory: %v", err)
	}
	if story == nil {
		t.Fatal("a PLAY report must put a story on air")
	}
	if story.Slug != "has the graphics item" {
		t.Errorf("on air = %q, want the story the PLAY named", story.Slug)
	}
	if story.Status != model.StatusType("PLAY") {
		t.Errorf("story status = %q, want PLAY", story.Status)
	}

	// The time must come from the report, not from arrival, or a delayed message misdates the
	// transition. MOS timestamps need ParseMOSTime; time.Parse cannot read the format.
	if since == nil {
		t.Fatal("OnAirSince must be set from the report's time")
	}
	if got := since.Format("2006-01-02T15:04:05"); got != "2026-09-10T20:20:30" {
		t.Errorf("OnAirSince = %s, want the report's own time 2026-09-10T20:20:30", got)
	}
}

// The pair's order is not guaranteed. A STOP for the story being left can arrive AFTER the PLAY for the
// story being entered, and clearing unconditionally would blank a pointer that had just been set --
// leaving nothing on air while the bar is plainly sitting somewhere.
func TestStopForAnotherStoryDoesNotClearOnAir(t *testing.T) {
	svc, _, _ := newStoryTestService(t)
	ctx := context.Background()
	seedThreeStories(t, svc, ctx)

	// PLAY the new story first, then the STOP for the old one arrives late.
	if err := svc.ProcessElementStatus(ctx, parseStat(t, statPlayFrame)); err != nil {
		t.Fatalf("PLAY: %v", err)
	}
	if err := svc.ProcessElementStatus(ctx, parseStat(t, statStopFrame)); err != nil {
		t.Fatalf("STOP: %v", err)
	}

	story, _, err := svc.OnAirStory(ctx, moveROID)
	if err != nil {
		t.Fatalf("OnAirStory: %v", err)
	}
	if story == nil {
		t.Fatal("a STOP naming a DIFFERENT story must not clear the on-air pointer; the bar is still " +
			"somewhere and reporting nothing on air is wrong")
	}
	if story.Slug != "has the graphics item" {
		t.Errorf("on air = %q, want the story the PLAY named", story.Slug)
	}
}

// A STOP for the story that IS on air clears it.
func TestStopForTheOnAirStoryClearsIt(t *testing.T) {
	svc, _, _ := newStoryTestService(t)
	ctx := context.Background()
	seedThreeStories(t, svc, ctx)

	if err := svc.ProcessElementStatus(ctx, parseStat(t, statPlayFrame)); err != nil {
		t.Fatalf("PLAY: %v", err)
	}
	// Same story, now stopping.
	stop := parseStat(t, statPlayFrame)
	stop.Status = "STOP"
	if err := svc.ProcessElementStatus(ctx, stop); err != nil {
		t.Fatalf("STOP: %v", err)
	}

	story, since, err := svc.OnAirStory(ctx, moveROID)
	if err != nil {
		t.Fatalf("OnAirStory: %v", err)
	}
	if story != nil {
		t.Errorf("on air = %q, want nothing after the on-air story stopped", story.Slug)
	}
	if since != nil {
		t.Error("OnAirSince must be cleared alongside the pointer")
	}
}

// Dragging the bar down the rundown must move the pointer, not accumulate stories.
func TestBarMovesThroughStories(t *testing.T) {
	svc, _, _ := newStoryTestService(t)
	ctx := context.Background()
	seedThreeStories(t, svc, ctx)

	for _, target := range []struct {
		wire string
		slug string
	}{
		{moveTargetS, "first"},
		{moveSourceS, "has the graphics item"},
		{moveThirdS, "third"},
	} {
		stat := parseStat(t, statPlayFrame)
		stat.StoryID = target.wire
		if err := svc.ProcessElementStatus(ctx, stat); err != nil {
			t.Fatalf("PLAY %s: %v", target.slug, err)
		}
		story, _, err := svc.OnAirStory(ctx, moveROID)
		if err != nil {
			t.Fatalf("OnAirStory: %v", err)
		}
		if story == nil || story.Slug != target.slug {
			got := "<nil>"
			if story != nil {
				got = story.Slug
			}
			t.Errorf("on air = %q, want %q", got, target.slug)
		}
	}
}

// A status report naming a story we do not hold is lost synchronisation, so the transport can pull a
// rebuild rather than silently ignoring the report.
func TestUnknownStoryStatusReportsLostSync(t *testing.T) {
	svc, _, _ := newStoryTestService(t)
	ctx := context.Background()
	seedThreeStories(t, svc, ctx)

	stat := parseStat(t, statPlayFrame)
	stat.StoryID = `NCS-HOST;P_STORYTELLING\W\R_2D526A13;NOT-HELD`
	err := svc.ProcessElementStatus(ctx, stat)
	if err == nil {
		t.Fatal("a report for a story we do not hold must be reported, not ignored")
	}
	var unknown *UnknownRunningOrderError
	if !errors.As(err, &unknown) {
		t.Errorf("error %q is not recognisable as lost synchronisation, so no rebuild is requested", err)
	}
}

// Running-order level reports carry control state such as "MANUAL CTRL" rather than an on-air position.
func TestRunningOrderStatusRecorded(t *testing.T) {
	svc, _, _ := newStoryTestService(t)
	ctx := context.Background()
	seedThreeStories(t, svc, ctx)

	if err := svc.ProcessElementStatus(ctx, xml.ROElementStat{
		Element: "RO", ROID: moveROID, Status: "MANUAL CTRL", Time: "2026-09-10T20:20:30",
	}); err != nil {
		t.Fatalf("RO status: %v", err)
	}

	ro, _, err := svc.GetRunningOrderWithStories(ctx, moveROID)
	if err != nil {
		t.Fatalf("GetRunningOrderWithStories: %v", err)
	}
	if ro.Status != model.StatusType("MANUAL CTRL") {
		t.Errorf("RO status = %q, want MANUAL CTRL", ro.Status)
	}
	// An RO-level report must not disturb the on-air pointer.
	if ro.OnAirStoryID != "" {
		t.Errorf("an RO-level status set the on-air story to %q", ro.OnAirStoryID)
	}
}
