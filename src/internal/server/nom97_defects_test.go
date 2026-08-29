package server

import (
	stdxml "encoding/xml"
	"strings"
	"testing"
	"time"

	mosxml "airshift/openmos/internal/xml"
)

// Four defects found by pointing the MOS 4 client at a live NOM 9.7, none of which any loopback
// test could have caught, because each one lived in the gap between "our handler works" and
// "a real peer's bytes reach it".
//
// The live exchange, verbatim from capture:
//
//	out: <mos><mosID>openmos.probe.mos</mosID><ncsID>DEMO-NCS</ncsID>
//	     <messageID>1</messageID><reqMachInfo></reqMachInfo></mos>
//	in:  <mos><mosID>openmos.probe.mos</mosID><ncsID>DEMO-NCS</ncsID>
//	     <mosAck><objID></objID><objRev></objRev><status>NACK</status>
//	     <statusDescription>MOS ID is not recognized by this NOM</statusDescription></mosAck></mos>
//
// Note what the reply lacks: a messageID. And note what OpenMOS did with it: reported "unknown
// message type", then after a partial fix "envelope is missing messageID", and reconnected once
// a second forever.

// TestEnvelopeCarriesMosAck covers the defect that made the refusal unreadable. Envelope had no
// mosAck field, so the message a real NCS uses to say no was rejected outright.
func TestEnvelopeCarriesMosAck(t *testing.T) {
	live := `<mos><mosID>openmos.probe.mos</mosID><ncsID>DEMO-NCS</ncsID>` +
		`<mosAck><objID></objID><objRev></objRev><status>NACK</status>` +
		`<statusDescription>MOS ID is not recognized by this NOM</statusDescription></mosAck></mos>`

	var env mosxml.Envelope
	if err := stdxml.Unmarshal([]byte(live), &env); err != nil {
		t.Fatalf("unmarshal live NACK: %v", err)
	}
	msg, err := env.Message()
	if err != nil {
		t.Fatalf("live NACK was not recognised: %v", err)
	}
	ack, ok := msg.(mosxml.MOSAck)
	if !ok {
		t.Fatalf("live NACK parsed as %T, want MOSAck", msg)
	}
	if ack.Status != "NACK" {
		t.Errorf("status = %q, want NACK", ack.Status)
	}
	if !strings.Contains(ack.StatusDescription, "not recognized") {
		t.Errorf("statusDescription was lost: %q", ack.StatusDescription)
	}
}

// TestInboundAcknowledgementNeedsNoMessageID covers the second defect. Requiring presence meant
// the refusal was rejected before anything could read it -- the worst message to be unable to
// read, since it is the one that says why the peer said no.
func TestInboundAcknowledgementNeedsNoMessageID(t *testing.T) {
	for _, gen := range []mosxml.Generation{mosxml.Gen2x, mosxml.Gen3x, mosxml.Gen4x} {
		ack := mosxml.MOSAck{Status: "NACK", StatusDescription: "MOS ID is not recognized by this NOM"}
		if err := mosxml.AcceptInboundMessageID(gen, ack, ""); err != nil {
			t.Errorf("%s rejected an acknowledgement carrying no messageID: %v. A real NOM sends "+
				"exactly that, and refusing it discards the diagnosis.", gen, err)
		}
	}

	// The relaxation must be narrow: a running-order construction message still needs one on the
	// generations that require it, because that is what correlation and dedup depend on.
	create := mosxml.RunningOrderInfo{ID: "RO-1", Slug: "S"}
	if err := mosxml.AcceptInboundMessageID(mosxml.Gen4x, create, ""); err == nil {
		t.Error("a MOS 4.0 roCreate with no messageID was accepted; the exemption is too broad")
	}
}

// TestGeneratedMosAckCarriesNoInventedAttributes covers the third defect, which is a repeat of
// one already fixed elsewhere. heartbeat once carried requestID, timestamp and source attributes
// that appear nowhere in the specification, and a live ENPS replied "Invalid command:
// heartbeat requestID=...". That was fixed without auditing the rest of the generator, so mosAck
// kept emitting the same three.
func TestGeneratedMosAckCarriesNoInventedAttributes(t *testing.T) {
	ack := mosxml.CreateMOSAck("NACK", "Something went wrong")

	raw, err := stdxml.Marshal(ack)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	emitted := string(raw)

	for _, invented := range []string{"requestID", "timestamp", "source"} {
		if strings.Contains(emitted, invented) {
			t.Errorf("generated mosAck carries a non-spec %s attribute: %s", invented, emitted)
		}
	}
	if !strings.Contains(emitted, "<status>NACK</status>") {
		t.Errorf("generated mosAck lost its status: %s", emitted)
	}
	if !strings.Contains(emitted, "Something went wrong") {
		t.Errorf("generated mosAck lost its description: %s", emitted)
	}
}

// TestRefusedHandshakeIsNotAnEstablishedSession covers the fourth and most damaging defect: the
// reconnect loop.
//
// The client treated a successful dial as an established session, so a peer that accepted the
// WebSocket upgrade and then refused the handshake reset the backoff on every attempt. Against
// the live rig that produced 26 connections in 20 seconds -- about one per second, indefinitely,
// each one appending to another team's exception log. After the fix the same 30-second window
// produced 6 connections, with the delay growing 500ms, 1s, 2s, 4s, 8s, 16s.
//
// The peer here completes the upgrade and then NACKs, which is exactly what NOM does for an
// unconfigured mosID.
func TestRefusedHandshakeIsNotAnEstablishedSession(t *testing.T) {
	cfg := clientTestConfig("")
	peer := newNCSPeer(t, cfg)
	peer.refuseHandshake.Store(true)
	cfg.WSClient.PeerURL = peer.wsURL()

	client := NewWSClient(cfg, nil, nil)
	stop := runClient(t, client)
	defer stop()

	// Give the client long enough that an un-backed-off loop would rack up many attempts. With
	// backoff growing from the 20ms initial, a bounded handful is expected instead.
	waitFor(t, 2*time.Second, func() bool { return peer.connections() >= 2 })
	time.Sleep(600 * time.Millisecond)

	got := peer.connections()
	if got > 12 {
		t.Errorf("client made %d connections to a peer that refuses the handshake; the backoff "+
			"is not growing, which is how a NACKing NCS gets hammered once a second forever", got)
	}
	if got < 2 {
		t.Errorf("client made %d connections; it should still retry a refusal, just patiently", got)
	}
}
