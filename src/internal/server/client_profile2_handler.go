package server

import (
	"context"
	"fmt"

	"airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"

	"github.com/getsentry/sentry-go"
)

// handleROReplace processes a roReplace message (Profile 2)
// Replaces the entire running order: deletes all existing stories and re-creates
func (c *ClientConnection) handleROReplace(ctx context.Context, roReplace xml.ROReplace) error {
	span := sentry.StartSpan(ctx, "handle_ro_replace")
	span.SetTag("ro_id", roReplace.ID)
	defer span.Finish()

	logger.Infof("Received roReplace from client %s for RO %s", c.id, roReplace.ID)

	// Delegate to service layer
	err := c.server.service.ReplaceRunningOrder(ctx, roReplace)
	if err != nil {
		logger.Errorf("Failed to replace running order %s: %v", roReplace.ID, err)
		return c.writeMessage(ctx, xml.CreateROAck(roReplace.ID, "ERROR", nil))
	}

	// Send success ack
	return c.writeMessage(ctx, xml.CreateROAck(roReplace.ID, "OK", nil))
}

// handleRODelete processes a roDelete message (Profile 2)
// Deletes the running order and all associated stories/items
func (c *ClientConnection) handleRODelete(ctx context.Context, roDelete xml.RODelete) error {
	span := sentry.StartSpan(ctx, "handle_ro_delete")
	span.SetTag("ro_id", roDelete.ID)
	defer span.Finish()

	logger.Infof("Received roDelete from client %s for RO %s", c.id, roDelete.ID)

	// Delegate to service layer
	err := c.server.service.DeleteRunningOrder(ctx, roDelete.ID)
	if err != nil {
		logger.Errorf("Failed to delete running order %s: %v", roDelete.ID, err)
		return c.writeMessage(ctx, xml.CreateROAck(roDelete.ID, "ERROR", nil))
	}

	return c.writeMessage(ctx, xml.CreateROAck(roDelete.ID, "OK", nil))
}

// handleROMetadataReplace processes a roMetadataReplace message (Profile 2)
// Updates only RO metadata fields without touching stories
func (c *ClientConnection) handleROMetadataReplace(ctx context.Context, roMeta xml.ROMetadataReplace) error {
	span := sentry.StartSpan(ctx, "handle_ro_metadata_replace")
	span.SetTag("ro_id", roMeta.ID)
	defer span.Finish()

	logger.Infof("Received roMetadataReplace from client %s for RO %s", c.id, roMeta.ID)

	// Delegate to service layer
	err := c.server.service.ReplaceMetadata(ctx, roMeta)
	if err != nil {
		logger.Errorf("Failed to replace metadata for RO %s: %v", roMeta.ID, err)
		ack := xml.CreateROAck(roMeta.ID, "NACK", nil)
		data, marshalErr := xml.GenerateMessage(ack)
		if marshalErr != nil {
			return marshalErr
		}
		return c.Write(data)
	}

	// Send success ack
	ack := xml.CreateROAck(roMeta.ID, "ACK", nil)
	data, err := xml.GenerateMessage(ack)
	if err != nil {
		return err
	}

	return c.Write(data)
}

// handleROListAll processes a received roListAll message (Profile 2)
// This is received when the NCS sends us the list, or we respond to a request
func (c *ClientConnection) handleROListAll(ctx context.Context, roListAll xml.ROListAll) error {
	logger.Infof("Received roListAll from client %s: %d running orders", c.id, len(roListAll.ROs))
	// Log receipt - the NCS is sending us the full list
	return nil
}

// handleROAck processes a received roAck message (Profile 2)
// This is typically received in response to roCreate/roReplace/roDelete
func (c *ClientConnection) handleROAck(ctx context.Context, roAck xml.ROAck) error {
	logger.Infof("Received roAck from client %s for RO %s: status=%s", c.id, roAck.ID, roAck.Status)
	// Log receipt
	return nil
}

// handleListAllRunningOrders responds to a request for all running orders in compact format (Profile 2)
// Called when the NCS requests roReqAll (distinct from single-RO roReqAll which has an roID)
func (c *ClientConnection) handleListAllRunningOrders(ctx context.Context) error {
	span := sentry.StartSpan(ctx, "handle_list_all_running_orders")
	defer span.Finish()

	logger.Infof("Serving roListAll request from client %s", c.id)

	// Get compact list from service
	items, err := c.server.service.ListAllRunningOrdersCompact(ctx)
	if err != nil {
		return c.sendErrorAck("", "ERROR", fmt.Sprintf("Failed to list running orders: %v", err))
	}

	// Create and send response
	response := xml.CreateROListAll(items)
	data, err := xml.GenerateMessage(response)
	if err != nil {
		return fmt.Errorf("failed to generate roListAll response: %w", err)
	}

	return c.Write(data)
}
