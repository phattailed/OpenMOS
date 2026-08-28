package server

import (
	"context"
	"strings"
	"testing"

	"airshift/openmos/internal/model"
)

// Parsing the payload is half the job. It also has to survive being stored, or it is lost
// the moment a running order is persisted -- which is the point at which a device is supposed
// to be able to hand it onward.
//
// The model previously held only map[string]string, which cannot represent nested XML. So
// even after the wire types were fixed, a payload would have been dropped on the way into the
// repository.

func TestExternalMetadataSurvivesIngest(t *testing.T) {
	tcpServer, runningOrders, stories, items := startMOS28Server(t)
	conn := dialMOS28(t, tcpServer)

	// Metadata at all three levels the spec allows, with nested payloads at each.
	request := `<mos><mosID>openmos.beltware.test</mosID><ncsID>beltware.test</ncsID>` +
		`<messageID>900</messageID><roCreate>` +
		`<roID>RO-META</roID><roSlug>With metadata</roSlug>` +
		`<mosExternalMetadata><mosScope>PLAYLIST</mosScope><mosSchema>ro-schema</mosSchema>` +
		`<mosPayload><Owner>OPERATOR</Owner><show>10pm</show></mosPayload></mosExternalMetadata>` +
		`<story><storyID>S-1</storyID><storySlug>First</storySlug>` +
		`<mosExternalMetadata><mosScope>STORY</mosScope><mosSchema>story-schema</mosSchema>` +
		`<mosPayload><RevisionNumber>5</RevisionNumber></mosPayload></mosExternalMetadata>` +
		`<item><itemID>I-1</itemID><objID>OBJ-1</objID><mosID>openmos.beltware.test</mosID>` +
		`<mosExternalMetadata><mosScope>PLAYLIST</mosScope><mosSchema>item-schema</mosSchema>` +
		`<mosPayload><transitionMode>2</transitionMode><nested><deep>x</deep></nested></mosPayload>` +
		`</mosExternalMetadata></item>` +
		`</story></roCreate></mos>`
	writeMOS28ForTest(t, conn, request)

	var ack struct {
		ROAck struct {
			Status string `xml:"roStatus"`
		} `xml:"roAck"`
	}
	readMOS28XMLForTest(t, conn, &ack)
	if ack.ROAck.Status != "OK" {
		t.Fatalf("roCreate was refused: %q", ack.ROAck.Status)
	}

	ctx := context.Background()

	ro, err := runningOrders.Get(ctx, "RO-META")
	if err != nil {
		t.Fatalf("running order missing: %v", err)
	}
	assertMetadata(t, "running order", ro.ExternalMetadata, "ro-schema", "<show>10pm</show>")

	stored, err := stories.ListByRunningOrder(ctx, "RO-META")
	if err != nil || len(stored) != 1 {
		t.Fatalf("expected 1 story, got %d (err %v)", len(stored), err)
	}
	assertMetadata(t, "story", stored[0].ExternalMetadata, "story-schema",
		"<RevisionNumber>5</RevisionNumber>")

	storedItems, err := items.ListByStory(ctx, stored[0].ID)
	if err != nil || len(storedItems) != 1 {
		t.Fatalf("expected 1 item, got %d (err %v)", len(storedItems), err)
	}
	// The nested payload is the interesting one: a flat key/value model could not hold it.
	assertMetadata(t, "item", storedItems[0].ExternalMetadata, "item-schema",
		"<nested><deep>x</deep></nested>")
}

// assertMetadata checks that one preserved block carries the expected schema and that its
// payload still contains a distinctive fragment of what was sent.
func assertMetadata(t *testing.T, level string, blocks []model.ExternalMetadata, wantSchema, wantFragment string) {
	t.Helper()
	if len(blocks) == 0 {
		t.Errorf("%s metadata was not stored at all; the payload is meant to be carried onward", level)
		return
	}
	got := blocks[0]
	if got.Schema != wantSchema {
		t.Errorf("%s mosSchema = %q, want %q", level, got.Schema, wantSchema)
	}
	if !strings.Contains(got.Payload, wantFragment) {
		t.Errorf("%s payload lost %q; got %q", level, wantFragment, got.Payload)
	}
	if got.Scope == "" {
		t.Errorf("%s mosScope was lost; it controls how far the block propagates", level)
	}
}
