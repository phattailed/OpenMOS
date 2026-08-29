package server

import (
	"context"
	"strings"
	"testing"

	mosxml "airshift/openmos/internal/xml"
)

// mosScope propagation, and the emission gap found while implementing it.
//
// The scope rule is a widening hierarchy, from doc/mos-protocol-source-synthesis.md: OBJECT
// "stays with object/list/search use", STORY "may enter an item reference in a story", PLAYLIST
// "may also enter running-order construction messages".
//
// The gap: mosExternalMetadata was parsed and stored on ingest, but the conversion back to wire
// form was DEAD CODE -- defined, never called, not referenced by any test. Go does not complain
// about an unused function, so it compiled cleanly for as long as it existed. Every roList
// OpenMOS built therefore dropped all vendor metadata. Pull recovery hands a peer its own state
// back, so nothing noticed until something compared what went in against what came out.
//
// These tests do that comparison over the wire, which is why they are here rather than as unit
// tests on the filter alone.

// TestScopePermitsLevel pins the hierarchy directly.
func TestScopePermitsLevel(t *testing.T) {
	cases := []struct {
		scope string
		ro    bool
		story bool
		item  bool
	}{
		// OBJECT belongs with object/list/search use, so nowhere in a running order.
		{"OBJECT", false, false, false},
		// STORY reaches a story and an item reference within it, but not the running order.
		{"STORY", false, true, true},
		// PLAYLIST reaches everything, including running-order construction.
		{"PLAYLIST", true, true, true},
		// Absent scope is kept: mosScope is optional, and omitting it is not a request to be
		// discarded.
		{"", true, true, true},
		// Unknown scope is kept for the same reason.
		{"SOMETHING_NEW", true, true, true},
		// Case and whitespace are tolerated; rejecting "Playlist" would cost real data.
		{" playlist ", true, true, true},
		{"Story", false, true, true},
	}

	for _, c := range cases {
		got := [3]bool{
			mosxml.ScopePermitsLevel(c.scope, mosxml.LevelRunningOrder),
			mosxml.ScopePermitsLevel(c.scope, mosxml.LevelStory),
			mosxml.ScopePermitsLevel(c.scope, mosxml.LevelItem),
		}
		want := [3]bool{c.ro, c.story, c.item}
		if got != want {
			t.Errorf("scope %q: ro/story/item = %v, want %v", c.scope, got, want)
		}
	}
}

// TestFilterMetadataForLevelPreservesOrderAndDropsNothingElse checks the filter does not reorder
// or mangle what it keeps. Element order is significant in MOS, and a vendor reading its own
// blocks back may depend on their sequence.
func TestFilterMetadataForLevelPreservesOrderAndDropsNothingElse(t *testing.T) {
	blocks := []mosxml.MosExternalMetadata{
		{MosScope: "PLAYLIST", MosSchema: "s1", MosPayload: mosxml.MosPayload{Raw: "<a>1</a>"}},
		{MosScope: "STORY", MosSchema: "s2", MosPayload: mosxml.MosPayload{Raw: "<b>2</b>"}},
		{MosScope: "PLAYLIST", MosSchema: "s3", MosPayload: mosxml.MosPayload{Raw: "<c>3</c>"}},
		{MosScope: "OBJECT", MosSchema: "s4", MosPayload: mosxml.MosPayload{Raw: "<d>4</d>"}},
	}

	ro := mosxml.FilterMetadataForLevel(blocks, mosxml.LevelRunningOrder)
	if len(ro) != 2 || ro[0].MosSchema != "s1" || ro[1].MosSchema != "s3" {
		t.Fatalf("running-order level kept %d blocks %v, want s1 then s3", len(ro), schemas(ro))
	}
	if ro[0].MosPayload.Raw != "<a>1</a>" {
		t.Errorf("payload was altered: %q", ro[0].MosPayload.Raw)
	}

	story := mosxml.FilterMetadataForLevel(blocks, mosxml.LevelStory)
	if got := schemas(story); strings.Join(got, ",") != "s1,s2,s3" {
		t.Errorf("story level kept %v, want s1,s2,s3", got)
	}

	// Nothing permitted must yield nil, not an empty slice, so omitempty removes the element.
	only := []mosxml.MosExternalMetadata{{MosScope: "OBJECT"}}
	if got := mosxml.FilterMetadataForLevel(only, mosxml.LevelRunningOrder); got != nil {
		t.Errorf("expected nil for a fully-filtered set, got %#v", got)
	}
}

func schemas(blocks []mosxml.MosExternalMetadata) []string {
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, b.MosSchema)
	}
	return out
}

// TestMetadataSurvivesTheRoundTripToTheWire is the test that would have caught the dead emission.
//
// It ingests a roCreate carrying metadata at all three levels, then asks for the running order
// back with roReq and inspects the roList actually produced. Storing the metadata is not enough;
// a peer recovering from us has to receive it.
func TestMetadataSurvivesTheRoundTripToTheWire(t *testing.T) {
	deps, _ := walkDeps(t)
	ctx := context.Background()
	r := &recordingResponder{label: "peer"}

	create := mosxml.RunningOrderInfo{
		ID:   "RO-META",
		Slug: "Metadata round trip",
		MosExternalMetadata: []mosxml.MosExternalMetadata{
			{MosScope: "PLAYLIST", MosSchema: "http://example/ro",
				MosPayload: mosxml.MosPayload{Raw: "<ro-level><keep>yes</keep></ro-level>"}},
			// STORY-scoped at running-order level: stored, but must not be emitted there.
			{MosScope: "STORY", MosSchema: "http://example/wrong-level",
				MosPayload: mosxml.MosPayload{Raw: "<strip-me/>"}},
		},
		Stories: []mosxml.StoryInfo{{
			ID:   "RO-META-STORY-1",
			Slug: "First",
			MosExternalMetadata: []mosxml.MosExternalMetadata{
				{MosScope: "STORY", MosSchema: "http://example/story",
					MosPayload: mosxml.MosPayload{Raw: "<story-level><keep>yes</keep></story-level>"}},
				// OBJECT-scoped anywhere in a running order must not be emitted.
				{MosScope: "OBJECT", MosSchema: "http://example/object",
					MosPayload: mosxml.MosPayload{Raw: "<object-only/>"}},
			},
			Items: []mosxml.ItemInfo{{
				ID:       "ITEM-1",
				Slug:     "Clip",
				ObjectID: "OBJ-1",
				MosID:    "openmos.example.mos",
				MosExternalMetadata: []mosxml.MosExternalMetadata{
					{MosScope: "STORY", MosSchema: "http://example/item",
						MosPayload: mosxml.MosPayload{Raw: "<item-level><keep>yes</keep></item-level>"}},
				},
			}},
		}},
	}

	if err := deps.service.ProcessRunningOrderInfo(ctx, create, deps.mosID); err != nil {
		t.Fatalf("ingest roCreate: %v", err)
	}

	// Ask for it back, exactly as a recovering peer would.
	handled, err := dispatchRunningOrder(ctx, deps, r, mosxml.ROReq{ROID: "RO-META"})
	if err != nil || !handled {
		t.Fatalf("roReq: handled=%v err=%v", handled, err)
	}

	var list *mosxml.ROList
	for _, msg := range r.sent {
		if l, ok := msg.(mosxml.ROList); ok {
			list = &l
			break
		}
	}
	if list == nil {
		t.Fatalf("no roList was produced; responder sent %d message(s)", len(r.sent))
	}

	// Running-order level: the PLAYLIST block must be present with its payload intact.
	if len(list.MosExternalMetadata) != 1 {
		t.Fatalf("roList carried %d running-order metadata blocks, want 1 (the PLAYLIST one). "+
			"If this is 0, the wire emission is missing, not merely misfiltered.",
			len(list.MosExternalMetadata))
	}
	roBlock := list.MosExternalMetadata[0]
	if roBlock.MosSchema != "http://example/ro" {
		t.Errorf("wrong block survived at running-order level: %s", roBlock.MosSchema)
	}
	if !strings.Contains(roBlock.MosPayload.Raw, "<keep>yes</keep>") {
		t.Errorf("running-order payload was lost or altered: %q", roBlock.MosPayload.Raw)
	}

	// Story level: the STORY block survives, the OBJECT block does not.
	if len(list.Stories) != 1 {
		t.Fatalf("roList carried %d stories, want 1", len(list.Stories))
	}
	story := list.Stories[0]
	if len(story.MosExternalMetadata) != 1 {
		t.Fatalf("story carried %d metadata blocks, want 1: %v",
			len(story.MosExternalMetadata), schemas(story.MosExternalMetadata))
	}
	if story.MosExternalMetadata[0].MosSchema != "http://example/story" {
		t.Errorf("wrong block survived at story level: %s", story.MosExternalMetadata[0].MosSchema)
	}

	// Item level: the STORY-scoped block is permitted on an item reference.
	if len(story.Items) != 1 {
		t.Fatalf("story carried %d items, want 1", len(story.Items))
	}
	item := story.Items[0]
	if len(item.MosExternalMetadata) != 1 {
		t.Fatalf("item carried %d metadata blocks, want 1", len(item.MosExternalMetadata))
	}
	if !strings.Contains(item.MosExternalMetadata[0].MosPayload.Raw, "<keep>yes</keep>") {
		t.Errorf("item payload was lost: %q", item.MosExternalMetadata[0].MosPayload.Raw)
	}
}

// TestStoredMetadataKeepsEverythingRegardlessOfScope pins the other half of the policy. Scope is
// enforced on emission only; storage stays faithful, because discarding what a peer sent would
// break lenient-inbound and lose data we may have to hand back.
func TestStoredMetadataKeepsEverythingRegardlessOfScope(t *testing.T) {
	deps, _ := walkDeps(t)
	ctx := context.Background()

	create := mosxml.RunningOrderInfo{
		ID:   "RO-KEEP",
		Slug: "Storage keeps everything",
		MosExternalMetadata: []mosxml.MosExternalMetadata{
			{MosScope: "OBJECT", MosSchema: "a"},
			{MosScope: "STORY", MosSchema: "b"},
			{MosScope: "PLAYLIST", MosSchema: "c"},
		},
	}
	if err := deps.service.ProcessRunningOrderInfo(ctx, create, deps.mosID); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	ro, _, err := deps.service.GetRunningOrderWithStories(ctx, "RO-KEEP")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(ro.ExternalMetadata) != 3 {
		t.Errorf("storage kept %d of 3 blocks; scope must be enforced on emission, not ingest",
			len(ro.ExternalMetadata))
	}
}
