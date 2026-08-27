package server

// Story-oriented handlers, deliberately not named after a single profile.
//
// This file was called client_profile6_handler.go, which attributed both of its
// handlers to the wrong profile. MOS 4.0 §2 is specific:
//
//   - roStorySend belongs to Profile 4, Advanced RO/Content List Workflow, alongside
//     roReqAll and roListAll (§2.5).
//   - roReqStoryAction belongs to Profile 7, MOS RO/Content List Modification (§2.8).
//   - Profile 6, MOS Redirection, "does not include any additional MOS messages"
//     (§2.7). It is a naming convention for fully qualified mosIDs, nothing more.
//
// The mislabel mattered because precise profile claims are the point of this
// project's status table; a file asserting the wrong profile undermines it.

import (
	"context"

	"airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"

	"github.com/getsentry/sentry-go"
)

// handleROReqStoryAction processes a roReqStoryAction message.
//
// This is Profile 7 (MOS RO/Content List Modification), not Profile 6. MOS 4.0 §2.8
// lists roReqStoryAction as Profile 7's only additional message.
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
