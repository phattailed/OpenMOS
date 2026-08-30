package bridge

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"strings"
	"testing"

	"airshift/openmos/internal/model"
)

// fakeReader is an in-memory RundownReader, so the bridge can be exercised end to
// end without repositories, an event bus or a live ENPS. It stands in for the
// data the MOS core would have persisted from roCreate/roStorySend.
type fakeReader struct {
	ros     []*model.RunningOrder
	stories map[string][]*model.Story
	items   map[string][]*model.Item
}

func (f *fakeReader) ListRunningOrders(_ context.Context) ([]*model.RunningOrder, error) {
	return f.ros, nil
}

func (f *fakeReader) GetRunningOrderWithStories(_ context.Context, id string) (*model.RunningOrder, []*model.Story, error) {
	for _, ro := range f.ros {
		if ro.ID == id {
			return ro, f.stories[id], nil
		}
	}
	return nil, nil, context.Canceled
}

func (f *fakeReader) GetItemsForStory(_ context.Context, storyID string) ([]*model.Item, error) {
	return f.items[storyID], nil
}

// sampleReader builds a small but representative rundown: one running order, two
// stories, one of which has two items and carries verbatim external metadata.
func sampleReader() *fakeReader {
	return &fakeReader{
		ros: []*model.RunningOrder{
			{ID: "RO1", Slug: "Evening News", Channel: "A", Duration: 1800, Status: model.StatusReady},
		},
		stories: map[string][]*model.Story{
			"RO1": {
				{ID: "RO1/S1", RawID: "S1", RunningOrderID: "RO1", Slug: "Cold Open", Number: "A1", Presenter: "Alex", Duration: 30, Order: 1, Status: model.StatusReady},
				{ID: "RO1/S2", RawID: "S2", RunningOrderID: "RO1", Slug: "Headlines", Number: "A2", Presenter: "Sam", Duration: 90, Order: 2, Status: model.StatusPending,
					ExternalMetadata: []model.ExternalMetadata{
						{Scope: "STORY", Schema: "http://vmix/graphic", Payload: "<lowerthird>Sam</lowerthird>"},
					}},
			},
		},
		items: map[string][]*model.Item{
			"RO1/S1": {
				{ID: "RO1/S1/I1", RawID: "I1", StoryID: "RO1/S1", Slug: "Open VT", ObjectID: "OBJ100", Duration: 30, Order: 1, Status: model.StatusReady},
			},
			// S2 intentionally has no items, to prove empty stories still emit a row.
		},
	}
}

func TestBuildProducesDeterministicRows(t *testing.T) {
	b := New(sampleReader(), nil) // nil -> default fields
	if err := func() error { b.rebuild(context.Background()); return nil }(); err != nil {
		t.Fatal(err)
	}
	snap := b.Snapshot()

	// One item row for S1 + one empty-story row for S2 = 2 rows.
	if len(snap.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(snap.Rows))
	}
	if snap.Rows[0].StorySlug != "Cold Open" || snap.Rows[0].ItemSlug != "Open VT" {
		t.Errorf("row 0 unexpected: %+v", snap.Rows[0])
	}
	if snap.Rows[1].StorySlug != "Headlines" || snap.Rows[1].ItemSlug != "" {
		t.Errorf("row 1 should be empty-story row: %+v", snap.Rows[1])
	}
}

func TestExternalMetadataPreservedVerbatim(t *testing.T) {
	b := New(sampleReader(), []string{"story.slug", "external.http://vmix/graphic"})
	b.rebuild(context.Background())
	snap := b.Snapshot()

	got := snap.Rows[1].value("external.http://vmix/graphic")
	want := "<lowerthird>Sam</lowerthird>"
	if got != want {
		t.Errorf("external metadata not preserved: got %q want %q", got, want)
	}
}

func TestRenderCSVHasHeaderAndRows(t *testing.T) {
	b := New(sampleReader(), []string{"story.number", "story.slug", "item.slug", "item.duration"})
	b.rebuild(context.Background())
	out, err := RenderCSV(b.Snapshot(), b.Fields())
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(strings.NewReader(string(out))).ReadAll()
	if err != nil {
		t.Fatalf("output is not valid CSV: %v", err)
	}
	if len(records) != 3 { // header + 2 rows
		t.Fatalf("expected 3 CSV records, got %d", len(records))
	}
	if records[0][1] != "story.slug" {
		t.Errorf("unexpected header: %v", records[0])
	}
	if records[1][2] != "Open VT" {
		t.Errorf("expected first data row item slug 'Open VT', got %v", records[1])
	}
}

func TestRenderJSONIsValidAndKeyed(t *testing.T) {
	b := New(sampleReader(), []string{"story.slug", "item.slug"})
	b.rebuild(context.Background())
	out, err := RenderJSON(b.Snapshot(), b.Fields())
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Fields []string            `json:"fields"`
		Rows   []map[string]string `json:"rows"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if len(payload.Rows) != 2 {
		t.Fatalf("expected 2 JSON rows, got %d", len(payload.Rows))
	}
	if payload.Rows[0]["story.slug"] != "Cold Open" {
		t.Errorf("unexpected JSON row 0: %v", payload.Rows[0])
	}
}

func TestRenderXMLIsValid(t *testing.T) {
	b := New(sampleReader(), []string{"story.slug", "item.slug"})
	b.rebuild(context.Background())
	out, err := RenderXML(b.Snapshot(), b.Fields())
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Rows []struct {
			Fields []struct {
				Name  string `xml:"name,attr"`
				Value string `xml:",chardata"`
			} `xml:"field"`
		} `xml:"row"`
	}
	if err := xml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output is not valid XML: %v", err)
	}
	if len(doc.Rows) != 2 {
		t.Fatalf("expected 2 XML rows, got %d", len(doc.Rows))
	}
}

func TestUnknownFieldDegradesToBlank(t *testing.T) {
	b := New(sampleReader(), []string{"story.slug", "does.not.exist"})
	b.rebuild(context.Background())
	out, err := RenderCSV(b.Snapshot(), b.Fields())
	if err != nil {
		t.Fatal(err)
	}
	records, _ := csv.NewReader(strings.NewReader(string(out))).ReadAll()
	// Header present, unknown column blank in data rows, no panic.
	if records[0][1] != "does.not.exist" {
		t.Errorf("unknown field should still appear as a header column: %v", records[0])
	}
	if records[1][1] != "" {
		t.Errorf("unknown field should render blank, got %q", records[1][1])
	}
}

func TestMetadataPrecedenceItemOverStory(t *testing.T) {
	r := sampleReader()
	r.stories["RO1"][0].Metadata = map[string]string{"tag": "story-level"}
	r.items["RO1/S1"][0].Metadata = map[string]string{"tag": "item-level"}
	b := New(r, []string{"meta.tag"})
	b.rebuild(context.Background())
	snap := b.Snapshot()
	if got := snap.Rows[0].value("meta.tag"); got != "item-level" {
		t.Errorf("item metadata should win over story: got %q", got)
	}
}
