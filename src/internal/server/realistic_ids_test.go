package server

import (
	"encoding/xml"
	"strings"
	"testing"

	"airshift/openmos/internal/model"
)

// Identifiers captured verbatim from a live AP ENPS during the interop exercise
// recorded in doc/interop/README.md §10. Every other fixture in this repository
// uses tokens like RO-41 and STORY-1; real ENPS identifiers are composites
// containing semicolons and backslashes, and a database path segment:
//
//	APSTSNOM21;P_STORYTELLING\W;C45B2CF1-D7C9-4E3D-AEF9-C60DAEC93538
//
// This worked on first contact, but it worked untested. These tests pin it.
const (
	realROID    = `APSTSNOM21;P_STORYTELLING\W;C45B2CF1-D7C9-4E3D-AEF9-C60DAEC93538`
	realStoryID = `APSTSNOM21;P_STORYTELLING\W\R_C45B2CF1-D7C9-4E3D-AEF9-C60DAEC93538;B7C56B36-890D-4A04-9A3A-4F32D8C180C3`
)

func TestRealENPSIdentifiersRoundTripThroughRoCreate(t *testing.T) {
	tcpServer, runningOrders, stories, _ := startMOS28Server(t)
	conn := dialMOS28(t, tcpServer)

	// roEdDur 01:30:00 is the 90-minute rundown seen live; it must parse to 5400.
	request := `<mos>
  <mosID>openmos.beltware.test</mosID>
  <ncsID>beltware.test</ncsID>
  <messageID>25</messageID>
  <roCreate>
    <roID>` + realROID + `</roID>
    <roSlug>tangible-test</roSlug>
    <roEdDur>01:30:00</roEdDur>
    <story>
      <storyID>` + realStoryID + `</storyID>
      <storySlug>gat</storySlug>
    </story>
  </roCreate>
</mos>`
	writeMOS28ForTest(t, conn, request)

	var ack struct {
		XMLName xml.Name `xml:"mos"`
		ROAck   struct {
			ROID   string `xml:"roID"`
			Status string `xml:"roStatus"`
		} `xml:"roAck"`
	}
	readMOS28XMLForTest(t, conn, &ack)

	// The acknowledgement must echo the roID byte for byte. An NCS correlates on
	// it, so any escaping or truncation here breaks the exchange.
	if ack.ROAck.ROID != realROID {
		t.Errorf("acknowledged roID does not match:\n got  %q\n want %q", ack.ROAck.ROID, realROID)
	}
	if ack.ROAck.Status != "OK" {
		t.Errorf("roStatus = %q, want OK", ack.ROAck.Status)
	}

	ro := runningOrders.value(realROID)
	if ro == nil {
		t.Fatalf("running order was not persisted under its composite roID")
	}
	if ro.Slug != "tangible-test" {
		t.Errorf("slug = %q, want tangible-test", ro.Slug)
	}
	if ro.Duration != 5400 {
		t.Errorf("duration = %d, want 5400 seconds for roEdDur 01:30:00", ro.Duration)
	}

	// The story key is derived from both composite IDs. Locate the story by its
	// RawID rather than recomputing the key here, so this test pins observable
	// behaviour instead of duplicating the escaping scheme.
	var storyKey string
	var story *model.Story
	stories.Lock()
	for key, value := range stories.values {
		if value.RawID == realStoryID {
			storyKey, story = key, value
			break
		}
	}
	stories.Unlock()

	if story == nil {
		t.Fatalf("no story was persisted with rawID %q", realStoryID)
	}
	if story.RunningOrderID != realROID {
		t.Errorf("story runningOrderID = %q, want %q", story.RunningOrderID, realROID)
	}

	// Escaping must remove the characters that would otherwise make the composite
	// key ambiguous or awkward as a document identifier.
	if strings.Contains(storyKey, `\`) {
		t.Errorf("story key still contains a backslash: %q", storyKey)
	}
	if strings.Count(storyKey, "/") != 1 {
		t.Errorf("story key should contain exactly one separator, got %q", storyKey)
	}
}

// A retried roCreate carrying composite identifiers must still be recognised as a
// duplicate. Hashing is over the marshalled operation, so the unusual characters
// must not disturb it.
func TestRealENPSIdentifiersDeduplicate(t *testing.T) {
	tcpServer, runningOrders, _, _ := startMOS28Server(t)
	conn := dialMOS28(t, tcpServer)

	request := `<mos>
  <mosID>openmos.beltware.test</mosID>
  <ncsID>beltware.test</ncsID>
  <messageID>26</messageID>
  <roCreate>
    <roID>` + realROID + `</roID>
    <roSlug>tangible-test</roSlug>
    <story><storyID>` + realStoryID + `</storyID><storySlug>gat</storySlug></story>
  </roCreate>
</mos>`

	for attempt := 1; attempt <= 2; attempt++ {
		writeMOS28ForTest(t, conn, request)
		var ack struct {
			XMLName xml.Name `xml:"mos"`
			ROAck   struct {
				ROID   string `xml:"roID"`
				Status string `xml:"roStatus"`
			} `xml:"roAck"`
		}
		readMOS28XMLForTest(t, conn, &ack)
		if ack.ROAck.Status != "OK" {
			t.Fatalf("attempt %d: roStatus = %q, want OK", attempt, ack.ROAck.Status)
		}
	}

	ro := runningOrders.value(realROID)
	if ro == nil {
		t.Fatal("running order missing after retry")
	}
	if ro.Version != 1 {
		t.Errorf("version = %d after a retry, want 1 -- the duplicate was re-applied", ro.Version)
	}
}
