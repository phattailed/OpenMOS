package server

import (
	"context"
	"fmt"

	"airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"
)

// SendToClient sends data to a specific client by ID (Profile 7)
func (s *TCPServer) SendToClient(clientID string, data []byte) error {
	client, ok := s.GetClient(clientID)
	if !ok {
		return fmt.Errorf("client %s not found", clientID)
	}

	return client.Write(data)
}

// SendROElementAction sends a roElementAction message to a specific NCS client (Profile 7)
// MOS-initiated running order modification directed at a specific client
func (s *TCPServer) SendROElementAction(ctx context.Context, clientID string, action xml.ROElementAction) error {
	data, err := s.service.SendROElementAction(ctx, action)
	if err != nil {
		return fmt.Errorf("failed to generate roElementAction: %w", err)
	}

	if clientID == "" {
		// Broadcast to all clients
		s.BroadcastMessage(data)
		logger.Infof("Broadcast roElementAction: operation=%s, roID=%s", action.Operation, action.ROID)
		return nil
	}

	err = s.SendToClient(clientID, data)
	if err != nil {
		return fmt.Errorf("failed to send roElementAction to client %s: %w", clientID, err)
	}

	logger.Infof("Sent roElementAction to client %s: operation=%s, roID=%s", clientID, action.Operation, action.ROID)
	return nil
}

// SendROStorySend sends a roStorySend message to a specific NCS client (Profile 7)
// MOS-initiated story send directed at a specific client
func (s *TCPServer) SendROStorySend(ctx context.Context, clientID string, storySend xml.ROStorySend) error {
	data, err := s.service.SendROStorySendMessage(ctx, storySend)
	if err != nil {
		return fmt.Errorf("failed to generate roStorySend: %w", err)
	}

	if clientID == "" {
		// Broadcast to all clients
		s.BroadcastMessage(data)
		logger.Infof("Broadcast roStorySend: roID=%s, storyID=%s", storySend.ROID, storySend.StoryID)
		return nil
	}

	err = s.SendToClient(clientID, data)
	if err != nil {
		return fmt.Errorf("failed to send roStorySend to client %s: %w", clientID, err)
	}

	logger.Infof("Sent roStorySend to client %s: roID=%s, storyID=%s", clientID, storySend.ROID, storySend.StoryID)
	return nil
}
