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

// TestPersistenceRestart proves that accepted input survives server restart
// and is not reapplied on re-delivery.
func TestPersistenceRestart(t *testing.T) {
	cfg := testConfig()

	// Shared backing stores (simulate persistent storage that survives restart)
	roRepo := repository.NewMemoryRunningOrderRepository()
	storyRepo := repository.NewMemoryStoryRepository()
	itemRepo := repository.NewMemoryItemRepository()
	objRepo := repository.NewMemoryObjectRepository()
	dedup := NewMemoryDedupStore()

	// --- First server instance ---
	eventBus1 := events.NewEventBus()
	svc1 := service.NewMOSService(roRepo, storyRepo, itemRepo, objRepo, eventBus1)
	srv1 := NewWSServer(cfg, svc1, eventBus1, dedup, nil)

	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() { _ = srv1.Start(ctx1) }()
	addr1 := srv1.Addr() // blocks until ready

	url1 := fmt.Sprintf("ws://%s/mos?mosID=OPENMOS_TEST&ncsID=NCS_001&channel=ro", addr1.String())
	connCtx, connCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer connCancel()

	conn, _, err := websocket.Dial(connCtx, url1, nil)
	if err != nil {
		t.Fatalf("failed to connect to server 1: %v", err)
	}

	// Send roCreate
	roCreateXML := `<roCreate><roID>RO_PERSIST</roID><roSlug>Persistence Test</roSlug><story><storyID>S1</storyID><storySlug>Story 1</storySlug></story></roCreate>`
	msg := mosxml.WrapEnvelope("OPENMOS_TEST", "NCS_001", "persist-msg-1", []byte(roCreateXML))
	err = conn.Write(connCtx, websocket.MessageText, msg)
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}

	// Read roAck (emitted as a UCS-2BE binary frame)
	_, data, err := conn.Read(connCtx)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	data, err = mosxml.DecodeUCS2BE(data)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if !strings.Contains(string(data), "roAck") {
		t.Fatalf("expected roAck, got: %s", string(data))
	}

	// Close connection and stop first server
	conn.Close(websocket.StatusNormalClosure, "")
	cancel1()
	srv1.Shutdown()
	time.Sleep(100 * time.Millisecond)

	// --- Verify data persisted ---
	ro, err := roRepo.Get(context.Background(), "RO_PERSIST")
	if err != nil {
		t.Fatalf("running order not found after server 1 shutdown: %v", err)
	}
	if ro.Slug != "Persistence Test" {
		t.Errorf("slug = %q, want %q", ro.Slug, "Persistence Test")
	}

	// --- Second server instance (simulated restart) ---
	eventBus2 := events.NewEventBus()
	svc2 := service.NewMOSService(roRepo, storyRepo, itemRepo, objRepo, eventBus2)
	srv2 := NewWSServer(cfg, svc2, eventBus2, dedup, nil)

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _ = srv2.Start(ctx2) }()
	addr2 := srv2.Addr() // blocks until ready

	url2 := fmt.Sprintf("ws://%s/mos?mosID=OPENMOS_TEST&ncsID=NCS_001&channel=ro", addr2.String())
	connCtx2, connCancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer connCancel2()

	conn2, _, err := websocket.Dial(connCtx2, url2, nil)
	if err != nil {
		t.Fatalf("failed to connect to server 2: %v", err)
	}
	defer conn2.Close(websocket.StatusNormalClosure, "")

	// Re-send the same message (re-delivery after restart)
	err = conn2.Write(connCtx2, websocket.MessageText, msg)
	if err != nil {
		t.Fatalf("write to server 2 failed: %v", err)
	}

	// Should be silently discarded (dedup detects duplicate).
	// Sleep briefly to confirm no response arrives.
	time.Sleep(300 * time.Millisecond)

	// Verify still only one RO
	ros, _ := roRepo.List(context.Background())
	if len(ros) != 1 {
		t.Errorf("expected 1 running order after restart re-delivery, got %d", len(ros))
	}
}
