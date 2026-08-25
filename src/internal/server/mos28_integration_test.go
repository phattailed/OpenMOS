package server

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"airshift/openmos/internal/config"
	"airshift/openmos/internal/events"
	"airshift/openmos/internal/model"
	"airshift/openmos/internal/service"
)

func TestMOS28ROCreatePersistsAndAcknowledgesOnSameSocket(t *testing.T) {
	tcpServer, runningOrders, stories, items := startMOS28Server(t)
	conn := dialMOS28(t, tcpServer)

	const request = `<mos>
  <mosID>openmos.beltware.test</mosID>
  <ncsID>beltware.test</ncsID>
  <messageID>41</messageID>
  <roCreate>
    <roID>RO-41</roID>
    <roSlug>Non-air tracer</roSlug>
    <roEdDur>00:01:00</roEdDur>
    <story>
      <storyID>STORY-1</storyID>
      <storySlug>Tracer story</storySlug>
      <item>
        <itemID>ITEM-1</itemID>
        <itemSlug>Tracer graphic</itemSlug>
        <objID>OBJ-1</objID>
        <mosID>openmos.beltware.test</mosID>
        <itemEdDur>25</itemEdDur>
      </item>
    </story>
  </roCreate>
</mos>`
	writeMOS28ForTest(t, conn, request)

	var ack struct {
		XMLName   xml.Name `xml:"mos"`
		MosID     string   `xml:"mosID"`
		NcsID     string   `xml:"ncsID"`
		MessageID string   `xml:"messageID"`
		ROAck     struct {
			ROID   string `xml:"roID"`
			Status string `xml:"roStatus"`
		} `xml:"roAck"`
	}
	readMOS28XMLForTest(t, conn, &ack)
	if ack.MosID != "openmos.beltware.test" || ack.NcsID != "beltware.test" {
		t.Fatalf("unexpected ACK route: mosID=%q ncsID=%q", ack.MosID, ack.NcsID)
	}
	if ack.MessageID != "41" {
		t.Fatalf("ACK messageID = %q, want request messageID 41", ack.MessageID)
	}
	if ack.ROAck.ROID != "RO-41" || ack.ROAck.Status != "OK" {
		t.Fatalf("unexpected roAck: roID=%q status=%q", ack.ROAck.ROID, ack.ROAck.Status)
	}
	_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("receive-only connection emitted an unsolicited message after the ACK")
	} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("connection closed after ACK: %v", err)
	}
	if ro := runningOrders.value("RO-41"); ro == nil || ro.Slug != "Non-air tracer" || ro.Duration != 60 {
		t.Fatalf("running order was not persisted: %#v", ro)
	}
	if story := stories.value("RO-41/STORY-1"); story == nil || story.RawID != "STORY-1" || story.RunningOrderID != "RO-41" {
		t.Fatalf("story was not persisted: %#v", story)
	}
	if item := items.value("RO-41/STORY-1/ITEM-1"); item == nil || item.RawID != "ITEM-1" || item.StoryID != "RO-41/STORY-1" || item.ObjectID != "OBJ-1" || item.Duration != 25 {
		t.Fatalf("item was not persisted: %#v", item)
	}
}

func TestMOS28AcceptsSplitUCS2BEAndRepliesUCS2BE(t *testing.T) {
	tcpServer, runningOrders, _, _ := startMOS28Server(t)
	conn := dialMOS28(t, tcpServer)
	request := encodeUCS2BEForTest("\ufeff" + `<?xml version="1.0" encoding="UTF-16"?>
<mos><mosID>openmos.beltware.test</mosID><ncsID>beltware.test</ncsID><messageID>x2A</messageID><roCreate><roID>RO-É</roID><roSlug>Café 東京</roSlug></roCreate></mos>`)
	for _, part := range [][]byte{request[:1], request[1:17], request[17:]} {
		if _, err := conn.Write(part); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	rawAck := readUCS2BEFrameForTest(t, conn)
	if len(rawAck) < 2 || rawAck[0] != 0 || rawAck[1] != '<' {
		t.Fatalf("ACK is not UCS-2BE XML: % x", rawAck[:min(len(rawAck), 8)])
	}
	var ack struct {
		MessageID string `xml:"messageID"`
		ROAck     struct {
			ROID   string `xml:"roID"`
			Status string `xml:"roStatus"`
		} `xml:"roAck"`
	}
	if err := xml.Unmarshal(stripXMLDeclarationForTest(decodeUCS2BEForTest(t, rawAck)), &ack); err != nil {
		t.Fatalf("decode ACK XML: %v", err)
	}
	if ack.MessageID != "x2A" || ack.ROAck.ROID != "RO-É" || ack.ROAck.Status != "OK" {
		t.Fatalf("unexpected ACK: %#v", ack)
	}
	if ro := runningOrders.value("RO-É"); ro == nil || ro.Slug != "Café 東京" {
		t.Fatalf("non-ASCII running order was not persisted: %#v", ro)
	}
}

func TestMOS28InvalidFrameClosesOnlyThatConnection(t *testing.T) {
	tcpServer, _, _, _ := startMOS28Server(t)
	invalid := dialMOS28(t, tcpServer)
	writeMOS28ForTest(t, invalid, `<mos><mosID>openmos.beltware.test</mosID><ncsID>beltware.test</ncsID><messageID>1</messageID><bogus/></mos>`)
	_ = invalid.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := invalid.Read(make([]byte, 1)); err == nil {
		t.Fatal("invalid MOS frame left its connection open")
	} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		t.Fatalf("invalid MOS frame caused a parse loop: %v", err)
	}

	valid := dialMOS28(t, tcpServer)
	const request = `<mos><mosID>openmos.beltware.test</mosID><ncsID>beltware.test</ncsID><messageID>2</messageID><roCreate><roID>RO-2</roID><roSlug>Still alive</roSlug></roCreate></mos>`
	writeMOS28ForTest(t, valid, request)
	_ = valid.SetReadDeadline(time.Now().Add(time.Second))
	var ack struct {
		ROAck struct {
			ROID string `xml:"roID"`
		} `xml:"roAck"`
	}
	readMOS28XMLForTest(t, valid, &ack)
	if ack.ROAck.ROID != "RO-2" {
		t.Fatalf("unexpected ACK after invalid frame: %#v", ack)
	}
}

func TestMOS28RejectsBarePayloadBeforePersistence(t *testing.T) {
	tcpServer, runningOrders, _, _ := startMOS28Server(t)
	conn := dialMOS28(t, tcpServer)
	writeMOS28ForTest(t, conn, `<roCreate><roID>BARE</roID><roSlug>Must not persist</roSlug></roCreate>`)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("bare payload was accepted")
	} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		t.Fatalf("bare payload left connection open: %v", err)
	}
	if runningOrders.value("BARE") != nil {
		t.Fatal("bare payload mutated persistence")
	}
}

func TestMOS28CapsAnIncompleteFrame(t *testing.T) {
	tcpServer, _, _, _ := startMOS28Server(t)
	conn := dialMOS28(t, tcpServer)
	oversized := encodeUCS2BEForTest("<mos>" + strings.Repeat("x", 2<<20))
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, _ = conn.Write(oversized)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("oversized frame left its connection open")
	} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		t.Fatalf("oversized frame was not capped: %v", err)
	}
}

func TestMOS28RejectsUnexpectedNCSID(t *testing.T) {
	tcpServer, _, _, _ := startMOS28Server(t)
	conn := dialMOS28(t, tcpServer)
	const request = `<mos><mosID>openmos.beltware.test</mosID><ncsID>other-ncs.test</ncsID><messageID>3</messageID><roCreate><roID>RO-3</roID><roSlug>Wrong NCS</roSlug></roCreate></mos>`
	writeMOS28ForTest(t, conn, request)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("unexpected NCS identity was accepted")
	} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		t.Fatalf("unexpected NCS identity left connection open: %v", err)
	}
}

func TestMOS28RejectsInvalidMessageID(t *testing.T) {
	tcpServer, _, _, _ := startMOS28Server(t)
	conn := dialMOS28(t, tcpServer)
	const request = `<mos><mosID>openmos.beltware.test</mosID><ncsID>beltware.test</ncsID><messageID>0</messageID><roCreate><roID>RO-0</roID><roSlug>Invalid ID</roSlug></roCreate></mos>`
	writeMOS28ForTest(t, conn, request)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("invalid messageID was accepted")
	} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		t.Fatalf("invalid messageID left connection open: %v", err)
	}
}

func TestMOS28EchoesAcceptedMessageIDFormats(t *testing.T) {
	tcpServer, _, _, _ := startMOS28Server(t)
	for _, messageID := range []string{"42", "0x2A", "x2A"} {
		t.Run(messageID, func(t *testing.T) {
			conn := dialMOS28(t, tcpServer)
			request := `<mos><mosID>openmos.beltware.test</mosID><ncsID>beltware.test</ncsID><messageID>` + messageID + `</messageID><roCreate><roID>RO-` + messageID + `</roID><roSlug>Message ID tracer</roSlug></roCreate></mos>`
			writeMOS28ForTest(t, conn, request)
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			var ack struct {
				MessageID string `xml:"messageID"`
			}
			readMOS28XMLForTest(t, conn, &ack)
			if ack.MessageID != messageID {
				t.Fatalf("ACK messageID = %q, want %q", ack.MessageID, messageID)
			}
		})
	}
}

func TestMOS28ROReplaceReplacesPersistedContentAndAcknowledges(t *testing.T) {
	tcpServer, runningOrders, stories, items := startMOS28Server(t)
	ctx := context.Background()
	_, _ = runningOrders.Create(ctx, &model.RunningOrder{ID: "RO-42", Slug: "Old rundown"})
	_, _ = stories.Create(ctx, &model.Story{ID: "OLD-STORY", RunningOrderID: "RO-42"})
	_, _ = items.Create(ctx, &model.Item{ID: "OLD-ITEM", StoryID: "OLD-STORY"})

	conn := dialMOS28(t, tcpServer)
	const request = `<mos>
  <mosID>openmos.beltware.test</mosID>
  <ncsID>beltware.test</ncsID>
  <messageID>42</messageID>
  <roReplace>
    <roID>RO-42</roID>
    <roSlug>Replacement rundown</roSlug>
    <roEdDur>01:02:03</roEdDur>
    <story>
      <storyID>NEW-STORY</storyID>
      <storySlug>Replacement story</storySlug>
      <item>
        <itemID>NEW-ITEM</itemID>
        <itemSlug>Replacement item</itemSlug>
        <objID>NEW-OBJECT</objID>
        <mosID>openmos.beltware.test</mosID>
      </item>
    </story>
  </roReplace>
</mos>`
	writeMOS28ForTest(t, conn, request)
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	var ack struct {
		XMLName xml.Name `xml:"mos"`
		ROAck   struct {
			ROID   string `xml:"roID"`
			Status string `xml:"roStatus"`
		} `xml:"roAck"`
	}
	readMOS28XMLForTest(t, conn, &ack)
	if ack.ROAck.ROID != "RO-42" || ack.ROAck.Status != "OK" {
		t.Fatalf("unexpected roReplace ACK: %#v", ack.ROAck)
	}
	if ro := runningOrders.value("RO-42"); ro == nil || ro.Slug != "Replacement rundown" || ro.Duration != 3723 {
		t.Fatalf("running order was not replaced: %#v", ro)
	}
	if stories.value("OLD-STORY") != nil || items.value("OLD-ITEM") != nil {
		t.Fatal("replaced story content was retained")
	}
	if story := stories.value("RO-42/NEW-STORY"); story == nil || story.RawID != "NEW-STORY" || story.RunningOrderID != "RO-42" {
		t.Fatalf("replacement story was not persisted: %#v", story)
	}
	if item := items.value("RO-42/NEW-STORY/NEW-ITEM"); item == nil || item.RawID != "NEW-ITEM" || item.StoryID != "RO-42/NEW-STORY" || item.ObjectID != "NEW-OBJECT" {
		t.Fatalf("replacement item was not persisted: %#v", item)
	}
}

func TestMOS28ROStorySendUpdatesCompositeStoryAndAcknowledges(t *testing.T) {
	tcpServer, _, stories, _ := startMOS28Server(t)
	ctx := context.Background()
	_, _ = stories.Create(ctx, &model.Story{
		ID:             "RO-STORY/STORY-1",
		RawID:          "STORY-1",
		RunningOrderID: "RO-STORY",
		Slug:           "Before",
	})

	conn := dialMOS28(t, tcpServer)
	const request = `<mos><mosID>openmos.beltware.test</mosID><ncsID>beltware.test</ncsID><messageID>84</messageID><roStorySend><roID>RO-STORY</roID><storyID>STORY-1</storyID><storySlug>After</storySlug><storyBody><p>Tracer body</p></storyBody></roStorySend></mos>`
	writeMOS28ForTest(t, conn, request)
	var ack struct {
		MessageID string `xml:"messageID"`
		ROAck     struct {
			ROID   string `xml:"roID"`
			Status string `xml:"roStatus"`
		} `xml:"roAck"`
	}
	readMOS28XMLForTest(t, conn, &ack)
	if ack.MessageID != "84" || ack.ROAck.ROID != "RO-STORY" || ack.ROAck.Status != "OK" {
		t.Fatalf("unexpected roStorySend ACK: %#v", ack)
	}
	story := stories.value("RO-STORY/STORY-1")
	if story == nil || story.RawID != "STORY-1" || story.RunningOrderID != "RO-STORY" || story.Slug != "After" {
		t.Fatalf("composite story was not updated: %#v", story)
	}
	if stories.value("STORY-1") != nil {
		t.Fatal("roStorySend created a raw-ID duplicate")
	}
}

func TestMOS28RODeleteDeletesPersistedContentAndAcknowledges(t *testing.T) {
	tcpServer, runningOrders, stories, items := startMOS28Server(t)
	ctx := context.Background()
	_, _ = runningOrders.Create(ctx, &model.RunningOrder{ID: "RO-DELETE", Slug: "Disposable"})
	_, _ = stories.Create(ctx, &model.Story{ID: "RO-DELETE/STORY", RawID: "STORY", RunningOrderID: "RO-DELETE"})
	_, _ = items.Create(ctx, &model.Item{ID: "RO-DELETE/STORY/ITEM", RawID: "ITEM", StoryID: "RO-DELETE/STORY"})

	conn := dialMOS28(t, tcpServer)
	const request = `<mos><mosID>openmos.beltware.test</mosID><ncsID>beltware.test</ncsID><messageID>85</messageID><roDelete><roID>RO-DELETE</roID></roDelete></mos>`
	writeMOS28ForTest(t, conn, request)
	var ack struct {
		MessageID string `xml:"messageID"`
		ROAck     struct {
			ROID   string `xml:"roID"`
			Status string `xml:"roStatus"`
		} `xml:"roAck"`
	}
	readMOS28XMLForTest(t, conn, &ack)
	if ack.MessageID != "85" || ack.ROAck.ROID != "RO-DELETE" || ack.ROAck.Status != "OK" {
		t.Fatalf("unexpected roDelete ACK: %#v", ack)
	}
	if runningOrders.value("RO-DELETE") != nil || stories.value("RO-DELETE/STORY") != nil || items.value("RO-DELETE/STORY/ITEM") != nil {
		t.Fatal("roDelete retained persisted content")
	}
}

func TestMOS28RODeleteReportsChildDeleteFailure(t *testing.T) {
	tcpServer, runningOrders, stories, items := startMOS28Server(t)
	ctx := context.Background()
	_, _ = runningOrders.Create(ctx, &model.RunningOrder{ID: "RO-DELETE-FAIL", Slug: "Keep on failure"})
	_, _ = stories.Create(ctx, &model.Story{ID: "RO-DELETE-FAIL/STORY", RawID: "STORY", RunningOrderID: "RO-DELETE-FAIL"})
	_, _ = items.Create(ctx, &model.Item{ID: "RO-DELETE-FAIL/STORY/ITEM", RawID: "ITEM", StoryID: "RO-DELETE-FAIL/STORY"})
	items.deleteErr = errors.New("injected item delete failure")

	conn := dialMOS28(t, tcpServer)
	const request = `<mos><mosID>openmos.beltware.test</mosID><ncsID>beltware.test</ncsID><messageID>86</messageID><roDelete><roID>RO-DELETE-FAIL</roID></roDelete></mos>`
	writeMOS28ForTest(t, conn, request)
	var ack struct {
		ROAck struct {
			Status string `xml:"roStatus"`
		} `xml:"roAck"`
	}
	readMOS28XMLForTest(t, conn, &ack)
	if ack.ROAck.Status != "ERROR" {
		t.Fatalf("ACK status = %q, want ERROR", ack.ROAck.Status)
	}
	if runningOrders.value("RO-DELETE-FAIL") == nil || stories.value("RO-DELETE-FAIL/STORY") == nil || items.value("RO-DELETE-FAIL/STORY/ITEM") == nil {
		t.Fatal("roDelete continued after a child deletion failed")
	}
}

func TestMOS28PersistsDuplicateItemIDsUnderCompositeChildKeys(t *testing.T) {
	tcpServer, _, stories, items := startMOS28Server(t)
	conn := dialMOS28(t, tcpServer)
	const request = `<mos><mosID>openmos.beltware.test</mosID><ncsID>beltware.test</ncsID><messageID>77</messageID><roCreate>
<roID>RO-DUP</roID><roSlug>Duplicate item tracer</roSlug>
<story><storyID>STORY-A</storyID><storySlug>First</storySlug><item><itemID>ITEM-SAME</itemID><itemSlug>First item</itemSlug><objID>OBJECT-A</objID><mosID>openmos.beltware.test</mosID></item></story>
<story><storyID>STORY-B</storyID><storySlug>Second</storySlug><item><itemID>ITEM-SAME</itemID><itemSlug>Second item</itemSlug><objID>OBJECT-B</objID><mosID>openmos.beltware.test</mosID></item></story>
</roCreate></mos>`
	writeMOS28ForTest(t, conn, request)
	var ack struct {
		ROAck struct {
			Status string `xml:"roStatus"`
		} `xml:"roAck"`
	}
	readMOS28XMLForTest(t, conn, &ack)
	if ack.ROAck.Status != "OK" {
		t.Fatalf("unexpected ACK status: %q", ack.ROAck.Status)
	}

	storyA := stories.value("RO-DUP/STORY-A")
	storyB := stories.value("RO-DUP/STORY-B")
	if storyA == nil || storyA.RawID != "STORY-A" || storyB == nil || storyB.RawID != "STORY-B" {
		t.Fatalf("stories did not retain composite and raw IDs: A=%#v B=%#v", storyA, storyB)
	}
	itemA := items.value("RO-DUP/STORY-A/ITEM-SAME")
	itemB := items.value("RO-DUP/STORY-B/ITEM-SAME")
	if itemA == nil || itemA.RawID != "ITEM-SAME" || itemA.StoryID != storyA.ID {
		t.Fatalf("first item missing or mis-keyed: %#v", itemA)
	}
	if itemB == nil || itemB.RawID != "ITEM-SAME" || itemB.StoryID != storyB.ID {
		t.Fatalf("second item missing or mis-keyed: %#v", itemB)
	}
}

func TestMOS28RepeatedROCreateUpdatesCompositeItem(t *testing.T) {
	tcpServer, _, _, items := startMOS28Server(t)
	conn := dialMOS28(t, tcpServer)
	for _, request := range []string{
		`<mos><mosID>openmos.beltware.test</mosID><ncsID>beltware.test</ncsID><messageID>80</messageID><roCreate><roID>RO-UPDATE</roID><roSlug>Update tracer</roSlug><story><storyID>STORY</storyID><storySlug>Story</storySlug><item><itemID>ITEM</itemID><itemSlug>Before</itemSlug><objID>OBJECT</objID><mosID>openmos.beltware.test</mosID></item></story></roCreate></mos>`,
		`<mos><mosID>openmos.beltware.test</mosID><ncsID>beltware.test</ncsID><messageID>81</messageID><roCreate><roID>RO-UPDATE</roID><roSlug>Update tracer</roSlug><story><storyID>STORY</storyID><storySlug>Story</storySlug><item><itemID>ITEM</itemID><itemSlug>After</itemSlug><objID>OBJECT</objID><mosID>openmos.beltware.test</mosID></item></story></roCreate></mos>`,
	} {
		writeMOS28ForTest(t, conn, request)
		var ack struct {
			ROAck struct {
				Status string `xml:"roStatus"`
			} `xml:"roAck"`
		}
		readMOS28XMLForTest(t, conn, &ack)
		if ack.ROAck.Status != "OK" {
			t.Fatalf("ACK status = %q, want OK", ack.ROAck.Status)
		}
	}
	if item := items.value("RO-UPDATE/STORY/ITEM"); item == nil || item.RawID != "ITEM" || item.Slug != "After" {
		t.Fatalf("composite item was not updated: %#v", item)
	}
}

func TestMOS28RejectsMissingRequiredFieldsBeforeMutation(t *testing.T) {
	tcpServer, runningOrders, stories, items := startMOS28Server(t)
	conn := dialMOS28(t, tcpServer)
	const request = `<mos><mosID>openmos.beltware.test</mosID><ncsID>beltware.test</ncsID><messageID>78</messageID><roCreate>
<roID>RO-INVALID</roID><roSlug>Must not persist</roSlug>
<story><storyID>STORY-INVALID</storyID><storySlug>Must not persist</storySlug><item><itemSlug>Missing ID</itemSlug></item></story>
</roCreate></mos>`
	writeMOS28ForTest(t, conn, request)
	var ack struct {
		ROAck struct {
			Status string `xml:"roStatus"`
		} `xml:"roAck"`
	}
	readMOS28XMLForTest(t, conn, &ack)
	if ack.ROAck.Status != "ERROR" {
		t.Fatalf("ACK status = %q, want ERROR", ack.ROAck.Status)
	}
	if runningOrders.value("RO-INVALID") != nil || stories.value("RO-INVALID/STORY-INVALID") != nil || items.value("RO-INVALID/STORY-INVALID/") != nil {
		t.Fatal("invalid roCreate mutated persistence")
	}
}

func TestMOS28AcceptsOptionalStoryAndItemSlugs(t *testing.T) {
	tcpServer, _, _, items := startMOS28Server(t)
	conn := dialMOS28(t, tcpServer)
	const request = `<mos><mosID>openmos.beltware.test</mosID><ncsID>beltware.test</ncsID><messageID>82</messageID><roCreate>
<roID>RO-OPTIONAL-SLUGS</roID><roSlug>Optional slugs</roSlug>
<story><storyID>STORY</storyID><item><itemID>ITEM</itemID><objID>OBJECT</objID><mosID>openmos.beltware.test</mosID></item></story>
</roCreate></mos>`
	writeMOS28ForTest(t, conn, request)
	var ack struct {
		ROAck struct {
			Status string `xml:"roStatus"`
		} `xml:"roAck"`
	}
	readMOS28XMLForTest(t, conn, &ack)
	if ack.ROAck.Status != "OK" {
		t.Fatalf("ACK status = %q, want OK", ack.ROAck.Status)
	}
	if items.value("RO-OPTIONAL-SLUGS/STORY/ITEM") == nil {
		t.Fatal("valid item with optional slugs omitted was not persisted")
	}
}

func TestMOS28RejectsMissingRequiredObjectFieldsBeforeMutation(t *testing.T) {
	tcpServer, runningOrders, stories, items := startMOS28Server(t)
	conn := dialMOS28(t, tcpServer)
	const request = `<mos><mosID>openmos.beltware.test</mosID><ncsID>beltware.test</ncsID><messageID>83</messageID><roCreate>
<roID>RO-MISSING-OBJECT</roID><roSlug>Invalid item</roSlug>
<story><storyID>STORY</storyID><storySlug>Story</storySlug><item><itemID>ITEM</itemID><itemSlug>Item</itemSlug></item></story>
</roCreate></mos>`
	writeMOS28ForTest(t, conn, request)
	var ack struct {
		ROAck struct {
			Status string `xml:"roStatus"`
		} `xml:"roAck"`
	}
	readMOS28XMLForTest(t, conn, &ack)
	if ack.ROAck.Status != "ERROR" {
		t.Fatalf("ACK status = %q, want ERROR", ack.ROAck.Status)
	}
	if runningOrders.value("RO-MISSING-OBJECT") != nil || stories.value("RO-MISSING-OBJECT/STORY") != nil || items.value("RO-MISSING-OBJECT/STORY/ITEM") != nil {
		t.Fatal("invalid item mutated persistence")
	}
}

func TestMOS28ROReplaceStopsOnDeleteFailure(t *testing.T) {
	tcpServer, runningOrders, stories, items := startMOS28Server(t)
	ctx := context.Background()
	_, _ = runningOrders.Create(ctx, &model.RunningOrder{ID: "RO-DELETE-FAIL", Slug: "Original"})
	_, _ = stories.Create(ctx, &model.Story{ID: "RO-DELETE-FAIL/STORY-OLD", RawID: "STORY-OLD", RunningOrderID: "RO-DELETE-FAIL"})
	_, _ = items.Create(ctx, &model.Item{ID: "RO-DELETE-FAIL/STORY-OLD/ITEM-OLD", RawID: "ITEM-OLD", StoryID: "RO-DELETE-FAIL/STORY-OLD"})
	items.deleteErr = errors.New("injected item delete failure")

	conn := dialMOS28(t, tcpServer)
	const request = `<mos><mosID>openmos.beltware.test</mosID><ncsID>beltware.test</ncsID><messageID>79</messageID><roReplace>
<roID>RO-DELETE-FAIL</roID><roSlug>Replacement</roSlug>
<story><storyID>STORY-NEW</storyID><storySlug>New</storySlug></story>
</roReplace></mos>`
	writeMOS28ForTest(t, conn, request)
	var ack struct {
		ROAck struct {
			Status string `xml:"roStatus"`
		} `xml:"roAck"`
	}
	readMOS28XMLForTest(t, conn, &ack)
	if ack.ROAck.Status != "ERROR" {
		t.Fatalf("ACK status = %q, want ERROR", ack.ROAck.Status)
	}
	if ro := runningOrders.value("RO-DELETE-FAIL"); ro == nil || ro.Slug != "Original" {
		t.Fatalf("running order changed after delete failure: %#v", ro)
	}
	if stories.value("RO-DELETE-FAIL/STORY-OLD") == nil || items.value("RO-DELETE-FAIL/STORY-OLD/ITEM-OLD") == nil {
		t.Fatal("existing content was lost after delete failure")
	}
	if stories.value("RO-DELETE-FAIL/STORY-NEW") != nil {
		t.Fatal("replacement content was created after delete failure")
	}
}

func startMOS28Server(t *testing.T) (*TCPServer, *memoryRunningOrders, *memoryStories, *memoryItems) {
	t.Helper()
	runningOrders := newMemoryRunningOrders()
	stories := newMemoryStories()
	items := newMemoryItems()
	eventBus := events.NewEventBus()
	mosService := service.NewMOSService(runningOrders, stories, items, nil, eventBus)
	cfg := &config.Config{}
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = 0
	cfg.Server.WriteTimeout = time.Second
	cfg.Server.ShutdownTimeout = time.Second
	cfg.MOS.ID = "openmos.beltware.test"
	cfg.MOS.NCSID = "beltware.test"
	cfg.MOS.HeartbeatInterval = time.Minute
	cfg.MOS.ClientTimeout = time.Minute

	tcpServer, err := NewTCPServer(cfg, mosService, eventBus)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = tcpServer.Start(ctx) }()
	return tcpServer, runningOrders, stories, items
}

func dialMOS28(t *testing.T, tcpServer *TCPServer) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", tcpServer.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func encodeUCS2BEForTest(value string) []byte {
	units := utf16.Encode([]rune(value))
	encoded := make([]byte, 0, len(units)*2)
	for _, unit := range units {
		encoded = append(encoded, byte(unit>>8), byte(unit))
	}
	return encoded
}

func writeMOS28ForTest(t *testing.T, conn net.Conn, value string) {
	t.Helper()
	if _, err := conn.Write(encodeUCS2BEForTest(value)); err != nil {
		t.Fatal(err)
	}
}

func readMOS28XMLForTest(t *testing.T, conn net.Conn, destination any) {
	t.Helper()
	frame := readUCS2BEFrameForTest(t, conn)
	if err := xml.Unmarshal(stripXMLDeclarationForTest(decodeUCS2BEForTest(t, frame)), destination); err != nil {
		t.Fatalf("decode ACK XML: %v", err)
	}
}

func decodeUCS2BEForTest(t *testing.T, encoded []byte) string {
	t.Helper()
	if len(encoded)%2 != 0 {
		t.Fatalf("odd UCS-2BE byte count: %d", len(encoded))
	}
	units := make([]uint16, len(encoded)/2)
	for i := range units {
		units[i] = uint16(encoded[i*2])<<8 | uint16(encoded[i*2+1])
	}
	return string(utf16.Decode(units))
}

func stripXMLDeclarationForTest(value string) []byte {
	value = strings.TrimPrefix(value, "\ufeff")
	if end := strings.Index(value, "?>"); strings.HasPrefix(value, "<?xml") && end >= 0 {
		value = value[end+2:]
	}
	return []byte(value)
}

func readUCS2BEFrameForTest(t *testing.T, conn net.Conn) []byte {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	closingTag := encodeUCS2BEForTest("</mos>")
	var frame []byte
	buffer := make([]byte, 256)
	for !bytes.Contains(frame, closingTag) {
		n, err := conn.Read(buffer)
		if err != nil {
			t.Fatalf("read UCS-2BE ACK: %v", err)
		}
		frame = append(frame, buffer[:n]...)
	}
	return frame
}

type memoryRunningOrders struct {
	sync.Mutex
	values map[string]*model.RunningOrder
}

func newMemoryRunningOrders() *memoryRunningOrders {
	return &memoryRunningOrders{values: make(map[string]*model.RunningOrder)}
}

func (r *memoryRunningOrders) Create(_ context.Context, value *model.RunningOrder) (*model.RunningOrder, error) {
	r.Lock()
	defer r.Unlock()
	r.values[value.ID] = value
	return value, nil
}

func (r *memoryRunningOrders) Get(_ context.Context, id string) (*model.RunningOrder, error) {
	r.Lock()
	defer r.Unlock()
	value, ok := r.values[id]
	if !ok {
		return nil, errors.New("running order not found")
	}
	return value, nil
}

func (r *memoryRunningOrders) Update(_ context.Context, value *model.RunningOrder) error {
	r.Lock()
	defer r.Unlock()
	r.values[value.ID] = value
	return nil
}

func (r *memoryRunningOrders) Delete(_ context.Context, id string) error {
	r.Lock()
	defer r.Unlock()
	delete(r.values, id)
	return nil
}

func (r *memoryRunningOrders) List(context.Context) ([]*model.RunningOrder, error) {
	r.Lock()
	defer r.Unlock()
	values := make([]*model.RunningOrder, 0, len(r.values))
	for _, value := range r.values {
		values = append(values, value)
	}
	return values, nil
}

func (r *memoryRunningOrders) value(id string) *model.RunningOrder {
	value, _ := r.Get(context.Background(), id)
	return value
}

type memoryStories struct {
	sync.Mutex
	values map[string]*model.Story
}

func newMemoryStories() *memoryStories { return &memoryStories{values: make(map[string]*model.Story)} }

func (r *memoryStories) Create(_ context.Context, value *model.Story) (*model.Story, error) {
	r.Lock()
	defer r.Unlock()
	r.values[value.ID] = value
	return value, nil
}

func (r *memoryStories) Get(_ context.Context, id string) (*model.Story, error) {
	r.Lock()
	defer r.Unlock()
	value, ok := r.values[id]
	if !ok {
		return nil, errors.New("story not found")
	}
	return value, nil
}

func (r *memoryStories) Update(_ context.Context, value *model.Story) error {
	r.Lock()
	defer r.Unlock()
	r.values[value.ID] = value
	return nil
}

func (r *memoryStories) DeleteMultiple(ctx context.Context, ids []string) error {
	for _, id := range ids {
		_ = r.Delete(ctx, id)
	}
	return nil
}

func (r *memoryStories) Delete(_ context.Context, id string) error {
	r.Lock()
	defer r.Unlock()
	delete(r.values, id)
	return nil
}

func (r *memoryStories) ListByRunningOrder(_ context.Context, roID string) ([]*model.Story, error) {
	r.Lock()
	defer r.Unlock()
	var values []*model.Story
	for _, value := range r.values {
		if value.RunningOrderID == roID {
			values = append(values, value)
		}
	}
	return values, nil
}

func (r *memoryStories) value(id string) *model.Story {
	value, _ := r.Get(context.Background(), id)
	return value
}

type memoryItems struct {
	sync.Mutex
	values    map[string]*model.Item
	deleteErr error
}

func newMemoryItems() *memoryItems { return &memoryItems{values: make(map[string]*model.Item)} }

func (r *memoryItems) Create(_ context.Context, value *model.Item) (*model.Item, error) {
	r.Lock()
	defer r.Unlock()
	if _, exists := r.values[value.ID]; exists {
		return nil, errors.New("item already exists")
	}
	r.values[value.ID] = value
	return value, nil
}

func (r *memoryItems) Get(_ context.Context, id string) (*model.Item, error) {
	r.Lock()
	defer r.Unlock()
	value, ok := r.values[id]
	if !ok {
		return nil, errors.New("item not found")
	}
	return value, nil
}

func (r *memoryItems) Update(_ context.Context, value *model.Item) error {
	r.Lock()
	defer r.Unlock()
	r.values[value.ID] = value
	return nil
}

func (r *memoryItems) DeleteMultiple(ctx context.Context, ids []string) error {
	for _, id := range ids {
		_ = r.Delete(ctx, id)
	}
	return nil
}

func (r *memoryItems) Delete(_ context.Context, id string) error {
	r.Lock()
	defer r.Unlock()
	if r.deleteErr != nil {
		return r.deleteErr
	}
	delete(r.values, id)
	return nil
}

func (r *memoryItems) ListByStory(_ context.Context, storyID string) ([]*model.Item, error) {
	r.Lock()
	defer r.Unlock()
	var values []*model.Item
	for _, value := range r.values {
		if value.StoryID == storyID {
			values = append(values, value)
		}
	}
	return values, nil
}

func (r *memoryItems) value(id string) *model.Item {
	value, _ := r.Get(context.Background(), id)
	return value
}
