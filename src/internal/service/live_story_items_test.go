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

// A customer's item shape: duration expressed as objDur plus objTB, with no itemEdDur at all.
//
// Structure taken from a production rundown where all 93 items carried objDur and objTB and only some
// carried itemEdDur. Element order is the real one, which differs markedly from the specification's --
// mosID and mosAbstract precede itemID, and objDur/objTB sit mid-element. Go does not care about order
// when unmarshalling, but recording it here documents that the real order is not the declared one.
//
// This test exists because adding objDur and objTB to the wire types was not sufficient: the conversion
// from the story-body form into ItemInfo dropped them, so every one of those durations stayed at zero
// even with the fields present and parsed (doc/interop §48).
const customerShapedStorySend = `<mos>
<mosID>openmos.example.mos</mosID><ncsID>NCS-HOST</ncsID><messageID>91</messageID>
<roStorySend>
<roID>NCS-HOST;P_STORYTELLING\W;2D526A13</roID>
<storyID>NCS-HOST;P_STORYTELLING\W\R_2D526A13;A0CEE368</storyID>
<storySlug>Story with a vendor item</storySlug>
<storyBody><p> </p>
<storyItem><mosID>vendor.cg.example.mos</mosID><mosAbstract>CG element</mosAbstract>
<itemID>2</itemID><mosPlugInID>vendor.plugin.1</mosPlugInID><objID>OBJ-77</objID>
<objDur>600</objDur><objTB>59.94</objTB><abstract>CG element</abstract>
<itemSlug>CG element</itemSlug></storyItem>
<p> </p>
</storyBody>
</roStorySend>
</mos>`

func TestCustomerItemDurationFromObjDur(t *testing.T) {
	svc, stories, items := newStoryTestService(t)
	ctx := context.Background()

	var env xml.Envelope
	if err := stdxml.Unmarshal([]byte(customerShapedStorySend), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msg, err := env.Message()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	send := msg.(xml.ROStorySend)

	if err := svc.ProcessRunningOrderInfo(ctx, xml.RunningOrderInfo{
		ID: send.ROID, Slug: "customer",
	}, "openmos.example.mos"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := svc.ProcessROStorySend(ctx, send); err != nil {
		t.Fatalf("ProcessROStorySend: %v", err)
	}

	stored := itemsFor(t, ctx, stories, items, send.ROID)
	if len(stored) != 1 {
		t.Fatalf("persisted %d items, want 1", len(stored))
	}
	item := stored[0]

	// 600 samples at 59.94 samples per second is ten seconds.
	if item.Duration != 10 {
		t.Errorf("duration = %d seconds, want 10 (600 samples at 59.94). Zero here means objDur and "+
			"objTB were parsed but not carried through the conversion into ItemInfo.", item.Duration)
	}
	if item.EditorialDuration != 600 {
		t.Errorf("editorialDuration = %d, want the 600 samples preserved", item.EditorialDuration)
	}
	if item.TimeBase != 60 {
		t.Errorf("timeBase = %d, want 59.94 rounded to 60", item.TimeBase)
	}
	// The exact rate must survive, since 59.94 does not round-trip through an int.
	if got := item.Metadata["objTB"]; got != "59.94" {
		t.Errorf("metadata objTB = %q, want the exact 59.94", got)
	}
	// The owning device is a different vendor from the receiving device, which is what makes
	// redirection possible and must not be flattened.
	if item.Metadata["mosID"] != "vendor.cg.example.mos" {
		t.Errorf("owning mosID = %q, want the vendor device that owns the object", item.Metadata["mosID"])
	}
}

// Media pointers, in the shape a live NCS sends them.
//
// objID names an object on a device; objPaths says where the bytes are. A rundown display needs the
// thumbnail, a preview needs the proxy, and a playout or bridge needs the essence — none derivable from
// objID. Fifty-seven captured frames carried objPaths and not one URL was stored, because ItemInfo
// declared a BARE objPath while the specification nests the paths one level down (doc/interop §49).
//
// Both essence and proxy are REPEATABLE, and the real traffic uses that: one essence path plus separate
// proxies distinguished only by a free-text techDescription — "Proxy" for a video preview, "JPG" for a
// still. Note also that the essence path's techDescription arrives EMPTY even though the specification
// makes the attribute required.
const mediaPathStorySend = `<mos>
<mosID>openmos.example.mos</mosID><ncsID>NCS-HOST</ncsID><messageID>92</messageID>
<roStorySend>
<roID>NCS-HOST;P_STORYTELLING\W;2D526A13</roID>
<storyID>NCS-HOST;P_STORYTELLING\W\R_2D526A13;A0CEE368</storyID>
<storySlug>Story with media</storySlug>
<storyBody><p> </p>
<storyItem><mosID>vendor.video.example.mos</mosID><itemID>4</itemID>
<objID>OBJ-99</objID><objDur>1200</objDur><objTB>59.94</objTB>
<itemSlug>Package</itemSlug>
<objPaths>
<objPath techDescription="">https://media.example.test/essence/clip99.mxf</objPath>
<objProxyPath techDescription="Proxy">https://media.example.test/proxy/clip99.mp4</objProxyPath>
<objProxyPath techDescription="JPG">https://media.example.test/thumb/clip99.jpg</objProxyPath>
<objMetadataPath>https://media.example.test/meta/clip99.xml</objMetadataPath>
</objPaths>
</storyItem>
<p> </p>
</storyBody>
</roStorySend>
</mos>`

func TestMediaPointersArePersisted(t *testing.T) {
	svc, stories, items := newStoryTestService(t)
	ctx := context.Background()

	var env xml.Envelope
	if err := stdxml.Unmarshal([]byte(mediaPathStorySend), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msg, err := env.Message()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	send := msg.(xml.ROStorySend)

	if err := svc.ProcessRunningOrderInfo(ctx, xml.RunningOrderInfo{ID: send.ROID, Slug: "media"},
		"openmos.example.mos"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := svc.ProcessROStorySend(ctx, send); err != nil {
		t.Fatalf("ProcessROStorySend: %v", err)
	}

	stored := itemsFor(t, ctx, stories, items, send.ROID)
	if len(stored) != 1 {
		t.Fatalf("persisted %d items, want 1", len(stored))
	}
	media := stored[0].Media
	if media == nil {
		t.Fatal("no media pointers stored. Nil here means objPaths was parsed but not carried across " +
			"the conversion into ItemInfo, which is the seam that dropped objDur.")
	}

	if len(media.Essence) != 1 || media.Essence[0].URL != "https://media.example.test/essence/clip99.mxf" {
		t.Errorf("essence = %+v, want the single objPath", media.Essence)
	}
	// Both proxies must survive. Keeping only one would discard the distinction a consumer needs.
	if len(media.Proxy) != 2 {
		t.Fatalf("stored %d proxies, want 2 (a video preview and a still)", len(media.Proxy))
	}
	if media.Proxy[0].TechDescription != "Proxy" || media.Proxy[1].TechDescription != "JPG" {
		t.Errorf("proxy descriptions = %q/%q, want Proxy and JPG in order",
			media.Proxy[0].TechDescription, media.Proxy[1].TechDescription)
	}
	if len(media.Metadata) != 1 {
		t.Errorf("stored %d metadata paths, want 1", len(media.Metadata))
	}
	// An empty techDescription must not disqualify the path: the specification requires the attribute and
	// a live NCS sends it empty anyway.
	if media.Essence[0].TechDescription != "" {
		t.Errorf("essence techDescription = %q, want the empty value preserved as sent",
			media.Essence[0].TechDescription)
	}

	// The accessors a consumer would actually use.
	paths := send.StoryBody.Items[0].ItemFields().ObjPaths
	if got := paths.Essence(); got != "https://media.example.test/essence/clip99.mxf" {
		t.Errorf("Essence() = %q", got)
	}
	if got := paths.ProxyMatching("jpg"); got != "https://media.example.test/thumb/clip99.jpg" {
		t.Errorf("ProxyMatching(jpg) = %q, want the still", got)
	}
	if got := paths.ProxyMatching("nothing-like-this"); got != "https://media.example.test/proxy/clip99.mp4" {
		t.Errorf("ProxyMatching should fall back to the first proxy, got %q", got)
	}
}

// A resend without objPaths must not erase pointers already held: update is the common path.
func TestMediaPointersSurviveAResendWithoutThem(t *testing.T) {
	svc, stories, items := newStoryTestService(t)
	ctx := context.Background()

	var env xml.Envelope
	_ = stdxml.Unmarshal([]byte(mediaPathStorySend), &env)
	msg, _ := env.Message()
	send := msg.(xml.ROStorySend)
	if err := svc.ProcessRunningOrderInfo(ctx, xml.RunningOrderInfo{ID: send.ROID, Slug: "media"},
		"openmos.example.mos"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := svc.ProcessROStorySend(ctx, send); err != nil {
		t.Fatalf("first send: %v", err)
	}

	// The same story again, with the pointers stripped.
	stripped := send
	body := *send.StoryBody.Items[0].ItemFields()
	body.ObjPaths = nil
	stripped.StoryBody = xml.StoryBody{Items: []xml.StoryItem{{MosItem: &body}}}
	if err := svc.ProcessROStorySend(ctx, stripped); err != nil {
		t.Fatalf("resend: %v", err)
	}

	stored := itemsFor(t, ctx, stories, items, send.ROID)
	if len(stored) != 1 {
		t.Fatalf("persisted %d items, want 1", len(stored))
	}
	if stored[0].Media == nil || len(stored[0].Media.Essence) != 1 {
		t.Error("a resend that omits objPaths must not erase the pointers already held")
	}
}

// The graphics text is truncated in itemSlug and complete in mosAbstract.
//
// itemSlug is capped at 128 characters by the specification; mosAbstract has no stated limit. A live NCS
// truncates the slug at exactly that cap -- 20 captured items sit at 128 characters, none above, and the
// longest ends mid-word -- while the abstract carries the full string, longer than the slug on 272 of 458
// captured items (doc/interop §50).
//
// Graphics items encode their template name and field values in that text, pipe-delimited, so rendering
// from the slug would put visibly truncated text on air: a five-field lower third loses its last field.
func TestGraphicsAbstractIsNotTruncatedLikeTheSlug(t *testing.T) {
	svc, stories, items := newStoryTestService(t)
	ctx := context.Background()

	// The complete graphics text, and the slug derived from it the way the NCS derives it: truncated at
	// the specification's 128-character cap, which lands mid-word.
	//
	// The shape is the live convention -- template name, then the template's field values joined with
	// pipes in template order, empty fields included, which is why real slugs contain runs of "|  |"
	// (doc/interop §§50, 51). Field values here are invented; the shape and the truncation are what the
	// live traffic showed. Measured over 458 captured item blocks: 20 slugs at exactly 128 characters,
	// none longer, and an abstract reaching 233.
	const full = "FS BULLETS WRAP:FIRST FIELD VALUE HERE | SECOND FIELD | NO | " +
		"THIRD FIELD VALUE IS LONGER HERE AND WIDER | FOURTH FIELD VALUE HERE | " +
		"FIFTH FIELD VALUE COMPLETE"
	const slugCap = 128
	truncated := full[:slugCap]
	if len(full) <= slugCap {
		t.Fatalf("fixture is only %d characters; it must exceed the %d-character cap to be truncated",
			len(full), slugCap)
	}
	if strings.HasSuffix(truncated, " ") || strings.HasPrefix(full[slugCap:], " ") {
		t.Fatalf("fixture truncates on a word boundary; the point is that a real NCS cuts mid-word")
	}

	frame := `<mos><mosID>openmos.example.mos</mosID><ncsID>NCS-HOST</ncsID><messageID>93</messageID>
<roStorySend>
<roID>NCS-HOST;P_STORYTELLING\W;2D526A13</roID>
<storyID>NCS-HOST;P_STORYTELLING\W\R_2D526A13;A0CEE368</storyID>
<storySlug>Graphics story</storySlug>
<storyBody><p> </p>
<storyItem><mosID>vendor.cg.example.mos</mosID><itemID>5</itemID><objID>CG-1</objID>
<itemSlug>` + truncated + `</itemSlug>
<mosAbstract>` + full + `</mosAbstract>
<objTB>29.97</objTB><objDur>0</objDur>
</storyItem>
<p> </p>
</storyBody>
</roStorySend>
</mos>`

	var env xml.Envelope
	if err := stdxml.Unmarshal([]byte(frame), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msg, err := env.Message()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	send := msg.(xml.ROStorySend)

	if err := svc.ProcessRunningOrderInfo(ctx, xml.RunningOrderInfo{ID: send.ROID, Slug: "gfx"},
		"openmos.example.mos"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := svc.ProcessROStorySend(ctx, send); err != nil {
		t.Fatalf("ProcessROStorySend: %v", err)
	}

	stored := itemsFor(t, ctx, stories, items, send.ROID)
	if len(stored) != 1 {
		t.Fatalf("persisted %d items, want 1", len(stored))
	}
	item := stored[0]

	// Both must survive, and they must remain DISTINCT. Folding one into the other loses either the
	// NCS's own label or the complete text.
	if item.Slug != truncated {
		t.Errorf("slug = %q, want the NCS's own truncated label preserved as sent", item.Slug)
	}
	if item.Abstract != full {
		t.Errorf("abstract = %q, want the complete text", item.Abstract)
	}
	if item.Abstract == item.Slug {
		t.Error("slug and abstract collapsed into one value; the distinction between the truncated " +
			"label and the complete text is the whole point")
	}
	// The complete text must still contain the final field the slug lost.
	if !strings.Contains(item.Abstract, "FIFTH FIELD VALUE COMPLETE") {
		t.Errorf("the field the slug truncated is missing from the abstract: %q", item.Abstract)
	}
	if strings.Contains(item.Slug, "FIFTH FIELD VALUE COMPLETE") {
		t.Error("fixture is wrong: the slug should be missing the final field")
	}
}

// An item with only an abstract still gets a usable label, so a caller wanting a name always has one.
func TestAbstractStandsInForAMissingSlug(t *testing.T) {
	svc, stories, items := newStoryTestService(t)
	ctx := context.Background()

	frame := `<mos><mosID>openmos.example.mos</mosID><ncsID>NCS-HOST</ncsID><messageID>94</messageID>
<roStorySend><roID>NCS-HOST;P_STORYTELLING\W;2D526A13</roID>
<storyID>NCS-HOST;P_STORYTELLING\W\R_2D526A13;A0CEE368</storyID>
<storyBody><storyItem><mosID>m</mosID><itemID>6</itemID><objID>CG-2</objID>
<mosAbstract>  Lower 3rd:NAME | TITLE  </mosAbstract></storyItem></storyBody>
</roStorySend></mos>`

	var env xml.Envelope
	_ = stdxml.Unmarshal([]byte(frame), &env)
	msg, _ := env.Message()
	send := msg.(xml.ROStorySend)
	if err := svc.ProcessRunningOrderInfo(ctx, xml.RunningOrderInfo{ID: send.ROID, Slug: "gfx"},
		"openmos.example.mos"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := svc.ProcessROStorySend(ctx, send); err != nil {
		t.Fatalf("send: %v", err)
	}
	stored := itemsFor(t, ctx, stories, items, send.ROID)
	if len(stored) != 1 {
		t.Fatalf("persisted %d items, want 1", len(stored))
	}
	if stored[0].Slug == "" {
		t.Error("an item with only an abstract must still present a label")
	}
	if stored[0].Abstract == "" {
		t.Error("the abstract must be kept in its own right, not only borrowed for the slug")
	}
}

// Non-MOS instructions in the story body, in the shape a live ENPS sends them.
//
// A newsroom drives equipment that has no MOS device: production commands read by automation or a
// director, and legacy serial character generators. ENPS carries both as ordinary body text, so
// they arrive only in roStorySend -- a roList describes structure and never carries a body.
//
// Structure is verbatim from a captured frame, with editorial text replaced. Two details are
// reproduced exactly because they are what a parser gets wrong:
//
//  1. The production command follows immediately after the </storyItem> it acts on.
//  2. [TAKE SOT / DURATION:0:18] is SPLIT ACROSS TWO PARAGRAPHS. 28 of 219 captured commands
//     arrive that way, and no paragraph carries a newline of its own, so the paragraph break is
//     the only separator. Reading one paragraph at a time loses the duration entirely.
const liveENPSStoryWithCues = `<mos><mosID>openmos.example.mos</mosID><ncsID>example.test</ncsID><messageID>412</messageID>
<roStorySend><roID>RO-CUES</roID><storyID>RO-CUES;STORY-1</storyID><storySlug>PRESS CONFERENCE-SOTVO</storySlug>
<storyBody><p>{***PRESENTER***}</p>
<p>Anchor intro line.</p>
<storyItem><mosItem><itemID>1</itemID><itemSlug>Lower 3rd:NAME | TITLE |  | No |  | </itemSlug><objID>{05895714-40FC}</objID><mosID>openmos.cg.example.mos</mosID><itemChannel>1</itemChannel></mosItem></storyItem>
<p>[TAKE SOT</p>
<p>DURATION:0:18]</p>
<p>{***SOT FULL***}</p>
<p>[CG :#Lower Third\Presenter Name\Saw it all]</p>
</storyBody></roStorySend></mos>`

// The seam test. The parser is proven in internal/xml; this asserts the cues actually reach
// storage, because the two defects before this one (objDur/objTB, then objPaths) were each correct
// at both ends of this conversion with passing unit tests on either side and nothing crossing
// (doc/interop §§48, 49).
func TestBodyCuesReachStorage(t *testing.T) {
	svc, stories, _ := newStoryTestService(t)
	ctx := context.Background()

	var env xml.Envelope
	if err := stdxml.Unmarshal([]byte(liveENPSStoryWithCues), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msg, _ := env.Message()
	send := msg.(xml.ROStorySend)

	if err := svc.ProcessRunningOrderInfo(ctx, xml.RunningOrderInfo{
		ID: send.ROID, Slug: "cue-test",
	}, "openmos.example.mos"); err != nil {
		t.Fatalf("seed running order: %v", err)
	}
	if err := svc.ProcessROStorySend(ctx, send); err != nil {
		t.Fatalf("ProcessROStorySend: %v", err)
	}

	stored, err := stories.ListByRunningOrder(ctx, send.ROID)
	if err != nil {
		t.Fatalf("list stories: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("persisted %d stories, want 1", len(stored))
	}
	cues := stored[0].Cues
	if len(cues) != 4 {
		t.Fatalf("persisted %d cues, want 4 (2 prompter, 1 production, 1 serial CG): %+v",
			len(cues), cues)
	}

	// Document order across all three classes, because a consumer sequencing playout depends
	// on it and Order is what carries it into storage.
	for i, c := range cues {
		if c.Order != i {
			t.Errorf("cue %d has Order %d", i, c.Order)
		}
	}

	if cues[0].Kind != xml.CuePrompter || cues[0].Target != "PRESENTER" {
		t.Errorf("cue 0 = %q/%q, want PROMPTER/PRESENTER", cues[0].Kind, cues[0].Target)
	}

	take := cues[1]
	if take.Kind != xml.CueProduction || take.Verb != "TAKE" || take.Target != "SOT" {
		t.Errorf("cue 1 = %q %q/%q, want PRODUCTION TAKE/SOT", take.Kind, take.Verb, take.Target)
	}
	if got := take.Params["DURATION"]; got != "0:18" {
		t.Errorf("DURATION = %q, want 0:18. The parameter is in the NEXT paragraph, so a "+
			"per-paragraph scan drops it and leaves an unterminated bracket.", got)
	}

	cg := cues[3]
	if cg.Kind != xml.CueSerialCG {
		t.Errorf("cue 3 kind = %q, want %q", cg.Kind, xml.CueSerialCG)
	}
	if cg.Verb != "CG" || cg.Target != ":#Lower Third" {
		t.Errorf("cue 3 = %q/%q, want CG/:#Lower Third", cg.Verb, cg.Target)
	}
	if len(cg.Fields) != 2 || cg.Fields[0] != "Presenter Name" {
		t.Errorf("cue 3 fields = %q, want two values", cg.Fields)
	}
	// Raw survives regardless of how well the parse went, so an unrecognised dialect is still
	// forwardable -- the same reason mosExternalMetadata is kept verbatim.
	if !strings.Contains(cg.Raw, `\Presenter Name\`) {
		t.Errorf("cue 3 lost its raw text: %q", cg.Raw)
	}

	// The MOS item is unaffected: cues are a separate class, not a competing interpretation of
	// the same body.
	if len(stored[0].Cues) > 0 && stored[0].Slug != "PRESS CONFERENCE-SOTVO" {
		t.Errorf("story slug = %q", stored[0].Slug)
	}
}

// A resend with a cue removed must drop it. This is the opposite of how item metadata is treated,
// where an absent field leaves the stored value alone (TestMediaPointersSurviveAResendWithoutThem):
// every roStorySend carries the COMPLETE body, so absence here is deletion, and a merge would keep
// firing a command the journalist deleted.
func TestRemovedCuesDisappearOnResend(t *testing.T) {
	svc, stories, _ := newStoryTestService(t)
	ctx := context.Background()

	var env xml.Envelope
	if err := stdxml.Unmarshal([]byte(liveENPSStoryWithCues), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msg, _ := env.Message()
	send := msg.(xml.ROStorySend)

	if err := svc.ProcessRunningOrderInfo(ctx, xml.RunningOrderInfo{
		ID: send.ROID, Slug: "cue-test",
	}, "openmos.example.mos"); err != nil {
		t.Fatalf("seed running order: %v", err)
	}
	if err := svc.ProcessROStorySend(ctx, send); err != nil {
		t.Fatalf("first send: %v", err)
	}

	// The journalist deletes the serial CG line and resends.
	trimmed := strings.Replace(liveENPSStoryWithCues,
		`<p>[CG :#Lower Third\Presenter Name\Saw it all]</p>`+"\n", "", 1)
	if trimmed == liveENPSStoryWithCues {
		t.Fatal("fixture did not change; the replace target is wrong")
	}
	var env2 xml.Envelope
	if err := stdxml.Unmarshal([]byte(trimmed), &env2); err != nil {
		t.Fatalf("unmarshal resend: %v", err)
	}
	msg2, _ := env2.Message()
	if err := svc.ProcessROStorySend(ctx, msg2.(xml.ROStorySend)); err != nil {
		t.Fatalf("resend: %v", err)
	}

	stored, err := stories.ListByRunningOrder(ctx, send.ROID)
	if err != nil {
		t.Fatalf("list stories: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("persisted %d stories, want 1", len(stored))
	}
	for _, c := range stored[0].Cues {
		if c.Kind == xml.CueSerialCG {
			t.Fatalf("deleted serial CG survived the resend: %+v", c)
		}
	}
	if len(stored[0].Cues) != 3 {
		t.Errorf("cues after resend = %d, want 3", len(stored[0].Cues))
	}
}
