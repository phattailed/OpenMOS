package timingsend

import (
	"context"
	stdxml "encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nhooyr.io/websocket"

	"airshift/openmos/internal/config"
	mosxml "airshift/openmos/internal/xml"
)

// fakeNCS is a controlled MOS peer standing in for ENPS: it accepts one
// WebSocket connection per Client.Send call (matching StoryActionClient's
// proven one-shot lifecycle, which this package models) and answers
// according to the configured behavior. This is the "controlled MOS peer"
// the milestone's validation requirements ask for connection-loss and
// lost-response coverage against.
type fakeNCS struct {
	server *httptest.Server
	cfg    *config.Config

	// behavior controls what the peer does with each accepted connection.
	behavior func(t *testing.T, conn *websocket.Conn, env mosxml.Envelope, stat mosxml.ROElementStat)

	connectCount atomic.Int32
	t            *testing.T
}

func newFakeNCS(t *testing.T, cfg *config.Config, behavior func(t *testing.T, conn *websocket.Conn, env mosxml.Envelope, stat mosxml.ROElementStat)) *fakeNCS {
	t.Helper()
	p := &fakeNCS{cfg: cfg, behavior: behavior, t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/mos", p.handle)
	p.server = httptest.NewServer(mux)
	t.Cleanup(p.server.Close)
	return p
}

func (p *fakeNCS) wsURL() string {
	return "ws" + strings.TrimPrefix(p.server.URL, "http") + "/mos"
}

func (p *fakeNCS) handle(w http.ResponseWriter, r *http.Request) {
	p.connectCount.Add(1)
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "peer closing")
	conn.SetReadLimit(4 << 20)

	ctx := r.Context()
	msgType, raw, err := conn.Read(ctx)
	if err != nil {
		return
	}
	var utf8XML []byte
	switch msgType {
	case websocket.MessageBinary:
		utf8XML, err = mosxml.DecodeUCS2BE(raw)
		if err != nil {
			return
		}
	case websocket.MessageText:
		utf8XML = raw
	default:
		return
	}

	var env mosxml.Envelope
	if err := stdxml.Unmarshal(utf8XML, &env); err != nil {
		return
	}
	msg, parseErr := env.Message()
	if parseErr != nil {
		return
	}
	stat, ok := msg.(mosxml.ROElementStat)
	if !ok {
		p.t.Fatalf("fakeNCS received unexpected message type %s, want roElementStat", msg.GetMessageType())
		return
	}

	p.behavior(p.t, conn, env, stat)
}

func testConfig(peerURL string) *config.Config {
	cfg := &config.Config{}
	cfg.MOS.ID = "gateway.test"
	cfg.MOS.NCSID = "ncs.test"
	cfg.WSClient.PeerURL = peerURL
	return cfg
}

func acceptingAck(env mosxml.Envelope, stat mosxml.ROElementStat) []byte {
	ack := mosxml.CreateROAck(stat.ROID, "OK", nil)
	reply, _ := mosxml.GenerateEnvelope(env.NcsID, env.MosID, env.MessageID, ack)
	return reply
}

func nackAck(env mosxml.Envelope, stat mosxml.ROElementStat, reason string) []byte {
	ack := mosxml.CreateROAck(stat.ROID, "NACK", []mosxml.ROAckStory{{StoryID: stat.StoryID, Status: reason}})
	reply, _ := mosxml.GenerateEnvelope(env.NcsID, env.MosID, env.MessageID, ack)
	return reply
}

func TestSend_AcceptedAckIsAccepted(t *testing.T) {
	peer := newFakeNCS(t, nil, func(t *testing.T, conn *websocket.Conn, env mosxml.Envelope, stat mosxml.ROElementStat) {
		reply := acceptingAck(env, stat)
		encoded, _ := mosxml.EncodeUCS2BE(reply)
		_ = conn.Write(context.Background(), websocket.MessageBinary, encoded)
	})
	cfg := testConfig(peer.wsURL())
	c := New(cfg)

	ack, err := c.Send(t.Context(), Target{RunningOrderID: "ro-1", StoryRawID: "wire-1"}, 3*time.Second)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !ack.Accepted {
		t.Fatalf("want Accepted, got %+v", ack)
	}
	if peer.connectCount.Load() != 1 {
		t.Fatalf("want exactly one connection, got %d", peer.connectCount.Load())
	}
}

func TestSend_NACKIsNotAccepted(t *testing.T) {
	peer := newFakeNCS(t, nil, func(t *testing.T, conn *websocket.Conn, env mosxml.Envelope, stat mosxml.ROElementStat) {
		reply := nackAck(env, stat, "External modification not allowed")
		encoded, _ := mosxml.EncodeUCS2BE(reply)
		_ = conn.Write(context.Background(), websocket.MessageBinary, encoded)
	})
	cfg := testConfig(peer.wsURL())
	c := New(cfg)

	ack, err := c.Send(t.Context(), Target{RunningOrderID: "ro-1", StoryRawID: "wire-1"}, 3*time.Second)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if ack.Accepted {
		t.Fatalf("want not Accepted, got %+v", ack)
	}
	if ack.Reason == "" {
		t.Fatalf("want a non-empty reason for a NACK, got %+v", ack)
	}
}

// TestSend_ConnectionLossBeforeAckIsTimedOut is the connection-loss
// coverage the milestone's validation requirements ask for: the peer
// accepts the connection, reads the message, then drops the connection
// without ever answering. Send must report this as TimedOut (mapping to
// StateUncertain one layer up in timingplay.Service), never as a
// definitive failure and never by hanging past the deadline.
func TestSend_ConnectionLossBeforeAckIsTimedOut(t *testing.T) {
	peer := newFakeNCS(t, nil, func(t *testing.T, conn *websocket.Conn, env mosxml.Envelope, stat mosxml.ROElementStat) {
		// Drop the connection immediately after reading -- no reply at all.
		_ = conn.Close(websocket.StatusAbnormalClosure, "simulated connection loss")
	})
	cfg := testConfig(peer.wsURL())
	c := New(cfg)

	ack, err := c.Send(t.Context(), Target{RunningOrderID: "ro-1", StoryRawID: "wire-1"}, 2*time.Second)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !ack.TimedOut {
		t.Fatalf("want TimedOut after connection loss, got %+v", ack)
	}
}

// TestSend_LostResponseWithinDeadlineIsTimedOut covers the "lost response"
// case distinctly from an outright connection drop: the peer accepts,
// reads, and then simply never writes anything back and never closes
// either, until Send's own deadline elapses. This proves Send does not
// depend on the peer closing the socket to detect an unanswered request.
func TestSend_LostResponseWithinDeadlineIsTimedOut(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	peer := newFakeNCS(t, nil, func(t *testing.T, conn *websocket.Conn, env mosxml.Envelope, stat mosxml.ROElementStat) {
		<-release // hold the connection open with no reply until the test cleans up
	})
	cfg := testConfig(peer.wsURL())
	c := New(cfg)

	start := time.Now()
	ack, err := c.Send(t.Context(), Target{RunningOrderID: "ro-1", StoryRawID: "wire-1"}, 1*time.Second)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !ack.TimedOut {
		t.Fatalf("want TimedOut for a lost/withheld response, got %+v", ack)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("Send took %s, want it bounded near the 1s deadline, not hanging", elapsed)
	}
}

func TestSend_NoPeerURLConfiguredFailsWithoutDialing(t *testing.T) {
	cfg := testConfig("")
	c := New(cfg)

	_, err := c.Send(t.Context(), Target{RunningOrderID: "ro-1", StoryRawID: "wire-1"}, time.Second)
	if err == nil {
		t.Fatal("want an error when no peer URL is configured")
	}
}

func TestSend_UnreachablePeerFailsDefinitively(t *testing.T) {
	// A closed server: nothing is listening on this URL at all.
	closedPeer := httptest.NewServer(http.NewServeMux())
	closedPeer.Close()

	cfg := testConfig(closedPeer.URL + "/mos")
	c := New(cfg)

	_, err := c.Send(t.Context(), Target{RunningOrderID: "ro-1", StoryRawID: "wire-1"}, time.Second)
	if err == nil {
		t.Fatal("want an error when the peer is unreachable -- this must be definitively Failed, not Uncertain, since nothing reached the wire")
	}
}
