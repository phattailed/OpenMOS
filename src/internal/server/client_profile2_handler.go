package server

import (
	"context"
	"fmt"

	"airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"

	"github.com/getsentry/sentry-go"
)

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
