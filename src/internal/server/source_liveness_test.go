package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"airshift/openmos/internal/repository"
	"airshift/openmos/internal/service"
	mosxml "airshift/openmos/internal/xml"
	"nhooyr.io/websocket"
)

func TestCommittedSourcePassivePongPreservesQuietCoverage(t *testing.T) {
	f := newPassiveSourceTest(t)
	before, _ := f.store.Checkpoint()
	// Only the control Pongs can renew this session during the quiet interval.
	time.Sleep(2 * f.timeout)
	got := f.publish(t)
	if !got.Complete || got.Revision != before.Revision || len(got.Stories) != 1 {
		t.Fatal("responsive passive socket lost complete source coverage after the peer timeout")
	}
	if f.wire.pongs.Load() == 0 {
		t.Fatal("passive source did not check the peer with a control Ping")
	}
}

func TestCommittedSourcePassiveMissingPongInvalidatesCoverage(t *testing.T) {
	for _, failure := range []string{"missing", "unmatched", "disconnected"} {
		t.Run(failure, func(t *testing.T) {
			f := newPassiveSourceTest(t)
			before, _ := f.store.Checkpoint()
			pong := f.holdPong(t)
			if failure == "unmatched" {
				pong[len(pong)-1] ^= 1
				f.wire.sendPong(t, pong)
			} else if failure == "disconnected" {
				_ = f.conn.CloseNow()
			}
			select {
			case <-f.clientDone:
			case <-time.After(2 * f.timeout):
				t.Fatal("passive source did not end a failed Ping session")
			}
			got := f.publish(t)
			if got.Complete || len(got.Stories) != 0 || got.Revision <= before.Revision {
				t.Fatal("missing or unmatched Pong left complete source coverage renewable")
			}
		})
	}
}

func TestCommittedSourcePassiveReaderDrainsBurstWhilePongOutstanding(t *testing.T) {
	f := newPassiveSourceTest(t)
	before, _ := f.store.Checkpoint()
	pong := f.holdPong(t)
	for i := 0; i < 4; i++ {
		frame := mosxml.WrapEnvelope("device", "newsroom", fmt.Sprintf("burst-%d", i), []byte(`<roReadyToAir><roID>rundown</roID><roAir>NOTREADY</roAir></roReadyToAir>`))
		encoded, err := mosxml.EncodeUCS2BE(frame)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.conn.Write(f.ctx, websocket.MessageBinary, encoded); err != nil {
			t.Fatal(err)
		}
	}
	// Four frames exceed the client's one-frame buffer. Each must be applied and
	// acknowledged before we release the matching Pong, not after a Ping timeout.
	for i := 0; i < 4; i++ {
		select {
		case ack, ok := <-f.acks:
			if !ok || !bytes.Contains(ack, []byte(fmt.Sprintf("<messageID>burst-%d</messageID>", i))) || !bytes.Contains(ack, []byte("<roStatus>OK</roStatus>")) {
				t.Fatal("passive reader stalled or lost an ACK while Pong was outstanding")
			}
		case <-f.ctx.Done():
			t.Fatal("passive reader blocked behind its Ping")
		}
	}
	after, _ := f.store.Checkpoint()
	if after.Revision != before.Revision+4 || len(after.Receipts) != len(before.Receipts)+4 {
		t.Fatal("burst ACK preceded durable source retention")
	}
	f.wire.hold.Store(false)
	f.wire.sendPong(t, pong)
	time.Sleep(2 * f.timeout)
	if got := f.publish(t); !got.Complete || got.Revision != after.Revision {
		t.Fatal("matching Pong after the burst failed to retain complete quiet coverage")
	}
}

type passiveSourceTest struct {
	ctx        context.Context
	cancel     context.CancelFunc
	timeout    time.Duration
	source     *service.CommittedSource
	store      *repository.Durable
	conn       *websocket.Conn
	wire       *sourcePongConn
	clientDone chan struct{}
	acks       chan []byte
	posted     chan service.SourceSnapshot
}

func newPassiveSourceTest(t *testing.T) *passiveSourceTest {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	posted := make(chan service.SourceSnapshot, 4)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var snapshot service.SourceSnapshot
		if err := json.NewDecoder(r.Body).Decode(&snapshot); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case posted <- snapshot:
		case <-ctx.Done():
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"acceptedRevision": snapshot.Revision, "duplicate": false, "destinationApplied": false})
	}))
	t.Cleanup(receiver.Close)
	cfg := testConfig()
	cfg.MOS.ID, cfg.MOS.NCSID = "device", "newsroom"
	cfg.MOS.HeartbeatInterval = 50 * time.Millisecond
	cfg.MOS.ClientTimeout = 750 * time.Millisecond
	cfg.State.Dir = t.TempDir()
	cfg.Source.Enabled, cfg.Source.Transport = true, "ws-client"
	cfg.WSClient.Channel, cfg.WSClient.Passive = "ro", true
	binding := repository.SourceBinding{SourceID: "source", RundownID: "rundown", MosID: "device", NCSID: "newsroom", Transport: "ws-client", Destination: receiver.URL + "/v1/openmos-snapshots"}
	durable, err := repository.OpenCommitted(cfg.State.Dir, binding, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = durable.Close() })
	svc := service.NewMOSService(durable.RunningOrders(), durable.Stories(), durable.Items(), durable.Objects(), nil)
	svc.Source, err = service.NewCommittedSource(ctx, durable, binding, "synthetic-token", cfg.MOS.ClientTimeout)
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan *websocket.Conn, 1)
	peer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		accepted <- conn
		<-ctx.Done()
		_ = conn.CloseNow()
	}))
	rawAccepted := make(chan *sourcePongConn, 1)
	peer.Listener = sourcePongListener{Listener: peer.Listener, accepted: rawAccepted}
	peer.Start()
	t.Cleanup(peer.Close)
	cfg.WSClient.PeerURL = "ws" + strings.TrimPrefix(peer.URL, "http")
	client := NewWSClient(cfg, nil, svc)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = client.runSession(ctx, cfg.WSClient.PeerURL, clientLane{name: "passive", passive: true})
	}()
	t.Cleanup(func() { cancel(); <-done })
	var conn *websocket.Conn
	select {
	case conn = <-accepted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	f := &passiveSourceTest{ctx: ctx, cancel: cancel, timeout: cfg.MOS.ClientTimeout, source: svc.Source, store: durable, conn: conn, wire: <-rawAccepted, clientDone: done, acks: make(chan []byte, 4), posted: posted}
	send := sourceWSSender(t, ctx, conn)
	for i, operation := range []string{
		`<roCreate><roID>rundown</roID><roSlug>Synthetic rundown</roSlug><story><storyID>story</storyID></story></roCreate>`,
		`<roStorySend><roID>rundown</roID><storyID>story</storyID><storyBody/></roStorySend>`,
	} {
		reply, _ := send(string(mosxml.WrapEnvelope("device", "newsroom", []string{"roster", "body"}[i], []byte(operation))))
		if !bytes.Contains(reply, []byte("<roStatus>OK</roStatus>")) {
			t.Fatalf("source setup was rejected: %s", reply)
		}
	}
	// Read control frames so the library answers Pings. No further MOS input can renew
	// this session; outgoing keepAlive frames do not prove that the peer is alive.
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		defer close(f.acks)
		for {
			_, frame, err := conn.Read(ctx)
			if err != nil {
				return
			}
			decoded, _ := mosxml.DecodeUCS2BE(frame)
			if bytes.Contains(decoded, []byte("<roAck>")) {
				select {
				case f.acks <- decoded:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-peerDone })
	return f
}

func (f *passiveSourceTest) holdPong(t *testing.T) []byte {
	t.Helper()
	f.wire.hold.Store(true)
	select {
	case pong := <-f.wire.held:
		return pong
	case <-f.ctx.Done():
		t.Fatal("passive source did not send a control Ping")
		return nil
	}
}

func (f *passiveSourceTest) publish(t *testing.T) service.SourceSnapshot {
	t.Helper()
	// The real publisher performs its initial expiry sweep after the quiet interval.
	// Keeping this start explicit avoids replacing its production timer with a test hook.
	publisherDone := make(chan struct{})
	go func() { defer close(publisherDone); f.source.RunPublisher(f.ctx) }()
	t.Cleanup(func() { f.cancel(); <-publisherDone })
	var snapshot service.SourceSnapshot
	select {
	case snapshot = <-f.posted:
	case <-f.ctx.Done():
		t.Fatal("publisher did not deliver the retained source")
	}
	waitFor(t, time.Second, func() bool {
		cp, err := f.store.Checkpoint()
		return err == nil && cp.AcceptedRevision == snapshot.Revision && cp.Revision == snapshot.Revision
	})
	return snapshot
}

type sourcePongListener struct {
	net.Listener
	accepted chan *sourcePongConn
}

func (l sourcePongListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	wire := &sourcePongConn{Conn: conn, held: make(chan []byte, 1)}
	l.accepted <- wire
	return wire, nil
}

// The pinned library flushes each short unmasked control frame in one write.
// Intercept only that Pong write; HTTP, MOS data and the actual reader stay native.
type sourcePongConn struct {
	net.Conn
	mu    sync.Mutex
	hold  atomic.Bool
	pongs atomic.Int64
	held  chan []byte
}

func (c *sourcePongConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(p) >= 2 && p[0] == 0x8a && p[1] <= 125 && len(p) == 2+int(p[1]) {
		c.pongs.Add(1)
		if c.hold.Load() {
			select {
			case c.held <- append([]byte(nil), p...):
			default:
			}
			return len(p), nil
		}
	}
	return c.Conn.Write(p)
}

func (c *sourcePongConn) sendPong(t *testing.T, pong []byte) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.Conn.Write(pong); err != nil {
		t.Fatal(err)
	}
}
