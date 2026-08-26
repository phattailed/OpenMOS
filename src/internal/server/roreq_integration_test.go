package server

import (
	"context"
	"net"
	"strings"
	"testing"

	"airshift/openmos/internal/model"
)

// The roReq/roReqAll pair is the protocol's resynchronisation mechanism: MOS 3.8.4
// §3.5.1 says a device uses roReq to "'resync' its Playlist with the NCS Running
// Order". Both were broken, in ways that cancelled out enough to go unnoticed:
//
//   - <roReq> carries a roID and asks for ONE running order, answered with roList.
//     Our roReq type had no roID field at all, so the requested identifier was
//     silently discarded, and the handler returned summaries of every running order.
//   - <roReqAll> carries no roID and asks for ALL of them, answered with roListAll.
//     Our type declared a roID, so a conformant <roReqAll/> parsed to an empty
//     identifier and the handler then failed looking it up.
//
// The Go type names were the inverse of their XML names, which is how the bindings
// came to be swapped. They are now ROReq and ROReqAll.
//
// No test covered any of this, which is why it survived. These do.

// readMOS28RawForTest returns the decoded UTF-8 XML of the next frame, so a test can
// assert on the wire shape itself rather than on what unmarshalling happens to
// tolerate. Invented attributes and wrong nesting are invisible to a struct decode.
func readMOS28RawForTest(t *testing.T, conn net.Conn) string {
	t.Helper()
	frame := readUCS2BEFrameForTest(t, conn)
	return string(stripXMLDeclarationForTest(decodeUCS2BEForTest(t, frame)))
}

func seedRunningOrder(t *testing.T, ros *memoryRunningOrders, stories *memoryStories, items *memoryItems, roID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := ros.Create(ctx, &model.RunningOrder{
		ID:       roID,
		Slug:     "Seeded " + roID,
		Duration: 1800,
	}); err != nil {
		t.Fatalf("seed running order: %v", err)
	}
	if _, err := stories.Create(ctx, &model.Story{
		ID:             roID + "/STORY-1",
		RawID:          "STORY-1",
		RunningOrderID: roID,
		Slug:           "First story",
		Order:          1,
	}); err != nil {
		t.Fatalf("seed story: %v", err)
	}
	if _, err := items.Create(ctx, &model.Item{
		ID:       roID + "/STORY-1/ITEM-1",
		RawID:    "ITEM-1",
		StoryID:  roID + "/STORY-1",
		Slug:     "First item",
		ObjectID: "OBJ-1",
		Order:    1,
	}); err != nil {
		t.Fatalf("seed item: %v", err)
	}
}

func TestRoReqReturnsRoListForTheRequestedRunningOrder(t *testing.T) {
	tcpServer, ros, stories, items := startMOS28Server(t)
	const roID = "RO-WANTED"
	seedRunningOrder(t, ros, stories, items, roID)
	// A second running order that must NOT appear in the answer.
	if _, err := ros.Create(context.Background(), &model.RunningOrder{ID: "RO-OTHER", Slug: "Other"}); err != nil {
		t.Fatalf("seed second running order: %v", err)
	}

	conn := dialMOS28(t, tcpServer)
	request := `<mos><mosID>openmos.beltware.test</mosID><ncsID>beltware.test</ncsID>` +
		`<messageID>500</messageID><roReq><roID>` + roID + `</roID></roReq></mos>`
	writeMOS28ForTest(t, conn, request)

	var reply struct {
		ROList struct {
			ROID    string `xml:"roID"`
			Slug    string `xml:"roSlug"`
			Stories []struct {
				StoryID string `xml:"storyID"`
				Items   []struct {
					ItemID string `xml:"itemID"`
				} `xml:"item"`
			} `xml:"story"`
		} `xml:"roList"`
		ROAck struct {
			RoStatus string `xml:"roStatus"`
		} `xml:"roAck"`
	}
	readMOS28XMLForTest(t, conn, &reply)

	if reply.ROAck.RoStatus != "" {
		t.Fatalf("roReq for a known running order was refused: %q", reply.ROAck.RoStatus)
	}
	if reply.ROList.ROID != roID {
		t.Fatalf("roList roID = %q, want %q: the requested identifier must be honoured",
			reply.ROList.ROID, roID)
	}
	if len(reply.ROList.Stories) != 1 {
		t.Fatalf("roList carried %d stories, want 1: roList is a full build, not a summary",
			len(reply.ROList.Stories))
	}
	if got := reply.ROList.Stories[0].StoryID; got != "STORY-1" {
		t.Errorf("storyID = %q, want STORY-1", got)
	}
	if len(reply.ROList.Stories[0].Items) != 1 {
		t.Errorf("story carried %d items, want 1", len(reply.ROList.Stories[0].Items))
	}
}

func TestRoReqForUnknownRunningOrderIsNacked(t *testing.T) {
	tcpServer, _, _, _ := startMOS28Server(t)
	conn := dialMOS28(t, tcpServer)

	request := `<mos><mosID>openmos.beltware.test</mosID><ncsID>beltware.test</ncsID>` +
		`<messageID>501</messageID><roReq><roID>RO-ABSENT</roID></roReq></mos>`
	writeMOS28ForTest(t, conn, request)

	var reply struct {
		ROAck struct {
			ROID     string `xml:"roID"`
			RoStatus string `xml:"roStatus"`
		} `xml:"roAck"`
	}
	readMOS28XMLForTest(t, conn, &reply)

	// §3.5.1: roList "or roAck (roAck with a NACK value is sent if: the Running Order
	// ID is not valid [...] or if the Running Order is not available)".
	if !strings.Contains(reply.ROAck.RoStatus, "NACK") {
		t.Errorf("roStatus = %q, want a NACK for an unavailable running order", reply.ROAck.RoStatus)
	}
	if reply.ROAck.ROID != "RO-ABSENT" {
		t.Errorf("roAck roID = %q, want the requested RO-ABSENT so the peer can correlate",
			reply.ROAck.ROID)
	}
}

func TestRoReqAllReturnsRoListAllSummaries(t *testing.T) {
	tcpServer, ros, stories, items := startMOS28Server(t)
	seedRunningOrder(t, ros, stories, items, "RO-A")
	seedRunningOrder(t, ros, stories, items, "RO-B")

	conn := dialMOS28(t, tcpServer)
	// Self-closing, as real devices send it, and carrying no roID.
	request := `<mos><mosID>openmos.beltware.test</mosID><ncsID>beltware.test</ncsID>` +
		`<messageID>502</messageID><roReqAll/></mos>`
	writeMOS28ForTest(t, conn, request)

	var reply struct {
		ROListAll struct {
			ROs []struct {
				ROID    string `xml:"roID"`
				Slug    string `xml:"roSlug"`
				Stories []struct {
					StoryID string `xml:"storyID"`
				} `xml:"story"`
			} `xml:"ro"`
		} `xml:"roListAll"`
		ROAck struct {
			RoStatus string `xml:"roStatus"`
		} `xml:"roAck"`
	}
	readMOS28XMLForTest(t, conn, &reply)

	if reply.ROAck.RoStatus != "" {
		t.Fatalf("roReqAll was refused: %q", reply.ROAck.RoStatus)
	}
	if len(reply.ROListAll.ROs) != 2 {
		t.Fatalf("roListAll carried %d running orders, want 2", len(reply.ROListAll.ROs))
	}

	seen := map[string]bool{}
	for _, ro := range reply.ROListAll.ROs {
		seen[ro.ROID] = true
		// roListAll is discovery: summaries only. Sending stories here would make it
		// indistinguishable from roList and defeat the two-stage recovery the spec
		// describes.
		if len(ro.Stories) != 0 {
			t.Errorf("roListAll entry %s carried stories; it must be a summary", ro.ROID)
		}
	}
	if !seen["RO-A"] || !seen["RO-B"] {
		t.Errorf("roListAll omitted a running order: %v", seen)
	}
}

// TestRoListCarriesNoInventedAttributes guards the other half of the defect: our
// roList was decorated with requestID, timestamp and source attributes the
// specification does not define, and wrapped its content in a nested <ro> element,
// which belongs to roListAll.
func TestRoListCarriesNoInventedAttributes(t *testing.T) {
	tcpServer, ros, stories, items := startMOS28Server(t)
	seedRunningOrder(t, ros, stories, items, "RO-SHAPE")

	conn := dialMOS28(t, tcpServer)
	request := `<mos><mosID>openmos.beltware.test</mosID><ncsID>beltware.test</ncsID>` +
		`<messageID>503</messageID><roReq><roID>RO-SHAPE</roID></roReq></mos>`
	writeMOS28ForTest(t, conn, request)

	raw := readMOS28RawForTest(t, conn)
	if !strings.Contains(raw, "<roList>") {
		t.Fatalf("expected a bare <roList> element, got: %s", raw)
	}
	for _, forbidden := range []string{"requestID=", "timestamp=", "source="} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("roList carried %s, which the spec does not define: %s", forbidden, raw)
		}
	}
	if strings.Contains(raw, "<ro>") {
		t.Errorf("roList nested its content in <ro>, which is roListAll's shape: %s", raw)
	}
}
