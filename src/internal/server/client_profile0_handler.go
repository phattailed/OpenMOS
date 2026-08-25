package server

import (
	"context"

	"airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"
)

// handleKeepAlive processes a keepAlive message (Profile 0)
// Responds with a keepAlive to acknowledge
func (c *ClientConnection) handleKeepAlive(ctx context.Context, keepAlive xml.KeepAlive) error {
	logger.Infof("Received keepAlive from client %s", c.id)

	// Record as heartbeat activity
	c.heartbeat.RecordHeartbeat()

	// Respond with keepAlive
	response := xml.CreateKeepAlive()
	data, err := xml.GenerateMessage(response)
	if err != nil {
		return err
	}

	return c.Write(data)
}

// handleReqMachInfo processes a reqMachInfo request (Profile 0)
// Responds with listMachInfo containing server info and supported profiles
func (c *ClientConnection) handleReqMachInfo(ctx context.Context, req xml.ReqMachInfo) error {
	logger.Infof("Received reqMachInfo from client %s", c.id)

	// Create listMachInfo response from config
	response := xml.CreateListMachInfo(c.config, xml.MosRev28)
	data, err := xml.GenerateMessage(response)
	if err != nil {
		return err
	}

	return c.Write(data)
}

// handleListMachInfo processes a received listMachInfo message (Profile 0)
// This is received when the remote device responds to our reqMachInfo
func (c *ClientConnection) handleListMachInfo(ctx context.Context, info xml.ListMachInfo) error {
	logger.Infof("Received listMachInfo from client %s: manufacturer=%s model=%s mosRev=%s",
		c.id, info.Manufacturer, info.Model, info.MosRev)

	// Log supported profiles
	for _, profile := range info.SupportedProfiles.Profiles {
		logger.Infof("  Profile %d: %v", profile.Number, profile.Value)
	}

	return nil
}
