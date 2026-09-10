package xml

import (
	stdxml "encoding/xml"
	"strings"
	"testing"
)

// The <item><mosItem> nesting, taken from the roList a live ENPS sent in answer to our roReq.
//
// One item in the thirteenth story failed validation for a missing itemID, and because applying a
// roList is atomic that rejected the ENTIRE running order. The device could not rebuild state at all
// and stayed permanently out of sync (doc/interop §45). So this is not a cosmetic parse gap.
const liveROListItem = `<roList>
<roID>NCS-HOST;P_STORYTELLING\W;2D526A13</roID>
<roSlug>Tacit-test</roSlug>
<story>
<storyID>NCS-HOST;P_STORYTELLING\W\R_2D526A13;593BEF12</storyID>
<storySlug>New Row 90 hot hat hits</storySlug>
<item>
<mosItem><itemID>1</itemID><itemSlug>LOWER THIRD: Mayor Jones / Transit Vote</itemSlug>
<objID>OM-T99124A</objID><mosID>openmos.example.mos</mosID>
<mosAbstract>LOWER THIRD: Mayor Jones / Transit Vote</mosAbstract>
<itemEdDur>150</itemEdDur><itemChannel>A</itemChannel>
<mosExternalMetadata><mosScope>STORY</mosScope>
<mosSchema>http://openmos.example/schema/graphics/v1</mosSchema>
<mosPayload><template>lower_third_2line</template></mosPayload>
</mosExternalMetadata></mosItem>
</item>
</story>
</roList>`

func TestNestedItemShapeParses(t *testing.T) {
	var list ROList
	if err := stdxml.Unmarshal([]byte(liveROListItem), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(list.Stories) != 1 {
		t.Fatalf("parsed %d stories, want 1", len(list.Stories))
	}
	items := list.Stories[0].Items
	if len(items) != 1 {
		t.Fatalf("parsed %d items, want 1", len(items))
	}
	it := items[0]

	// itemID is the field whose absence rejected a whole running order.
	if it.ID != "1" {
		t.Errorf("itemID = %q, want 1. Empty here fails validation and takes the entire roList with "+
			"it, so the device cannot rebuild state at all.", it.ID)
	}
	if it.ObjectID != "OM-T99124A" {
		t.Errorf("objID = %q, want OM-T99124A", it.ObjectID)
	}
	if it.MosID != "openmos.example.mos" {
		t.Errorf("mosID = %q, want the owning device", it.MosID)
	}
	if it.Duration != "150" {
		t.Errorf("itemEdDur = %q, want 150", it.Duration)
	}
	if it.Channel != "A" {
		t.Errorf("itemChannel = %q, want A", it.Channel)
	}
	if !strings.Contains(it.Slug, "Mayor Jones") {
		t.Errorf("itemSlug = %q, want the slug from the nested form", it.Slug)
	}
	if len(it.MosExternalMetadata) != 1 {
		t.Fatalf("carried %d metadata blocks, want 1", len(it.MosExternalMetadata))
	}
	if !strings.Contains(it.MosExternalMetadata[0].MosPayload.Raw, "lower_third_2line") {
		t.Errorf("graphics payload lost: %q", it.MosExternalMetadata[0].MosPayload.Raw)
	}
}

// The flat form is the specification's own, and must keep working unchanged.
func TestFlatItemShapeStillParses(t *testing.T) {
	flat := `<item><itemID>7</itemID><itemSlug>Flat</itemSlug><objID>M000224</objID>
<mosID>openmos.example.mos</mosID><itemEdDur>645</itemEdDur><itemChannel>B</itemChannel></item>`
	var it ItemInfo
	if err := stdxml.Unmarshal([]byte(flat), &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if it.ID != "7" || it.Slug != "Flat" || it.ObjectID != "M000224" || it.Channel != "B" {
		t.Errorf("flat form regressed: %+v", it)
	}
	if it.Duration != "645" {
		t.Errorf("itemEdDur = %q, want 645", it.Duration)
	}
}

// Where both shapes carry a field, the outer one wins: it is the shape the specification defines.
func TestFlatFieldsWinOverNested(t *testing.T) {
	both := `<item><itemID>outer</itemID><objID>OUTER-OBJ</objID>
<mosItem><itemID>inner</itemID><objID>INNER-OBJ</objID><itemChannel>C</itemChannel></mosItem></item>`
	var it ItemInfo
	if err := stdxml.Unmarshal([]byte(both), &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if it.ID != "outer" || it.ObjectID != "OUTER-OBJ" {
		t.Errorf("the specification's shape should win where both are present: %+v", it)
	}
	// A field only the nested form carries is still picked up.
	if it.Channel != "C" {
		t.Errorf("itemChannel = %q, want C from the nested form", it.Channel)
	}
}

// mosAbstract stands in for a missing itemSlug. It is an object field rather than an item field, but
// ENPS populates both with the same text and some peers send only the abstract.
func TestAbstractFallsBackToSlug(t *testing.T) {
	only := `<item><mosItem><itemID>3</itemID><objID>X</objID><mosID>m</mosID>
<mosAbstract>  Abstract only  </mosAbstract></mosItem></item>`
	var it ItemInfo
	if err := stdxml.Unmarshal([]byte(only), &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if it.Slug != "Abstract only" {
		t.Errorf("slug = %q, want the trimmed abstract", it.Slug)
	}
}
