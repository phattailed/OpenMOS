package server

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"airshift/openmos/internal/events"
	"airshift/openmos/internal/model"
	"airshift/openmos/internal/service"
	mosxml "airshift/openmos/internal/xml"

	"nhooyr.io/websocket"
)

type blockedRunningOrders struct {
	*memoryRunningOrders
	entered chan struct{}
	release chan struct{}
	creates atomic.Int32
}

func (r *blockedRunningOrders) Create(ctx context.Context, value *model.RunningOrder) (*model.RunningOrder, error) {
	r.creates.Add(1)
	select {
	case r.entered <- struct{}{}:
	default:
	}
	<-r.release
	return r.memoryRunningOrders.Create(ctx, value)
}

func TestReconnectDuringPersistenceReplaysOneROAck(t *testing.T) {
	cfg := testConfig()
	roRepo := &blockedRunningOrders{memoryRunningOrders: newMemoryRunningOrders(), entered: make(chan struct{}, 1), release: make(chan struct{})}
	svc := service.NewMOSService(roRepo, newMemoryStories(), newMemoryItems(), nil, events.NewEventBus())
	srv := NewWSServer(cfg, svc, NewMemoryDedupStore())
	serverCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(serverCtx) }()
	url := fmt.Sprintf("ws://%s/mos?mosID=OPENMOS_TEST&ncsID=NCS_001&channel=ro", srv.Addr())
	ctx, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	first, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(websocket.StatusNormalClosure, "")
	request := mosxml.WrapEnvelope("OPENMOS_TEST", "NCS_001", "41", []byte(`<roCreate><roID>RO-RACE</roID><roSlug>Sample</roSlug></roCreate>`))
	encoded, err := mosxml.EncodeUCS2BE(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Write(ctx, websocket.MessageBinary, encoded); err != nil {
		t.Fatal(err)
	}
	select {
	case <-roRepo.entered:
	case <-ctx.Done():
		t.Fatal("first persistence did not start")
	}
	second, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(websocket.StatusNormalClosure, "")
	if err := second.Write(ctx, websocket.MessageBinary, encoded); err != nil {
		t.Fatal(err)
	}
	// Let the second session reach the in-flight retry before releasing storage.
	time.Sleep(20 * time.Millisecond)
	close(roRepo.release)
	_, reply, err := second.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reply, err = mosxml.DecodeUCS2BE(reply)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reply), "<roAck>") || !strings.Contains(string(reply), "<roStatus>OK</roStatus>") {
		t.Fatalf("retry response = %s, want successful roAck", reply)
	}
	if got := roRepo.value("RO-RACE"); got == nil || got.Version != 1 || roRepo.creates.Load() != 1 {
		t.Fatalf("overlapping retry applied twice: RO=%+v creates=%d", got, roRepo.creates.Load())
	}
}

// TestReconnectNoDuplicate proves that the ro channel resumes without duplicating
// accepted work when the same NCS reconnects.
func TestReconnectNoDuplicate(t *testing.T) {
	cfg := testConfig()
	roRepo := newMemoryRunningOrders()
	storyRepo := newMemoryStories()
	itemRepo := newMemoryItems()
	eventBus := events.NewEventBus()
	svc := service.NewMOSService(roRepo, storyRepo, itemRepo, nil, eventBus)
	dedup := NewMemoryDedupStore()

	srv := NewWSServer(cfg, svc, dedup)
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
	if string(replay) != string(data) {
		t.Fatal("retried request did not replay the original response bytes")
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
