package server

import (
	"context"
	xmlstd "encoding/xml"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"airshift/openmos/internal/config"
	"airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"

	"github.com/getsentry/sentry-go"
)

// ClientConnection represents a connected client
type ClientConnection struct {
	conn       net.Conn
	id         string
	server     *TCPServer // Forward declaration - TCPServer is defined in server.go
	heartbeat  *xml.HeartbeatMonitor
	parser     *xml.MessageParser
	framer     *xml.UCS2BEFramer
	closeChan  chan struct{}
	closeOnce  sync.Once
	writeMutex sync.Mutex
	config     *config.Config

	lastHeartbeatReply   xml.Heartbeat
	lastHeartbeatReplyID string
}

// NewClientConnection creates a new client connection
func NewClientConnection(conn net.Conn, server *TCPServer, cfg *config.Config) *ClientConnection {
	clientID := fmt.Sprintf("%s", conn.RemoteAddr())

	client := &ClientConnection{
		conn:      conn,
		id:        clientID,
		server:    server,
		parser:    xml.NewMessageParser(),
		framer:    xml.NewUCS2BEFramer(),
		closeChan: make(chan struct{}),
		config:    cfg,
	}

	// Create heartbeat monitor
	client.heartbeat = xml.NewHeartbeatMonitor(
		cfg.MOS.ID,
		clientID,
		cfg.MOS.ClientTimeout,
		cfg.MOS.HeartbeatInterval/2,
		client.Close,
	)

	return client
}

// Start starts processing for this client connection
func (c *ClientConnection) Start(ctx context.Context) {
	defer c.Close()

	// Start heartbeat monitoring
	monitorCtx, cancelMonitor := context.WithCancel(ctx)
	defer cancelMonitor()
	go c.heartbeat.Start(monitorCtx)

	// Create a Sentry span for this client connection
	span := sentry.StartSpan(ctx, "client_connection")
	span.SetTag("client_id", c.id)
	span.SetTag("remote_addr", c.conn.RemoteAddr().String())
	defer span.Finish()

	// Read loop
	buffer := make([]byte, 4096)

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closeChan:
			return
		default:
			// Set read deadline
			err := c.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			if err != nil {
				c.trackError(err, "set_read_deadline", nil)
				return
			}

			n, err := c.conn.Read(buffer)
			if err != nil {
				if opErr, ok := err.(*net.OpError); ok && opErr.Timeout() {
					// Just a timeout, continue
					continue
				}
				if err == io.EOF {
					// Client closed connection
					logger.Infof("Client %s closed connection", c.id)
					return
				}
				c.trackError(err, "read", nil)
				return
			}

			// Process the data
			if n > 0 {
				if err := c.framer.Append(buffer[:n]); err != nil {
					c.trackError(err, "parse", nil)
					return
				}

				for {
					frame, complete, err := c.framer.Next()
					if err != nil {
						c.trackError(err, "parse", nil)
						return
					}
					if !complete {
						break
					}
					c.parser.Clear()
					c.parser.AppendData(frame)
					message, _, err := c.parser.Parse()
					if err != nil {
						c.trackError(err, "parse", nil)
						return
					}

					// Handle the message
					err = c.handleMessage(ctx, message)
					if err != nil {
						c.trackError(err, "handle_message", map[string]interface{}{
							"message_type": message.GetMessageType(),
						})
						return
					}
				}
			}
		}
	}
}

// trackError captures an error with Sentry and returns it
func (c *ClientConnection) trackError(err error, operationType string, details map[string]interface{}) error {
	if err == nil {
		return nil
	}

	// Create tags and context for error tracking
	tags := map[string]string{
		"client_id":      c.id,
		"operation_type": operationType,
	}

	// Add extra context if provided
	if details == nil {
		details = make(map[string]interface{})
	}

	// Add client connection info to context
	details["remote_addr"] = c.conn.RemoteAddr().String()

	// Use a scope to capture error with all context
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTags(tags)
		scope.SetContext("client", details)
		scope.SetLevel(sentry.LevelError)
		sentry.CaptureException(err)
	})

	// Log locally as well
	logger.Errorf("[Client %s] %s error: %v", c.id, operationType, err)

	return err
}

// handleMessage processes a parsed MOS message.
//
// MOS 2.8.4 defines messageID, but this receive path tolerates its absence as
// an intentional inbound compatibility seam. See xml.ValidateEnvelope.
func (c *ClientConnection) handleMessage(ctx context.Context, message xml.MOSMessage) error {
	envelope, ok := message.(xml.Envelope)
	if !ok {
		return fmt.Errorf("MOS envelope required")
	}

	inner, err := xml.ValidateEnvelope(envelope, xml.Gen2x, c.config.MOS.ID, c.config.MOS.NCSID)
	if err != nil {
		return err
	}

	return c.handlePayload(context.WithValue(ctx, envelopeContextKey{}, envelope), inner)
}

func (c *ClientConnection) handlePayload(ctx context.Context, message xml.MOSMessage) error {
	// Create a span for this message handling
	span := sentry.StartSpan(ctx, "handle_message")
	span.SetTag("message_type", message.GetMessageType())
	span.SetTag("client_id", c.id)
	defer span.Finish()

	var err error

	switch msg := message.(type) {
	// Profile 0: Basic Communication
	case xml.Heartbeat:
		err = c.handleHeartbeat(ctx, msg)
	case xml.KeepAlive:
		err = c.handleKeepAlive(ctx, msg)
	case xml.ReqMachInfo:
		err = c.handleReqMachInfo(ctx, msg)
	case xml.ListMachInfo:
		err = c.handleListMachInfo(ctx, msg)

	// Running Order messages (existing)
	case xml.ROReqAll:
		err = c.handleROReqAll(ctx)
	case xml.RunningOrderInfo:
		err = c.handleRunningOrderInfo(ctx, msg)
	case xml.MOSAck:
		err = c.handleMOSAck(ctx, msg)

	default:
		err = fmt.Errorf("unknown message type: %T", message)
	}

	if err != nil {
		span.Status = sentry.SpanStatusInternalError
		span.SetData("error", err.Error())
	}

	return err
}

type envelopeContextKey struct{}

// buildMessage wraps a message in the MOS envelope for the current request,
// returning the bytes that would be sent.
func (c *ClientConnection) buildMessage(ctx context.Context, message xml.MOSMessage) ([]byte, error) {
	envelope, ok := ctx.Value(envelopeContextKey{}).(xml.Envelope)
	if !ok {
		return nil, fmt.Errorf("MOS envelope context required")
	}

	ncsID := envelope.NcsID
	if c.config.MOS.NCSID != "" {
		ncsID = c.config.MOS.NCSID
	}
	return xml.GenerateEnvelope(c.config.MOS.ID, ncsID, envelope.MessageID, message)
}

func (c *ClientConnection) writeMessage(ctx context.Context, message xml.MOSMessage) error {
	data, err := c.buildMessage(ctx, message)
	if err != nil {
		return err
	}
	return c.Write(data)
}

// handleHeartbeat processes a heartbeat message (Profile 0).
//
// The spec's workflow is "Send a <heartbeat> message to another application and
// receive a <heartbeat> message in response", so a heartbeat is answered with a
// heartbeat. An exact reflection of our last response is ignored to prevent
// a reply loop without dropping a distinct rapid request.
func (c *ClientConnection) handleHeartbeat(ctx context.Context, heartbeat xml.Heartbeat) error {
	logger.Infof("Received heartbeat from client %s", c.id)

	// Record the heartbeat so the connection is not reaped as idle.
	c.heartbeat.RecordHeartbeat()

	envelope, _ := ctx.Value(envelopeContextKey{}).(xml.Envelope)
	if heartbeat == c.lastHeartbeatReply && envelope.MessageID == c.lastHeartbeatReplyID {
		return nil
	}
	reply := xml.CreateHeartbeatResponse(heartbeat.RequestID)
	c.lastHeartbeatReply = reply
	c.lastHeartbeatReplyID = envelope.MessageID
	return c.writeMessage(ctx, reply)
}

// handleRunningOrderInfo processes a running order create/update message.
//
// Retried messageIDs are made idempotent: a re-delivery replays the original ack
// without applying the operation again, and a messageID reused with different
// content is rejected. See MOS 4.0 §4.1.6 for why this matters -- an NCS that
// times out resends the same request, and applying it twice "will lead to an
// unwanted result in many cases".
func (c *ClientConnection) handleRunningOrderInfo(ctx context.Context, roInfo xml.RunningOrderInfo) error {
	response, err := c.prepareRunningOrderAck(ctx, roInfo)
	if err != nil {
		return err
	}
	return c.Write(response)
}

func (c *ClientConnection) prepareRunningOrderAck(ctx context.Context, roInfo xml.RunningOrderInfo) ([]byte, error) {
	logger.Infof("Received running order info from client %s for RO %s", c.id, roInfo.ID)

	envelope, hasEnvelope := ctx.Value(envelopeContextKey{}).(xml.Envelope)

	// A missing inbound ID cannot be used to recognize a retry.
	dedupable := hasEnvelope && envelope.MessageID != "" && c.server != nil && c.server.dedup != nil
	if dedupable {
		c.server.roMu.Lock()
		defer c.server.roMu.Unlock()
	}

	if dedupable {
		// Hash the operation only, not the envelope, so a re-delivery that differs
		// in envelope whitespace is a duplicate rather than a conflict. Marshalling
		// the parsed payload normalises formatting for free.
		content, err := xmlstd.Marshal(roInfo)
		if err != nil {
			return nil, fmt.Errorf("failed to hash running order for deduplication: %w", err)
		}

		switch result := c.server.dedup.Check(c.dedupScope(), envelope.NcsID, envelope.MessageID, content); result {
		case DedupDuplicate:
			logger.Infof("Re-delivery of messageID=%s from ncsID=%s; replaying the original ack",
				envelope.MessageID, envelope.NcsID)
			if original, ok := c.server.dedup.Response(c.dedupScope(), envelope.NcsID, envelope.MessageID); ok {
				return original, nil
			}
			// The lock rules out an in-flight first attempt. A previous attempt
			// failed before producing a response, so retry the operation.
			logger.Warningf("No stored ack for messageID=%s; processing as new", envelope.MessageID)
		case DedupConflict:
			logger.Errorf("Message-ID conflict on messageID=%s from ncsID=%s: same ID, different content",
				envelope.MessageID, envelope.NcsID)
			return c.buildMessage(ctx, xml.CreateROAck(roInfo.ID, "NACK: messageID conflict, same ID with different content", nil))
		}
	}

	// Process the running order creation/update
	err := c.server.service.ProcessRunningOrderInfo(ctx, roInfo, c.config.MOS.ID)
	if err != nil {
		logger.Errorf("Failed to process running order %s: %v", roInfo.ID, err)
		return c.buildMessage(ctx, xml.CreateROAck(roInfo.ID, roNackStatus(err), nil))
	}

	// Acknowledge only after the running order is persisted.
	ack, err := c.buildMessage(ctx, xml.CreateROAck(roInfo.ID, "OK", nil))
	if err != nil {
		return nil, err
	}
	if dedupable {
		c.server.dedup.Remember(c.dedupScope(), envelope.NcsID, envelope.MessageID, ack)
	}
	return ack, nil
}

// dedupScope namespaces dedup keys for this transport. The WebSocket transport
// runs concurrently and each sender increments its own messageID sequence per
// channel, so the same value can legitimately mean different things.
func (c *ClientConnection) dedupScope() string {
	return "tcp:ro"
}

func roNackStatus(err error) string {
	if strings.Contains(err.Error(), " is required") {
		return "NACK: " + err.Error()
	}
	return "NACK: running order storage failed"
}

// handleMOSAck processes an acknowledgment message
func (c *ClientConnection) handleMOSAck(ctx context.Context, ack xml.MOSAck) error {
	logger.Infof("Received acknowledgment from client %s: %s - %s", c.id, ack.Status, ack.StatusDescription)
	// Just log for now
	return nil
}

// Write sends data to the client
func (c *ClientConnection) Write(data []byte) error {
	wireData, err := xml.EncodeUCS2BE(data)
	if err != nil {
		return c.trackError(err, "encode", nil)
	}
	c.writeMutex.Lock()
	defer c.writeMutex.Unlock()

	// Set write deadline
	err = c.conn.SetWriteDeadline(time.Now().Add(c.config.Server.WriteTimeout))
	if err != nil {
		return c.trackError(err, "set_write_deadline", nil)
	}

	_, err = c.conn.Write(wireData)
	if err != nil {
		return c.trackError(err, "write", map[string]interface{}{
			"data_length": len(wireData),
		})
	}

	return nil
}

// Close closes the client connection
func (c *ClientConnection) Close() {
	c.closeOnce.Do(func() {
		logger.Infof("Closing connection for client %s", c.id)
		close(c.closeChan)

		// Track connection closure in Sentry
		sentry.WithScope(func(scope *sentry.Scope) {
			scope.SetTag("client_id", c.id)
			scope.SetLevel(sentry.LevelInfo)
			sentry.CaptureMessage(fmt.Sprintf("Client connection %s closed", c.id))
		})

		if err := c.conn.Close(); err != nil {
			c.trackError(err, "close", nil)
		}

		c.server.unregisterClient(c.id)
	})
}

// ID returns the client ID
func (c *ClientConnection) ID() string {
	return c.id
}
