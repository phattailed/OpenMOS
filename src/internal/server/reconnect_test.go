package server

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"airshift/openmos/internal/events"
	"airshift/openmos/internal/repository"
	"airshift/openmos/internal/service"
	mosxml "airshift/openmos/internal/xml"

	"nhooyr.io/websocket"
)

// TestReconnectNoDuplicate proves that the ro channel resumes without duplicating
// accepted work when the same NCS reconnects.
func TestReconnectNoDuplicate(t *testing.T) {
	cfg := testConfig()
	roRepo := repository.NewMemoryRunningOrderRepository()
	storyRepo := repository.NewMemoryStoryRepository()
	itemRepo := repository.NewMemoryItemRepository()
	objRepo := repository.NewMemoryObjectRepository()
	eventBus := events.NewEventBus()
	svc := service.NewMOSService(roRepo, storyRepo, itemRepo, objRepo, eventBus)
	dedup := NewMemoryDedupStore()

	srv := NewWSServer(cfg, svc, eventBus, dedup)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Start(ctx) }()
	addr := srv.Addr() // blocks until ready

	baseURL := fmt.Sprintf("ws://%s/mos?mosID=OPENMOS_TEST&ncsID=NCS_001&channel=ro", addr.String())

	// --- First connection ---
	connCtx1, connCancel1 := context.WithTimeout(ctx, 3*time.Second)
	defer connCancel1()
	conn1, _, err := websocket.Dial(connCtx1, baseURL, nil)
	if err != nil {
		t.Fatalf("connect 1 failed: %v", err)
	}

	// Send roCreate
	roCreateXML := `<roCreate><roID>RO_RECONNECT</roID><roSlug>Reconnect Test</roSlug></roCreate>`
	msg := mosxml.WrapEnvelope("OPENMOS_TEST", "NCS_001", "reconnect-msg-1", []byte(roCreateXML))
	err = conn1.Write(connCtx1, websocket.MessageText, msg)
	if err != nil {
		t.Fatalf("write 1 failed: %v", err)
	}

	// Read roAck (emitted as a UCS-2BE binary frame)
	_, data, err := conn1.Read(connCtx1)
	if err != nil {
		t.Fatalf("read 1 failed: %v", err)
	}
	data, err = mosxml.DecodeUCS2BE(data)
	if err != nil {
		t.Fatalf("decode 1 failed: %v", err)
	}
	if !strings.Contains(string(data), "roAck") {
		t.Fatalf("expected roAck, got: %s", string(data))
	}

	// Disconnect
	conn1.Close(websocket.StatusNormalClosure, "")
	time.Sleep(50 * time.Millisecond)

	// --- Reconnect ---
	connCtx2, connCancel2 := context.WithTimeout(ctx, 3*time.Second)
	defer connCancel2()
	conn2, _, err := websocket.Dial(connCtx2, baseURL, nil)
	if err != nil {
		t.Fatalf("connect 2 failed: %v", err)
	}
	defer conn2.Close(websocket.StatusNormalClosure, "")

	// Re-send same messageID (simulating NCS retransmit on reconnect)
	err = conn2.Write(connCtx2, websocket.MessageText, msg)
	if err != nil {
		t.Fatalf("write 2 failed: %v", err)
	}

	// A retry must be answered with the original ack, not with silence. The spec
	// has the sender retrying "at intervals until a response is received", so
	// discarding a re-delivery quietly would simply invite another retry.
	_, replay, err := conn2.Read(connCtx2)
	if err != nil {
		t.Fatalf("read of replayed ack failed: %v", err)
	}
	replay, err = mosxml.DecodeUCS2BE(replay)
	if err != nil {
		t.Fatalf("decode of replayed ack failed: %v", err)
	}
	if !strings.Contains(string(replay), "roAck") || !strings.Contains(string(replay), "RO_RECONNECT") {
		t.Fatalf("expected the original ack replayed for RO_RECONNECT, got: %s", string(replay))
	}

	// Send a NEW message to verify the connection is still functional
	newMsg := mosxml.WrapEnvelope("OPENMOS_TEST", "NCS_001", "reconnect-msg-2",
		[]byte(`<roCreate><roID>RO_RECONNECT_2</roID><roSlug>After Reconnect</roSlug></roCreate>`))
	newMsgBinary, err := mosxml.EncodeUCS2BE(newMsg)
	if err != nil {
		t.Fatalf("encode 3 failed: %v", err)
	}
	if err := conn2.Write(connCtx2, websocket.MessageBinary, newMsgBinary); err != nil {
		t.Fatalf("write 3 failed: %v", err)
	}
	_, data2, err := conn2.Read(connCtx2)
	if err != nil {
		t.Fatalf("read 3 failed: %v", err)
	}
	data2, err = mosxml.DecodeUCS2BE(data2)
	if err != nil {
		t.Fatalf("decode 3 failed: %v", err)
	}
	if !strings.Contains(string(data2), "roAck") {
		t.Fatalf("expected roAck for new message, got: %s", string(data2))
	}

	// Verify we have exactly 2 ROs (not duplicated)
	ros, _ := roRepo.List(context.Background())
	if len(ros) != 2 {
		t.Errorf("expected 2 running orders, got %d", len(ros))
	}
}
