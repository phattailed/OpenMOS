package server

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"airshift/openmos/internal/events"
	"airshift/openmos/internal/service"
	mosxml "airshift/openmos/internal/xml"

	"nhooyr.io/websocket"
)

// TestWSMessageIDConflict verifies that same messageID with different content is rejected.
func TestWSMessageIDConflict(t *testing.T) {
	cfg := testConfig()
	eventBus := events.NewEventBus()
	roRepo := newMemoryRunningOrders()
	storyRepo := newMemoryStories()
	itemRepo := newMemoryItems()
	svc := service.NewMOSService(roRepo, storyRepo, itemRepo, nil, eventBus)
	dedup := NewMemoryDedupStore()

	srv := NewWSServer(cfg, svc, dedup)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Start(ctx) }()
	addr := srv.Addr() // blocks until ready

	url := fmt.Sprintf("ws://%s/mos?mosID=OPENMOS_TEST&ncsID=NCS_001&channel=ro", addr.String())
	connCtx, connCancel := context.WithTimeout(ctx, 3*time.Second)
	defer connCancel()

	conn, _, err := websocket.Dial(connCtx, url, nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// First message
	msg1 := mosxml.WrapEnvelope("OPENMOS_TEST", "NCS_001", "conflict-msg-1",
		[]byte(`<roCreate><roID>RO_A</roID><roSlug>First</roSlug></roCreate>`))
	err = conn.Write(connCtx, websocket.MessageText, msg1)
	if err != nil {
		t.Fatalf("write 1 failed: %v", err)
	}
	_, _, err = conn.Read(connCtx) // roAck
	if err != nil {
		t.Fatalf("read 1 failed: %v", err)
	}

	// Second message with same messageID but different content
	msg2 := mosxml.WrapEnvelope("OPENMOS_TEST", "NCS_001", "conflict-msg-1",
		[]byte(`<roCreate><roID>RO_B</roID><roSlug>Different</roSlug></roCreate>`))
	err = conn.Write(connCtx, websocket.MessageText, msg2)
	if err != nil {
		t.Fatalf("write 2 failed: %v", err)
	}

	// Should get a NACK (emitted as a UCS-2BE binary frame)
	_, data, err := conn.Read(connCtx)
	if err != nil {
		t.Fatalf("read 2 failed: %v", err)
	}
	data, err = mosxml.DecodeUCS2BE(data)
	if err != nil {
		t.Fatalf("decode 2 failed: %v", err)
	}
	if !strings.Contains(string(data), "NACK") {
		t.Errorf("expected NACK, got: %s", string(data))
	}
	if !strings.Contains(string(data), "<roAck>") || !strings.Contains(string(data), "<roID>RO_B</roID>") {
		t.Errorf("expected a running-order ACK for RO_B, got: %s", data)
	}
	if !strings.Contains(string(data), "conflict") {
		t.Errorf("expected conflict message, got: %s", string(data))
	}
	ros, err := roRepo.List(context.Background())
	if err != nil || len(ros) != 1 {
		t.Fatalf("conflicting request changed stored running orders: count=%d err=%v", len(ros), err)
	}
}
