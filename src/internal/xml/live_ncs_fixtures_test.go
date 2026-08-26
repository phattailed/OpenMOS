package xml

import (
	"encoding/xml"
	"strings"
	"testing"
)

// Fixtures in this file are real frames captured from a live AP ENPS on the MOS
// 4.0 WebSocket transport, in an exchange OpenMOS initiated. Every other fixture
// in this repository was written by hand.
//
// Two defects were found within seconds of the first live exchange, neither of
// which any hand-written fixture had caught, because our own encoder and parser
// agreed with each other:
//
//  1. listMachInfo could not be parsed at all. The NCS spells MOS booleans YES and
//     NO, and a plain Go bool field expects the XML Schema spelling. See YesNo.
//  2. Our heartbeat was rejected outright. We were decorating <heartbeat> with
//     requestID, timestamp and source attributes that the spec does not define.
//
// Site-identifying values are replaced with placeholders. The serial number in
// particular carried real hardware identifiers.

// listMachInfoFromLiveNCS is frame 0002 of the captured exchange, verbatim apart
// from the sanitized SN and ID. Note the formatting: the NCS emits one element per
// line with no indentation, and DOM in US locale text rather than ISO 8601.
const listMachInfoFromLiveNCS = `<mos>
<mosID>openmos.example.mos</mosID>
<ncsID>NCS-HOST</ncsID>
<messageID>1</messageID>
<listMachInfo>
<manufacturer>AP</manufacturer>
<model>NOM</model>
<hwRev>9.6.2026</hwRev>
<swRev>9.6.2026</swRev>
<DOM>4/15/2026 2:21:26 PM</DOM>
<SN>SANITIZED-SERIAL</SN>
<ID>NCS-HOST</ID>
<time>2026-08-26T03:52:26</time>
<opTime>2026-08-26T03:20:27</opTime>
<mosRev>2.8.4</mosRev>
<supportedProfiles deviceType="NCS">
<mosProfile number="0">YES</mosProfile>
<mosProfile number="1">YES</mosProfile>
<mosProfile number="2">YES</mosProfile>
<mosProfile number="3">YES</mosProfile>
<mosProfile number="4">YES</mosProfile>
<mosProfile number="5">NO</mosProfile>
<mosProfile number="6">YES</mosProfile>
<mosProfile number="7">YES</mosProfile>
</supportedProfiles>
</listMachInfo>
</mos>`

// heartbeatReplyFromLiveNCS is frame 0004. The NCS answers a heartbeat with a
// heartbeat carrying only <time>, and no attributes -- which is exactly what the
// spec says and exactly what OpenMOS was failing to send.
const heartbeatReplyFromLiveNCS = `<mos>
<mosID>openmos.example.mos</mosID>
<ncsID>NCS-HOST</ncsID>
<messageID>2</messageID>
<heartbeat>
<time>2026-08-26T03:52:26</time>
</heartbeat>
</mos>`

func TestLiveNCSListMachInfoParses(t *testing.T) {
	env, msg, _, err := ParseEnvelope([]byte(listMachInfoFromLiveNCS))
	if err != nil {
		t.Fatalf("a real NCS listMachInfo must parse: %v", err)
	}
	if env.MessageID != "1" {
		t.Errorf("messageID = %q, want 1", env.MessageID)
	}

	info, ok := msg.(ListMachInfo)
	if !ok {
		t.Fatalf("payload parsed as %T, want ListMachInfo", msg)
	}

	if info.Manufacturer != "AP" || info.Model != "NOM" {
		t.Errorf("manufacturer/model = %q/%q, want AP/NOM", info.Manufacturer, info.Model)
	}

	// The NCS reports mosRev 2.8.4 even on the MOS 4.0 WebSocket transport. Worth
	// asserting because it contradicts the natural assumption that the MOS 4
	// transport implies mosRev 4.0, and a future change to our own mosRev handling
	// should not be made on that assumption.
	if info.MosRev != "2.8.4" {
		t.Errorf("mosRev = %q; the live NCS reports 2.8.4 on the MOS 4 transport", info.MosRev)
	}

	if info.SupportedProfiles.DeviceType != "NCS" {
		t.Errorf("deviceType = %q, want NCS", info.SupportedProfiles.DeviceType)
	}

	// Profile 5 is the only one this NCS does not support.
	want := map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: false, 6: true, 7: true}
	if len(info.SupportedProfiles.Profiles) != len(want) {
		t.Fatalf("parsed %d profiles, want %d", len(info.SupportedProfiles.Profiles), len(want))
	}
	for _, profile := range info.SupportedProfiles.Profiles {
		if got := profile.Value.Bool(); got != want[profile.Number] {
			t.Errorf("profile %d = %v, want %v", profile.Number, got, want[profile.Number])
		}
	}
}

func TestLiveNCSHeartbeatReplyParses(t *testing.T) {
	_, msg, _, err := ParseEnvelope([]byte(heartbeatReplyFromLiveNCS))
	if err != nil {
		t.Fatalf("a real NCS heartbeat reply must parse: %v", err)
	}
	hb, ok := msg.(Heartbeat)
	if !ok {
		t.Fatalf("payload parsed as %T, want Heartbeat", msg)
	}
	if hb.Time != "2026-08-26T03:52:26" {
		t.Errorf("time = %q, want the NCS value verbatim", hb.Time)
	}
	// The NCS sends no attributes, and neither should we.
	if hb.RequestID != "" || hb.Timestamp != "" || hb.Source != "" {
		t.Errorf("live NCS heartbeat carried unexpected attributes: %+v", hb)
	}
}

// TestOutboundHeartbeatCarriesNoInventedAttributes is the regression guard for the
// defect that a live NCS rejected with:
//
//	<mos>Invalid command: heartbeat requestID="2" timestamp="..." source="..."</mos>
//
// The spec is <!ELEMENT heartbeat (time)> with no attributes.
func TestOutboundHeartbeatCarriesNoInventedAttributes(t *testing.T) {
	encoded, err := xml.Marshal(CreateHeartbeat())
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	text := string(encoded)

	for _, forbidden := range []string{"requestID=", "timestamp=", "source="} {
		if strings.Contains(text, forbidden) {
			t.Errorf("originated heartbeat must not carry %s; a live NCS rejects it. got %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "<time>") {
		t.Errorf("heartbeat must carry <time>, the one element the spec requires: %s", text)
	}

	// A response echoes requestID only when the peer supplied one.
	bare, err := xml.Marshal(CreateHeartbeatResponse(""))
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if strings.Contains(string(bare), "requestID=") {
		t.Errorf("response to a peer that sent no requestID must not invent one: %s", bare)
	}

	echoed, err := xml.Marshal(CreateHeartbeatResponse("abc"))
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if !strings.Contains(string(echoed), `requestID="abc"`) {
		t.Errorf("a peer-supplied requestID must be echoed for correlation: %s", echoed)
	}
}
