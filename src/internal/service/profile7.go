package service

import (
	"context"
	"fmt"

	"airshift/openmos/internal/events"
	"airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"
)

// SendROElementAction generates a roElementAction XML message for sending to NCS clients (Profile 7)
// This is the MOS-initiated direction: MOS sends modifications to connected NCS clients
func (s *MOSService) SendROElementAction(ctx context.Context, action xml.ROElementAction) ([]byte, error) {
	data, err := xml.GenerateMessage(action)
	if err != nil {
		return nil, fmt.Errorf("failed to generate roElementAction message: %w", err)
	}

	// Publish event to notify that a MOS-initiated modification is being sent
	if s.eventBus != nil {
		s.eventBus.Publish(events.Event{
			Type: events.ROModificationInitiated,
			Payload: map[string]string{
				"roID":      action.ROID,
				"operation": action.Operation,
			},
			Source: "mos_service",
		})
	}

	logger.Infof("Generated roElementAction for NCS: operation=%s, roID=%s", action.Operation, action.ROID)
	return data, nil
}

// SendROStorySendMessage generates a roStorySend XML message for sending to NCS clients (Profile 7)
// MOS-initiated story send to NCS direction
func (s *MOSService) SendROStorySendMessage(ctx context.Context, storySend xml.ROStorySend) ([]byte, error) {
	data, err := xml.GenerateMessage(storySend)
	if err != nil {
		return nil, fmt.Errorf("failed to generate roStorySend message: %w", err)
	}

	// Publish event
	if s.eventBus != nil {
		s.eventBus.Publish(events.Event{
			Type:    events.StoryReceived,
			Payload: storySend.StoryID,
			Source:  "mos_service",
		})
	}

	logger.Infof("Generated roStorySend for NCS: roID=%s, storyID=%s", storySend.ROID, storySend.StoryID)
	return data, nil
}

// InitiateROModification creates and returns a complete roElementAction for broadcasting to NCS clients (Profile 7)
// This method handles the full workflow of generating the message data ready for transmission
func (s *MOSService) InitiateROModification(ctx context.Context, roID, operation string, target *xml.ElementTarget, source xml.ElementSource) ([]byte, error) {
	action := xml.ROElementAction{
		Operation: operation,
		ROID:      roID,
		Target:    target,
		Source:    source,
	}

	return s.SendROElementAction(ctx, action)
}
