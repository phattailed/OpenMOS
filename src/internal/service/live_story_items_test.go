package service

import (
	"context"
	"strings"
	"testing"

	stdxml "encoding/xml"

	"airshift/openmos/internal/events"
	"airshift/openmos/internal/model"
	"airshift/openmos/internal/repository"
	"airshift/openmos/internal/xml"
)

// newStoryTestService builds a service over the in-memory repositories, which already implement
// ordering correctly, so these tests exercise the real persistence path rather than a double.
func newStoryTestService(t *testing.T) (*MOSService, repository.StoryRepository, repository.ItemRepository) {
	t.Helper()
	stories := repository.NewMemoryStoryRepository()
	items := repository.NewMemoryItemRepository()
	svc := NewMOSService(
		repository.NewMemoryRunningOrderRepository(),
		stories, items,
		repository.NewMemoryObjectRepository(),
		events.NewEventBus(),
	)
	return svc, stories, items
}

// itemsFor walks running order to story to item through the public interfaces, so the test does
// not depend on how persistence identifiers are derived.
func itemsFor(t *testing.T, ctx context.Context, stories repository.StoryRepository,
	items repository.ItemRepository, roID string) []*model.Item {
	t.Helper()
	storyList, err := stories.ListByRunningOrder(ctx, roID)
	if err != nil {
		t.Fatalf("list stories: %v", err)
	}
	var out []*model.Item
	for _, st := range storyList {
		found, err := items.ListByStory(ctx, st.ID)
		if err != nil {
			t.Fatalf("list items for %s: %v", st.ID, err)
		}
		out = append(out, found...)
	}
	return out
}

// Items arriving inside a story body, using the shape a live ENPS actually sends.
//
// Three defects met here, all found by pointing the standing appliance at a real NCS and sending
// one graphics item (doc/interop §40):
//
//  1. storyItem was only modelled as a child of <p>. A live ENPS emits it as a DIRECT CHILD of
//     storyBody, so encoding/xml had nowhere to unmarshal it and every item was silently dropped.
//  2. The fields are nested one level deeper, inside <mosItem>, not flat on <storyItem>.
//  3. processStoryBody built the item slice and threw it away, with a comment promising
//     persistence "in a later step". So even a correctly-shaped item was only logged.
//
// The fixture below is the captured frame, trimmed and with identifiers replaced by placeholders.
// Structure is verbatim -- including the empty paragraphs around the item and the RO-level
// PLAYLIST metadata block -- because the structure is exactly what was wrong.
const liveENPSStorySend = `<mos>
<mosID>openmos.example.mos</mosID>
<ncsID>NCS-HOST</ncsID>
<messageID>4211</messageID>
<roStorySend>
<roID>NCS-HOST;P_STORYTELLING\W;2D526A13-0B19-4B40-A98C-C69221CEFBC7</roID>
<storyID>NCS-HOST;P_STORYTELLING\W\R_2D526A13;593BEF12-2F88-4201-81FB-F477EB3CF8BF</storyID>
<storySlug>New Row 9 Storytelling Admin</storySlug>
<storyNum></storyNum>
<storyBody><p> </p>
<p> </p><storyItem><mosItem><itemID>1</itemID><itemSlug>LOWER THIRD: Mayor Jones / Transit Vote</itemSlug><objID>OM-T99124A</objID><mosID>openmos.example.mos</mosID><mosAbstract>LOWER THIRD: Mayor Jones / Transit Vote</mosAbstract><itemEdDur>150</itemEdDur><itemChannel>A</itemChannel><mosExternalMetadata><mosScope>STORY</mosScope><mosSchema>http://openmos.example/schema/graphics/v1</mosSchema><mosPayload><template>lower_third_2line</template><line1>Mayor Alicia Jones</line1><line2>Transit expansion approved 7-2</line2><transitionIn>fade</transitionIn><holdSeconds>5</holdSeconds></mosPayload></mosExternalMetadata></mosItem></storyItem><p> </p>
</storyBody>
</roStorySend>
</mos>`

// TestLiveENPSStoryItemParses pins the two structural findings before persistence is involved.
func TestLiveENPSStoryItemParses(t *testing.T) {
	var env xml.Envelope
	if err := stdxml.Unmarshal([]byte(liveENPSStorySend), &env); err != nil {
		t.Fatalf("unmarshal captured frame: %v", err)
	}
	msg, err := env.Message()
	if err != nil {
		t.Fatalf("captured frame not recognised: %v", err)
	}
	send, ok := msg.(xml.ROStorySend)
	if !ok {
		t.Fatalf("parsed as %T, want ROStorySend", msg)
	}

	// The item is a direct child of storyBody, not inside a paragraph.
	if len(send.StoryBody.Items) != 1 {
		t.Fatalf("storyBody carried %d direct storyItem children, want 1. If this is 0 the "+
			"element has nowhere to unmarshal into and every item a live ENPS sends is dropped.",
			len(send.StoryBody.Items))
	}

	f := send.StoryBody.Items[0].ItemFields()
	if f == nil {
		t.Fatal("ItemFields() returned nil: the mosItem nesting was not read")
	}
	if f.ItemID != "1" || f.ObjID != "OM-T99124A" {
		t.Errorf("itemID=%q objID=%q, want 1 and OM-T99124A", f.ItemID, f.ObjID)
	}
	if f.ItemEdDur != 150 {
		t.Errorf("itemEdDur = %d, want 150", f.ItemEdDur)
	}
	if f.ItemChannel != "A" {
		t.Errorf("itemChannel = %q, want A", f.ItemChannel)
	}
	if len(f.ExternalMeta) != 1 {
		t.Fatalf("item carried %d metadata blocks, want 1", len(f.ExternalMeta))
	}
	if !strings.Contains(f.ExternalMeta[0].MosPayload.Raw, "Mayor Alicia Jones") {
		t.Errorf("graphics payload was lost: %q", f.ExternalMeta[0].MosPayload.Raw)
	}
}

// TestLiveENPSStoryItemPersists is the third finding: parsing was never the whole problem.
func TestLiveENPSStoryItemPersists(t *testing.T) {
	svc, stories, items := newStoryTestService(t)
	ctx := context.Background()

	var env xml.Envelope
	if err := stdxml.Unmarshal([]byte(liveENPSStorySend), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msg, _ := env.Message()
	send := msg.(xml.ROStorySend)

	// The running order must exist first, or roStorySend is correctly refused.
	if err := svc.ProcessRunningOrderInfo(ctx, xml.RunningOrderInfo{
		ID: send.ROID, Slug: "Tacit-test",
	}, "openmos.example.mos"); err != nil {
		t.Fatalf("seed running order: %v", err)
	}

	if err := svc.ProcessROStorySend(ctx, send); err != nil {
		t.Fatalf("ProcessROStorySend: %v", err)
	}

	stored := itemsFor(t, ctx, stories, items, send.ROID)
	if len(stored) != 1 {
		t.Fatalf("persisted %d items, want 1. Parsing the item is not enough; processStoryBody "+
			"used to build the slice and discard it.", len(stored))
	}
	item := stored[0]
	if item.ObjectID != "OM-T99124A" {
		t.Errorf("objectID = %q, want OM-T99124A", item.ObjectID)
	}
	// itemEdDur is 150 SAMPLES, and this frame carries no objTB, so the duration in seconds is unknown:
	// 150 samples is 2.5 seconds at NTSC and 3 at PAL. The sample count is preserved; Duration stays
	// zero rather than repeating the figure as though it were seconds (doc/interop §48).
	if item.EditorialDuration != 150 {
		t.Errorf("editorialDuration = %d samples, want 150", item.EditorialDuration)
	}
	if item.Duration != 0 {
		t.Errorf("duration = %d seconds; without a time base it cannot be computed and must not be "+
			"guessed", item.Duration)
	}
	if item.Metadata["mosID"] != "openmos.example.mos" {
		t.Errorf("owning mosID lost: %v", item.Metadata)
	}
	if len(item.ExternalMetadata) != 1 {
		t.Fatalf("persisted %d metadata blocks, want 1", len(item.ExternalMetadata))
	}
	if !strings.Contains(item.ExternalMetadata[0].Payload, "Transit expansion approved 7-2") {
		t.Errorf("graphics payload not persisted verbatim: %q", item.ExternalMetadata[0].Payload)
	}
	if item.ExternalMetadata[0].Scope != "STORY" {
		t.Errorf("scope = %q, want STORY", item.ExternalMetadata[0].Scope)
	}
}

// TestResentStorySendKeepsItemMetadata covers the update path, which is the COMMON one: a live
// ENPS re-sends the same roStorySend repeatedly while an operator edits. Metadata was set on
// create and not on update, so a graphics payload survived first arrival and was dropped by every
// message after it.
func TestResentStorySendKeepsItemMetadata(t *testing.T) {
	svc, stories, items := newStoryTestService(t)
	ctx := context.Background()

	var env xml.Envelope
	_ = stdxml.Unmarshal([]byte(liveENPSStorySend), &env)
	msg, _ := env.Message()
	send := msg.(xml.ROStorySend)

	if err := svc.ProcessRunningOrderInfo(ctx, xml.RunningOrderInfo{ID: send.ROID, Slug: "x"},
		"openmos.example.mos"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Send it three times, exactly as an editing session would.
	for i := 0; i < 3; i++ {
		if err := svc.ProcessROStorySend(ctx, send); err != nil {
			t.Fatalf("resend %d: %v", i, err)
		}
	}

	stored := itemsFor(t, ctx, stories, items, send.ROID)
	if len(stored) != 1 {
		t.Fatalf("three identical sends produced %d items, want 1", len(stored))
	}
	if len(stored[0].ExternalMetadata) != 1 {
		t.Errorf("metadata lost after resend: %d blocks, want 1. Update is the common path.",
			len(stored[0].ExternalMetadata))
	}
}
