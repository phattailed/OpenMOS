package server

import (
	"context"

	"airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"

	"github.com/getsentry/sentry-go"
)

// handleROElementAction processes a roElementAction message (Profile 4)
// Dispatches based on operation attribute: INSERT, REPLACE, MOVE, DELETE, SWAP
func (c *ClientConnection) handleROElementAction(ctx context.Context, action xml.ROElementAction) error {
	span := sentry.StartSpan(ctx, "handle_ro_element_action")
	span.SetTag("ro_id", action.ROID)
	span.SetTag("operation", action.Operation)
	defer span.Finish()

	logger.Infof("Received roElementAction from client %s: operation=%s, roID=%s",
		c.id, action.Operation, action.ROID)

	// Delegate to service layer
	err := c.server.service.ProcessElementAction(ctx, action)
	if err != nil {
		logger.Errorf("Failed to process roElementAction (op=%s) for RO %s: %v",
			action.Operation, action.ROID, err)
		ack := xml.CreateROAck(action.ROID, "NACK", nil)
		data, marshalErr := xml.GenerateMessage(ack)
		if marshalErr != nil {
			return marshalErr
		}
		return c.Write(data)
	}

	// Send success ack
	ack := xml.CreateROAck(action.ROID, "ACK", nil)
	data, err := xml.GenerateMessage(ack)
	if err != nil {
		return err
	}

	return c.Write(data)
}

// handleROReadyToAir processes a roReadyToAir message (Profile 4)
// Sets the ready-to-air status of a running order
func (c *ClientConnection) handleROReadyToAir(ctx context.Context, msg xml.ROReadyToAir) error {
	span := sentry.StartSpan(ctx, "handle_ro_ready_to_air")
	span.SetTag("ro_id", msg.ROID)
	span.SetTag("ro_air", msg.ROAir)
	defer span.Finish()

	logger.Infof("Received roReadyToAir from client %s: roID=%s, roAir=%s", c.id, msg.ROID, msg.ROAir)

	// Delegate to service layer
	err := c.server.service.SetReadyToAir(ctx, msg.ROID, msg.ROAir)
	if err != nil {
		logger.Errorf("Failed to set ready-to-air for RO %s: %v", msg.ROID, err)
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

// handleROElementStat processes a roElementStat message (Profile 4)
// Reports the status of an element in a running order
func (c *ClientConnection) handleROElementStat(ctx context.Context, msg xml.ROElementStat) error {
	span := sentry.StartSpan(ctx, "handle_ro_element_stat")
	span.SetTag("ro_id", msg.ROID)
	span.SetTag("item_id", msg.ItemID)
	span.SetTag("status", msg.Status)
	defer span.Finish()

	logger.Infof("Received roElementStat from client %s: roID=%s, itemID=%s, status=%s",
		c.id, msg.ROID, msg.ItemID, msg.Status)

	// Delegate to service layer
	err := c.server.service.ReportElementStatus(ctx, msg)
	if err != nil {
		logger.Errorf("Failed to report element status for item %s in RO %s: %v",
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
