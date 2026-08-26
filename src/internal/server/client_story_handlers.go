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
	"airshift/openmos/internal/service"
	"context"
	"errors"

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
		var unknown *service.UnknownRunningOrderError
		if errors.As(err, &unknown) {
			// Lost synchronisation, which the specification treats as recoverable by
			// asking rather than as a plain failure:
			//
			//   "If a message references an unknown roID or storyID, the MOS device
			//   should treat this as lost synchronization, send roReq, and replace its
			//   local state from the returned full roList."
			//
			// The NACK still goes first, because the peer is waiting for an answer to
			// this message and must know it was not applied. The roReq follows as a
			// separate request, on the same connection, because this peer is the one
			// holding the stale belief.
			logger.Warningf("Lost synchronisation on RO %s; requesting a rebuild", unknown.ROID)
			if ackErr := c.writeMessage(ctx, xml.CreateROAck(msg.ROID,
				"NACK: running order not held by this device, requesting resync", nil)); ackErr != nil {
				return ackErr
			}
			c.requestResync(ctx, unknown.ROID)
			return nil
		}

		logger.Errorf("Failed to process roStorySend for story %s in RO %s: %v",
			msg.StoryID, msg.ROID, err)
		return c.writeMessage(ctx, xml.CreateROAck(msg.ROID, "ERROR", nil))
	}

	return c.writeMessage(ctx, xml.CreateROAck(msg.ROID, "OK", nil))
}

// requestResync sends a roReq for a running order we should be holding but are not.
//
// Failures are logged and swallowed. Recovery is best-effort: the operation that
// triggered it has already been answered, and turning a failed recovery attempt into a
// connection error would replace a recoverable disagreement with an outage.
func (c *ClientConnection) requestResync(ctx context.Context, roID string) {
	if c.server == nil || !c.server.resync.shouldRequest(roID) {
		// Either already asked recently, or asking is not possible. Declining is safe;
		// the guard exists because asking on every refusal is how a loop starts.
		return
	}

	logger.Infof("Sending roReq for RO %s to recover local state", roID)
	if err := c.writeMessage(ctx, xml.ROReq{ROID: roID}); err != nil {
		logger.Errorf("Failed to send roReq for RO %s: %v", roID, err)
	}
}

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
