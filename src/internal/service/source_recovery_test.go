package service

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"airshift/openmos/internal/repository"
)

func TestCommittedSourceRequestAndResponseReceiptSpaces(t *testing.T) {
	f := newSourceFixture(t, "")
	request := sourceRoster("story")
	response := strings.ReplaceAll(request, "roCreate", "roList")
	original, err := f.send(t, "7", request)
	if err != nil || !bytes.Contains(original, []byte("<roStatus>OK</roStatus>")) {
		t.Fatalf("initial peer request failed: %s, %v", original, err)
	}
	if reply, err := f.send(t, "7", response); err != nil || len(reply) != 0 {
		t.Fatalf("correlated device response collided with a peer request ID: %s, %v", reply, err)
	}
	f.accept(t, sourceBody("story", sourceItem("video")))
	if !f.snapshot(t).Complete {
		t.Fatal("correlated response did not establish fresh roster coverage")
	}
	replay := func() {
		t.Helper()
		before := f.snapshot(t)
		if reply, err := f.send(t, "7", request); err != nil || !bytes.Equal(reply, original) {
			t.Fatalf("peer request lost its original ACK: %s, %v", reply, err)
		}
		if reply, err := f.send(t, "7", response); err != nil || len(reply) != 0 {
			t.Fatalf("device response lost its silent original receipt: %s, %v", reply, err)
		}
		if got := f.snapshot(t); !reflect.DeepEqual(got, before) {
			t.Fatal("original receipt replay reapplied state or refreshed coverage")
		}
	}
	replay()
	for _, operation := range []string{response, request} {
		before, _ := f.store.Checkpoint()
		reply, err := f.send(t, "7", strings.Replace(operation, "Synthetic rundown", "Changed rundown", 1))
		if err == nil || operation == request && !bytes.Contains(reply, []byte("NACK")) || operation == response && len(reply) != 0 {
			t.Fatalf("changed duplicate within its direction was accepted: %s, %v", reply, err)
		}
		after, _ := f.store.Checkpoint()
		if !reflect.DeepEqual(before.Receipts, after.Receipts) || f.snapshot(t).Complete {
			t.Fatal("changed duplicate replaced an original receipt or left coverage fresh")
		}
		order, err := f.store.RunningOrders().Get(context.Background(), "rundown")
		if err != nil || order.Slug != "Synthetic rundown" {
			t.Fatal("changed duplicate changed the retained roster")
		}
		replay()
		f.accept(t, sourceRoster("story"))
		f.accept(t, sourceBody("story", sourceItem("video")))
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store, err = repository.OpenCommitted(f.dir, f.binding, false)
	if err != nil {
		t.Fatalf("distinct request and response receipts did not survive reopen: %v", err)
	}
	f.source, err = NewCommittedSource(context.Background(), f.store, f.binding, "synthetic-token", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Establish the new connection first; its invalidation is separate from input replay.
	if err := f.source.Observe(context.Background(), SourceInput{Transport: "tcp", Scope: "tcp:ro", NCSID: "newsroom", Session: "connection"}); err != nil {
		t.Fatal(err)
	}
	replay()
	if f.snapshot(t).Complete {
		t.Fatal("replayed requests or responses restored source coverage after restart")
	}
}

func TestCommittedSourceOrderedItemsSurviveReopen(t *testing.T) {
	for _, shape := range []string{"inline-formatting", "complementary-fields"} {
		t.Run(shape, func(t *testing.T) {
			f := newSourceFixture(t, "")
			f.accept(t, sourceRoster("story"))
			first := sourceItem("first")
			if shape == "inline-formatting" {
				first = "<p><b>" + first + "</b></p>"
			} else {
				first = strings.Replace(first, "<storyItem><mosItem><itemID>first</itemID><objID>object</objID><mosID>media</mosID>", "<storyItem><itemID>first</itemID><mosID>media</mosID><mosItem><objID>object</objID>", 1)
			}
			operation := sourceBody("story", first+`<p>[CG L3\Headline]</p>`+sourceItem("last"))
			f.accept(t, operation)
			accepted := f.snapshot(t)
			if !accepted.Complete || len(accepted.Stories) != 1 || len(accepted.Stories[0].Occurrences) != 3 {
				t.Fatal("accepted ordered source was not complete")
			}
			occurrences := accepted.Stories[0].Occurrences
			if occurrences[0].ID != "first" || occurrences[1].Kind != "cue" || occurrences[2].ID != "last" {
				t.Fatalf("accepted mixed source order changed: %+v", occurrences)
			}
			if err := f.store.Close(); err != nil {
				t.Fatal(err)
			}
			var err error
			f.store, err = repository.OpenCommitted(f.dir, f.binding, false)
			if err != nil {
				t.Fatal(err)
			}
			items, err := f.store.Items().ListByStory(context.Background(), "rundown/story")
			if err != nil || len(items) != 2 {
				t.Fatalf("accepted ordered items missing from reopened repository: count=%d, %v", len(items), err)
			}
			for i, item := range items {
				position := i * 2
				if item.RawID != occurrences[position].ID || item.StoryID != "rundown/story" || item.Order != position+1 || item.ObjectID != "object" || item.Metadata["mosID"] != "media" || item.Abstract != "Full text" {
					t.Fatalf("accepted item identity, content or relative order changed after reopen: %+v", item)
				}
				if item.Media == nil || len(item.Media.Essence) != 1 || item.Media.Essence[0].URL != "https://media.invalid/clip.mp4" || len(item.ExternalMetadata) != 1 || item.ExternalMetadata[0].Payload != "<duration>unchanged</duration>" {
					t.Fatalf("accepted media or opaque metadata missing after reopen: %+v", item)
				}
			}
			cp, err := f.store.Checkpoint()
			if err != nil {
				t.Fatal(err)
			}
			state, err := readSourceState(cp.State)
			if err != nil || state.Stories[0].Raw != operation || !reflect.DeepEqual(f.snapshot(t), accepted) {
				t.Fatal("reopen changed the accepted raw source or its presence-preserving snapshot")
			}
		})
	}
}

func TestCommittedSourceCannotCertifyLegacyBodyFallback(t *testing.T) {
	f := newSourceFixture(t, "")
	f.accept(t, sourceRoster("story"))
	original := sourceBody("story", sourceItem("retained"))
	f.accept(t, original)
	reply, err := f.send(t, "uncertain-body", sourceBody("story", sourceItem("replacement")+"<unsupported/>"))
	if err == nil || !bytes.Contains(reply, []byte("NACK")) || f.snapshot(t).Complete {
		t.Fatal("legacy body fallback certified a source with unavailable order")
	}
	items, err := f.store.Items().ListByStory(context.Background(), "rundown/story")
	if err != nil || len(items) != 1 || items[0].RawID != "retained" {
		t.Fatal("failed source parsing changed the retained item view")
	}
	cp, err := f.store.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	state, err := readSourceState(cp.State)
	if err != nil || state.Stories[0].Raw != original {
		t.Fatal("failed source parsing replaced the retained raw body")
	}
}
