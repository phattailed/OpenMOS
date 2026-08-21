package server

import (
	"context"

	"airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"
)

// handleKeepAlive processes a keepAlive message (Profile 0)
// Per MOS 4 spec: keepAlive receives NO response.
func (c *ClientConnection) handleKeepAlive(ctx context.Context, keepAlive xml.KeepAlive) error {
	logger.Infof("Received keepAlive from client %s", c.id)

	// Record as heartbeat activity - no response sent per MOS 4 spec
	c.heartbeat.RecordHeartbeat()
	return nil
}

// handleReqMachInfo processes a reqMachInfo request (Profile 0)
// Responds with listMachInfo containing server info and supported profiles
func (c *ClientConnection) handleReqMachInfo(ctx context.Context, req xml.ReqMachInfo) error {
	logger.Infof("Received reqMachInfo from client %s", c.id)

	// Create listMachInfo response from config
	response := xml.CreateListMachInfo(c.config)
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
