package xml

import (
	stdxml "encoding/xml"
	"strings"
	"testing"
)

// Frames below are real traffic between AP ENPS and four different vendors' MOS
// devices: a prompter, a graphics system, two automation systems and a gateway.
// Site and vendor names are replaced with the device class, because the behaviour is
// what matters and attributing non-conformance to named products is not this
// repository's business. Structure is verbatim.
//
// They exist because our own reference NCS is a single ENPS version on a single
// transport, and agreeing with one peer is not interoperability. Every finding here
// came from traffic we could not have produced ourselves.

// listMachInfoFlatProfiles is ENPS 8.2 answering reqMachInfo on the MOS 2.x socket.
//
// Note the profile encoding: flat <mosProfile0>..<mosProfile7> elements, NOT the
// <supportedProfiles><mosProfile number="0"> container that the same vendor's 9.6
// release sends on the MOS 4.0 transport. Both are ENPS. A device that understands
// only one of them is broken against half the estate.
const listMachInfoFlatProfiles = `<mos>
<mosID>device.example.mos</mosID>
<ncsID>NCS-BUDDY</ncsID>
<messageID>1127212</messageID>
<listMachInfo>
<manufacturer>AP</manufacturer>
<model>NOM</model>
<hwRev>8.2.4075</hwRev>
<swRev>8.2.4075</swRev>
<DOM>12/9/2019 3:18:18 PM</DOM>
<SN>SANITIZED-SERIAL</SN>
<ID>NCS-BUDDY</ID>
<time>2022-03-30T15:14:10</time>
<opTime>2021-11-14T21:26:59</opTime>
<mosRev>2.8.3</mosRev>
<mosProfile0>YES</mosProfile0>
<mosProfile1>YES</mosProfile1>
<mosProfile2>YES</mosProfile2>
<mosProfile3>YES</mosProfile3>
<mosProfile4>YES</mosProfile4>
<mosProfile5>NO</mosProfile5>
<mosProfile6>YES</mosProfile6>
<mosProfile7>YES</mosProfile7>
</listMachInfo>
</mos>`

// roAckBuddyRefusal is what an ENPS buddy (standby) server returns for every
// running-order message while the main server is up. Devices connect to both.
//
// Two things matter. roStatus is free prose, not an enum -- we emit "OK" and
// "ERROR", but a real NCS sends a whole sentence. And roID is EMPTY, so a device
// keying its acks by roID must not assume one is present. Every one of the 2820
// roAcks in the sampled corpus had this shape.
const roAckBuddyRefusal = `<mos>
<mosID>device.example.mos</mosID>
<ncsID>NCS-BUDDY</ncsID>
<messageID>20564</messageID>
<roAck>
<roID></roID>
<roStatus>Buddy server cannot respond because main server is available</roStatus>
</roAck>
</mos>`

// heartbeatNoMessageID is the graphics system on the MOS 2.x socket. It sent 12,717
// heartbeats in the sampled corpus and not one carried a messageID -- and NOM's
// replies carried none either. This is why inbound messageID presence cannot be
// required on the socket transport, and why #20 was settled as lenient inbound.
const heartbeatNoMessageID = `<mos>
<mosID>device.example.mos</mosID>
<ncsID>NCS-BUDDY</ncsID>
<heartbeat>
<time>2022-03-30T23:57:14</time>
</heartbeat>
</mos>`

// roElementStatCommaFraction is an automation system reporting item playback status.
//
// The timestamp uses a COMMA as the decimal separator: 20:05:07,453Z. ISO 8601
// permits it and this vendor uses it; Go's time package does not accept it. Any
// code that parses MOS times must cope.
const roElementStatCommaFraction = `<mos><mosID>device.example.mos</mosID>` +
	`<ncsID>NCS-BUDDY</ncsID><messageID>1125850</messageID>` +
	`<roElementStat element="ITEM">` +
	`<roID>NCS-MAIN;P_NEWS\W;52F55A77-DA5E-4213-BFB3BF6470882478</roID>` +
	`<storyID>NCS-MAIN;P_NEWS\W\R_52F55A77-DA5E-4213-BFB3BF6470882478;4E83D075-6566-4161-9794C7561E657265</storyID>` +
	`<itemID>3</itemID><objID>PACKAGE;SOT VO CLIP</objID><itemChannel/>` +
	`<status>STOP</status><time>2022-03-29T20:05:07,453Z</time>` +
	`</roElementStat></mos>`

// selfClosingRequests: real devices send operations as self-closing elements.
const selfClosingReqMachInfo = `<mos><mosID>device.example.mos</mosID>` +
	`<ncsID>NCS-BUDDY</ncsID><messageID>1127212</messageID><reqMachInfo/></mos>`

const selfClosingRoReqAll = `<mos><mosID>device.example.mos</mosID>` +
	`<ncsID>NCS-BUDDY</ncsID><messageID>1127213</messageID><roReqAll/></mos>`

// emptyRoListAll: the buddy answers roReqAll with an empty list rather than an
// error. An empty container is a valid answer, not a parse failure.
const emptyRoListAll = `<mos>
<mosID>device.example.mos</mosID>
<ncsID>NCS-BUDDY</ncsID>
<messageID>1127213</messageID>
<roListAll>
</roListAll>
</mos>`

func TestRealVendorFramesParse(t *testing.T) {
	// gen matters: MOS 4.0 §4.1.1 requires a messageID on everything but keepAlive,
	// while the 2.x socket transport does not. The graphics system exercises exactly that
	// difference, so each frame is checked against the generation it arrived on.
	cases := []struct {
		name  string
		frame string
		gen   Generation
	}{
		{"listMachInfo with flat mosProfileN (ENPS 8.2, socket)", listMachInfoFlatProfiles, Gen2x},
		{"roAck buddy refusal, empty roID and prose roStatus", roAckBuddyRefusal, Gen2x},
		{"heartbeat with no messageID (graphics system, socket)", heartbeatNoMessageID, Gen2x},
		{"roElementStat with comma fractional seconds (automation system)", roElementStatCommaFraction, Gen2x},
		{"self-closing reqMachInfo", selfClosingReqMachInfo, Gen2x},
		{"self-closing roReqAll", selfClosingRoReqAll, Gen2x},
		{"empty roListAll", emptyRoListAll, Gen2x},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The socket transport's path: unmarshal the envelope, then validate for
			// the generation.
			var env Envelope
			if err := stdxml.Unmarshal([]byte(tc.frame), &env); err != nil {
				t.Fatalf("real vendor traffic must unmarshal, got: %v", err)
			}
			msg, err := ValidateEnvelope(env, tc.gen, env.MosID, "")
			if err != nil {
				t.Fatalf("real vendor traffic must validate on %s, got: %v", tc.gen, err)
			}
			if msg == nil {
				t.Fatal("validated to a nil payload")
			}
		})
	}
}

// TestSocketHeartbeatWithoutMessageIDIsInvalidOnMOS4 records the boundary deliberately: the same
// frame that is legitimate on the socket transport is not legitimate on MOS 4.0.
// This is the rule being generation-scoped rather than global, which is the whole
// reason xml.Generation exists.
func TestSocketHeartbeatWithoutMessageIDIsInvalidOnMOS4(t *testing.T) {
	var env Envelope
	if err := stdxml.Unmarshal([]byte(heartbeatNoMessageID), &env); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if _, err := ValidateEnvelope(env, Gen2x, env.MosID, ""); err != nil {
		t.Errorf("must be accepted on the 2.x socket transport: %v", err)
	}
	if _, err := ValidateEnvelope(env, Gen4x, env.MosID, ""); err == nil {
		t.Error("must be refused on MOS 4.0, where §4.1.1 requires a messageID")
	}
}

// TestFlatProfileEncodingIsUnderstood is the specific regression guard. ENPS sends
// flat mosProfileN on the socket transport and the supportedProfiles container on
// MOS 4.0. Reading only one of them silently loses the peer's capabilities.
func TestFlatProfileEncodingIsUnderstood(t *testing.T) {
	var env Envelope
	if err := stdxml.Unmarshal([]byte(listMachInfoFlatProfiles), &env); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	msg, err := ValidateEnvelope(env, Gen2x, env.MosID, "")
	if err != nil {
		t.Fatalf("validate failed: %v", err)
	}
	info, ok := msg.(ListMachInfo)
	if !ok {
		t.Fatalf("parsed as %T, want ListMachInfo", msg)
	}

	if info.MosRev != "2.8.3" {
		t.Errorf("mosRev = %q, want 2.8.3", info.MosRev)
	}

	// Profile 5 is the only NO in this reply.
	want := map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: false, 6: true, 7: true}
	got := info.Profiles()
	if len(got) != len(want) {
		t.Fatalf("Profiles() returned %d entries, want %d: %v", len(got), len(want), got)
	}
	for number, supported := range want {
		if got[number] != supported {
			t.Errorf("profile %d = %v, want %v", number, got[number], supported)
		}
	}
}

// TestContainerProfileEncodingStillUnderstood guards the other encoding, so adding
// flat support cannot regress the MOS 4.0 form.
func TestContainerProfileEncodingStillUnderstood(t *testing.T) {
	_, msg, _, err := ParseEnvelope([]byte(listMachInfoFromLiveNCS))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	info := msg.(ListMachInfo)
	got := info.Profiles()
	if len(got) != 8 {
		t.Fatalf("Profiles() returned %d entries, want 8: %v", len(got), got)
	}
	if !got[0] || got[5] {
		t.Errorf("container encoding misread: profile0=%v profile5=%v", got[0], got[5])
	}
}

// TestRoStatusIsNotTreatedAsAnEnum records that a real NCS puts prose here.
func TestRoStatusIsNotTreatedAsAnEnum(t *testing.T) {
	var env Envelope
	if err := stdxml.Unmarshal([]byte(roAckBuddyRefusal), &env); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	msg, err := ValidateEnvelope(env, Gen2x, env.MosID, "")
	if err != nil {
		t.Fatalf("validate failed: %v", err)
	}
	ack, ok := msg.(ROAck)
	if !ok {
		t.Fatalf("parsed as %T, want ROAck", msg)
	}
	if !strings.Contains(ack.Status, "Buddy server") {
		t.Errorf("roStatus prose was lost: %q", ack.Status)
	}
	if ack.ID != "" {
		t.Errorf("roID = %q; a buddy refusal carries none and that must survive", ack.ID)
	}
}
