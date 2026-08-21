package server

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"airshift/openmos/internal/config"
	"airshift/openmos/internal/events"
	"airshift/openmos/internal/service"
	mosxml "airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"

	"nhooyr.io/websocket"
)

// WSServer is a MOS 4 WebSocket server operating in passive mode.
// NCS peers connect to this server at ws://host:port/mos?mosID=X&ncsID=Y&channel=ro
type WSServer struct {
	config     *config.Config
	service    *service.MOSService
	eventBus   *events.EventBus
	dedup      DedupStore
	httpServer *http.Server
	listener   net.Listener
	ready      chan struct{} // closed once the listener is bound

	sessions   map[string]*WSSession
	sessionsMu sync.RWMutex

	shutdownCh chan struct{}
}

// WSSession represents an active WebSocket connection from an NCS.
type WSSession struct {
	conn      *websocket.Conn
	mosID     string
	ncsID     string
	channel   string
	closeChan chan struct{}
	closeOnce sync.Once
}

// NewWSServer creates a new WebSocket server.
func NewWSServer(cfg *config.Config, mosService *service.MOSService, eventBus *events.EventBus, dedup DedupStore) *WSServer {
	return &WSServer{
		config:     cfg,
		service:    mosService,
		eventBus:   eventBus,
		dedup:      dedup,
		sessions:   make(map[string]*WSSession),
		shutdownCh: make(chan struct{}),
		ready:      make(chan struct{}),
	}
}

// Start begins listening for WebSocket connections.
func (s *WSServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/mos", s.handleUpgrade)

	addr := s.config.GetWebSocketAddress()

	var ln net.Listener
	var err error

	if s.config.WebSocket.TLSCertFile != "" && s.config.WebSocket.TLSKeyFile != "" {
		cert, tlsErr := tls.LoadX509KeyPair(s.config.WebSocket.TLSCertFile, s.config.WebSocket.TLSKeyFile)
		if tlsErr != nil {
			return fmt.Errorf("failed to load TLS cert/key: %w", tlsErr)
		}
		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		ln, err = tls.Listen("tcp", addr, tlsConfig)
	} else {
		ln, err = net.Listen("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}
	s.listener = ln
	close(s.ready) // signal that listener is bound and Addr() is safe to call

	s.httpServer = &http.Server{
		Handler:     mux,
		ReadTimeout: s.config.Server.ReadTimeout,
	}

	logger.Infof("WebSocket server listening on %s", ln.Addr().String())

	go func() {
		<-ctx.Done()
		s.Shutdown()
	}()

	if err := s.httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Addr returns the listener address (useful for tests).
// Blocks until the listener is bound.
func (s *WSServer) Addr() net.Addr {
	<-s.ready
	return s.listener.Addr()
}

// Shutdown gracefully stops the server.
func (s *WSServer) Shutdown() {
	select {
	case <-s.shutdownCh:
		return // already closed
	default:
	}
	close(s.shutdownCh)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s.sessionsMu.Lock()
	for _, sess := range s.sessions {
		sess.close()
	}
	s.sessionsMu.Unlock()

	if s.httpServer != nil {
		_ = s.httpServer.Shutdown(ctx)
	}
}

// handleUpgrade validates the connection parameters and upgrades to WebSocket.
func (s *WSServer) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	mosID := q.Get("mosID")
	ncsID := q.Get("ncsID")
	channel := q.Get("channel")

	// Validate mosID matches our configured ID
	if mosID == "" || mosID != s.config.MOS.ID {
		http.Error(w, "invalid or missing mosID", http.StatusForbidden)
		return
	}

	// Validate ncsID is provided (and matches config if configured)
	if ncsID == "" {
		http.Error(w, "missing ncsID", http.StatusBadRequest)
		return
	}
	if s.config.MOS.NcsID != "" && ncsID != s.config.MOS.NcsID {
		http.Error(w, "ncsID not authorized", http.StatusForbidden)
		return
	}

	// Only channel=ro is supported
	if channel != "ro" {
		http.Error(w, "unsupported channel; only 'ro' is supported", http.StatusBadRequest)
		return
	}

	// NOTE: CORS/origin restriction is absent. websocket.AcceptOptions{} accepts
	// connections from any origin. This is acceptable for a lab slice but must be
	// restricted before any network-facing or production deployment.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		logger.Errorf("WebSocket upgrade failed: %v", err)
		return
	}

	sess := &WSSession{
		conn:      conn,
		mosID:     mosID,
		ncsID:     ncsID,
		channel:   channel,
		closeChan: make(chan struct{}),
	}

	sessionKey := ncsID + ":" + channel
	s.sessionsMu.Lock()
	// Close any existing session for this identity (reconnect case)
	if existing, ok := s.sessions[sessionKey]; ok {
		existing.close()
	}
	s.sessions[sessionKey] = sess
	s.sessionsMu.Unlock()

	logger.Infof("WebSocket session established: ncsID=%s channel=%s", ncsID, channel)

	// Handle messages in a goroutine
	go s.handleSession(sess)
}

// handleSession reads messages from a WebSocket session.
func (s *WSServer) handleSession(sess *WSSession) {
	defer func() {
		sess.close()
		s.sessionsMu.Lock()
		key := sess.ncsID + ":" + sess.channel
		if s.sessions[key] == sess {
			delete(s.sessions, key)
		}
		s.sessionsMu.Unlock()
		logger.Infof("WebSocket session closed: ncsID=%s", sess.ncsID)
	}()

	// Heartbeat monitor: if no message arrives within ClientTimeout, close.
	heartbeatTimer := time.NewTimer(s.config.MOS.ClientTimeout)
	defer heartbeatTimer.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Monitor shutdown
	go func() {
		select {
		case <-s.shutdownCh:
			cancel()
		case <-sess.closeChan:
			cancel()
		case <-ctx.Done():
		}
	}()

	// Feed incoming messages through a channel so the select can multiplex
	// reads with timer expiry and context cancellation without busy-spinning.
	type readResult struct {
		msgType websocket.MessageType
		data    []byte
		err     error
	}
	msgCh := make(chan readResult, 1)

	go func() {
		for {
			msgType, data, err := sess.conn.Read(ctx)
			select {
			case msgCh <- readResult{msgType, data, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeatTimer.C:
			logger.Infof("Session timed out (no messages): ncsID=%s", sess.ncsID)
			return
		case msg := <-msgCh:
			if msg.err != nil {
				// Connection closed or context canceled
				return
			}
			if msg.msgType != websocket.MessageText {
				continue // Ignore binary frames
			}

			// Reset heartbeat timer on any message
			if !heartbeatTimer.Stop() {
				select {
				case <-heartbeatTimer.C:
				default:
				}
			}
			heartbeatTimer.Reset(s.config.MOS.ClientTimeout)

			// Process the message
			s.processMessage(ctx, sess, msg.data)
		}
	}
}

// processMessage handles a single MOS message within its envelope.
func (s *WSServer) processMessage(ctx context.Context, sess *WSSession, data []byte) {
	env, msg, innerOpXML, err := mosxml.ParseEnvelope(data)
	if err != nil {
		logger.Errorf("Envelope parse error from ncsID=%s: %v", sess.ncsID, err)
		s.sendNack(ctx, sess, "", "NACK", "invalid envelope")
		return
	}

	if msg == nil {
		// Empty body envelope - nothing to process
		return
	}

	switch m := msg.(type) {
	case mosxml.KeepAlive:
		// MOS 4 Profile 0: keepAlive produces NO response.
		_ = m
		return

	case mosxml.ReqMachInfo:
		s.handleReqMachInfo(ctx, sess, env)
		return

	case mosxml.RunningOrderInfo:
		s.handleRoCreate(ctx, sess, env, m, innerOpXML)
		return

	default:
		// Log receipt of unhandled message type but do not echo raw XML
		logger.Infof("Received unhandled message type %s from ncsID=%s", msg.GetMessageType(), sess.ncsID)
	}
}

// handleReqMachInfo responds with listMachInfo.
func (s *WSServer) handleReqMachInfo(ctx context.Context, sess *WSSession, env *mosxml.MosEnvelope) {
	info := mosxml.CreateListMachInfo(s.config)
	innerXML, err := xml.Marshal(info)
	if err != nil {
		logger.Errorf("Failed to marshal listMachInfo: %v", err)
		return
	}

	response := mosxml.WrapEnvelope(s.config.MOS.ID, sess.ncsID, env.MessageID, innerXML)
	if err := sess.conn.Write(ctx, websocket.MessageText, response); err != nil {
		logger.Errorf("Write failed to ncsID=%s: %v", sess.ncsID, err)
		sess.close()
	}
}

// handleRoCreate processes a roCreate message with dedup and ack-after-persist.
func (s *WSServer) handleRoCreate(ctx context.Context, sess *WSSession, env *mosxml.MosEnvelope, roInfo mosxml.RunningOrderInfo, innerOpXML []byte) {
	// Check dedup using only the inner operation XML (not the full envelope),
	// so that re-deliveries with different envelope whitespace or ordering are
	// correctly identified as duplicates rather than conflicts.
	result := s.dedup.Check(env.NcsID, env.MessageID, innerOpXML)
	switch result {
	case DedupDuplicate:
		// Silently discard re-delivery
		logger.Infof("Duplicate message discarded: ncsID=%s messageID=%s", env.NcsID, env.MessageID)
		return
	case DedupConflict:
		// Reject: same messageID but different content
		logger.Errorf("Message-ID conflict: ncsID=%s messageID=%s", env.NcsID, env.MessageID)
		s.sendNack(ctx, sess, env.MessageID, "NACK", "messageID conflict: same ID with different content")
		return
	}

	// Persist via service layer
	err := s.service.ProcessRunningOrderInfo(ctx, roInfo)
	if err != nil {
		logger.Errorf("Failed to persist roCreate: %v", err)
		s.sendNack(ctx, sess, env.MessageID, "NACK", "persistence failure")
		return
	}

	// Send roAck only after successful persistence
	ack := mosxml.CreateROAck(roInfo.ID, "OK", nil)
	innerXML, err := xml.Marshal(ack)
	if err != nil {
		logger.Errorf("Failed to marshal roAck: %v", err)
		return
	}

	response := mosxml.WrapEnvelope(s.config.MOS.ID, sess.ncsID, env.MessageID, innerXML)
	if err := sess.conn.Write(ctx, websocket.MessageText, response); err != nil {
		logger.Errorf("Write failed to ncsID=%s: %v", sess.ncsID, err)
		sess.close()
	}
}

// sendNack sends a NACK response envelope.
func (s *WSServer) sendNack(ctx context.Context, sess *WSSession, messageID, status, description string) {
	ack := mosxml.MOSAck{
		Status:            status,
		StatusDescription: description,
	}
	innerXML, err := xml.Marshal(ack)
	if err != nil {
		return
	}
	response := mosxml.WrapEnvelope(s.config.MOS.ID, sess.ncsID, messageID, innerXML)
	if err := sess.conn.Write(ctx, websocket.MessageText, response); err != nil {
		logger.Errorf("Write failed to ncsID=%s: %v", sess.ncsID, err)
		sess.close()
	}
}

// close terminates a WebSocket session.
func (sess *WSSession) close() {
	sess.closeOnce.Do(func() {
		close(sess.closeChan)
		_ = sess.conn.Close(websocket.StatusNormalClosure, "session closed")
	})
}
