package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"airshift/openmos/internal/model"
)

// Running orders must survive a restart, and must come back in the order the NCS gave them.
//
// The ordering half is not incidental. The in-memory backend once returned Go map order for
// stories and items while Mongo sorted correctly, so the default backend silently reordered
// rundowns; the specification calls element order "essential". A snapshot that round-trips
// through a map would reintroduce exactly that defect, which is why the persisted shape uses
// slices and why this test checks sequence rather than membership.

func seedRundown(t *testing.T, d *Durable) {
	t.Helper()
	ctx := context.Background()

	if _, err := d.RunningOrders().Create(ctx, &model.RunningOrder{
		ID:   "RO-1",
		Slug: "Evening bulletin",
		ExternalMetadata: []model.ExternalMetadata{
			{Scope: "PLAYLIST", Schema: "http://example/ro", Payload: "<keep/>"},
		},
	}); err != nil {
		t.Fatalf("create running order: %v", err)
	}

	// Deliberately not in alphabetical or numeric order, so a map-backed or sorted
	// implementation cannot accidentally pass.
	for i, slug := range []string{"Headline", "Weather", "Sport", "And finally"} {
		storyID := []string{"S-40", "S-10", "S-30", "S-20"}[i]
		if _, err := d.Stories().Create(ctx, &model.Story{
			ID:             storyID,
			RunningOrderID: "RO-1",
			Slug:           slug,
			Number:         storyID,
			// Order carries the NCS-supplied sequence. It is set deliberately against
			// alphabetical ID order, so an implementation that sorted by ID instead of honouring
			// this field would fail rather than pass by coincidence.
			Order: i,
		}); err != nil {
			t.Fatalf("create story %s: %v", storyID, err)
		}
		if _, err := d.Items().Create(ctx, &model.Item{
			ID:       storyID + "-ITEM",
			StoryID:  storyID,
			Slug:     slug + " clip",
			ObjectID: "OBJ-" + storyID,
			Order:    i,
		}); err != nil {
			t.Fatalf("create item for %s: %v", storyID, err)
		}
	}
}

func TestRunningOrdersSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	first := OpenDurable(dir)
	if first.Degraded() {
		t.Fatalf("degraded with a usable directory %s", dir)
	}
	seedRundown(t, first)

	// Restart.
	second := OpenDurable(dir)
	if second.Degraded() {
		t.Fatalf("degraded after reopening %s", dir)
	}

	ro, err := second.RunningOrders().Get(ctx, "RO-1")
	if err != nil {
		t.Fatalf("running order did not survive the restart: %v", err)
	}
	if ro.Slug != "Evening bulletin" {
		t.Errorf("slug came back as %q", ro.Slug)
	}
	if len(ro.ExternalMetadata) != 1 || ro.ExternalMetadata[0].Payload != "<keep/>" {
		t.Errorf("external metadata did not survive the restart: %#v", ro.ExternalMetadata)
	}

	stories, err := second.Stories().ListByRunningOrder(ctx, "RO-1")
	if err != nil {
		t.Fatalf("list stories: %v", err)
	}
	if len(stories) != 4 {
		t.Fatalf("got %d stories after restart, want 4", len(stories))
	}

	// Order must be the order they were created in, not sorted and not map order.
	want := []string{"S-40", "S-10", "S-30", "S-20"}
	for i, story := range stories {
		if story.ID != want[i] {
			var got []string
			for _, s := range stories {
				got = append(got, s.ID)
			}
			t.Fatalf("story order after restart was %v, want %v. Element order is essential in "+
				"MOS and a device must retain the NCS-supplied sequence.", got, want)
		}
	}

	// And each story's item must still be attached to it.
	for _, story := range stories {
		items, err := second.Items().ListByStory(ctx, story.ID)
		if err != nil {
			t.Fatalf("list items for %s: %v", story.ID, err)
		}
		if len(items) != 1 || items[0].ID != story.ID+"-ITEM" {
			t.Errorf("items for %s did not survive correctly: %#v", story.ID, items)
		}
	}
}

func TestDeletionsSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	first := OpenDurable(dir)
	seedRundown(t, first)
	if err := first.RunningOrders().Delete(ctx, "RO-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// A snapshot that only ever grows would resurrect deleted state, which is worse than not
	// persisting at all: the NCS would be told we hold a running order it has removed.
	second := OpenDurable(dir)
	if _, err := second.RunningOrders().Get(ctx, "RO-1"); err == nil {
		t.Error("a deleted running order came back after a restart")
	}
	ros, err := second.RunningOrders().List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ros) != 0 {
		t.Errorf("expected no running orders after restart, got %d", len(ros))
	}
}

func TestDurableDegradesWithoutADirectory(t *testing.T) {
	d := OpenDurable("")
	if !d.Degraded() {
		t.Error("an empty state directory should report degraded")
	}
	// It must still function as a repository.
	ctx := context.Background()
	if _, err := d.RunningOrders().Create(ctx, &model.RunningOrder{ID: "RO-1", Slug: "S"}); err != nil {
		t.Fatalf("degraded repository did not function: %v", err)
	}
	if _, err := d.RunningOrders().Get(ctx, "RO-1"); err != nil {
		t.Errorf("degraded repository lost in-memory state: %v", err)
	}
}

// TestCorruptSnapshotIsSkippedNotFatal pins the choice to start empty rather than refuse. Pull
// recovery can rebuild local state by asking the NCS; failing to start cannot.
func TestCorruptSnapshotIsSkippedNotFatal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runningorders.json"),
		[]byte("{this is not json"), 0o644); err != nil {
		t.Fatalf("write corrupt snapshot: %v", err)
	}

	d := OpenDurable(dir)
	if d.Degraded() {
		t.Error("a corrupt snapshot should not disable persistence for the rest of the run")
	}
	ctx := context.Background()
	ros, err := d.RunningOrders().List(ctx)
	if err != nil {
		t.Fatalf("list after corrupt snapshot: %v", err)
	}
	if len(ros) != 0 {
		t.Errorf("expected empty state after a corrupt snapshot, got %d running orders", len(ros))
	}

	// And it must recover: a write now should persist and reload.
	if _, err := d.RunningOrders().Create(ctx, &model.RunningOrder{ID: "RO-NEW", Slug: "New"}); err != nil {
		t.Fatalf("create after corrupt snapshot: %v", err)
	}
	again := OpenDurable(dir)
	if _, err := again.RunningOrders().Get(ctx, "RO-NEW"); err != nil {
		t.Errorf("state written after a corrupt snapshot did not persist: %v", err)
	}
}
