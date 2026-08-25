package server

import (
	"context"

	"airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"
)

// handleKeepAlive processes a keepAlive message (Profile 0).
//
// keepAlive is never answered. MOS 4.0 §4.1.1: "When received by the other end,
// the keepAlive messages are simply discarded. No reply (ACK, NACK, etc.) is
// necessary." It exists to hold a connection open through firewalls and proxies,
// so replying would double idle traffic for no benefit and, because keepAlive
// carries no messageID, the reply could not be correlated anyway.
func (c *ClientConnection) handleKeepAlive(ctx context.Context, keepAlive xml.KeepAlive) error {
	logger.Debugf("Received keepAlive from client %s", c.id)

	// Still counts as activity, so the connection is not reaped as idle.
	c.heartbeat.RecordHeartbeat()

	return nil
}

// handleReqMachInfo processes a reqMachInfo request (Profile 0) and answers with
// listMachInfo describing this device and the profiles it actually supports.
//
// This is the MOS 2.x TCP transport, so the advertised revision is the 2.x one.
func (c *ClientConnection) handleReqMachInfo(ctx context.Context, req xml.ReqMachInfo) error {
	logger.Infof("Received reqMachInfo from client %s", c.id)

	return c.writeMessage(ctx, xml.CreateListMachInfo(c.config, xml.MosRev28))
}

// handleListMachInfo processes an inbound listMachInfo (Profile 0).
//
// This arrives when the remote device answers a reqMachInfo we sent. There is no
// response to a listMachInfo.
func (c *ClientConnection) handleListMachInfo(ctx context.Context, info xml.ListMachInfo) error {
	logger.Infof("Received listMachInfo from client %s: manufacturer=%s model=%s mosRev=%s",
		c.id, info.Manufacturer, info.Model, info.MosRev)

	for _, profile := range info.SupportedProfiles.Profiles {
		logger.Infof("  Profile %d: %v", profile.Number, profile.Value)
	}

	return nil
}
