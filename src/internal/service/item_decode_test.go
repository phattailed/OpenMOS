package service

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"airshift/openmos/internal/model"
	"airshift/openmos/internal/xml"
)

func applyItemXML(t *testing.T, svc *MOSService, body string) {
	t.Helper()
	message, err := xml.ParseMessage(`<mos><mosID>receiver.example.test</mosID>` +
		`<ncsID>source.example.test</ncsID><messageID>1</messageID>` + body + `</mos>`)
	if err != nil {
		t.Fatalf("parse XML: %v", err)
	}
	envelope, ok := message.(xml.Envelope)
	if !ok {
		t.Fatalf("parsed %T, want Envelope", message)
	}
	message, err = envelope.Message()
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	ctx := context.Background()
	switch m := message.(type) {
	case xml.RunningOrderInfo:
		err = svc.ProcessRunningOrderInfo(ctx, m, envelope.MosID)
	case xml.ROList:
		err = svc.ApplyROList(ctx, m, envelope.MosID)
	case xml.ROReplace:
		err = svc.ReplaceRunningOrder(ctx, m)
	case xml.ROStorySend:
		err = svc.ProcessROStorySend(ctx, m)
	default:
		t.Fatalf("unexpected item message %T", message)
	}
	if err != nil {
		t.Fatalf("apply %s: %v", message.GetMessageType(), err)
	}
}

func TestRunningOrderItemXML(t *testing.T) {
	for _, message := range []string{"roCreate", "roList"} {
		for _, shape := range []string{"flat", "nested", "mixed"} {
			t.Run(message+"/"+shape, func(t *testing.T) {
				svc, stories, items := newStoryTestService(t)
				metadataPath := `<objMetadataPath techDescription=" Data "> https://media.example.test/clip.xml </objMetadataPath>`
				if shape == "mixed" {
					metadataPath = ""
				}
				fields := `<itemID>item-1</itemID><itemSlug>Short label</itemSlug>` +
					`<objID>object-1</objID><mosID>media.example.test</mosID>` +
					`<mosAbstract>  Complete item description  </mosAbstract><objDur> 600 </objDur>` +
					`<objTB> 59.940 </objTB>` +
					`<objPaths><objPath techDescription=""> https://media.example.test/clip.mxf </objPath>` +
					`<objPath> </objPath><objProxyPath techDescription=" Preview "> https://media.example.test/clip.mp4 </objProxyPath>` +
					`<objProxyPath techDescription=" Still ">https://media.example.test/clip.jpg</objProxyPath>` +
					metadataPath + `</objPaths>` +
					`<objPath> https://media.example.test/alternate.mxf </objPath>` +
					`<mosExternalMetadata><mosScope>STORY</mosScope><mosSchema>not-a-uri</mosSchema>` +
					`<mosPayload><value format="opaque">  untouched  </value><duration>unread</duration></mosPayload></mosExternalMetadata>`
				if shape == "nested" {
					fields = `<mosItem>` + fields + `</mosItem>`
				} else if shape == "mixed" {
					fields += `<mosItem><itemID>nested-item</itemID><itemSlug>Nested label</itemSlug>` +
						`<objID>nested-object</objID><mosID>nested.example.test</mosID>` +
						`<mosAbstract> Nested description </mosAbstract><objDur>1200</objDur><objTB>25</objTB>` +
						`<objPaths><objPath>https://media.example.test/nested.mxf</objPath>` +
						`<objProxyPath>https://media.example.test/nested.mp4</objProxyPath>` +
						`<objMetadataPath>https://media.example.test/nested.xml</objMetadataPath></objPaths>` +
						`<objPath>https://media.example.test/nested-alternate.mxf</objPath>` +
						`<mosExternalMetadata><mosScope>STORY</mosScope><mosSchema/>` +
						`<mosPayload><value>nested</value></mosPayload></mosExternalMetadata></mosItem>`
				}
				applyItemXML(t, svc, fmt.Sprintf(`<%s><roID>rundown-1</roID><roSlug>Rundown</roSlug>`+
					`<story><storyID>story-1</storyID><storySlug>Story</storySlug><item>%s</item></story></%s>`,
					message, fields, message))
				stored := itemsFor(t, context.Background(), stories, items, "rundown-1")
				if len(stored) != 1 {
					t.Fatalf("stored %d items, want 1", len(stored))
				}
				item := stored[0]
				if item.Abstract != "Complete item description" {
					t.Errorf("abstract = %q, want trimmed full description", item.Abstract)
				}
				if item.EditorialDuration != 600 {
					t.Errorf("selected samples = %d, want 600 from objDur", item.EditorialDuration)
				}
				if item.Duration != 10 || item.TimeBase != 60 || item.Metadata["objTB"] != "59.940" {
					t.Errorf("timing = %ds, display rate %d, exact rate %q; want 10s, 60, 59.940",
						item.Duration, item.TimeBase, item.Metadata["objTB"])
				}
				wantMedia := &model.MediaPaths{
					Essence: []model.MediaPath{{URL: "https://media.example.test/clip.mxf"}, {URL: "https://media.example.test/alternate.mxf"}},
					Proxy: []model.MediaPath{
						{URL: "https://media.example.test/clip.mp4", TechDescription: "Preview"},
						{URL: "https://media.example.test/clip.jpg", TechDescription: "Still"},
					},
					Metadata: []model.MediaPath{{URL: "https://media.example.test/clip.xml", TechDescription: "Data"}},
				}
				if shape == "mixed" {
					// The populated outer container wins even for a role only the nested one carries.
					wantMedia.Metadata = nil
				}
				if !reflect.DeepEqual(item.Media, wantMedia) {
					t.Errorf("media = %+v, want %+v", item.Media, wantMedia)
				}
				if len(item.ExternalMetadata) != 1 || item.ExternalMetadata[0].Schema != "not-a-uri" ||
					item.ExternalMetadata[0].Payload != `<value format="opaque">  untouched  </value><duration>unread</duration>` {
					t.Errorf("opaque metadata changed: %+v", item.ExternalMetadata)
				}
				if item.Slug != "Short label" || item.RawID != "item-1" ||
					item.ObjectID != "object-1" || item.Metadata["mosID"] != "media.example.test" || item.Order != 1 {
					t.Errorf("item identity or label changed: %+v", item)
				}
			})
		}
	}
}

func TestItemXMLUpdatesExactRate(t *testing.T) {
	for _, tc := range []struct {
		name, initialRate, incomingRate, wantExact string
		wantSeconds, wantTimeBase                  int
	}{
		{"changed", "59.940", " 50.000 ", "50.000", 12, 50},
		{"newly supplied", "", " 29.970 ", "29.970", 20, 30},
		{"omitted", "59.940", "", "59.940", 0, 0},
		{"blank", "59.940", " ", "59.940", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, stories, items := newStoryTestService(t)
			for _, rate := range []string{tc.initialRate, tc.incomingRate} {
				rateXML := ""
				if rate != "" {
					rateXML = "<objTB>" + rate + "</objTB>"
				}
				applyItemXML(t, svc, `<roList><roID>rundown-1</roID><roSlug>Rundown</roSlug>`+
					`<story><storyID>story-1</storyID><item><itemID>item-1</itemID>`+
					`<objID>object-1</objID><mosID>media.example.test</mosID><objDur>600</objDur>`+
					rateXML+`</item></story></roList>`)
			}
			stored := itemsFor(t, context.Background(), stories, items, "rundown-1")
			if len(stored) != 1 {
				t.Fatalf("stored %d items after update, want 1", len(stored))
			}
			item := stored[0]
			if item.Metadata["objTB"] != tc.wantExact {
				t.Errorf("exact rate after update = %q, want %q", item.Metadata["objTB"], tc.wantExact)
			}
			if item.EditorialDuration != 600 || item.Duration != tc.wantSeconds || item.TimeBase != tc.wantTimeBase {
				t.Errorf("timing after update = %d samples, %ds, display rate %d; want 600, %d, %d",
					item.EditorialDuration, item.Duration, item.TimeBase, tc.wantSeconds, tc.wantTimeBase)
			}
			if item.Metadata["mosID"] != "media.example.test" {
				t.Errorf("owning MOS identity lost: %v", item.Metadata)
			}
		})
	}
}

func TestRunningOrderItemTimingXML(t *testing.T) {
	for _, tc := range []struct {
		name, editorial          string
		wantSamples, wantSeconds int
	}{
		{"editorial preferred", " 300 ", 300, 5},
		{"invalid editorial falls back", "invalid", 600, 10},
		{"zero editorial is valid", "0", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, stories, items := newStoryTestService(t)
			applyItemXML(t, svc, `<roList><roID>rundown-1</roID><roSlug>Rundown</roSlug>`+
				`<story><storyID>story-1</storyID><item><itemID>item-1</itemID>`+
				`<objID>object-1</objID><mosID>media.example.test</mosID>`+
				`<itemEdDur>`+tc.editorial+`</itemEdDur><objDur>600</objDur><objTB>59.94</objTB>`+
				`</item></story></roList>`)
			stored := itemsFor(t, context.Background(), stories, items, "rundown-1")
			if len(stored) != 1 {
				t.Fatalf("stored %d items, want 1", len(stored))
			}
			item := stored[0]
			if item.EditorialDuration != tc.wantSamples || item.Duration != tc.wantSeconds ||
				item.TimeBase != 60 || item.Metadata["objTB"] != "59.94" {
				t.Errorf("timing = %d samples, %ds, display rate %d, exact rate %q; want %d, %d, 60, 59.94",
					item.EditorialDuration, item.Duration, item.TimeBase, item.Metadata["objTB"], tc.wantSamples, tc.wantSeconds)
			}
		})
	}
}

func TestROReplaceItemXML(t *testing.T) {
	svc, stories, items := newStoryTestService(t)
	applyItemXML(t, svc, `<roCreate><roID>rundown-1</roID><roSlug>Rundown</roSlug>`+
		`<story><storyID>story-1</storyID><item><itemID>old-item</itemID>`+
		`<objID>old-object</objID><mosID>media.example.test</mosID></item></story></roCreate>`)
	applyItemXML(t, svc, `<roReplace><roID>rundown-1</roID><roSlug>Replacement</roSlug>`+
		`<story><storyID>story-1</storyID><item><mosItem><itemID>new-item</itemID>`+
		`<objID>new-object</objID><mosID>media.example.test</mosID><mosAbstract> New description </mosAbstract>`+
		`<objDur>300</objDur><objTB>50</objTB><objPaths><objPath> https://media.example.test/new.mxf </objPath></objPaths>`+
		`</mosItem></item></story></roReplace>`)
	stored := itemsFor(t, context.Background(), stories, items, "rundown-1")
	if len(stored) != 1 {
		t.Fatalf("stored %d items after replace, want 1", len(stored))
	}
	item := stored[0]
	if item.RawID != "new-item" || item.ObjectID != "new-object" || item.Metadata["mosID"] != "media.example.test" {
		t.Errorf("replacement identity = %+v", item)
	}
	if item.Abstract != "New description" || item.Slug != "New description" || item.EditorialDuration != 300 ||
		item.Duration != 6 || item.TimeBase != 50 || item.Metadata["objTB"] != "50" {
		t.Errorf("replacement fields = %+v", item)
	}
	if item.Media == nil || len(item.Media.Essence) != 1 || item.Media.Essence[0].URL != "https://media.example.test/new.mxf" {
		t.Errorf("replacement media = %+v", item.Media)
	}
}

func TestROStorySendItemUpdateXML(t *testing.T) {
	svc, stories, items := newStoryTestService(t)
	ctx := context.Background()
	applyItemXML(t, svc, `<roCreate><roID>rundown-1</roID><roSlug>Rundown</roSlug></roCreate>`)
	for _, fields := range []string{
		`<mosAbstract> Retained description </mosAbstract><objDur>600</objDur><objTB>59.940</objTB>` +
			`<objPaths><objPath> https://media.example.test/clip.mxf </objPath></objPaths>` +
			`<mosExternalMetadata><mosScope>STORY</mosScope><mosSchema/>` +
			`<mosPayload><value>opaque</value></mosPayload></mosExternalMetadata>`,
		`<objDur>600</objDur><objTB> 50.000 </objTB>`,
	} {
		applyItemXML(t, svc, `<roStorySend><roID>rundown-1</roID><storyID>story-1</storyID>`+
			`<storySlug>Story</storySlug><storyBody><storyItem><mosItem><itemID>item-1</itemID>`+
			`<itemSlug>Short label</itemSlug><objID>object-1</objID><mosID>media.example.test</mosID>`+
			fields+`</mosItem></storyItem><p>[TAKE VO]</p><p>[CG :#CARD\first\\third]</p></storyBody></roStorySend>`)
	}
	stored := itemsFor(t, ctx, stories, items, "rundown-1")
	if len(stored) != 1 {
		t.Fatalf("stored %d items after resend, want 1", len(stored))
	}
	item := stored[0]
	if item.RawID != "item-1" || item.ObjectID != "object-1" || item.Metadata["mosID"] != "media.example.test" || item.Order != 1 {
		t.Errorf("resend identity = %+v", item)
	}
	if item.Abstract != "Retained description" || item.Slug != "Short label" || item.EditorialDuration != 600 ||
		item.Duration != 12 || item.TimeBase != 50 || item.Metadata["objTB"] != "50.000" {
		t.Errorf("resend fields = %+v", item)
	}
	if item.Media == nil || len(item.Media.Essence) != 1 || item.Media.Essence[0].URL != "https://media.example.test/clip.mxf" {
		t.Errorf("omitted media erased retained paths: %+v", item.Media)
	}
	if len(item.ExternalMetadata) != 1 || item.ExternalMetadata[0].Payload != `<value>opaque</value>` {
		t.Errorf("omitted external metadata erased opaque payload: %+v", item.ExternalMetadata)
	}
	_, storedStories, err := svc.GetRunningOrderWithStories(ctx, "rundown-1")
	if err != nil || len(storedStories) != 1 {
		t.Fatalf("stories after resend = %+v, error %v", storedStories, err)
	}
	cues := storedStories[0].Cues
	if len(cues) != 2 || cues[0].Kind != "PRODUCTION" || cues[0].Raw != "TAKE VO" ||
		cues[0].Order != 0 || cues[0].Paragraph != 0 || cues[1].Kind != "SERIAL_CG" ||
		cues[1].Raw != `CG :#CARD\first\\third` || cues[1].Target != ":#CARD" ||
		cues[1].Order != 1 || cues[1].Paragraph != 1 || !reflect.DeepEqual(cues[1].Fields, []string{"first", "", "third"}) {
		t.Errorf("story cues changed during item update: %+v", cues)
	}
}
