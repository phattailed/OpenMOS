package server

import (
	"context"
	"strings"
	"testing"
	"time"

	mosxml "airshift/openmos/internal/xml"
	"nhooyr.io/websocket"
)

// dialChannel opens a MOS 4 session on the named channel.
func dialChannel(t *testing.T, ctx context.Context, baseURL, channel string) *websocket.Conn {
	t.Helper()
	url := baseURL + "/mos?mosID=OPENMOS_TEST&ncsID=NCS_001&channel=" + channel
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("failed to connect on channel %s: %v", channel, err)
	}
	return conn
}

// exchange sends one enveloped operation and returns the decoded reply.
func exchange(t *testing.T, ctx context.Context, conn *websocket.Conn, messageID, innerXML string) (*mosxml.MosEnvelope, mosxml.MOSMessage) {
	t.Helper()

	frame := mosxml.WrapEnvelope("OPENMOS_TEST", "NCS_001", messageID, []byte(innerXML))
	binary, err := mosxml.EncodeUCS2BE(frame)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, binary); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	frameType, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if frameType != websocket.MessageBinary {
		t.Errorf("reply frame type = %v, want binary", frameType)
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

// Standard mode opens one connection per channel, so a single peer holds mom, ro
// and aux at the same time. Sessions are keyed on (ncsID, channel), so none of
// them may evict another.
func TestChannelsCoexistForOnePeer(t *testing.T) {
	_, baseURL, cancel := testServer(t)
	defer cancel()

	ctx, ctxCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer ctxCancel()

	mom := dialChannel(t, ctx, baseURL, ChannelMom)
	defer mom.Close(websocket.StatusNormalClosure, "")
	ro := dialChannel(t, ctx, baseURL, ChannelRO)
	defer ro.Close(websocket.StatusNormalClosure, "")
	aux := dialChannel(t, ctx, baseURL, ChannelAux)
	defer aux.Close(websocket.StatusNormalClosure, "")

	// Profile 0 must work on all three, and the earlier connections must still be
	// alive after the later ones were opened.
	for name, conn := range map[string]*websocket.Conn{
		ChannelMom: mom,
		ChannelRO:  ro,
		ChannelAux: aux,
	} {
		env, msg := exchange(t, ctx, conn, "700", "<reqMachInfo/>")
		if _, ok := msg.(mosxml.ListMachInfo); !ok {
			t.Errorf("channel %s: reply was %T, want ListMachInfo", name, msg)
		}
		if env.MessageID != "700" {
			t.Errorf("channel %s: messageID = %q, want 700", name, env.MessageID)
		}
	}
}

// A running order belongs on ro. Arriving on mom it must be refused with an
// explanation, not silently accepted -- the spec keeps traffic on the two ports
// independent, and quietly accepting it would make sequencing bugs very hard to
// trace.
func TestRunningOrderRefusedOnObjectChannel(t *testing.T) {
	_, baseURL, cancel := testServer(t)
	defer cancel()

	ctx, ctxCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer ctxCancel()

	conn := dialChannel(t, ctx, baseURL, ChannelMom)
	defer conn.Close(websocket.StatusNormalClosure, "")

	_, msg := exchange(t, ctx, conn, "701",
		`<roCreate><roID>RO-CHAN</roID><roSlug>wrong channel</roSlug></roCreate>`)

	ack, ok := msg.(mosxml.MOSAck)
	if !ok {
		t.Fatalf("reply was %T, want MOSAck carrying a NACK", msg)
	}
	if ack.Status != "NACK" {
		t.Errorf("status = %q, want NACK", ack.Status)
	}
	// The peer needs to be told where the message should have gone.
	for _, want := range []string{"ro", "10541"} {
		if !strings.Contains(ack.StatusDescription, want) {
			t.Errorf("statusDescription %q should mention %q", ack.StatusDescription, want)
		}
	}
}

// The same running order on its own channel is accepted, which confirms the
// refusal above was about the channel and not the message.
func TestRunningOrderAcceptedOnRunningOrderChannel(t *testing.T) {
	_, baseURL, cancel := testServer(t)
	defer cancel()

	ctx, ctxCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer ctxCancel()

	conn := dialChannel(t, ctx, baseURL, ChannelRO)
	defer conn.Close(websocket.StatusNormalClosure, "")

	_, msg := exchange(t, ctx, conn, "702",
		`<roCreate><roID>RO-CHAN-OK</roID><roSlug>right channel</roSlug></roCreate>`)

	ack, ok := msg.(mosxml.ROAck)
	if !ok {
		t.Fatalf("reply was %T, want ROAck", msg)
	}
	if ack.Status != "OK" {
		t.Errorf("roStatus = %q, want OK", ack.Status)
	}
}
