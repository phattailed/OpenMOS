package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
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

// handleMessage processes a parsed MOS message
func (c *ClientConnection) handleMessage(ctx context.Context, message xml.MOSMessage) error {
	envelope, ok := message.(xml.Envelope)
	if !ok {
		return fmt.Errorf("MOS envelope required")
	}
	if envelope.MosID == "" || envelope.NcsID == "" || envelope.MessageID == "" {
		return fmt.Errorf("invalid MOS envelope identity")
	}
	messageIDText := envelope.MessageID
	base := 10
	if strings.HasPrefix(messageIDText, "0x") || strings.HasPrefix(messageIDText, "0X") {
		messageIDText = messageIDText[2:]
		base = 16
	} else if strings.HasPrefix(messageIDText, "x") || strings.HasPrefix(messageIDText, "X") {
		messageIDText = messageIDText[1:]
		base = 16
	}
	messageID, err := strconv.ParseInt(messageIDText, base, 32)
	if err != nil || messageID < 1 {
		return fmt.Errorf("invalid MOS messageID %q", envelope.MessageID)
	}
	if envelope.MosID != c.config.MOS.ID {
		return fmt.Errorf("MOS envelope addressed to %q, expected %q", envelope.MosID, c.config.MOS.ID)
	}
	if c.config.MOS.NCSID != "" && envelope.NcsID != c.config.MOS.NCSID {
		return fmt.Errorf("MOS envelope from NCS %q, expected %q", envelope.NcsID, c.config.MOS.NCSID)
	}
	inner, err := envelope.Message()
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
	case xml.ReqRunningOrderList:
		err = c.handleReqRunningOrderList(ctx, msg)
	case xml.ReqRunningOrder:
		err = c.handleReqRunningOrder(ctx, msg)
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

	// Profile 6: MOS Redirection / Story Send
	case xml.ROStorySend:
		err = c.handleROStorySend(ctx, msg)
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

func (c *ClientConnection) writeMessage(ctx context.Context, message xml.MOSMessage) error {
	envelope, ok := ctx.Value(envelopeContextKey{}).(xml.Envelope)
	if !ok {
		return fmt.Errorf("MOS envelope context required")
	}

	ncsID := envelope.NcsID
	if c.config.MOS.NCSID != "" {
		ncsID = c.config.MOS.NCSID
	}
	data, err := xml.GenerateEnvelope(c.config.MOS.ID, ncsID, envelope.MessageID, message)
	if err != nil {
		return err
	}
	return c.Write(data)
}

// handleHeartbeat processes a heartbeat message
func (c *ClientConnection) handleHeartbeat(ctx context.Context, heartbeat xml.Heartbeat) error {
	logger.Infof("Received heartbeat from client %s, source: %s", c.id, heartbeat.Source)

	// Record the heartbeat
	c.heartbeat.RecordHeartbeat()

	// Send response
	response, err := c.heartbeat.CreateHeartbeatResponse(heartbeat.RequestID)
	if err != nil {
		return fmt.Errorf("failed to create heartbeat response: %w", err)
	}

	return c.Write(response)
}

// handleReqRunningOrderList processes a request for running order list
func (c *ClientConnection) handleReqRunningOrderList(ctx context.Context, req xml.ReqRunningOrderList) error {
	logger.Infof("Received running order list request from client %s", c.id)

	// Get running orders from the server
	runningOrders, err := c.server.service.ListRunningOrders(ctx)
	if err != nil {
		return c.sendErrorAck(req.RequestID, "ERROR", fmt.Sprintf("Failed to list running orders: %v", err))
	}

	// Convert to ROListItem
	items := make([]xml.ROListItem, 0, len(runningOrders))
	for _, ro := range runningOrders {
		items = append(items, xml.ROListItem{
			ID:       ro.ID,
			Slug:     ro.Slug,
			Channel:  ro.Channel,
			Status:   string(ro.Status),
			Duration: fmt.Sprintf("%d", ro.Duration),
		})
	}

	// Create response
	response := xml.CreateRunningOrderList(c.config.MOS.ID, req.RequestID, items)
	data, err := xml.GenerateMessage(response)
	if err != nil {
		return fmt.Errorf("failed to generate running order list response: %w", err)
	}

	return c.Write(data)
}

// handleReqRunningOrder processes a request for a specific running order
func (c *ClientConnection) handleReqRunningOrder(ctx context.Context, req xml.ReqRunningOrder) error {
	logger.Infof("Received running order request from client %s for RO %s", c.id, req.ROID)

	// Get the running order from the server
	ro, stories, err := c.server.service.GetRunningOrderWithStories(ctx, req.ROID)
	if err != nil {
		return c.sendErrorAck(req.RequestID, "ERROR", fmt.Sprintf("Failed to get running order: %v", err))
	}

	// Convert to StoryInfo
	storyInfos := make([]xml.StoryInfo, 0, len(stories))
	for _, story := range stories {
		// Get items for this story
		items, err := c.server.service.GetItemsForStory(ctx, story.ID)
		if err != nil {
			logger.Warningf("Failed to get items for story %s: %v", story.ID, err)
			continue
		}

		// Convert items
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

		// Add story info
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

	// Create response
	response := xml.CreateRunningOrderInfo(
		c.config.MOS.ID,
		req.RequestID,
		ro.ID,
		ro.Slug,
		ro.Channel,
		"", // EditTime
		"", // StartTime
		fmt.Sprintf("%d", ro.Duration),
		storyInfos,
	)

	data, err := xml.GenerateMessage(response)
	if err != nil {
		return fmt.Errorf("failed to generate running order response: %w", err)
	}

	return c.Write(data)
}

// handleRunningOrderInfo processes a running order create/update message
func (c *ClientConnection) handleRunningOrderInfo(ctx context.Context, roInfo xml.RunningOrderInfo) error {
	logger.Infof("Received running order info from client %s for RO %s", c.id, roInfo.ID)

	// Process the running order creation/update
	err := c.server.service.ProcessRunningOrderInfo(ctx, roInfo)
	if err != nil {
		logger.Errorf("Failed to process running order %s: %v", roInfo.ID, err)
		return c.writeMessage(ctx, xml.CreateROAck(roInfo.ID, "ERROR", nil))
	}

	// Send acknowledgment
	return c.writeMessage(ctx, xml.CreateROAck(roInfo.ID, "OK", nil))
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
