package server

import (
	"bytes"
	"context"
	stdxml "encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"airshift/openmos/internal/config"
	mosxml "airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"

	"nhooyr.io/websocket"
)

// clientTestConfig builds a WSClient-oriented config pointing at peerURL.
func clientTestConfig(peerURL string) *config.Config {
	cfg := testConfig() // reuse the shared harness config from ws_test.go
	cfg.MOS.NCSID = "NCS_001"
	cfg.MOS.HeartbeatInterval = 50 * time.Millisecond
	cfg.WSClient.Enabled = true
	cfg.WSClient.PeerURL = peerURL
	cfg.WSClient.Channel = "ro"
	cfg.WSClient.Passive = true
	cfg.WSClient.ReconnectInitial = 20 * time.Millisecond
	cfg.WSClient.ReconnectMax = 200 * time.Millisecond
	return cfg
}

// ncsPeer is a minimal in-process MOS 4 NCS peer used to exercise the client.
// It mimics the frame discipline of a real ENPS endpoint: it decodes inbound
// UCS-2BE binary frames, validates the envelope through the shared validator,
// and replies to reqMachInfo with listMachInfo and to heartbeat with heartbeat,
// always as UCS-2BE binary frames.
type ncsPeer struct {
	server *httptest.Server
	cfg    *config.Config

	mu           sync.Mutex
	lastAuthHdr  string
	lastQuery    string
	connectCount int32

	// forceDropAfterFirst, when set, closes the connection right after the first
	// successful Profile 0 exchange (reqMachInfo + heartbeat) to force a reconnect.
	forceDropAfterFirst atomic.Bool
	firstExchangeDone   atomic.Bool
}

func newNCSPeer(t *testing.T, cfg *config.Config) *ncsPeer {
	t.Helper()
	p := &ncsPeer{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("/mos", p.handle)
	p.server = httptest.NewServer(mux)
	t.Cleanup(p.server.Close)
	return p
}

// wsURL returns the ws:// dial URL for the peer.
func (p *ncsPeer) wsURL() string {
	return "ws" + strings.TrimPrefix(p.server.URL, "http") + "/mos"
}

func (p *ncsPeer) handle(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&p.connectCount, 1)
	p.mu.Lock()
	p.lastAuthHdr = r.Header.Get("Authorization")
	p.lastQuery = r.URL.RawQuery
	p.mu.Unlock()

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "peer closing")
	conn.SetReadLimit(4 << 20)

	ctx := r.Context()
	exchanges := 0
	for {
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
			continue
		}

		var env mosxml.Envelope
		if err := stdxml.Unmarshal(utf8XML, &env); err != nil {
			return
		}
		msg, err := mosxml.ValidateEnvelope(env, mosxml.Gen4x, p.cfg.MOS.ID, "")
		if err != nil {
			return
		}

		var reply []byte
		switch msg.(type) {
		case mosxml.ReqMachInfo:
			info := mosxml.CreateListMachInfo(p.cfg, mosxml.MosRev40)
			reply, err = mosxml.GenerateEnvelope(p.cfg.MOS.ID, env.NcsID, env.MessageID, info)
		case mosxml.Heartbeat:
			hb := mosxml.CreateHeartbeatResponse(p.cfg.MOS.ID, env.MessageID)
			reply, err = mosxml.GenerateEnvelope(p.cfg.MOS.ID, env.NcsID, env.MessageID, hb)
		default:
			continue
		}
		if err != nil {
			return
		}

		encoded, err := mosxml.EncodeUCS2BE(reply)
		if err != nil {
			return
		}
		if err := conn.Write(ctx, websocket.MessageBinary, encoded); err != nil {
			return
		}

		exchanges++
		// After the client has completed reqMachInfo + heartbeat, optionally
		// force a drop to prove the client reconnects.
		if exchanges >= 2 {
			p.firstExchangeDone.Store(true)
			if p.forceDropAfterFirst.Load() {
				conn.Close(websocket.StatusInternalError, "forced drop")
				return
			}
		}
	}
}

func (p *ncsPeer) connections() int32 { return atomic.LoadInt32(&p.connectCount) }

func (p *ncsPeer) authHeader() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastAuthHdr
}

func (p *ncsPeer) query() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastQuery
}

// TestWSClientProfile0AgainstPeer proves the client completes the full Profile 0
// exchange (reqMachInfo -> listMachInfo, then heartbeat -> heartbeat) against a
// MOS 4 peer using UCS-2BE binary frames and the shared validator.
func TestWSClientProfile0AgainstPeer(t *testing.T) {
	cfg := clientTestConfig("")
	peer := newNCSPeer(t, cfg)
	cfg.WSClient.PeerURL = peer.wsURL()

	client := NewWSClient(cfg)
	stop := runClient(t, client)
	defer stop()

	// The peer flips firstExchangeDone once reqMachInfo + heartbeat both replied.
	waitFor(t, 2*time.Second, func() bool { return peer.firstExchangeDone.Load() })

	// Passive mode and the required query params must be present on the dial URL.
	q := peer.query()
	for _, want := range []string{"mosID=OPENMOS_TEST", "channel=ro", "passive=true"} {
		if !strings.Contains(q, want) {
			t.Errorf("dial query %q missing %q", q, want)
		}
	}
}

// TestWSClientProfile0AgainstRealWSServer stands up a real server.WSServer and
// proves the client's reqMachInfo -> listMachInfo leg works against it (the
// server does not answer heartbeats, so only the machine-info leg is asserted
// here; the full heartbeat exchange is covered against the NCS peer above).
func TestWSClientReqMachInfoAgainstRealWSServer(t *testing.T) {
	srv, baseURL, cancelSrv := testServer(t)
	defer cancelSrv()
	defer srv.Shutdown()

	cfg := clientTestConfig(baseURL + "/mos")
	// The real WSServer never replies to heartbeat, so a slow periodic timer
	// keeps the client from erroring on the handshake heartbeat wait. Instead we
	// assert the reqMachInfo leg directly by driving doProfile0's first half.
	client := NewWSClient(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, resp, err := websocket.Dial(ctx, cfg.WSClient.PeerURL+"?mosID=OPENMOS_TEST&ncsID=NCS_001&channel=ro", client.dialOptions())
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("client failed to dial real WSServer: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	reqID := client.messageID()
	reqEnv, err := client.buildReqMachInfo(reqID)
	if err != nil {
		t.Fatalf("build reqMachInfo: %v", err)
	}
	if err := client.writeFrame(ctx, conn, reqEnv); err != nil {
		t.Fatalf("send reqMachInfo: %v", err)
	}
	msg, err := client.readMessage(ctx, conn)
	if err != nil {
		t.Fatalf("await listMachInfo: %v", err)
	}
	if _, ok := msg.(mosxml.ListMachInfo); !ok {
		t.Fatalf("expected ListMachInfo from real WSServer, got %T", msg)
	}
}

// TestWSClientReconnectsAfterDrop proves the client automatically reconnects
// with backoff after a forced drop and completes another Profile 0 exchange.
func TestWSClientReconnectsAfterDrop(t *testing.T) {
	cfg := clientTestConfig("")
	peer := newNCSPeer(t, cfg)
	peer.forceDropAfterFirst.Store(true)
	cfg.WSClient.PeerURL = peer.wsURL()

	client := NewWSClient(cfg)
	stop := runClient(t, client)
	defer stop()

	// Expect at least two connections: the original plus a reconnect after drop.
	waitFor(t, 3*time.Second, func() bool { return peer.connections() >= 2 })
	if got := peer.connections(); got < 2 {
		t.Fatalf("expected client to reconnect (>=2 connections), got %d", got)
	}
}

// TestWSClientSendsBasicAuthAndNeverLogsCredentials proves the Authorization
// header is present when credentials are configured, and that neither the
// username nor the password appears anywhere in captured log output.
func TestWSClientSendsBasicAuthAndNeverLogsCredentials(t *testing.T) {
	const (
		secretUser = "mos-operator"
		secretPass = "sup3r-s3cret-token"
	)

	// Capture all logger output for the duration of this test. The global logger
	// is a process-wide singleton, so it is only restored AFTER the client
	// goroutine has fully stopped (defers run LIFO: stop() below runs before this
	// restore), which keeps the capture free of data races with client logging.
	var buf syncBuffer
	prev := logger.DefaultLogger()
	logger.SetGlobalLogger(logger.NewLogger(logger.LevelDebug, &buf))
	defer logger.SetGlobalLogger(prev)

	cfg := clientTestConfig("")
	cfg.WSClient.Username = secretUser
	cfg.WSClient.Password = secretPass
	peer := newNCSPeer(t, cfg)
	cfg.WSClient.PeerURL = peer.wsURL()

	client := NewWSClient(cfg)
	stop := runClient(t, client)
	defer stop()

	waitFor(t, 2*time.Second, func() bool { return peer.authHeader() != "" })

	// The peer must have received a Basic auth header.
	auth := peer.authHeader()
	if !strings.HasPrefix(auth, "Basic ") {
		t.Fatalf("expected Basic auth header, got %q", auth)
	}
	// Sanity: it decodes to the configured credentials (test-only check).
	req := &http.Request{Header: http.Header{"Authorization": []string{auth}}}
	gotUser, gotPass, ok := req.BasicAuth()
	if !ok || gotUser != secretUser || gotPass != secretPass {
		t.Fatalf("Basic auth did not carry the configured credentials")
	}

	// Let the client run a little to accumulate log lines, then assert secrecy.
	time.Sleep(150 * time.Millisecond)
	logs := buf.String()
	if strings.Contains(logs, secretUser) {
		t.Errorf("username leaked into log output")
	}
	if strings.Contains(logs, secretPass) {
		t.Errorf("password leaked into log output")
	}
	if strings.Contains(logs, auth) {
		t.Errorf("Authorization header leaked into log output")
	}
}

// syncBuffer is a goroutine-safe bytes.Buffer for capturing log output.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// runClient starts the client in a goroutine and returns a stop function that
// cancels its context and blocks until Start has fully returned. Waiting for the
// goroutine to exit ensures no client logging outlives the test, which matters
// because the logger is a process-wide singleton shared across tests.
func runClient(t *testing.T, client *WSClient) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = client.Start(ctx)
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("client did not stop within 3s")
			}
		})
	}
}

// waitFor polls cond until it returns true or the timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %s", timeout)
	}
}
