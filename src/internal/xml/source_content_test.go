package xml

import (
	"reflect"
	"strings"
	"testing"
)

func parseSourceStory(t *testing.T, body string) ROStorySend {
	t.Helper()
	msg, err := ParseMessage(`<roStorySend><roID>rundown</roID><storyID>story</storyID>` + body + `</roStorySend>`)
	if err != nil {
		t.Fatal(err)
	}
	return msg.(ROStorySend)
}

func parseSourceListItem(t *testing.T, fields string) ItemInfo {
	t.Helper()
	msg, err := ParseMessage(`<roList><roID>rundown</roID><story><storyID>story</storyID><item>` + fields + `</item></story></roList>`)
	if err != nil {
		t.Fatal(err)
	}
	return msg.(ROList).Stories[0].Items[0]
}

func TestSourceItemPreservesWireValues(t *testing.T) {
	metadata := `<mosExternalMetadata><mosScope>PLAYLIST</mosScope><mosSchema>urn:example:opaque</mosSchema><mosPayload><opaque duration="carry-only"><value><![CDATA[A<&B]]></value><empty/></opaque></mosPayload></mosExternalMetadata>`
	secondMetadata := `<mosExternalMetadata><mosPayload>  second &lt; value  </mosPayload></mosExternalMetadata>`
	fields := `<itemID>item</itemID><mosID> device </mosID><objID>object</objID><objType>video</objType>` +
		`<itemSlug/><mosAbstract> Abstract &amp; value </mosAbstract><itemEdDur> 00:00:01.25 </itemEdDur>` +
		`<objDur>0x20</objDur><objTB>60000/1001</objTB><objPaths>` +
		`<objProxyPath> https://media.example.test/proxy </objProxyPath>` +
		`<objPath techDescription="">https://media.example.test/main</objPath>` +
		`<objProxyPath techDescription=" low ">https://media.example.test/second</objProxyPath>` +
		`<objMetadataPath>https://media.example.test/info?a=1&amp;b=2</objMetadataPath></objPaths>` + metadata + secondMetadata
	for _, form := range []string{"flat", "nested"} {
		t.Run(form, func(t *testing.T) {
			wire := fields
			if form == "nested" {
				wire = `<mosItem>` + wire + `</mosItem>`
			}
			list := parseSourceListItem(t, wire)
			story := parseSourceStory(t, `<storyBody><storyItem>`+wire+`</storyItem></storyBody>`)
			ordered, err := story.StoryBody.OrderedSource()
			if err != nil || len(ordered) != 1 {
				t.Fatalf("ordered item = %+v, %v", ordered, err)
			}
			for _, item := range []*SourceItem{list.Source, ordered[0].Item.Source} {
				if err := item.Validate(); err != nil {
					t.Fatal(err)
				}
				if item.ID != "item" {
					t.Errorf("itemID = %q", item.ID)
				}
				for _, check := range []struct {
					name string
					got  *string
					want string
				}{
					{"mosID", item.MosID, " device "}, {"objID", item.ObjectID, "object"},
					{"objType", item.ObjectType, "video"}, {"itemSlug", item.Label, ""},
					{"mosAbstract", item.Abstract, " Abstract & value "},
					{"itemEdDur", item.ItemEdDur, " 00:00:01.25 "},
					{"objDur", item.ObjDur, "0x20"}, {"objTB", item.ObjTB, "60000/1001"},
				} {
					if check.got == nil || *check.got != check.want {
						t.Errorf("%s = %v, want supplied %q", check.name, check.got, check.want)
					}
				}
				empty, description := "", " low "
				wantMedia := []SourceMedia{
					{Role: "objProxyPath", URL: " https://media.example.test/proxy "},
					{Role: "objPath", URL: "https://media.example.test/main", TechDescription: &empty},
					{Role: "objProxyPath", URL: "https://media.example.test/second", TechDescription: &description},
					{Role: "objMetadataPath", URL: "https://media.example.test/info?a=1&b=2"},
				}
				if item.Media == nil || !reflect.DeepEqual(*item.Media, wantMedia) {
					t.Errorf("media = %+v, want supplied order/values %+v", item.Media, wantMedia)
				}
				if item.Metadata == nil || !reflect.DeepEqual(*item.Metadata, []string{metadata, secondMetadata}) {
					t.Errorf("opaque metadata changed: %v", item.Metadata)
				}
			}
		})
	}
}

func TestSourceItemDistinguishesAbsentAndEmpty(t *testing.T) {
	for _, supplied := range []bool{false, true} {
		fields := `<itemID>item</itemID>`
		if supplied {
			fields += `<mosID/><objID/><objType/><itemSlug/><mosAbstract/><itemEdDur/><objDur/><objTB/><objPaths/><mosExternalMetadata/>`
		}
		list := parseSourceListItem(t, fields)
		story := parseSourceStory(t, `<storyBody><p><storyItem>`+fields+`</storyItem></p></storyBody>`)
		ordered, err := story.StoryBody.OrderedSource()
		if err != nil || len(ordered) != 1 {
			t.Fatalf("ordered item = %+v, %v", ordered, err)
		}
		for _, item := range []*SourceItem{list.Source, ordered[0].Item.Source} {
			if err := item.Validate(); err != nil {
				t.Fatal(err)
			}
			for _, value := range []*string{item.MosID, item.ObjectID, item.ObjectType, item.Label, item.Abstract, item.ItemEdDur, item.ObjDur, item.ObjTB} {
				if (value != nil) != supplied || value != nil && *value != "" {
					t.Errorf("supplied=%v, optional value=%v", supplied, value)
				}
			}
			if (item.Media != nil) != supplied || item.Media != nil && len(*item.Media) != 0 {
				t.Errorf("supplied=%v, media=%v", supplied, item.Media)
			}
			if (item.Metadata != nil) != supplied || item.Metadata != nil && !reflect.DeepEqual(*item.Metadata, []string{`<mosExternalMetadata/>`}) {
				t.Errorf("supplied=%v, metadata=%v", supplied, item.Metadata)
			}
		}
	}
}

func TestOrderedSourcePreservesMixedBodyOrder(t *testing.T) {
	body := `<storyBody>` +
		`<p>[TAKE <b>FIRST</b>]<storyItem><itemID>inside</itemID><objID>one</objID></storyItem>` +
		`<i>[CG :#Template\one\\three]</i><pi>TAKE :SECOND</pi></p>` +
		`<storyItem><mosItem><itemID>direct</itemID><objID>two</objID><itemEdDur>0x10</itemEdDur></mosItem></storyItem>` +
		`<p>[TAKE CLIP</p>` + "\n  " + `<p>DURATION:<u>0:18</u>]</p>` +
		`<p><pi>[TAKE :THIRD]</pi>{***LAST***}</p>` +
		`</storyBody>`
	story := parseSourceStory(t, body)
	ordered, err := story.StoryBody.OrderedSource()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, element := range ordered {
		if element.Item != nil {
			if element.Cue != nil {
				t.Fatal("occurrence has both item and cue")
			}
			got = append(got, "item:"+element.Item.ID)
		} else if element.Cue != nil {
			got = append(got, "cue:"+element.Cue.Target)
		} else {
			t.Fatal("empty occurrence")
		}
	}
	want := []string{"cue:FIRST", "item:inside", "cue::#Template", "cue::SECOND", "item:direct", "cue:CLIP", "cue::THIRD", "cue:LAST"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered source = %q, want %q", got, want)
	}
	serial := ordered[2].Cue
	if serial.Kind != CueSerialCG || serial.Verb != "CG" || serial.Raw != `CG :#Template\one\\three` || !reflect.DeepEqual(serial.Fields, []string{"one", "", "three"}) {
		t.Errorf("serial cue lost raw text or empty slots: %+v", serial)
	}
	span := ordered[5].Cue
	if span.Raw != "TAKE CLIP\nDURATION:0:18" || span.Paragraph != 1 || span.Params["DURATION"] != "0:18" {
		t.Errorf("formatted paragraph-spanning cue changed: %+v", span)
	}
	if ordered[3].Cue.Raw != "TAKE :SECOND" || ordered[3].Cue.Verb != "TAKE" || ordered[6].Cue.Raw != "TAKE :THIRD" || ordered[7].Cue.Kind != CuePrompter {
		t.Errorf("instruction or prompter interpretation changed: %+v", ordered)
	}
	if *ordered[4].Item.Source.ItemEdDur != "0x10" {
		t.Fatal("ordered item duration lost its raw value")
	}
	if len(story.StoryBody.Items) != 1 || len(story.StoryBody.Paragraphs[0].Items) != 1 {
		t.Fatal("legacy body collections changed")
	}
}

func TestOrderedSourceDistinguishesAbsentAndKnownEmptyBody(t *testing.T) {
	absent := parseSourceStory(t, "").StoryBody
	if absent.XMLName.Local != "" {
		t.Fatal("absent body gained an XML name")
	}
	if _, err := absent.OrderedSource(); err == nil {
		t.Fatal("absent body was certified empty")
	}
	empty := parseSourceStory(t, `<storyBody/>`).StoryBody
	if empty.XMLName.Local != "storyBody" {
		t.Fatal("present empty body lost its XML name")
	}
	if ordered, err := empty.OrderedSource(); err != nil || len(ordered) != 0 {
		t.Fatalf("known empty body = %+v, %v", ordered, err)
	}
	if _, err := (StoryBody{Items: []StoryItem{{ItemID: "item"}}}).OrderedSource(); err == nil {
		t.Fatal("constructed collections were mistaken for known XML order")
	}
}

func TestSourceItemConflictsKeepLegacyFallback(t *testing.T) {
	fields := `<itemID>item</itemID><mosAbstract/><itemEdDur/><objDur/><objTB/><objPaths/>` +
		`<mosItem><itemID>item</itemID><mosAbstract> Nested </mosAbstract><itemEdDur>150</itemEdDur>` +
		`<objDur>900</objDur><objTB>59.94</objTB><objPaths><objPath>https://media.example.test/nested</objPath></objPaths></mosItem>`
	item := parseSourceListItem(t, fields)
	if item.Slug != "Nested" || item.Abstract != " Nested " || item.Duration != "150" || item.ObjDur != "900" || item.ObjTB != "59.94" || item.ObjPaths.Essence() != "https://media.example.test/nested" {
		t.Fatalf("legacy empty-flat fallback changed: %+v", item)
	}
	if err := item.Source.Validate(); err == nil {
		t.Fatal("empty and populated source values were silently conflated")
	}
	outer := `<itemID>item</itemID><mosAbstract> Flat </mosAbstract><itemEdDur>300</itemEdDur><objDur>600</objDur><objTB>50</objTB>` +
		`<objPaths><objProxyPath>https://media.example.test/outer</objProxyPath></objPaths>`
	item = parseSourceListItem(t, outer+fields[strings.Index(fields, "<mosItem>"):])
	if item.Abstract != " Flat " || item.Duration != "300" || item.ObjDur != "600" || item.ObjTB != "50" || len(item.ObjPaths.ObjPath) != 0 || item.ObjPaths.Proxy() != "https://media.example.test/outer" {
		t.Fatalf("legacy populated-flat or whole-container selection changed: %+v", item)
	}
	if err := item.Source.Validate(); err == nil {
		t.Fatal("conflicting source values were certified faithful")
	}
}

func TestSourceItemRejectsAmbiguityWithoutRejectingXML(t *testing.T) {
	for _, tc := range []struct{ name, fields string }{
		{"duplicate scalar", `<itemID>item</itemID><objTB>50</objTB><objTB>50</objTB>`},
		{"structured scalar", `<itemID>item</itemID><objTB><value>50</value></objTB>`},
		{"duplicate wrapper", `<mosItem><itemID>item</itemID></mosItem><mosItem><itemID>item</itemID></mosItem>`},
		{"duplicate path container", `<itemID>item</itemID><objPaths/><objPaths/>`},
		{"nonempty path container text", `<itemID>item</itemID><objPaths>unrepresented</objPaths>`},
		{"competing path shapes", `<itemID>item</itemID><objPath>direct</objPath><objPaths/>`},
		{"conflicting path containers", `<itemID>item</itemID><objPaths><objProxyPath>outer</objProxyPath></objPaths><mosItem><itemID>item</itemID><objPaths><objPath>inner</objPath></objPaths></mosItem>`},
		{"conflicting metadata", `<itemID>item</itemID><mosExternalMetadata/><mosItem><itemID>item</itemID><mosExternalMetadata><mosPayload><data/></mosPayload></mosExternalMetadata></mosItem>`},
		{"conflicting type", `<itemID>item</itemID><objType>first</objType><mosItem><itemID>item</itemID><objType>second</objType></mosItem>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := parseSourceListItem(t, tc.fields)
			if err := item.Source.Validate(); err == nil {
				t.Fatal("ambiguous source item accepted")
			}
			body := parseSourceStory(t, `<storyBody><storyItem>`+tc.fields+`</storyItem></storyBody>`).StoryBody
			if _, err := body.OrderedSource(); err == nil {
				t.Fatal("ambiguous body source was certified complete")
			}
		})
	}
}

func TestSourceItemAllowsConsistentComplementaryForms(t *testing.T) {
	fields := `<itemID>item</itemID><mosID>device</mosID><objPaths/>` +
		`<mosItem><itemID>item</itemID><objID>object</objID><objPaths/></mosItem>`
	item := parseSourceListItem(t, fields).Source
	if err := item.Validate(); err != nil {
		t.Fatal(err)
	}
	if *item.MosID != "device" || *item.ObjectID != "object" || item.Media == nil || len(*item.Media) != 0 {
		t.Fatalf("complementary source fields changed: %+v", item)
	}
}

func TestOrderedSourceRejectsUncertainOrder(t *testing.T) {
	for _, body := range []string{
		`<storyBody><storyItem><itemID>same</itemID></storyItem><p><storyItem><itemID>same</itemID></storyItem></p></storyBody>`,
		`<storyBody><p>[TAKE <storyItem><itemID>item</itemID></storyItem>CLIP]</p></storyBody>`,
		`<storyBody><p>[TAKE <pi>TAKE OTHER</pi>CLIP]</p></storyBody>`,
		`<storyBody><extension><storyItem><itemID>hidden</itemID></storyItem></extension></storyBody>`,
		`<storyBody/><storyBody/>`,
	} {
		story := parseSourceStory(t, body)
		if _, err := story.StoryBody.OrderedSource(); err == nil {
			t.Fatal("unrepresentable source order was certified complete")
		}
	}
}

func TestOrderedSourceReadsFormattedInstructionsAndRootText(t *testing.T) {
	story := parseSourceStory(t, `<storyBody><b>[TAKE</b> <i>ROOT]</i><p><pi>AUTOMATION:<b>CG</b>\001\\tail</pi><pi>TAKE X<br/>DURATION: 0:12</pi></p></storyBody>`)
	ordered, err := story.StoryBody.OrderedSource()
	if err != nil || len(ordered) != 3 {
		t.Fatalf("ordered instructions = %+v, %v", ordered, err)
	}
	if ordered[0].Cue.Raw != "TAKE ROOT" || ordered[0].Cue.Paragraph != -1 {
		t.Errorf("formatted root text changed: %+v", ordered[0].Cue)
	}
	if cue := ordered[1].Cue; cue.Raw != `AUTOMATION:CG\001\\tail` || cue.Verb != "AUTOMATION" || cue.Kind != CueSerialCG || !reflect.DeepEqual(cue.Fields, []string{"001", "", "tail"}) {
		t.Errorf("formatted instruction fields changed: %+v", cue)
	}
	if cue := ordered[2].Cue; cue.Raw != "TAKE X\nDURATION: 0:12" || cue.Params["DURATION"] != "0:12" {
		t.Errorf("instruction parameters changed: %+v", cue)
	}
}
