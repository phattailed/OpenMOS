package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"airshift/openmos/internal/config"
	"airshift/openmos/internal/events"
	"airshift/openmos/internal/service"
	"airshift/openmos/pkg/logger"
)

// TCPServer represents the TCP socket server
type TCPServer struct {
	listener     net.Listener
	clients      map[string]*ClientConnection
	clientsMu    sync.RWMutex
	service      *service.MOSService
	config       *config.Config
	eventBus     *events.EventBus
	wg           sync.WaitGroup
	shutdownCh   chan struct{}
	shutdownOnce sync.Once
	// dedup makes retried messageIDs idempotent. Shared across all connections
	// so a retry that arrives on a reconnected socket is still recognised --
	// which is the usual case, since the spec has the NCS reset the connection
	// before retrying.
	dedup *MemoryDedupStore
	// ponytail: serialize writes until their replay receipts exist; use per-ID
	// locks only if concurrent running-order throughput becomes necessary.
	roMu sync.Mutex
}

// NewTCPServer creates a new TCP server instance
func NewTCPServer(cfg *config.Config, mosService *service.MOSService, eventBus *events.EventBus) (*TCPServer, error) {
	address := cfg.GetServerAddress()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("failed to create listener on %s: %w", address, err)
	}

	server := &TCPServer{
		listener:   listener,
		dedup:      NewMemoryDedupStore(),
		clients:    make(map[string]*ClientConnection),
		service:    mosService,
		config:     cfg,
		eventBus:   eventBus,
		shutdownCh: make(chan struct{}),
	}

	return server, nil
}

// Start begins accepting connections
func (s *TCPServer) Start(ctx context.Context) error {

	address := s.listener.Addr().String()
	logger.Infof("Server listening on %s", address)

	// Accept connections in a loop
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.shutdownCh:
				return
			default:
				// Set accept timeout so we can check for shutdown
				tcpListener, ok := s.listener.(*net.TCPListener)
				if ok {
					tcpListener.SetDeadline(time.Now().Add(time.Second))
				}

				conn, err := s.listener.Accept()
				if err != nil {
					if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
						return
					}
					if opErr, ok := err.(*net.OpError); ok && opErr.Timeout() {
						// This is just a timeout from our deadline, continue
						continue
					}
					logger.Errorf("Error accepting connection: %v", err)
					continue
				}

				// Create new client connection
				client := NewClientConnection(conn, s, s.config)

				// Register client
				s.registerClient(client)

				// Handle client in a goroutine
				s.wg.Add(1)
				go func() {
					defer s.wg.Done()
					client.Start(ctx)
				}()
			}
		}
	}()

	<-ctx.Done()
	return nil
}

// Shutdown gracefully shuts down the server
func (s *TCPServer) Shutdown(ctx context.Context) error {
	logger.Info("Shutting down server...")

	// Signal all goroutines to stop
	s.shutdownOnce.Do(func() { close(s.shutdownCh) })

	// Close listener
	if s.listener != nil {
		s.listener.Close()
	}

	// Close all client connections
	s.clientsMu.RLock()
	clients := make([]*ClientConnection, 0, len(s.clients))
	for _, client := range s.clients {
		clients = append(clients, client)
	}
	s.clientsMu.RUnlock()
	for _, client := range clients {
		client.Close()
	}

	// Wait for all goroutines to finish with a timeout
	shutdownCtx, cancel := context.WithTimeout(ctx, s.config.Server.ShutdownTimeout)
	defer cancel()

	// Create a channel to signal when all goroutines have finished
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	// Wait for either the context to be canceled or all goroutines to finish
	select {
	case <-shutdownCtx.Done():
		return fmt.Errorf("server shutdown timed out")
	case <-done:
		logger.Info("Server shutdown complete")
		return nil
	}
}

// registerClient registers a client connection
func (s *TCPServer) registerClient(client *ClientConnection) {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()

	s.clients[client.ID()] = client
	logger.Infof("Client registered: %s", client.ID())
}

// unregisterClient removes a client connection
func (s *TCPServer) unregisterClient(clientID string) {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()

	delete(s.clients, clientID)
	logger.Infof("Client unregistered: %s", clientID)
}

// GetClient returns a client connection by ID
func (s *TCPServer) GetClient(clientID string) (*ClientConnection, bool) {
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()

	client, ok := s.clients[clientID]
	return client, ok
}

// BroadcastMessage sends a message to all connected clients
func (s *TCPServer) BroadcastMessage(data []byte) {
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()

	for _, client := range s.clients {
		// Send in a non-blocking way to avoid one slow client affecting others
		go func(c *ClientConnection) {
			if err := c.Write(data); err != nil {
				logger.Errorf("Error broadcasting to client %s: %v", c.ID(), err)
			}
		}(client)
	}
}
