package server

import (
	"context"

	"airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"

	"github.com/getsentry/sentry-go"
)

// handleROCtrl processes a roCtrl message (Profile 5)
// Dispatches item control commands: READY, EXECUTE, PAUSE, STOP, SIGNAL
func (c *ClientConnection) handleROCtrl(ctx context.Context, msg xml.ROCtrl) error {
	span := sentry.StartSpan(ctx, "handle_ro_ctrl")
	span.SetTag("ro_id", msg.ROID)
	span.SetTag("story_id", msg.StoryID)
	span.SetTag("item_id", msg.ItemID)
	span.SetTag("command", msg.Command)
	defer span.Finish()

	logger.Infof("Received roCtrl from client %s: roID=%s, storyID=%s, itemID=%s, command=%s",
		c.id, msg.ROID, msg.StoryID, msg.ItemID, msg.Command)

	// Delegate to service layer
	err := c.server.service.ProcessROCtrl(ctx, msg)
	if err != nil {
		logger.Errorf("Failed to process roCtrl for item %s in RO %s: %v",
			msg.ItemID, msg.ROID, err)
		ack := xml.CreateROAck(msg.ROID, "NACK", nil)
		data, marshalErr := xml.GenerateMessage(ack)
		if marshalErr != nil {
			return marshalErr
		}
		return c.Write(data)
	}

	// Send success ack
	ack := xml.CreateROAck(msg.ROID, "ACK", nil)
	data, err := xml.GenerateMessage(ack)
	if err != nil {
		return err
	}

	return c.Write(data)
}

// handleROItemCue processes a roItemCue message (Profile 5)
// Processes item cue events and logs timing information
func (c *ClientConnection) handleROItemCue(ctx context.Context, msg xml.ROItemCue) error {
	span := sentry.StartSpan(ctx, "handle_ro_item_cue")
	span.SetTag("ro_id", msg.ROID)
	span.SetTag("story_id", msg.StoryID)
	span.SetTag("item_id", msg.ItemID)
	span.SetTag("event_type", msg.ROEventType)
	defer span.Finish()

	logger.Infof("Received roItemCue from client %s: roID=%s, storyID=%s, itemID=%s, eventType=%s, eventTime=%s",
		c.id, msg.ROID, msg.StoryID, msg.ItemID, msg.ROEventType, msg.ROEventTime)

	// Delegate to service layer
	err := c.server.service.ProcessROItemCue(ctx, msg)
	if err != nil {
		logger.Errorf("Failed to process roItemCue for item %s in RO %s: %v",
			msg.ItemID, msg.ROID, err)
		ack := xml.CreateROAck(msg.ROID, "NACK", nil)
		data, marshalErr := xml.GenerateMessage(ack)
		if marshalErr != nil {
			return marshalErr
		}
		return c.Write(data)
	}

	// Send success ack
	ack := xml.CreateROAck(msg.ROID, "ACK", nil)
	data, err := xml.GenerateMessage(ack)
	if err != nil {
		return err
	}

	return c.Write(data)
}
