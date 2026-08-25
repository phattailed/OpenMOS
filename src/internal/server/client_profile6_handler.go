package server

import (
	"context"

	"airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"

	"github.com/getsentry/sentry-go"
)

// handleROStorySend processes a received roStorySend message.
func (c *ClientConnection) handleROStorySend(ctx context.Context, msg xml.ROStorySend) error {
	span := sentry.StartSpan(ctx, "handle_ro_story_send")
	span.SetTag("ro_id", msg.ROID)
	span.SetTag("story_id", msg.StoryID)
	defer span.Finish()

	logger.Infof("Received roStorySend from client %s: roID=%s, storyID=%s",
		c.id, msg.ROID, msg.StoryID)

	// Delegate to service layer
	err := c.server.service.ProcessROStorySend(ctx, msg)
	if err != nil {
		logger.Errorf("Failed to process roStorySend for story %s in RO %s: %v",
			msg.StoryID, msg.ROID, err)
		return c.writeMessage(ctx, xml.CreateROAck(msg.ROID, "ERROR", nil))
	}

	return c.writeMessage(ctx, xml.CreateROAck(msg.ROID, "OK", nil))
}

// handleROReqStoryAction processes a roReqStoryAction message (Profile 6)
// MOS requests a story modification from the NCS
func (c *ClientConnection) handleROReqStoryAction(ctx context.Context, msg xml.ROReqStoryAction) error {
	span := sentry.StartSpan(ctx, "handle_ro_req_story_action")
	span.SetTag("operation", msg.Operation)
	span.SetTag("ro_id", msg.ROStorySend.ROID)
	span.SetTag("story_id", msg.ROStorySend.StoryID)
	defer span.Finish()

	logger.Infof("Received roReqStoryAction from client %s: operation=%s, roID=%s, storyID=%s",
		c.id, msg.Operation, msg.ROStorySend.ROID, msg.ROStorySend.StoryID)

	// Delegate to service layer
	err := c.server.service.ProcessROReqStoryAction(ctx, msg)
	if err != nil {
		logger.Errorf("Failed to process roReqStoryAction for story %s in RO %s: %v",
			msg.ROStorySend.StoryID, msg.ROStorySend.ROID, err)
		ack := xml.CreateROAck(msg.ROStorySend.ROID, "NACK", nil)
		data, marshalErr := xml.GenerateMessage(ack)
		if marshalErr != nil {
			return marshalErr
		}
		return c.Write(data)
	}

	// Send success ack
	ack := xml.CreateROAck(msg.ROStorySend.ROID, "ACK", nil)
	data, err := xml.GenerateMessage(ack)
	if err != nil {
		return err
	}

	return c.Write(data)
}
