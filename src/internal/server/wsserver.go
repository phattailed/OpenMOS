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

	"airshift/openmos/internal/capture"
	"airshift/openmos/internal/config"
	"airshift/openmos/internal/events"
	"airshift/openmos/internal/service"
	mosxml "airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"

	"nhooyr.io/websocket"
)

// WSServer is a MOS 4 WebSocket server operating in standard mode: the peer
// initiates the connection to us, at
// ws://host:port/<path>?mosID=X&ncsID=Y&channel=mom|ro|aux
//
// This is deliberately not passive mode, despite an earlier comment here saying
// so. In MOS 4.0 passive mode is the inverse: a device behind a firewall opens an
// outbound client connection carrying passive=true, so the peer can reply through
// the hole punched in the initiator's firewall. That is implemented separately in
// WSClient, which is the side that dials out.
type WSServer struct {
	config   *config.Config
	service  *service.MOSService
	eventBus *events.EventBus
	dedup    DedupStore
	// resync rate-limits outbound roReq so pull recovery cannot loop, exactly as on the
	// socket transport. Separate from the TCP server's guard because the two transports
	// hold independent conversations with independent state.
	resync     *resyncGuard
	httpServer *http.Server
	listener   net.Listener
	ready      chan struct{} // closed once the listener is bound

	sessions   map[string]*WSSession
	sessionsMu sync.RWMutex

	shutdownCh chan struct{}
	// capture records raw frames when enabled; nil means off.
	capture *capture.Recorder
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
func NewWSServer(cfg *config.Config, mosService *service.MOSService, eventBus *events.EventBus, dedup DedupStore, frames *capture.Recorder) *WSServer {
	return &WSServer{
		config:     cfg,
		service:    mosService,
		eventBus:   eventBus,
		dedup:      dedup,
		resync:     newResyncGuard(),
		sessions:   make(map[string]*WSSession),
		shutdownCh: make(chan struct{}),
		ready:      make(chan struct{}),
		capture:    frames,
	}
}

// Start begins listening for WebSocket connections.
func (s *WSServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	// The endpoint path is site-specific and must not be hardcoded. MOS 4.0 §1
	// shows wss://<SERVERNAME>/mos/Communication, and our reference ENPS serves
	// its own endpoint at /MOS4NCS/ -- so a peer's path is whatever they publish,
	// and ours has to be configurable for them to point at it.
	//
	// Defaulted here as well as in config loading, because a Config built directly
	// (as tests do) would otherwise pass an empty pattern to HandleFunc, which
	// panics.
	path := s.config.WebSocket.Path
	if path == "" {
		path = "/mos"
	}
	mux.HandleFunc(path, s.handleUpgrade)

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
	if s.config.MOS.NCSID != "" && ncsID != s.config.MOS.NCSID {
		http.Error(w, "ncsID not authorized", http.StatusForbidden)
		return
	}

	// Accept every channel the MOS 4.0 spec defines. Standard mode opens one
	// connection per channel, so a peer may hold mom, ro and aux at once; sessions
	// are keyed on (ncsID, channel) below to keep them distinct.
	if !IsKnownChannel(channel) {
		http.Error(w, "unknown channel; expected mom, ro or aux", http.StatusBadRequest)
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
			// MOS 4.0 §2.1 mandates UCS-2 big-endian binary frames. ENPS
			// rejects text frames with InvalidMessageType. Accept binary
			// frames (decoding UCS-2BE to UTF-8) and, for tolerance, accept
			// text frames leniently by treating their bytes as UTF-8 XML.
			var data []byte
			switch msg.msgType {
			case websocket.MessageBinary:
				decoded, err := mosxml.DecodeUCS2BE(msg.data)
				if err != nil {
					logger.Errorf("Failed to decode UCS-2BE frame from ncsID=%s: %v", sess.ncsID, err)
					continue
				}
				data = decoded
			case websocket.MessageText:
				data = msg.data
			default:
				continue
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
			// Recorded before dispatch so frames we reject are captured too --
			// those are the ones most worth having. wireBytes is the frame as it
			// arrived, which preserves evidence of the encoding actually used.
			s.recordFrame(capture.Inbound, sess, data, len(msg.data), wireEncoding(msg.msgType))

			s.processMessage(ctx, sess, data)
		}
	}
}

// processMessage handles a single MOS message within its envelope.
func (s *WSServer) processMessage(ctx context.Context, sess *WSSession, data []byte) {
	env, msg, innerOpXML, err := mosxml.ParseEnvelope(data)
	if err != nil {
		// Say what was actually wrong. A bare "invalid envelope" gives a vendor
		// nothing to debug against, and since inbound messageID format is no longer
		// policed, the rejections that remain are real structural faults worth
		// naming precisely.
		//
		// ParseEnvelope returns a nil envelope on some paths, so echo the messageID
		// only when there is one, and bound the text: the error can embed
		// peer-supplied content and a NACK is not the place to reflect an arbitrary
		// amount of it.
		logger.Errorf("Envelope parse error from ncsID=%s: %v", sess.ncsID, err)
		echoID := ""
		if env != nil {
			echoID = env.MessageID
		}
		s.sendNack(ctx, sess, echoID, "NACK", truncateForNack("invalid envelope: "+err.Error()))
		return
	}

	if msg == nil {
		// Empty body envelope - nothing to process
		return
	}

	// Reject messages that arrived on the wrong channel. Channel selection is how
	// a MOS 4 peer signals intent, and the spec keeps traffic on the two ports
	// independent of each other, so honouring the separation matters.
	family := classifyMessage(msg)
	if ok, why := channelAccepts(sess.channel, family); !ok {
		logger.Errorf("Wrong channel from ncsID=%s: %s (%s)", sess.ncsID, msg.GetMessageType(), why)
		s.sendNack(ctx, sess, env.MessageID, "NACK", why)
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
		// Running-order handling is shared with the MOS 2.x transport. Previously this
		// transport NACKed everything except roCreate as unimplemented, which made the
		// "one shared message core" claim untrue for the entire Profile 2 family.
		if handled, err := dispatchRunningOrder(ctx, s.roDeps(),
			wsResponder{server: s, sess: sess, messageID: env.MessageID}, msg); handled {
			if err != nil {
				logger.Errorf("Failed to handle %s from ncsID=%s: %v",
					msg.GetMessageType(), sess.ncsID, err)
			}
			return
		}

		// Recognised as belonging on this channel, but not implemented. Say so
		// rather than staying silent: the spec has senders retrying until they get
		// a response, so silence would just invite a retry.
		logger.Infof("Unimplemented message type %s on channel %s from ncsID=%s",
			msg.GetMessageType(), sess.channel, sess.ncsID)
		s.sendNack(ctx, sess, env.MessageID, "NACK",
			"message type "+msg.GetMessageType()+" is not implemented")
	}
}

// handleReqMachInfo responds with listMachInfo.
func (s *WSServer) handleReqMachInfo(ctx context.Context, sess *WSSession, env *mosxml.MosEnvelope) {
	info := mosxml.CreateListMachInfo(s.config, mosxml.MosRev40)
	innerXML, err := xml.Marshal(info)
	if err != nil {
		logger.Errorf("Failed to marshal listMachInfo: %v", err)
		return
	}

	response := mosxml.WrapEnvelope(s.config.MOS.ID, sess.ncsID, env.MessageID, innerXML)
	s.writeMessage(ctx, sess, response)
}

// handleRoCreate processes a roCreate message with dedup and ack-after-persist.
func (s *WSServer) handleRoCreate(ctx context.Context, sess *WSSession, env *mosxml.MosEnvelope, roInfo mosxml.RunningOrderInfo, innerOpXML []byte) {
	// Check dedup using only the inner operation XML (not the full envelope),
	// so that re-deliveries with different envelope whitespace or ordering are
	// correctly identified as duplicates rather than conflicts.
	//
	// Scope is the channel: MOS 4 runs one connection per channel and each sender
	// increments its own messageID sequence per channel, so the same value can
	// mean different things on mom, ro and aux.
	scope := "ws:" + sess.channel
	switch result := s.dedup.Check(scope, env.NcsID, env.MessageID, innerOpXML); result {
	case DedupDuplicate:
		// A retry, not a new request. Replay the original response rather than
		// re-applying, and do not answer with silence: the spec has the peer
		// retrying "at intervals until a response is received", so discarding
		// would simply invite another retry.
		logger.Infof("Re-delivery of messageID=%s from ncsID=%s; replaying the original response",
			env.MessageID, env.NcsID)
		if original, ok := s.dedup.Response(scope, env.NcsID, env.MessageID); ok {
			s.writeMessage(ctx, sess, original)
		} else {
			// Seen but the response was never recorded, e.g. the first attempt
			// failed before replying. Safe to fall through and process it.
			logger.Warningf("No stored response for messageID=%s; processing as new", env.MessageID)
			break
		}
		return
	case DedupConflict:
		// Same messageID, different content. A protocol error on the sender's side.
		logger.Errorf("Message-ID conflict: ncsID=%s messageID=%s", env.NcsID, env.MessageID)
		s.sendNack(ctx, sess, env.MessageID, "NACK", "messageID conflict: same ID with different content")
		return
	}

	// Persist via service layer
	err := s.service.ProcessRunningOrderInfo(ctx, roInfo, env.MosID)
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
	// Remember the exact bytes so a retry of this messageID is answered
	// identically without the operation being applied a second time.
	s.dedup.Remember(scope, env.NcsID, env.MessageID, response)
	s.writeMessage(ctx, sess, response)
}

// maxNackDescription bounds what a NACK reflects back to the peer.
const maxNackDescription = 200

// truncateForNack keeps a statusDescription useful without reflecting an
// unbounded amount of peer-supplied text.
func truncateForNack(text string) string {
	if len(text) <= maxNackDescription {
		return text
	}
	return text[:maxNackDescription] + "..."
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
	s.writeMessage(ctx, sess, response)
}

// writeMessage encodes a UTF-8 XML MOS envelope to UCS-2 big-endian and writes
// it as a binary WebSocket frame. MOS 4.0 §2.1 mandates UCS-2BE, and live ENPS
// endpoints reject text frames with InvalidMessageType, so OpenMOS never emits
// text frames — only binary.
func (s *WSServer) writeMessage(ctx context.Context, sess *WSSession, utf8XML []byte) {
	encoded, err := mosxml.EncodeUCS2BE(utf8XML)
	if err != nil {
		logger.Errorf("Failed to encode UCS-2BE frame for ncsID=%s: %v", sess.ncsID, err)
		sess.close()
		return
	}
	if err := sess.conn.Write(ctx, websocket.MessageBinary, encoded); err != nil {
		logger.Errorf("Write failed to ncsID=%s: %v", sess.ncsID, err)
		sess.close()
		return
	}

	s.recordFrame(capture.Outbound, sess, utf8XML, len(encoded), "UCS-2BE")
}

// wireEncoding names the encoding a received frame type implies.
func wireEncoding(msgType websocket.MessageType) string {
	if msgType == websocket.MessageBinary {
		return "UCS-2BE"
	}
	return "UTF-8"
}

// recordFrame captures a frame. Failures are logged and dropped: losing a capture
// must not disturb the exchange.
func (s *WSServer) recordFrame(direction capture.Direction, sess *WSSession, utf8XML []byte, wireBytes int, encoding string) {
	if s.capture == nil {
		return
	}
	transport := "mos4-ws-" + sess.channel
	if err := s.capture.Record(transport, direction, sess.ncsID, utf8XML, wireBytes, encoding); err != nil {
		logger.Warningf("frame capture: %v", err)
	}
}

// close terminates a WebSocket session.
func (sess *WSSession) close() {
	sess.closeOnce.Do(func() {
		close(sess.closeChan)
		_ = sess.conn.Close(websocket.StatusNormalClosure, "session closed")
	})
}
