package server

import (
	"airshift/openmos/internal/config"
	"airshift/openmos/internal/events"
	"airshift/openmos/internal/service"
	"context"
	"sync"
	"testing"
	"time"
)

// Shutdown is called twice on every ordinary shutdown, so it has to be idempotent.
//
// This was reported from a real run: "TCPServer.Shutdown panics with close of closed channel".
// It is not an edge case and not intermittent in cause -- it is structural. TCPServer.Start ends
// with:
//
//	<-ctx.Done()
//	return s.Shutdown(context.Background())
//
// and main does:
//
//	cancel()                                  // Start's <-ctx.Done() fires, Shutdown call one
//	tcpServer.Shutdown(context.Background())  // Shutdown call two
//
// So both paths run every time and race to close the same channel. WSServer had the same shape
// with a select-and-default guard, which narrows the window without closing it: two callers can
// both observe the channel open and both proceed to close it.
//
// These tests call Shutdown twice, and then many times concurrently. Under -race the concurrent
// case is what catches a guard that merely looks correct.

func TestTCPServerShutdownIsIdempotent(t *testing.T) {
	srv, _ := newTestTCPServer(t)

	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("first shutdown: %v", err)
	}
	// The second call is what main makes after Start has already returned through Shutdown.
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Errorf("second shutdown returned %v; it must be safe, since it happens on every run", err)
	}
}

func TestTCPServerShutdownIsSafeConcurrently(t *testing.T) {
	srv, _ := newTestTCPServer(t)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = srv.Shutdown(context.Background())
		}()
	}
	wg.Wait() // a panic in any goroutine fails the test process
}

func TestWSServerShutdownIsSafeConcurrently(t *testing.T) {
	srv, _, cancel := testServer(t)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv.Shutdown()
		}()
	}
	wg.Wait()
}

// TestShutdownAfterStartCancellationDoesNotPanic reproduces the reported sequence end to end
// rather than by calling Shutdown twice directly: cancel the context Start is running under, let
// Start return through its own Shutdown, then call Shutdown from the outside as main does.
func TestShutdownAfterStartCancellationDoesNotPanic(t *testing.T) {
	srv, _ := newTestTCPServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- srv.Start(ctx)
	}()
	<-started
	time.Sleep(50 * time.Millisecond) // let Start reach its wait

	cancel() // Start returns through Shutdown

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not return after its context was cancelled")
	}

	// And now the call main makes.
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Errorf("shutdown after Start already shut down returned %v", err)
	}
}

// newTestTCPServer builds a TCP server on an ephemeral port, without starting it.
func newTestTCPServer(t *testing.T) (*TCPServer, *config.Config) {
	t.Helper()

	runningOrders := newMemoryRunningOrders()
	stories := newMemoryStories()
	items := newMemoryItems()
	eventBus := events.NewEventBus()
	mosService := service.NewMOSService(runningOrders, stories, items, nil, eventBus)

	cfg := &config.Config{}
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = 0
	cfg.Server.WriteTimeout = time.Second
	cfg.Server.ShutdownTimeout = time.Second
	cfg.MOS.ID = "openmos.example.mos"
	cfg.MOS.NCSID = "example.ncs"
	cfg.MOS.HeartbeatInterval = time.Minute
	cfg.MOS.ClientTimeout = time.Minute

	srv, err := NewTCPServer(cfg, mosService, eventBus, nil)
	if err != nil {
		t.Fatalf("NewTCPServer: %v", err)
	}
	return srv, cfg
}
