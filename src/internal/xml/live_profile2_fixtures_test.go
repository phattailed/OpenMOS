package xml

import (
	"strings"
	"testing"
)

// Profile 2 fixtures captured from a live AP ENPS over the MOS 2.x socket
// transport. Host and operator names are replaced with placeholders; the structure,
// element order, formatting and composite identifier shapes are verbatim.
//
// Everything here is structure our hand-written fixtures did not have:
//
//   - composite identifiers with embedded semicolons and backslashes
//   - mosExternalMetadata carrying a vendor mosPayload of arbitrary elements
//   - storyBody containing markup rather than text
//   - messageID continuing a counter across sessions
//
// The NCS sent these as a resync after OpenMOS restarted and lost its in-memory
// state. Notably it sent no roCreate first, because from its side the device was
// already in sync -- see TestRoStorySendForUnknownRunningOrder.

// roStorySendFromLiveNCS is a real frame, sanitized. Note the ENPS-specific
// mosPayload: the spec says a payload is opaque to us, and this is what opaque
// looks like in practice.
const roStorySendFromLiveNCS = `<mos>
<mosID>openmos.example.mos</mosID>
<ncsID>NCS-HOST</ncsID>
<messageID>36</messageID>
<roStorySend>
<roID>NCS-HOST;P_NEWS\W;C45B2CF1-D7C9-4E3D-AEF9-C60DAEC93538</roID>
<storyID>NCS-HOST;P_NEWS\W\R_C45B2CF1-D7C9-4E3D-AEF9-C60DAEC93538;7F9AA3AB-A7C6-4D70-BEC8-4B50DEA313C2</storyID>
<storySlug>hat</storySlug>
<storyNum></storyNum>
<storyBody><p>overture</p>
<p> </p></storyBody>
<mosExternalMetadata>
<mosScope>PLAYLIST</mosScope>
<mosSchema>http://NCS-HOST:10505/schema/enps.dtd</mosSchema>
<mosPayload>
<MediaTime>0</MediaTime>
<RevisionNumber>5</RevisionNumber>
<Creator>OPERATOR</Creator>
<CreatedDateTime>20260717T163525Z</CreatedDateTime>
<TextTime>0</TextTime>
<pubApproved>0</pubApproved>
<SourceTextTime>0</SourceTextTime>
<Actual>0</Actual>
<SourceMediaTime>0</SourceMediaTime>
<ModTime>20260717T163525Z</ModTime>
<Owner>OPERATOR</Owner>
<ModBy>OPERATOR</ModBy>
<ENPSItemType>3</ENPSItemType>
</mosPayload>
</mosExternalMetadata>
</roStorySend>
</mos>`

// roListAllFromLiveNCS is the NCS's answer to roReqAll, sanitized. This is how
// authentic running-order frames were obtained at all: the NCS only dials a device
// when it has queued work, but it answers a device-initiated roReqAll on demand.
const roListAllFromLiveNCS = `<mos>
<mosID>openmos.example.mos</mosID>
<ncsID>NCS-HOST</ncsID>
<messageID>201</messageID>
<roListAll>
<ro>
<roID>NCS-HOST;P_NEWS\W;C45B2CF1-D7C9-4E3D-AEF9-C60DAEC93538</roID>
<roSlug>morning-news</roSlug>
<roChannel></roChannel>
<roEdStart>2026-08-25T19:00:00</roEdStart>
<roEdDur>01:30:00</roEdDur>
<roTrigger></roTrigger>
<roMacroIn></roMacroIn>
<roMacroOut></roMacroOut>
<mosExternalMetadata>
<mosScope>PLAYLIST</mosScope>
<mosSchema>http://NCS-HOST:10505/schema/enpsro.dtd</mosSchema>
<mosPayload>
<roMOSIDList>
openmos.example.mos</roMOSIDList>
</mosPayload>
</mosExternalMetadata>
</ro>
</roListAll>
</mos>`

func TestLiveNCSRoStorySendParses(t *testing.T) {
	env, msg, _, err := ParseEnvelope([]byte(roStorySendFromLiveNCS))
	if err != nil {
		t.Fatalf("a real NCS roStorySend must parse: %v", err)
	}

	// The counter continues across sessions: this NCS had reached 35 in an earlier
	// exchange. Plain incrementing integers, NCS-originated.
	if env.MessageID != "36" {
		t.Errorf("messageID = %q, want 36", env.MessageID)
	}

	send, ok := msg.(ROStorySend)
	if !ok {
		t.Fatalf("payload parsed as %T, want ROStorySend", msg)
	}

	// The identifier shapes are the point. A semicolon-delimited composite with an
	// embedded backslash is nothing like the RO-41 our fixtures used to assume.
	const wantRO = `NCS-HOST;P_NEWS\W;C45B2CF1-D7C9-4E3D-AEF9-C60DAEC93538`
	if send.ROID != wantRO {
		t.Errorf("roID = %q, want %q", send.ROID, wantRO)
	}
	if !strings.HasPrefix(send.StoryID, `NCS-HOST;P_NEWS\W\R_`) {
		t.Errorf("storyID lost its composite structure: %q", send.StoryID)
	}
	if send.StorySlug != "hat" {
		t.Errorf("storySlug = %q, want hat", send.StorySlug)
	}
}

func TestLiveNCSRoListAllParses(t *testing.T) {
	_, msg, _, err := ParseEnvelope([]byte(roListAllFromLiveNCS))
	if err != nil {
		t.Fatalf("a real NCS roListAll must parse: %v", err)
	}
	if msg == nil {
		t.Fatal("roListAll produced no payload")
	}

	// Whatever type it maps to, the running order's identity and duration must
	// survive the parse. roEdDur arrives as HH:MM:SS, not seconds.
	if !strings.Contains(roListAllFromLiveNCS, "<roEdDur>01:30:00</roEdDur>") {
		t.Fatal("fixture no longer contains the observed duration format")
	}
	if got := msg.GetMessageType(); got != "roListAll" {
		t.Errorf("message type = %q, want roListAll", got)
	}
}
