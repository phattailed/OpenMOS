package server

import (
	"strings"

	"airshift/openmos/internal/model"
	"context"
	xmlstd "encoding/xml"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"airshift/openmos/internal/capture"
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

	// Guards against heartbeat reflection. The MOS spec warns: "care should be
	// taken in implementation of this message to avoid an endless looping
	// condition on response." If a peer answers our heartbeat response with
	// another heartbeat, replying again would loop indefinitely, so responses
	// are rate limited.
	heartbeatMutex     sync.Mutex
	lastHeartbeatReply time.Time
}

// minHeartbeatReplyInterval bounds how often this connection will answer an
// inbound heartbeat, so a peer that echoes heartbeats cannot drive an unbounded
// exchange.
const minHeartbeatReplyInterval = time.Second

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
					// frame is decoded UTF-8; on the wire it was UCS-2BE, so the
					// wire size is twice its rune count. Recorded before parsing so
					// a frame we reject is captured too -- those are the ones worth
					// having.
					c.recordFrame(capture.Inbound, frame)

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
// This is the MOS 2.x raw TCP transport, so envelopes are validated against the
// 2.x rules: the MOS 2.6 / 2.8.x DTD has no messageID element, so one must not be
// required here. See xml.ValidateEnvelope.
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

	// Profile 1: Basic Object Based Workflow
	case xml.MosObj:
		err = c.handleMosObj(ctx, msg)
	case xml.MosReqObj:
		err = c.handleMosReqObj(ctx, msg)
	case xml.MosReqAll:
		err = c.handleMosReqAll(ctx, msg)
	case xml.MosListAll:
		err = c.handleMosListAll(ctx, msg)

	// Profile 2: Basic Running Order Workflow
	case xml.ROReplace:
		err = c.handleROReplace(ctx, msg)
	case xml.RODelete:
		err = c.handleRODelete(ctx, msg)
	case xml.ROMetadataReplace:
		err = c.handleROMetadataReplace(ctx, msg)
	case xml.ROListAll:
		err = c.handleROListAll(ctx, msg)
	case xml.ROAck:
		err = c.handleROAck(ctx, msg)

	// Profile 3: Advanced Object Based Workflow
	case xml.MosObjCreate:
		err = c.handleMosObjCreate(ctx, msg)
	case xml.MosItemReplace:
		err = c.handleMosItemReplace(ctx, msg)
	case xml.MosReqSearchableSchema:
		err = c.handleMosReqSearchableSchema(ctx, msg)
	case xml.MosListSearchableSchema:
		err = c.handleMosListSearchableSchema(ctx, msg)

	// Running Order messages (existing)
	case xml.ROReq:
		err = c.handleROReq(ctx, msg)
	case xml.ROReqAll:
		err = c.handleROReqAll(ctx, msg)
	case xml.ROList:
		err = c.handleROList(ctx, msg)
	case xml.RunningOrderInfo:
		err = c.handleRunningOrderInfo(ctx, msg)
	case xml.MOSAck:
		err = c.handleMOSAck(ctx, msg)

	// Profile 4: Advanced RO/Content List Workflow
	case xml.ROElementAction:
		err = c.handleROElementAction(ctx, msg)
	case xml.ROReadyToAir:
		err = c.handleROReadyToAir(ctx, msg)
	case xml.ROElementStat:
		err = c.handleROElementStat(ctx, msg)

	// Profile 5: Item Control
	case xml.ROCtrl:
		err = c.handleROCtrl(ctx, msg)
	case xml.ROItemCue:
		err = c.handleROItemCue(ctx, msg)

	// Profile 4 continued: roStorySend carries the body of a story (MOS 4.0 §2.5).
	// It is NOT Profile 6 -- Profile 6 is MOS Redirection and "does not include any
	// additional MOS messages", being only a mosID naming convention (§2.7).
	case xml.ROStorySend:
		err = c.handleROStorySend(ctx, msg)

	// Profile 7: MOS RO/Content List Modification (§2.8).
	case xml.ROReqStoryAction:
		err = c.handleROReqStoryAction(ctx, msg)

	// Story messages
	case xml.NCSReqStoryAction:
		err = c.handleNCSReqStoryAction(ctx, msg)

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
// heartbeat. Responses are rate limited to avoid the endless looping condition
// the spec warns about when the peer also echoes.
func (c *ClientConnection) handleHeartbeat(ctx context.Context, heartbeat xml.Heartbeat) error {
	logger.Infof("Received heartbeat from client %s", c.id)

	// Record the heartbeat so the connection is not reaped as idle.
	c.heartbeat.RecordHeartbeat()

	if !c.allowHeartbeatReply() {
		logger.Debugf("Suppressing heartbeat reply to client %s: within %s of the previous reply",
			c.id, minHeartbeatReplyInterval)
		return nil
	}

	return c.writeMessage(ctx, xml.CreateHeartbeatResponse(heartbeat.RequestID))
}

// allowHeartbeatReply reports whether enough time has passed since the last
// heartbeat response on this connection to send another.
func (c *ClientConnection) allowHeartbeatReply() bool {
	c.heartbeatMutex.Lock()
	defer c.heartbeatMutex.Unlock()

	now := time.Now()
	if !c.lastHeartbeatReply.IsZero() && now.Sub(c.lastHeartbeatReply) < minHeartbeatReplyInterval {
		return false
	}
	c.lastHeartbeatReply = now
	return true
}

// handleReqRunningOrderList processes a request for running order list
func (c *ClientConnection) handleROReq(ctx context.Context, req xml.ROReq) error {
	logger.Infof("Received roReq from client %s for RO %s", c.id, req.ROID)

	if strings.TrimSpace(req.ROID) == "" {
		return c.writeMessage(ctx, xml.CreateROAck("", "NACK: roReq requires a roID", nil))
	}

	ro, stories, err := c.server.service.GetRunningOrderWithStories(ctx, req.ROID)
	if err != nil {
		// MOS 3.8.4 §3.5.1: roReq is answered with roList, or "roAck with a NACK
		// value [...] if the Running Order ID is not valid, roList cannot be returned
		// for some reason, or if the Running Order is not available". Reporting that
		// honestly is what lets a peer resynchronise.
		logger.Infof("roReq for unknown or unavailable RO %s: %v", req.ROID, err)
		return c.writeMessage(ctx, xml.CreateROAck(req.ROID,
			"NACK: running order not available", nil))
	}

	storyInfos, err := c.storyInfosFor(ctx, stories)
	if err != nil {
		return c.writeMessage(ctx, xml.CreateROAck(req.ROID,
			fmt.Sprintf("NACK: %v", err), nil))
	}

	// Answered with roList, not roCreate. roCreate is the NCS telling a device about a
	// new running order; roList is the answer to a request for one.
	return c.writeMessage(ctx, xml.CreateROList(xml.ROListEntry{
		ID:      ro.ID,
		Slug:    ro.Slug,
		Channel: ro.Channel,
		EdDur:   fmt.Sprintf("%d", ro.Duration),
		Stories: storyInfos,
	}))
}

// handleROReqAll answers <roReqAll> with <roListAll>.
//
// MOS 3.8.4 §3.5.3/§3.5.4: roReqAll carries no roID and roListAll describes every
// running order in summary. Stories are deliberately absent -- this is discovery, and
// a peer wanting content follows up with roReq per running order.
func (c *ClientConnection) handleROReqAll(ctx context.Context, _ xml.ROReqAll) error {
	logger.Infof("Received roReqAll from client %s", c.id)

	runningOrders, err := c.server.service.ListRunningOrders(ctx)
	if err != nil {
		return c.writeMessage(ctx, xml.CreateROAck("",
			fmt.Sprintf("NACK: failed to list running orders: %v", err), nil))
	}

	entries := make([]xml.ROListAllItem, 0, len(runningOrders))
	for _, ro := range runningOrders {
		entries = append(entries, xml.ROListAllItem{
			ID:      ro.ID,
			Slug:    ro.Slug,
			Channel: ro.Channel,
			EdDur:   fmt.Sprintf("%d", ro.Duration),
		})
	}

	// An empty roListAll is a valid answer, observed from a real NCS.
	return c.writeMessage(ctx, xml.CreateROListAll(entries))
}

// handleROList applies an inbound <roList>, completing pull recovery.
//
// MOS 3.8.4: on lost synchronisation a device should "send roReq, and replace its local
// state from the returned full roList". This is that replacement. A roList may also
// arrive unsolicited -- §3.5.2 notes it "can be sent by either the NCS or MOS" -- so it
// is applied on its own merits rather than only when a request is outstanding.
//
// No response is defined for roList, so none is sent.
func (c *ClientConnection) handleROList(ctx context.Context, list xml.ROList) error {
	logger.Infof("Received roList from client %s for RO %s with %d stories",
		c.id, list.ID, len(list.Stories))

	if err := c.server.service.ApplyROList(ctx, list, c.config.MOS.ID); err != nil {
		logger.Errorf("Failed to apply roList for RO %s: %v", list.ID, err)
		return nil
	}

	// The disagreement is resolved, so allow an immediate request if a later one
	// occurs rather than holding the rate limit against a running order we now hold.
	c.server.resync.forget(list.ID)
	logger.Infof("Applied roList for RO %s; local state rebuilt", list.ID)
	return nil
}

// storyInfosFor converts stored stories, with their items, into the wire shape shared
// by roList, roCreate and roReplace.
func (c *ClientConnection) storyInfosFor(ctx context.Context, stories []*model.Story) ([]xml.StoryInfo, error) {
	storyInfos := make([]xml.StoryInfo, 0, len(stories))
	for _, story := range stories {
		items, err := c.server.service.GetItemsForStory(ctx, story.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get items for story %s: %w", story.ID, err)
		}

		itemInfos := make([]xml.ItemInfo, 0, len(items))
		for _, item := range items {
			itemID := item.RawID
			if itemID == "" {
				itemID = item.ID
			}
			itemInfos = append(itemInfos, xml.ItemInfo{
				ID:       itemID,
				Slug:     item.Slug,
				Duration: fmt.Sprintf("%d", item.Duration),
				ObjectID: item.ObjectID,
			})
		}

		storyID := story.RawID
		if storyID == "" {
			storyID = story.ID
		}
		storyInfos = append(storyInfos, xml.StoryInfo{
			ID:       storyID,
			Slug:     story.Slug,
			Number:   story.Number,
			Duration: fmt.Sprintf("%d", story.Duration),
			Items:    itemInfos,
		})
	}
	return storyInfos, nil
}

// handleRunningOrderInfo processes a running order create/update message.
//
// Retried messageIDs are made idempotent: a re-delivery replays the original ack
// without applying the operation again, and a messageID reused with different
// content is rejected. See MOS 4.0 §4.1.6 for why this matters -- an NCS that
// times out resends the same request, and applying it twice "will lead to an
// unwanted result in many cases".
func (c *ClientConnection) handleRunningOrderInfo(ctx context.Context, roInfo xml.RunningOrderInfo) error {
	logger.Infof("Received running order info from client %s for RO %s", c.id, roInfo.ID)

	envelope, hasEnvelope := ctx.Value(envelopeContextKey{}).(xml.Envelope)

	// MOS 2.6 and 2.8.x envelopes have no messageID, so there is nothing to
	// deduplicate against. Process normally rather than failing.
	dedupable := hasEnvelope && envelope.MessageID != "" && c.server != nil && c.server.dedup != nil

	if dedupable {
		// Hash the operation only, not the envelope, so a re-delivery that differs
		// in envelope whitespace is a duplicate rather than a conflict. Marshalling
		// the parsed payload normalises formatting for free.
		content, err := xmlstd.Marshal(roInfo)
		if err != nil {
			return fmt.Errorf("failed to hash running order for deduplication: %w", err)
		}

		switch result := c.server.dedup.Check(c.dedupScope(), envelope.NcsID, envelope.MessageID, content); result {
		case DedupDuplicate:
			logger.Infof("Re-delivery of messageID=%s from ncsID=%s; replaying the original ack",
				envelope.MessageID, envelope.NcsID)
			if original, ok := c.server.dedup.Response(c.dedupScope(), envelope.NcsID, envelope.MessageID); ok {
				return c.Write(original)
			}
			// Seen, but no response was recorded -- the first attempt must have
			// failed before replying. Fall through and process it.
			logger.Warningf("No stored ack for messageID=%s; processing as new", envelope.MessageID)
		case DedupConflict:
			logger.Errorf("Message-ID conflict on messageID=%s from ncsID=%s: same ID, different content",
				envelope.MessageID, envelope.NcsID)
			return c.writeMessage(ctx, xml.CreateROAck(roInfo.ID, "NACK: messageID conflict, same ID with different content", nil))
		}
	}

	// Process the running order creation/update
	err := c.server.service.ProcessRunningOrderInfo(ctx, roInfo, c.config.MOS.ID)
	if err != nil {
		logger.Errorf("Failed to process running order %s: %v", roInfo.ID, err)
		return c.writeMessage(ctx, xml.CreateROAck(roInfo.ID, "ERROR", nil))
	}

	// Acknowledge only after the running order is persisted.
	ack, err := c.buildMessage(ctx, xml.CreateROAck(roInfo.ID, "OK", nil))
	if err != nil {
		return err
	}
	if dedupable {
		c.server.dedup.Remember(c.dedupScope(), envelope.NcsID, envelope.MessageID, ack)
	}
	return c.Write(ack)
}

// dedupScope namespaces dedup keys for this transport. The WebSocket transport
// runs concurrently and each sender increments its own messageID sequence per
// channel, so the same value can legitimately mean different things.
func (c *ClientConnection) dedupScope() string {
	return "tcp:ro"
}

// handleMOSAck processes an acknowledgment message
func (c *ClientConnection) handleMOSAck(ctx context.Context, ack xml.MOSAck) error {
	logger.Infof("Received acknowledgment from client %s: %s - %s", c.id, ack.Status, ack.StatusDescription)
	// Just log for now
	return nil
}

// sendErrorAck sends an error acknowledgment
func (c *ClientConnection) sendErrorAck(requestID, status, description string) error {
	ack := xml.CreateMOSAck(c.config.MOS.ID, requestID, status, description)
	data, err := xml.GenerateMessage(ack)
	if err != nil {
		return fmt.Errorf("failed to generate error ack: %w", err)
	}

	return c.Write(data)
}

// sendSuccessAck sends a success acknowledgment
func (c *ClientConnection) sendSuccessAck(requestID, description string) error {
	return c.sendErrorAck(requestID, "ACK", description)
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

	c.recordFrameWire(capture.Outbound, data, len(wireData))
	return nil
}

// recordFrame captures a decoded frame whose wire form was UCS-2BE.
func (c *ClientConnection) recordFrame(direction capture.Direction, utf8XML []byte) {
	c.recordFrameWire(direction, utf8XML, len([]rune(string(utf8XML)))*2)
}

// recordFrameWire captures a frame with an explicit wire size. Capture failures
// are logged and dropped: losing a capture must not disturb the exchange.
func (c *ClientConnection) recordFrameWire(direction capture.Direction, utf8XML []byte, wireBytes int) {
	if c.server == nil || c.server.capture == nil {
		return
	}
	if err := c.server.capture.Record("mos2-tcp", direction, c.id, utf8XML, wireBytes, "UCS-2BE"); err != nil {
		logger.Warningf("frame capture: %v", err)
	}
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
