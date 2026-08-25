package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"airshift/openmos/internal/config"
	"airshift/openmos/internal/events"
	"airshift/openmos/internal/repository"
	"airshift/openmos/internal/service"
	mosxml "airshift/openmos/internal/xml"

	"nhooyr.io/websocket"
)

// testServer creates a running WSServer for tests and returns its base URL.
func testServer(t *testing.T) (*WSServer, string, context.CancelFunc) {
	t.Helper()
	cfg := testConfig()
	eventBus := events.NewEventBus()
	svc := testService()
	dedup := NewMemoryDedupStore()

	srv := NewWSServer(cfg, svc, eventBus, dedup)
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		_ = srv.Start(ctx)
	}()

	// Addr() blocks until the listener is ready
	addr := srv.Addr()
	url := fmt.Sprintf("ws://%s", addr.String())
	return srv, url, cancel
}

func testConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.ReadTimeout = 5 * time.Second
	cfg.Server.WriteTimeout = 5 * time.Second
	cfg.Server.ShutdownTimeout = 5 * time.Second
	cfg.WebSocket.Port = 0 // Use port 0 for random available port
	cfg.MOS.ID = "OPENMOS_TEST"
	cfg.MOS.NCSID = "" // Accept any ncsID by default
	cfg.MOS.HeartbeatInterval = 30 * time.Second
	cfg.MOS.ClientTimeout = 10 * time.Second
	cfg.MOS.Manufacturer = "Test"
	cfg.MOS.Model = "TestModel"
	cfg.MOS.HwRev = "1.0"
	cfg.MOS.SwRev = "1.0.0"
	cfg.MOS.DOM = "2024-01-01"
	cfg.MOS.SN = "TEST-001"
	return cfg
}

func testService() *service.MOSService {
	roRepo := repository.NewMemoryRunningOrderRepository()
	storyRepo := repository.NewMemoryStoryRepository()
	itemRepo := repository.NewMemoryItemRepository()
	objRepo := repository.NewMemoryObjectRepository()
	eventBus := events.NewEventBus()
	return service.NewMOSService(roRepo, storyRepo, itemRepo, objRepo, eventBus)
}

// TestWSValidConnection tests that a connection with valid params succeeds.
func TestWSValidConnection(t *testing.T) {
	_, baseURL, cancel := testServer(t)
	defer cancel()

	ctx, ctxCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer ctxCancel()

	url := baseURL + "/mos?mosID=OPENMOS_TEST&ncsID=NCS_001&channel=ro"
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
}

// TestWSRejectWrongMosID tests that wrong mosID is rejected.
func TestWSRejectWrongMosID(t *testing.T) {
	_, baseURL, cancel := testServer(t)
	defer cancel()

	ctx, ctxCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer ctxCancel()

	url := baseURL + "/mos?mosID=WRONG_ID&ncsID=NCS_001&channel=ro"
	_, resp, err := websocket.Dial(ctx, url, nil)
	if err == nil {
		t.Fatal("expected connection to be rejected")
	}
	if resp != nil && resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", resp.StatusCode)
	}
}

// TestWSRejectEmptyNcsID tests that empty ncsID is rejected.
func TestWSRejectEmptyNcsID(t *testing.T) {
	_, baseURL, cancel := testServer(t)
	defer cancel()

	ctx, ctxCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer ctxCancel()

	url := baseURL + "/mos?mosID=OPENMOS_TEST&ncsID=&channel=ro"
	_, resp, err := websocket.Dial(ctx, url, nil)
	if err == nil {
		t.Fatal("expected connection to be rejected")
	}
	if resp != nil && resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", resp.StatusCode)
	}
}

// TestWSRejectWrongChannel tests that non-ro channel is rejected.
func TestWSRejectWrongChannel(t *testing.T) {
	_, baseURL, cancel := testServer(t)
	defer cancel()

	ctx, ctxCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer ctxCancel()

	url := baseURL + "/mos?mosID=OPENMOS_TEST&ncsID=NCS_001&channel=media"
	_, resp, err := websocket.Dial(ctx, url, nil)
	if err == nil {
		t.Fatal("expected connection to be rejected")
	}
	if resp != nil && resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", resp.StatusCode)
	}
}

// TestWSRejectMissingChannel tests that missing channel is rejected.
func TestWSRejectMissingChannel(t *testing.T) {
	_, baseURL, cancel := testServer(t)
	defer cancel()

	ctx, ctxCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer ctxCancel()

	url := baseURL + "/mos?mosID=OPENMOS_TEST&ncsID=NCS_001"
	_, resp, err := websocket.Dial(ctx, url, nil)
	if err == nil {
		t.Fatal("expected connection to be rejected")
	}
	if resp != nil && resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", resp.StatusCode)
	}
}

// TestWSKeepAliveNoResponse proves keepAlive produces no response.
func TestWSKeepAliveNoResponse(t *testing.T) {
	_, baseURL, cancel := testServer(t)
	defer cancel()

	ctx, ctxCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer ctxCancel()

	url := baseURL + "/mos?mosID=OPENMOS_TEST&ncsID=NCS_001&channel=ro"
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Send keepAlive in envelope
	keepAliveMsg := mosxml.WrapEnvelope("OPENMOS_TEST", "NCS_001", "ka-001", []byte("<keepAlive/>"))
	err = conn.Write(ctx, websocket.MessageText, keepAliveMsg)
	if err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	// Wait briefly and verify no response comes back.
	// Use a sleep rather than a read with timeout context, because
	// nhooyr.io/websocket closes the connection when the read context expires.
	time.Sleep(300 * time.Millisecond)

	// Verify the connection is still alive by sending reqMachInfo
	reqMsg := mosxml.WrapEnvelope("OPENMOS_TEST", "NCS_001", "verify-alive", []byte("<reqMachInfo/>"))
	err = conn.Write(ctx, websocket.MessageText, reqMsg)
	if err != nil {
		t.Fatalf("connection died after keepAlive (write failed): %v", err)
	}
	respType, respData, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("connection died after keepAlive (read failed): %v", err)
	}
	if respType != websocket.MessageBinary {
		t.Errorf("expected binary response frame, got %v", respType)
	}
	respXML, err := mosxml.DecodeUCS2BE(respData)
	if err != nil {
		t.Fatalf("failed to decode UCS-2BE response: %v", err)
	}
	if !strings.Contains(string(respXML), "listMachInfo") {
		t.Errorf("expected listMachInfo response, got: %s", string(respXML))
	}
}

// TestWSReqMachInfo proves reqMachInfo returns correct listMachInfo with only Profile 0.
func TestWSReqMachInfo(t *testing.T) {
	_, baseURL, cancel := testServer(t)
	defer cancel()

	ctx, ctxCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer ctxCancel()

	url := baseURL + "/mos?mosID=OPENMOS_TEST&ncsID=NCS_001&channel=ro"
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Send reqMachInfo
	reqMsg := mosxml.WrapEnvelope("OPENMOS_TEST", "NCS_001", "mach-001", []byte("<reqMachInfo/>"))
	err = conn.Write(ctx, websocket.MessageText, reqMsg)
	if err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	// Read response (emitted as a UCS-2BE binary frame)
	respType, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}
	if respType != websocket.MessageBinary {
		t.Errorf("expected binary response frame, got %v", respType)
	}
	data, err = mosxml.DecodeUCS2BE(data)
	if err != nil {
		t.Fatalf("failed to decode UCS-2BE response: %v", err)
	}

	// Parse the response envelope
	env, msg, _, err := mosxml.ParseEnvelope(data)
	if err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if env.MessageID != "mach-001" {
		t.Errorf("response messageID = %q, want %q", env.MessageID, "mach-001")
	}

	listMach, ok := msg.(mosxml.ListMachInfo)
	if !ok {
		t.Fatalf("expected ListMachInfo, got %T", msg)
	}
	if listMach.MosRev != "4.0.0" {
		t.Errorf("mosRev = %q, want %q", listMach.MosRev, "4.0.0")
	}
	if listMach.Manufacturer != "Test" {
		t.Errorf("manufacturer = %q, want %q", listMach.Manufacturer, "Test")
	}

	// Verify only Profile 0 is true
	for _, p := range listMach.SupportedProfiles.Profiles {
		if p.Number == 0 && !p.Value {
			t.Error("Profile 0 should be true")
		}
		if p.Number > 0 && p.Value {
			t.Errorf("Profile %d should be false, but is true", p.Number)
		}
	}
}

// TestWSBinaryFrameExchange proves the server accepts a binary UCS-2BE frame
// (MOS 4.0 §2.1) and replies with a binary UCS-2BE frame, never text. This is
// the exact frame discipline a live ENPS MOS 4 endpoint requires.
func TestWSBinaryFrameExchange(t *testing.T) {
	_, baseURL, cancel := testServer(t)
	defer cancel()

	ctx, ctxCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer ctxCancel()

	url := baseURL + "/mos?mosID=OPENMOS_TEST&ncsID=NCS_001&channel=ro"
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Send reqMachInfo as a UCS-2BE binary frame.
	reqUTF8 := mosxml.WrapEnvelope("OPENMOS_TEST", "NCS_001", "bin-001", []byte("<reqMachInfo/>"))
	reqBinary, err := mosxml.EncodeUCS2BE(reqUTF8)
	if err != nil {
		t.Fatalf("failed to encode request: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, reqBinary); err != nil {
		t.Fatalf("failed to write binary frame: %v", err)
	}

	// Response must be a binary frame carrying UCS-2BE.
	respType, respData, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}
	if respType != websocket.MessageBinary {
		t.Fatalf("expected binary response frame, got %v", respType)
	}

	respXML, err := mosxml.DecodeUCS2BE(respData)
	if err != nil {
		t.Fatalf("failed to decode UCS-2BE response: %v", err)
	}

	env, msg, _, err := mosxml.ParseEnvelope(respXML)
	if err != nil {
		t.Fatalf("failed to parse response envelope: %v", err)
	}
	if env.MessageID != "bin-001" {
		t.Errorf("response messageID = %q, want %q", env.MessageID, "bin-001")
	}
	if _, ok := msg.(mosxml.ListMachInfo); !ok {
		t.Fatalf("expected ListMachInfo, got %T", msg)
	}
}
