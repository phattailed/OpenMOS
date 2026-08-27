package server

import (
	"context"
	"strings"
	"testing"
	"time"

	mosxml "airshift/openmos/internal/xml"
	"nhooyr.io/websocket"
)

// Profile 2 over the MOS 4.0 WebSocket transport, end to end.
//
// Until the running-order handling was shared, this transport implemented keepAlive,
// reqMachInfo and roCreate and answered everything else with "message type X is not
// implemented". So the socket transport could build and maintain a running order and the
// WebSocket transport could only create one -- which made the project's "one shared
// message core" claim untrue for the whole family, and was the largest asymmetry left
// between the two.
//
// These exercise the real transport: real frames, UCS-2BE, real envelopes.

func TestMOS4RunningOrderMaintenance(t *testing.T) {
	_, baseURL, cancel := testServer(t)
	defer cancel()

	ctx, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()

	conn := dialChannel(t, ctx, baseURL, ChannelRO)
	defer conn.Close(1000, "done")

	const roID = "RO-MOS4"

	// roCreate first, so there is something to maintain.
	_, msg := exchange(t, ctx, conn, "1",
		`<roCreate><roID>`+roID+`</roID><roSlug>Original</roSlug>`+
			`<story><storyID>S-1</storyID><storySlug>First</storySlug></story></roCreate>`)
	assertROAckOK(t, msg, "roCreate")

	// Each of these was previously answered "not implemented" on this transport.
	steps := []struct {
		name     string
		inner    string
		wantType string
	}{
		{
			name: "roReplace",
			inner: `<roReplace><roID>` + roID + `</roID><roSlug>Replaced</roSlug>` +
				`<story><storyID>S-2</storyID><storySlug>Second</storySlug></story></roReplace>`,
			wantType: "roAck",
		},
		{
			name:     "roMetadataReplace",
			inner:    `<roMetadataReplace><roID>` + roID + `</roID><roSlug>Retitled</roSlug></roMetadataReplace>`,
			wantType: "roAck",
		},
		{
			name:     "roReadyToAir",
			inner:    `<roReadyToAir><roID>` + roID + `</roID><roAir>READY</roAir></roReadyToAir>`,
			wantType: "roAck",
		},
		{
			name: "roElementStat",
			inner: `<roElementStat element="RO"><roID>` + roID + `</roID>` +
				`<status>PLAY</status><time>2026-08-27T12:00:00</time></roElementStat>`,
			wantType: "roAck",
		},
		{
			name:     "roReqAll",
			inner:    `<roReqAll/>`,
			wantType: "roListAll",
		},
		{
			name:     "roReq",
			inner:    `<roReq><roID>` + roID + `</roID></roReq>`,
			wantType: "roList",
		},
	}

	for i, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			_, msg := exchange(t, ctx, conn, messageIDFor(i+2), step.inner)
			if msg == nil {
				t.Fatalf("%s produced no payload", step.name)
			}
			if got := msg.GetMessageType(); got != step.wantType {
				t.Fatalf("%s answered with %s, want %s", step.name, got, step.wantType)
			}
			// A NACK saying "not implemented" is the specific regression guarded here.
			if ack, ok := msg.(mosxml.ROAck); ok && strings.Contains(ack.Status, "not implemented") {
				t.Fatalf("%s is still unimplemented on the MOS 4 transport: %q", step.name, ack.Status)
			}
		})
	}

	// roDelete last, since it removes what the others rely on.
	_, msg = exchange(t, ctx, conn, "90", `<roDelete><roID>`+roID+`</roID></roDelete>`)
	assertROAckOK(t, msg, "roDelete")
}

// TestMOS4PullRecovery proves the recovery behaviour works on this transport too, not
// only on the socket. A roStorySend for a running order we do not hold must be refused
// and followed by a roReq.
func TestMOS4PullRecovery(t *testing.T) {
	_, baseURL, cancel := testServer(t)
	defer cancel()

	ctx, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()

	conn := dialChannel(t, ctx, baseURL, ChannelRO)
	defer conn.Close(1000, "done")

	_, msg := exchange(t, ctx, conn, "500",
		`<roStorySend><roID>RO-ABSENT</roID><storyID>S-1</storyID>`+
			`<storyBody><p>text</p></storyBody></roStorySend>`)

	ack, ok := msg.(mosxml.ROAck)
	if !ok {
		t.Fatalf("answered with %T, want ROAck", msg)
	}
	if !strings.Contains(ack.Status, "NACK") {
		t.Fatalf("roStatus = %q, want a NACK for an unknown running order", ack.Status)
	}

	// The roReq follows as a second frame. MOS 4.0 §2.3 requires the pull, not just the
	// refusal.
	_, follow := readFrame(t, ctx, conn)
	req, ok := follow.(mosxml.ROReq)
	if !ok {
		t.Fatalf("second frame was %T, want ROReq: recovery must ask for what it lacks", follow)
	}
	if req.ROID != "RO-ABSENT" {
		t.Errorf("roReq roID = %q, want RO-ABSENT", req.ROID)
	}
}

func assertROAckOK(t *testing.T, msg mosxml.MOSMessage, label string) {
	t.Helper()
	ack, ok := msg.(mosxml.ROAck)
	if !ok {
		t.Fatalf("%s answered with %T, want ROAck", label, msg)
	}
	if ack.Status != "OK" {
		t.Fatalf("%s roStatus = %q, want OK", label, ack.Status)
	}
}

func messageIDFor(n int) string {
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// readFrame reads one more frame without sending anything, for exchanges where the server
// sends two messages in response to one -- a NACK followed by a roReq.
func readFrame(t *testing.T, ctx context.Context, conn *websocket.Conn) (*mosxml.MosEnvelope, mosxml.MOSMessage) {
	t.Helper()
	frameType, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if frameType != websocket.MessageBinary {
		t.Errorf("frame type = %v, want binary", frameType)
	}
	decoded, err := mosxml.DecodeUCS2BE(data)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	env, msg, _, err := mosxml.ParseEnvelope(decoded)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	return env, msg
}
