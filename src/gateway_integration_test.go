package main

import (
	"bytes"
	"context"
	"encoding/json"
	stdxml "encoding/xml"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	mosxml "airshift/openmos/internal/xml"

	"nhooyr.io/websocket"
)

// TestGatewayIntegrationEntrypoint exercises the real, integrated main.go
// binary end to end: it re-execs main() in a subprocess (same pattern as
// TestSourceStateDirectoryEntrypoint), with Gateway.Enabled true, the
// "file" backend (this milestone's supported backend), and a real fake NCS
// peer standing in for ENPS on the outbound timingsend connection.
//
// This is the "build the actual integrated appliance and exercise the
// adapters against the supported backend using disposable local data and a
// fake peer" requirement -- a real subprocess and a real HTTP call, not a
// unit test double for either mosService or the HTTP layer.
func TestGatewayIntegrationEntrypoint(t *testing.T) {
	if mode := os.Getenv("OPENMOS_TEST_ENTRYPOINT"); mode == "gateway" {
		flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
		os.Args = []string{os.Args[0]}
		main()
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("subprocess interrupt delivery is unavailable on Windows")
	}

	t.Run("disabled configuration preserves existing startup behavior", func(t *testing.T) {
		testGatewayDisabledPreservesStartup(t)
	})
	t.Run("enabled configuration serves a real play request against a fake peer", func(t *testing.T) {
		testGatewayEnabledServesPlay(t)
	})
	t.Run("shutdown waits for an in-flight request instead of severing it", func(t *testing.T) {
		testGatewayShutdownDuringInFlightRequest(t)
	})
	t.Run("a request outlasting the old short shutdown grace still completes and its outcome is durably recorded", func(t *testing.T) {
		testGatewayShutdownGraceCoversSlowInFlightRequest(t)
	})
}

// --- shared subprocess plumbing -------------------------------------------------

type gatewayHarness struct {
	t         *testing.T
	dir       string
	tcpPort   int
	gwPort    int
	extraEnv  []string
	tcpListen net.Listener // reserved to pick a free port, then closed before the subprocess binds it
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func newGatewayHarness(t *testing.T, gatewayEnabled bool, extraEnv ...string) *gatewayHarness {
	t.Helper()
	dir := t.TempDir()
	h := &gatewayHarness{
		t:        t,
		dir:      dir,
		tcpPort:  freePort(t),
		gwPort:   freePort(t),
		extraEnv: extraEnv,
	}
	_ = gatewayEnabled
	return h
}

func (h *gatewayHarness) command(ctx context.Context) *exec.Cmd {
	t := h.t
	t.Helper()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGatewayIntegrationEntrypoint$")
	cmd.Dir = h.dir
	env := []string{
		"OPENMOS_TEST_ENTRYPOINT=gateway",
		"STORAGE_BACKEND=file", "STATE_DIR=" + filepath.Join(h.dir, "state"),
		"SERVER_ENABLED=true", "SERVER_HOST=127.0.0.1", "SERVER_PORT=" + strconv.Itoa(h.tcpPort),
		"WS_ENABLED=false", "WS_CLIENT_ENABLED=false",
		"SOURCE_ENABLED=false",
		"MOS_ID=openmos.gateway.test", "MOS_NCS_ID=ncs.gateway.test",
		"MOS_CLIENT_TIMEOUT=5s", "MOS_HEARTBEAT_INTERVAL=30s",
		"LOG_LEVEL=warning",
	}
	env = append(env, h.extraEnv...)
	cmd.Env = env
	return cmd
}

// start launches the subprocess, waits for the TCP listener to accept
// connections (proof the appliance actually started, not just that the
// process exists), and returns a stop function that sends SIGINT and waits
// for exit -- exercising the same graceful-shutdown path production uses.
func (h *gatewayHarness) start(ctx context.Context) (*exec.Cmd, func() (string, error)) {
	t := h.t
	t.Helper()
	cmd := h.command(ctx)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start subprocess: %v", err)
	}
	h.waitForTCP(t, cmd, &out)
	stop := func() (string, error) {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			return out.String(), err
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			return out.String(), fmt.Errorf("subprocess did not exit within 15s of SIGINT")
		}
	}
	return cmd, stop
}

func (h *gatewayHarness) waitForTCP(t *testing.T, cmd *exec.Cmd, out *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	addr := fmt.Sprintf("127.0.0.1:%d", h.tcpPort)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		if cmd.ProcessState != nil {
			t.Fatalf("subprocess exited before TCP listener was ready:\n%s", out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("TCP listener on %s never became ready:\n%s", addr, out.String())
}

// seedRunningOrderAndStory drives a real roCreate over the appliance's real
// TCP transport -- the same production ingestion path OpenMOS's own
// integration tests use -- so the gateway's resolver has a real running
// order/story to find. This is "disposable local data," not a mock of the
// resolver's lookup.
func seedRunningOrderAndStory(t *testing.T, tcpPort int, roID, storyID string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tcpPort), 3*time.Second)
	if err != nil {
		t.Fatalf("dial appliance TCP listener: %v", err)
	}
	defer conn.Close()

	request := fmt.Sprintf(`<mos>
  <mosID>openmos.gateway.test</mosID>
  <ncsID>ncs.gateway.test</ncsID>
  <messageID>1</messageID>
  <roCreate>
    <roID>%s</roID>
    <roSlug>Gateway integration tracer</roSlug>
    <story>
      <storyID>%s</storyID>
      <storySlug>Gateway integration story</storySlug>
    </story>
  </roCreate>
</mos>`, roID, storyID)
	if _, err := conn.Write(encodeUCS2BEForGatewayTest(request)); err != nil {
		t.Fatalf("write roCreate: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	frame := readUCS2BEFrameForGatewayTest(t, conn)
	var ack struct {
		XMLName stdxml.Name `xml:"mos"`
		ROAck   struct {
			ROID   string `xml:"roID"`
			Status string `xml:"roStatus"`
		} `xml:"roAck"`
	}
	if err := stdxml.Unmarshal(stripXMLDeclarationForGatewayTest(decodeUCS2BEForGatewayTest(t, frame)), &ack); err != nil {
		t.Fatalf("decode roAck: %v\nframe: %s", err, frame)
	}
	if ack.ROAck.ROID != roID {
		t.Fatalf("roAck.roID = %q, want %q", ack.ROAck.ROID, roID)
	}
	if ack.ROAck.Status != "OK" {
		t.Fatalf("roCreate was not accepted: roStatus=%q", ack.ROAck.Status)
	}
}

func encodeUCS2BEForGatewayTest(value string) []byte {
	units := utf16.Encode([]rune(value))
	encoded := make([]byte, 0, len(units)*2)
	for _, unit := range units {
		encoded = append(encoded, byte(unit>>8), byte(unit))
	}
	return encoded
}

func decodeUCS2BEForGatewayTest(t *testing.T, encoded []byte) string {
	t.Helper()
	if len(encoded)%2 != 0 {
		t.Fatalf("odd UCS-2BE byte count: %d", len(encoded))
	}
	units := make([]uint16, len(encoded)/2)
	for i := range units {
		units[i] = uint16(encoded[i*2])<<8 | uint16(encoded[i*2+1])
	}
	return string(utf16.Decode(units))
}

func stripXMLDeclarationForGatewayTest(s string) []byte {
	if idx := strings.Index(s, "<?xml"); idx == 0 {
		if end := strings.Index(s, "?>"); end != -1 {
			s = s[end+2:]
		}
	}
	return []byte(s)
}

// readUCS2BEFrameForGatewayTest reads one framed reply the same way
// TCPServer writes it: a length-prefixed byte count is NOT used on this
// transport (raw UCS2BE stream, one message per TCP write), so we read
// until the peer's single response write completes -- bounded by the read
// deadline set by the caller, matching the pattern OpenMOS's own
// mos28_integration_test.go readMOS28XMLForTest / connection use.
func readUCS2BEFrameForGatewayTest(t *testing.T, conn net.Conn) []byte {
	t.Helper()
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	// A single Read is exactly right for one MOS message: the appliance
	// closes neither the connection nor writes a second message here.
	n, err := conn.Read(chunk)
	if err != nil {
		t.Fatalf("read roAck frame: %v", err)
	}
	buf = append(buf, chunk[:n]...)
	return buf
}

// --- fake NCS peer for the outbound timingsend connection -----------------

// fakeNCSForGatewayTest mirrors internal/timingsend's own fakeNCS: it
// accepts one WebSocket connection per outbound roElementStat and answers
// with a real MOS ack envelope. Reused here (structurally identical, not
// imported, since it lives in an internal test-only file of a different
// package) so this integration test drives the same wire shape the
// already-reviewed unit tests already prove Client.Send produces.
func fakeNCSForGatewayTest(t *testing.T, onElementStat func(env mosxml.Envelope, stat mosxml.ROElementStat) []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/mos", func(w http.ResponseWriter, r *http.Request) {
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
			return
		}
		reply := onElementStat(env, stat)
		if reply == nil {
			return
		}
		encoded, encErr := mosxml.EncodeUCS2BE(reply)
		if encErr != nil {
			return
		}
		_ = conn.Write(ctx, websocket.MessageBinary, encoded)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func acceptingAckForGatewayTest(env mosxml.Envelope, stat mosxml.ROElementStat) []byte {
	ack := mosxml.CreateROAck(stat.ROID, "OK", nil)
	reply, _ := mosxml.GenerateEnvelope(env.NcsID, env.MosID, env.MessageID, ack)
	return reply
}

func wsURLForGatewayTest(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http") + "/mos"
}

// --- HTTP client for the gateway's own API --------------------------------

type playResponseForGatewayTest struct {
	RequestID     string `json:"requestId"`
	State         string `json:"state"`
	FailureReason string `json:"failureReason,omitempty"`
}

func postPlayForGatewayTest(t *testing.T, gwPort int, token, sourceID, rundownID, storyID, requestID string) (int, playResponseForGatewayTest) {
	return postPlayWithTimeoutForGatewayTest(t, gwPort, token, sourceID, rundownID, storyID, requestID, 8*time.Second)
}

// postPlayWithTimeoutForGatewayTest lets a caller whose request is expected
// to stay in flight longer than the default 8s client timeout (e.g. a
// deliberately held outbound send) name a client-side timeout that covers
// it, without changing the default for every other caller of
// postPlayForGatewayTest.
func postPlayWithTimeoutForGatewayTest(t *testing.T, gwPort int, token, sourceID, rundownID, storyID, requestID string, clientTimeout time.Duration) (int, playResponseForGatewayTest) {
	t.Helper()
	body := fmt.Sprintf(`{"sourceId":%q,"rundownId":%q,"storyId":%q,"requestId":%q}`, sourceID, rundownID, storyID, requestID)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/timing/play", gwPort), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: clientTimeout}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /api/timing/play: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var parsed playResponseForGatewayTest
	_ = json.Unmarshal(data, &parsed)
	return resp.StatusCode, parsed
}

// --- the three scenarios ---------------------------------------------------

// testGatewayDisabledPreservesStartup proves Gateway.Enabled=false (the
// default) is behaviorally identical to the pre-integration binary: the
// appliance starts, its own transports come up, and nothing listens on the
// gateway's port.
func testGatewayDisabledPreservesStartup(t *testing.T) {
	h := newGatewayHarness(t, false, "GATEWAY_ENABLED=false", "LOG_LEVEL=info")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, stop := h.start(ctx)
	out, err := stop()
	if err != nil {
		t.Fatalf("graceful shutdown failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "Timing-play gateway listening") {
		t.Fatalf("gateway must not start when disabled:\n%s", out)
	}
	if !strings.Contains(out, "Timing-play gateway disabled by configuration") {
		t.Fatalf("expected the disabled log line, got:\n%s", out)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", h.gwPort)
	if conn, dialErr := net.DialTimeout("tcp", addr, 500*time.Millisecond); dialErr == nil {
		conn.Close()
		t.Fatalf("something is listening on the gateway port %s despite Gateway.Enabled=false", addr)
	}
}

// testGatewayEnabledServesPlay is the core adapter-wiring proof: a real
// running order/story seeded via a real roCreate on the appliance's real
// TCP transport, then a real POST /api/timing/play against the real
// integrated binary, whose outbound send reaches a real fake NCS peer and
// gets a real accepted roAck back.
func testGatewayEnabledServesPlay(t *testing.T) {
	const roID = "RO-GATEWAY-1"
	const storyID = "STORY-GATEWAY-1"
	const sourceID = "breakglass"
	const callerToken = "test-token-abc"

	peer := fakeNCSForGatewayTest(t, func(env mosxml.Envelope, stat mosxml.ROElementStat) []byte {
		if stat.ROID != roID || stat.StoryID != storyID {
			t.Errorf("unexpected roElementStat target: roID=%q storyID=%q, want %q/%q", stat.ROID, stat.StoryID, roID, storyID)
		}
		return acceptingAckForGatewayTest(env, stat)
	})

	h := newGatewayHarness(t, true,
		"GATEWAY_ENABLED=true",
		"GATEWAY_BIND_ADDR=127.0.0.1:"+strconv.Itoa(0), // placeholder, overwritten below
	)
	// GATEWAY_BIND_ADDR must name the reserved port exactly.
	h.extraEnv[len(h.extraEnv)-1] = "GATEWAY_BIND_ADDR=127.0.0.1:" + strconv.Itoa(h.gwPort)
	h.extraEnv = append(h.extraEnv,
		"GATEWAY_AUTH_TOKENS=breakglass:"+callerToken,
		"GATEWAY_SOURCE_ID="+sourceID,
		"WS_CLIENT_PEER_URL="+wsURLForGatewayTest(peer.URL),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, stop := h.start(ctx)
	defer func() { _, _ = stop() }()

	seedRunningOrderAndStory(t, h.tcpPort, roID, storyID)

	deadline := time.Now().Add(10 * time.Second)
	var status int
	var resp playResponseForGatewayTest
	// The resolver matches request.storyId against OpenMOS's internal
	// composite persistence key (url.PathEscape(roID)+"/"+url.PathEscape(storyID)),
	// never the bare wire storyID -- see internal/service's
	// storyPersistenceID and timingplay.Resolver.Resolve's story loop.
	compositeStoryID := url.PathEscape(roID) + "/" + url.PathEscape(storyID)
	for {
		status, resp = postPlayForGatewayTest(t, h.gwPort, callerToken, sourceID, roID, compositeStoryID, "req-1")
		if status == http.StatusAccepted || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if status != http.StatusAccepted {
		t.Fatalf("POST /api/timing/play: status=%d body=%+v", status, resp)
	}
	if resp.RequestID != "req-1" {
		t.Fatalf("requestId = %q, want %q", resp.RequestID, "req-1")
	}

	// Poll GET status until the outcome is terminal -- the send is
	// dispatched asynchronously relative to the 202, per the milestone's
	// own "202 is not proof of execution" contract.
	final := pollStatusForGatewayTest(t, h.gwPort, callerToken, resp.RequestID)
	if final.State != "sent" {
		t.Fatalf("final state = %q, want %q (body=%+v)", final.State, "sent", final)
	}
}

// testGatewayShutdownDuringInFlightRequest proves http.Server.Shutdown's
// documented behavior actually holds for this wiring: a request already
// inside the handler when SIGINT arrives completes and its response
// reaches the caller, rather than being severed and left for a client to
// guess whether it should retry.
func testGatewayShutdownDuringInFlightRequest(t *testing.T) {
	const roID = "RO-GATEWAY-2"
	const storyID = "STORY-GATEWAY-2"
	const sourceID = "breakglass"
	const callerToken = "test-token-xyz"

	release := make(chan struct{})
	reached := make(chan struct{}, 1)
	peer := fakeNCSForGatewayTest(t, func(env mosxml.Envelope, stat mosxml.ROElementStat) []byte {
		select {
		case reached <- struct{}{}:
		default:
		}
		<-release // hold the outbound send open until the test releases it
		return acceptingAckForGatewayTest(env, stat)
	})

	h := newGatewayHarness(t, true)
	h.extraEnv = append(h.extraEnv,
		"GATEWAY_ENABLED=true",
		"GATEWAY_BIND_ADDR=127.0.0.1:"+strconv.Itoa(h.gwPort),
		"GATEWAY_AUTH_TOKENS=breakglass:"+callerToken,
		"GATEWAY_SOURCE_ID="+sourceID,
		"WS_CLIENT_PEER_URL="+wsURLForGatewayTest(peer.URL),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd, stop := h.start(ctx)
	_ = cmd

	seedRunningOrderAndStory(t, h.tcpPort, roID, storyID)

	type result struct {
		status int
		resp   playResponseForGatewayTest
	}
	done := make(chan result, 1)
	compositeStoryID := url.PathEscape(roID) + "/" + url.PathEscape(storyID)
	go func() {
		status, resp := postPlayForGatewayTest(t, h.gwPort, callerToken, sourceID, roID, compositeStoryID, "req-shutdown-1")
		done <- result{status, resp}
	}()

	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("outbound send never reached the fake peer")
	}

	// Signal shutdown while the handler is still blocked inside the
	// outbound send -- this is the "in-flight request" moment.
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		close(release)
		t.Fatalf("signal SIGINT: %v", err)
	}
	// Give the process a moment to enter its shutdown sequence before
	// releasing the held send, so Shutdown is genuinely already waiting
	// on this handler rather than racing ahead of it.
	time.Sleep(300 * time.Millisecond)
	close(release)

	select {
	case r := <-done:
		if r.status != http.StatusAccepted {
			t.Fatalf("in-flight request did not complete normally across shutdown: status=%d body=%+v", r.status, r.resp)
		}
		if r.resp.RequestID != "req-shutdown-1" {
			t.Fatalf("requestId = %q, want %q", r.resp.RequestID, "req-shutdown-1")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("in-flight request never completed -- shutdown severed it instead of waiting")
	}

	out, err := stop()
	if err != nil {
		t.Fatalf("graceful shutdown failed after in-flight request: %v\n%s", err, out)
	}
}

func pollStatusForGatewayTest(t *testing.T, gwPort int, token, requestID string) playResponseForGatewayTest {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/timing/play/%s", gwPort, requestID), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET status: %v", err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var parsed playResponseForGatewayTest
		_ = json.Unmarshal(data, &parsed)
		if parsed.State != "" && parsed.State != "pending" {
			return parsed
		}
		if time.Now().After(deadline) {
			t.Fatalf("status never left pending: %+v", parsed)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// testGatewayShutdownGraceCoversSlowInFlightRequest is the regression for
// the real shutdown/store-lifetime bug: the original code used a fixed
// 5-second Shutdown deadline while timingplay.Service's own dispatch
// timeout was 30 seconds, and closed gwStore via an unconditional defer
// that ran at main()'s exit regardless of whether Shutdown had actually
// finished waiting. A handler still legitimately blocked in its own
// bounded outbound send past 5 seconds would have had Shutdown give up and
// return early, main() would then proceed to close the journal file, and
// the handler's later TransitionSent/TransitionUncertain call would write
// to an already-closed *os.File.
//
// This holds the fake peer open for 8 seconds -- comfortably past the OLD
// 5-second grace, comfortably within the fixed code's ~40-second grace
// (gatewayDispatchTimeout + margin) -- signals shutdown while the request
// is in flight, and then proves two things the old code could not: (1) the
// HTTP response still completes normally, and (2) the request's "sent"
// outcome is actually found in the on-disk journal after the process has
// fully exited, which is only possible if the store was still open and
// writable at the moment that transition was recorded.
func testGatewayShutdownGraceCoversSlowInFlightRequest(t *testing.T) {
	const roID = "RO-GATEWAY-3"
	const storyID = "STORY-GATEWAY-3"
	const sourceID = "breakglass"
	const callerToken = "test-token-slow"
	const holdDuration = 8 * time.Second

	release := make(chan struct{})
	reached := make(chan struct{}, 1)
	peer := fakeNCSForGatewayTest(t, func(env mosxml.Envelope, stat mosxml.ROElementStat) []byte {
		select {
		case reached <- struct{}{}:
		default:
		}
		<-release // held open by the test for holdDuration, past the old 5s grace
		return acceptingAckForGatewayTest(env, stat)
	})

	h := newGatewayHarness(t, true)
	h.extraEnv = append(h.extraEnv,
		"GATEWAY_ENABLED=true",
		"GATEWAY_BIND_ADDR=127.0.0.1:"+strconv.Itoa(h.gwPort),
		"GATEWAY_AUTH_TOKENS=breakglass:"+callerToken,
		"GATEWAY_SOURCE_ID="+sourceID,
		"WS_CLIENT_PEER_URL="+wsURLForGatewayTest(peer.URL),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd, stop := h.start(ctx)

	seedRunningOrderAndStory(t, h.tcpPort, roID, storyID)

	type result struct {
		status int
		resp   playResponseForGatewayTest
	}
	done := make(chan result, 1)
	compositeStoryID := url.PathEscape(roID) + "/" + url.PathEscape(storyID)
	go func() {
		status, resp := postPlayWithTimeoutForGatewayTest(t, h.gwPort, callerToken, sourceID, roID, compositeStoryID, "req-slow-1", 30*time.Second)
		done <- result{status, resp}
	}()

	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("outbound send never reached the fake peer")
	}

	// Signal shutdown immediately -- the handler is now blocked inside the
	// outbound send, and will stay blocked for holdDuration, well past the
	// OLD 5-second grace this test exists to regress against.
	shutdownSignaledAt := time.Now()
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		close(release)
		t.Fatalf("signal SIGINT: %v", err)
	}

	// Release only after outliving the old buggy grace, proving Shutdown is
	// still genuinely waiting at that point rather than having already
	// given up and let main() proceed to close the store.
	time.Sleep(holdDuration)
	close(release)

	select {
	case r := <-done:
		if r.status != http.StatusAccepted {
			t.Fatalf("in-flight request did not complete normally: status=%d body=%+v", r.status, r.resp)
		}
		if r.resp.RequestID != "req-slow-1" {
			t.Fatalf("requestId = %q, want %q", r.resp.RequestID, "req-slow-1")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("in-flight request never completed -- shutdown grace did not cover it")
	}
	if elapsed := time.Since(shutdownSignaledAt); elapsed < holdDuration {
		t.Fatalf("request completed after only %s since SIGINT, want at least %s -- the hold was not actually exercised", elapsed, holdDuration)
	}

	out, err := stop()
	if err != nil {
		t.Fatalf("graceful shutdown failed after slow in-flight request: %v\n%s", err, out)
	}

	// The decisive assertion: read the on-disk journal AFTER the process
	// has fully exited and prove the "sent" transition for this exact
	// request was durably recorded. This is only possible if gwStore was
	// still open (not yet Close()'d) at the moment the handler's
	// TransitionSent call ran, which happens strictly after the fake peer
	// released the send -- i.e. strictly after the old 5s grace would have
	// already caused a close-out-from-under-the-handler.
	journalPath := filepath.Join(h.dir, "state", "timingplay", "timingplay.jsonl")
	data, readErr := os.ReadFile(journalPath)
	if readErr != nil {
		t.Fatalf("read timingplay journal %s: %v", journalPath, readErr)
	}
	journal := string(data)
	if !strings.Contains(journal, `"request_id":"req-slow-1"`) {
		t.Fatalf("journal does not mention req-slow-1 at all:\n%s", journal)
	}
	if !strings.Contains(journal, `"op":"transition"`) || !strings.Contains(journal, `"state":"sent"`) {
		t.Fatalf("journal has no durable 'sent' transition -- the outcome was lost (store closed before the handler could record it):\n%s", journal)
	}
}
