package repository

import (
	"context"
	"fmt"
	"testing"

	"airshift/openmos/internal/model"
)

// MOS 3.8.4: "Element order is significant. Items arrive in intended play order, and
// a MOS device must retain the sequence supplied by the NCS even if it executes items
// out of order."
//
// For a rundown, order IS meaning. A device that returns stories in a different
// sequence than the NCS sent them has corrupted the programme, not merely presented
// it oddly.
//
// This backend stores by ID in a map, so before an explicit sort the returned order
// was Go's map iteration order -- unspecified, and deliberately randomised per run.
// The Mongo backend already sorted by the same field, so the two backends disagreed
// and the in-memory one is the default, which meant the default configuration
// silently reordered rundowns.
//
// Twenty elements are used because map iteration can coincidentally match insertion
// order for very small maps; at this size a regression is essentially certain to show.
const orderingElements = 20

func TestStoriesAreReturnedInNCSOrder(t *testing.T) {
	repo := NewMemoryStoryRepository()
	ctx := context.Background()

	// Insert in a deliberately jumbled sequence so that "returned in insertion order"
	// cannot be mistaken for "returned in NCS order".
	for _, n := range shuffledSequence(orderingElements) {
		if _, err := repo.Create(ctx, &model.Story{
			ID:             fmt.Sprintf("RO-1/STORY-%02d", n),
			RawID:          fmt.Sprintf("STORY-%02d", n),
			RunningOrderID: "RO-1",
			Slug:           fmt.Sprintf("Story %02d", n),
			Order:          n,
		}); err != nil {
			t.Fatalf("create failed: %v", err)
		}
	}

	stories, err := repo.ListByRunningOrder(ctx, "RO-1")
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(stories) != orderingElements {
		t.Fatalf("got %d stories, want %d", len(stories), orderingElements)
	}
	for i, story := range stories {
		if want := i + 1; story.Order != want {
			t.Fatalf("position %d holds Order %d, want %d: NCS-supplied order was not preserved",
				i, story.Order, want)
		}
	}
}

func TestItemsAreReturnedInNCSOrder(t *testing.T) {
	repo := NewMemoryItemRepository()
	ctx := context.Background()

	for _, n := range shuffledSequence(orderingElements) {
		if _, err := repo.Create(ctx, &model.Item{
			ID:      fmt.Sprintf("STORY-1/ITEM-%02d", n),
			RawID:   fmt.Sprintf("ITEM-%02d", n),
			StoryID: "STORY-1",
			Order:   n,
		}); err != nil {
			t.Fatalf("create failed: %v", err)
		}
	}

	items, err := repo.ListByStory(ctx, "STORY-1")
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(items) != orderingElements {
		t.Fatalf("got %d items, want %d", len(items), orderingElements)
	}
	for i, item := range items {
		if want := i + 1; item.Order != want {
			t.Fatalf("position %d holds Order %d, want %d: play order within the story was not preserved",
				i, item.Order, want)
		}
	}
}

// TestOrderingIsStableAcrossRepeatedReads guards the specific failure mode: map
// iteration order varies between calls, so a single passing read proves nothing.
func TestOrderingIsStableAcrossRepeatedReads(t *testing.T) {
	repo := NewMemoryStoryRepository()
	ctx := context.Background()
	for _, n := range shuffledSequence(orderingElements) {
		if _, err := repo.Create(ctx, &model.Story{
			ID:             fmt.Sprintf("RO-1/STORY-%02d", n),
			RunningOrderID: "RO-1",
			Order:          n,
		}); err != nil {
			t.Fatalf("create failed: %v", err)
		}
	}

	var first []string
	for read := 0; read < 25; read++ {
		stories, err := repo.ListByRunningOrder(ctx, "RO-1")
		if err != nil {
			t.Fatalf("list failed: %v", err)
		}
		ids := make([]string, len(stories))
		for i, s := range stories {
			ids[i] = s.ID
		}
		if first == nil {
			first = ids
			continue
		}
		for i := range ids {
			if ids[i] != first[i] {
				t.Fatalf("read %d differs from the first read at position %d: %q vs %q",
					read, i, ids[i], first[i])
			}
		}
	}
}

// shuffledSequence returns 1..n in an order that is not ascending, without needing a
// random source: interleaving the halves is enough to make "insertion order" and
// "NCS order" distinguishable.
func shuffledSequence(n int) []int {
	out := make([]int, 0, n)
	half := (n + 1) / 2
	for i := 0; i < half; i++ {
		out = append(out, half-i)
		if second := half + i + 1; second <= n {
			out = append(out, second)
		}
	}
	return out
}
