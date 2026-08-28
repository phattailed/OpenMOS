package xml

import (
	stdxml "encoding/xml"
	"strings"
	"testing"
)

// mosExternalMetadata is an opaque payload MOS is required to carry, not interpret. MOS 4.0
// §4.1.5 calls it "a mechanism for transporting additional metadata, independent of schema
// or DTD", and the DTD types the payload as `<!ELEMENT mosPayload ANY>`.
//
// It was being discarded, twice over:
//
//  1. MosPayload was typed as a plain string. encoding/xml collects only an element's
//     character data into a string, and a payload made of child elements has none -- so
//     every real payload parsed to "" and vanished silently.
//  2. StoryInfo, ItemInfo and roCreate had no field for the block at all, so story-level,
//     item-level and running-order-level metadata was dropped structurally.
//
// These use payload shapes taken from real traffic: the ENPS production fields captured in
// doc/interop §13, and the nested vendor template definitions a graphics device sends.

// realENPSPayload is the shape a live ENPS sends on roStorySend, sanitized.
const realENPSPayload = `<mosExternalMetadata>
<mosScope>PLAYLIST</mosScope>
<mosSchema>http://NCS-HOST:10505/schema/enps.dtd</mosSchema>
<mosPayload>
<MediaTime>0</MediaTime>
<RevisionNumber>5</RevisionNumber>
<Creator>OPERATOR</Creator>
<CreatedDateTime>20260717T163525Z</CreatedDateTime>
<pubApproved>0</pubApproved>
<ModTime>20260717T163525Z</ModTime>
<ENPSItemType>3</ENPSItemType>
</mosPayload>
</mosExternalMetadata>`

// nestedVendorPayload is the shape an automation vendor sends: a template definition several
// levels deep, with attributes. Flattening this to key/value pairs would destroy it.
const nestedVendorPayload = `<mosExternalMetadata>
<mosScope>PLAYLIST</mosScope>
<mosSchema>http://vendor.example/schema/mositem.dtd</mosSchema>
<mosPayload><template type="LOWERTHIRDS" category="GRAPHICS"><variants fieldtype="LIST" value="AUTOOUT"><variant name="AUTOOUT"><fields><field name="graphics_id" fieldtype="TEXT"/><field name="tc_in" fieldtype="TIMECODE" default="00:00"/></fields></variant></variants></template></mosPayload>
</mosExternalMetadata>`

func TestExternalMetadataPayloadSurvivesParsing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame string
		// A distinctive fragment that must appear in the preserved payload.
		wants []string
	}{
		{"live ENPS production fields", realENPSPayload,
			[]string{"<RevisionNumber>5</RevisionNumber>", "<ENPSItemType>3</ENPSItemType>"}},
		{"nested vendor template", nestedVendorPayload,
			[]string{`type="LOWERTHIRDS"`, `<field name="tc_in"`, "</template>"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mem MosExternalMetadata
			if err := stdxml.Unmarshal([]byte(tc.frame), &mem); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}

			if mem.MosPayload.IsEmpty() {
				t.Fatal("the payload was discarded; MOS is required to carry it, not interpret it")
			}
			for _, want := range tc.wants {
				if !strings.Contains(mem.MosPayload.Raw, want) {
					t.Errorf("payload lost %q\ngot: %s", want, mem.MosPayload.Raw)
				}
			}
			// The descriptive fields must survive alongside it.
			if mem.MosScope != "PLAYLIST" {
				t.Errorf("mosScope = %q, want PLAYLIST", mem.MosScope)
			}
			if mem.MosSchema == "" {
				t.Error("mosSchema was lost")
			}
		})
	}
}

// TestExternalMetadataRoundTrips is the property that matters for an opaque payload: what we
// emit must be what we received. Anything less means we are quietly editing a vendor's data.
func TestExternalMetadataRoundTrips(t *testing.T) {
	var original MosExternalMetadata
	if err := stdxml.Unmarshal([]byte(nestedVendorPayload), &original); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	encoded, err := stdxml.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var again MosExternalMetadata
	if err := stdxml.Unmarshal(encoded, &again); err != nil {
		t.Fatalf("our own output must parse: %v", err)
	}
	if again.MosPayload.Raw != original.MosPayload.Raw {
		t.Errorf("payload changed across a round trip\nbefore: %s\nafter:  %s",
			original.MosPayload.Raw, again.MosPayload.Raw)
	}
	if again.MosSchema != original.MosSchema || again.MosScope != original.MosScope {
		t.Errorf("descriptive fields changed: scope %q->%q schema %q->%q",
			original.MosScope, again.MosScope, original.MosSchema, again.MosSchema)
	}
}

// TestMetadataIsCarriedAtEveryLevel guards the structural half of the defect. The spec places
// mosExternalMetadata* at running-order, story AND item level, and none of the three had a
// field, so all three were dropped regardless of the payload typing.
func TestMetadataIsCarriedAtEveryLevel(t *testing.T) {
	const frame = `<roCreate>
<roID>RO-1</roID>
<roSlug>Rundown</roSlug>
<mosExternalMetadata><mosScope>PLAYLIST</mosScope><mosSchema>ro-level</mosSchema><mosPayload><a>ro</a></mosPayload></mosExternalMetadata>
<story>
<storyID>S-1</storyID>
<mosExternalMetadata><mosScope>STORY</mosScope><mosSchema>story-level</mosSchema><mosPayload><b>story</b></mosPayload></mosExternalMetadata>
<item>
<itemID>I-1</itemID><objID>OBJ-1</objID><mosID>dev.mos</mosID>
<mosExternalMetadata><mosScope>PLAYLIST</mosScope><mosSchema>item-level</mosSchema><mosPayload><c>item</c></mosPayload></mosExternalMetadata>
</item>
</story>
</roCreate>`

	var ro RunningOrderInfo
	if err := stdxml.Unmarshal([]byte(frame), &ro); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if len(ro.MosExternalMetadata) != 1 || ro.MosExternalMetadata[0].MosPayload.Raw != "<a>ro</a>" {
		t.Errorf("running-order metadata lost: %+v", ro.MosExternalMetadata)
	}
	if len(ro.Stories) != 1 {
		t.Fatalf("parsed %d stories, want 1", len(ro.Stories))
	}
	story := ro.Stories[0]
	if len(story.MosExternalMetadata) != 1 || story.MosExternalMetadata[0].MosPayload.Raw != "<b>story</b>" {
		t.Errorf("story metadata lost: %+v", story.MosExternalMetadata)
	}
	if len(story.Items) != 1 {
		t.Fatalf("parsed %d items, want 1", len(story.Items))
	}
	if item := story.Items[0]; len(item.MosExternalMetadata) != 1 ||
		item.MosExternalMetadata[0].MosPayload.Raw != "<c>item</c>" {
		t.Errorf("item metadata lost: %+v", story.Items[0].MosExternalMetadata)
	}
}

// TestMosScopeValuesAreCarriedNotInterpreted records that scope is preserved as sent. The spec
// defines what each value MEANS for propagation, which is a filtering decision this project
// does not yet make -- but it cannot be made at all if the value is not kept.
func TestMosScopeValuesAreCarriedNotInterpreted(t *testing.T) {
	for _, scope := range []string{"OBJECT", "STORY", "PLAYLIST"} {
		frame := `<mosExternalMetadata><mosScope>` + scope +
			`</mosScope><mosSchema>s</mosSchema><mosPayload><x/></mosPayload></mosExternalMetadata>`
		var mem MosExternalMetadata
		if err := stdxml.Unmarshal([]byte(frame), &mem); err != nil {
			t.Fatalf("unmarshal failed for scope %s: %v", scope, err)
		}
		if mem.MosScope != scope {
			t.Errorf("mosScope %q was altered to %q", scope, mem.MosScope)
		}
	}
}
