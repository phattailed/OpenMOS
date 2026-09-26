package server

import (
	"context"
	"fmt"

	"airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"
	"airshift/openmos/pkg/utils"
)

// handleROReqAll returns running order summaries for discovery.
func (c *ClientConnection) handleROReqAll(ctx context.Context) error {
	logger.Infof("Serving roListAll request from client %s", c.id)

	// Get compact list from service
	runningOrders, err := c.server.service.ListRunningOrders(ctx)
	if err != nil {
		return fmt.Errorf("failed to list running orders: %w", err)
	}
	items := make([]xml.ROListAllItem, 0, len(runningOrders))
	for _, ro := range runningOrders {
		items = append(items, xml.ROListAllItem{
			ID: ro.ID, Slug: ro.Slug, Channel: ro.Channel,
			EdDur: utils.FormatDuration(ro.Duration),
		})
	}
	return c.writeMessage(ctx, xml.CreateROListAll(items))
}
