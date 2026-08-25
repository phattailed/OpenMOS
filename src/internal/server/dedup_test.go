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

func TestDedupStore_New(t *testing.T) {
	store := NewMemoryDedupStore()
	result := store.Check("NCS_001", "msg-1", []byte("content-A"))
	if result != DedupNew {
		t.Errorf("expected DedupNew, got %v", result)
	}
}

func TestDedupStore_Duplicate(t *testing.T) {
	store := NewMemoryDedupStore()
	store.Check("NCS_001", "msg-1", []byte("content-A"))
	result := store.Check("NCS_001", "msg-1", []byte("content-A"))
	if result != DedupDuplicate {
		t.Errorf("expected DedupDuplicate, got %v", result)
	}
}

func TestDedupStore_Conflict(t *testing.T) {
	store := NewMemoryDedupStore()
	store.Check("NCS_001", "msg-1", []byte("content-A"))
	result := store.Check("NCS_001", "msg-1", []byte("content-B"))
	if result != DedupConflict {
		t.Errorf("expected DedupConflict, got %v", result)
	}
}

func TestDedupStore_DifferentMessageIDs(t *testing.T) {
	store := NewMemoryDedupStore()
	store.Check("NCS_001", "msg-1", []byte("content-A"))
	result := store.Check("NCS_001", "msg-2", []byte("content-A"))
	if result != DedupNew {
		t.Errorf("expected DedupNew for different messageID, got %v", result)
	}
}

func TestDedupStore_DifferentNcsIDs(t *testing.T) {
	store := NewMemoryDedupStore()
	store.Check("NCS_001", "msg-1", []byte("content-A"))
	result := store.Check("NCS_002", "msg-1", []byte("content-B"))
	if result != DedupNew {
		t.Errorf("expected DedupNew for different ncsID, got %v", result)
	}
}

// TestWSDuplicateRoCreate verifies that sending the same roCreate twice only persists once.
func TestWSDuplicateRoCreate(t *testing.T) {
	cfg := testConfig()
	eventBus := events.NewEventBus()
	roRepo := repository.NewMemoryRunningOrderRepository()
	storyRepo := repository.NewMemoryStoryRepository()
	itemRepo := repository.NewMemoryItemRepository()
	objRepo := repository.NewMemoryObjectRepository()
	svc := service.NewMOSService(roRepo, storyRepo, itemRepo, objRepo, eventBus)
	dedup := NewMemoryDedupStore()

	srv := NewWSServer(cfg, svc, eventBus, dedup)
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

	// Build roCreate envelope
	roCreateXML := `<roCreate><roID>RO_DEDUP</roID><roSlug>Dedup Test</roSlug></roCreate>`
	msg := mosxml.WrapEnvelope("OPENMOS_TEST", "NCS_001", "dedup-msg-1", []byte(roCreateXML))

	// Send first time
	err = conn.Write(connCtx, websocket.MessageText, msg)
	if err != nil {
		t.Fatalf("write 1 failed: %v", err)
	}

	// Read roAck (emitted as a UCS-2BE binary frame)
	_, data, err := conn.Read(connCtx)
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

	// Send same message again (duplicate)
	err = conn.Write(connCtx, websocket.MessageText, msg)
	if err != nil {
		t.Fatalf("write 2 failed: %v", err)
	}

	// Should get no response (silently discarded).
	// Sleep briefly to confirm no response arrives, rather than using a read
	// context timeout which would close the connection in nhooyr.io/websocket.
	time.Sleep(300 * time.Millisecond)

	// Verify only one RO exists
	ros, _ := roRepo.List(context.Background())
	if len(ros) != 1 {
		t.Errorf("expected 1 running order, got %d", len(ros))
	}
}

// TestWSMessageIDConflict verifies that same messageID with different content is rejected.
func TestWSMessageIDConflict(t *testing.T) {
	cfg := testConfig()
	eventBus := events.NewEventBus()
	roRepo := repository.NewMemoryRunningOrderRepository()
	storyRepo := repository.NewMemoryStoryRepository()
	itemRepo := repository.NewMemoryItemRepository()
	objRepo := repository.NewMemoryObjectRepository()
	svc := service.NewMOSService(roRepo, storyRepo, itemRepo, objRepo, eventBus)
	dedup := NewMemoryDedupStore()

	srv := NewWSServer(cfg, svc, eventBus, dedup)
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
	if !strings.Contains(string(data), "conflict") {
		t.Errorf("expected conflict message, got: %s", string(data))
	}
}
