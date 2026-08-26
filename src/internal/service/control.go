package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"airshift/openmos/internal/events"
	"airshift/openmos/internal/model"
	"airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"
)

// ProcessROCtrl processes a roCtrl message (Profile 5)
// Updates item status based on command (READY/EXECUTE/PAUSE/STOP/SIGNAL)
func (s *MOSService) ProcessROCtrl(ctx context.Context, ctrl xml.ROCtrl) error {
	// Validate the command
	switch ctrl.Command {
	case "READY", "EXECUTE", "PAUSE", "STOP", "SIGNAL":
		// Valid commands
	default:
		return fmt.Errorf("unsupported roCtrl command: %s", ctrl.Command)
	}

	// Update the item status if it exists
	if ctrl.ItemID != "" {
		item, err := s.itemRepo.Get(ctx, ctrl.ItemID)
		if err != nil {
			return fmt.Errorf("item %s not found for roCtrl command %s: %w", ctrl.ItemID, ctrl.Command, err)
		}

		// Map command to status
		var newStatus model.StatusType
		switch ctrl.Command {
		case "READY":
			newStatus = model.StatusReady
		case "EXECUTE":
			newStatus = model.StatusPlaying
		case "PAUSE":
			newStatus = model.StatusPaused
		case "STOP":
			newStatus = model.StatusStopped
		case "SIGNAL":
			newStatus = model.StatusType("SIGNAL")
		}

		item.Status = newStatus
		item.UpdatedAt = time.Now()
		if item.Metadata == nil {
			item.Metadata = make(map[string]string)
		}
		item.Metadata["lastCommand"] = ctrl.Command
		item.Metadata["lastCommandTime"] = time.Now().Format(time.RFC3339)

		if err := s.itemRepo.Update(ctx, item); err != nil {
			return fmt.Errorf("failed to update item status: %w", err)
		}
	}

	// Publish event
	if s.eventBus != nil {
		s.eventBus.Publish(events.Event{
			Type: events.ItemControlled,
			Payload: map[string]string{
				"roID":    ctrl.ROID,
				"storyID": ctrl.StoryID,
				"itemID":  ctrl.ItemID,
				"command": ctrl.Command,
			},
			Source: "mos_service",
		})
	}

	logger.Infof("Processed roCtrl: RO %s, story %s, item %s, command %s",
		ctrl.ROID, ctrl.StoryID, ctrl.ItemID, ctrl.Command)
	return nil
}

// ProcessROItemCue processes a roItemCue message (Profile 5)
// Logs and forwards item cue events
func (s *MOSService) ProcessROItemCue(ctx context.Context, cue xml.ROItemCue) error {
	// Update item metadata with cue event timing
	if cue.ItemID != "" {
		item, err := s.itemRepo.Get(ctx, cue.ItemID)
		if err != nil {
			logger.Warningf("roItemCue references unknown item %s: %v", cue.ItemID, err)
		} else {
			if item.Metadata == nil {
				item.Metadata = make(map[string]string)
			}
			item.Metadata["lastCueEventType"] = cue.ROEventType
			item.Metadata["lastCueEventTime"] = cue.ROEventTime
			item.UpdatedAt = time.Now()

			if err := s.itemRepo.Update(ctx, item); err != nil {
				logger.Warningf("Failed to update item cue metadata: %v", err)
			}
		}
	}

	// Publish event
	if s.eventBus != nil {
		s.eventBus.Publish(events.Event{
			Type: events.ItemCued,
			Payload: map[string]string{
				"mosID":       cue.MosID,
				"roID":        cue.ROID,
				"storyID":     cue.StoryID,
				"itemID":      cue.ItemID,
				"roEventType": cue.ROEventType,
				"roEventTime": cue.ROEventTime,
			},
			Source: "mos_service",
		})
	}

	logger.Infof("Processed roItemCue: MOS %s, RO %s, story %s, item %s, eventType=%s, eventTime=%s",
		cue.MosID, cue.ROID, cue.StoryID, cue.ItemID, cue.ROEventType, cue.ROEventTime)
	return nil
}

// ProcessROStorySend stores the identity and metadata from a received story.
func (s *MOSService) ProcessROStorySend(ctx context.Context, storySend xml.ROStorySend) error {
	if strings.TrimSpace(storySend.ROID) == "" {
		return fmt.Errorf("roID is required")
	}
	if strings.TrimSpace(storySend.StoryID) == "" {
		return fmt.Errorf("storyID is required")
	}

	// The running order must already exist. roStorySend adds a story to a running
	// order; it is not a way to bring one into being, and creating the story anyway
	// leaves it pointing at a running order that is not there.
	//
	// This is not hypothetical. After OpenMOS restarted with in-memory storage, a
	// live ENPS resynchronised by sending ten roStorySend messages and no roCreate,
	// because from its side the device still held the running order. All ten were
	// accepted, producing ten orphaned stories and a running order that existed only
	// as a dangling reference.
	//
	// Reporting it is only half the answer. Real multi-vendor traffic shows that the
	// protocol expects the DEVICE to recover by pulling: a prompter in the sampled
	// corpus sends roReq twelve times across three days, and an automation system's
	// startup handshake is reqMachInfo then roReqAll within the same second. The NCS
	// is not obliged to notice our amnesia, so a device that only complains stays
	// broken. Issuing that pull is tracked separately; until then this error at least
	// names the mechanism instead of implying roCreate is the only route.
	if _, err := s.runningOrderRepo.Get(ctx, storySend.ROID); err != nil {
		return fmt.Errorf("roStorySend for unknown running order %q: "+
			"the NCS believes this device holds it, so recover with roReq for this roID "+
			"or roReqAll, or await a roCreate (%w)", storySend.ROID, err)
	}

	storyID := storyPersistenceID(storySend.ROID, storySend.StoryID)

	// Get or create the story
	story, err := s.storyRepo.Get(ctx, storyID)
	if err != nil {
		// Story doesn't exist yet; create it
		story = &model.Story{
			ID:             storyID,
			RawID:          storySend.StoryID,
			RunningOrderID: storySend.ROID,
			Slug:           storySend.StorySlug,
			Number:         storySend.StoryNum,
			Status:         model.StatusPending,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		}

		_, err = s.storyRepo.Create(ctx, story)
		if err != nil {
			return fmt.Errorf("failed to create story from roStorySend: %w", err)
		}
	} else {
		// Update existing story
		story.RawID = storySend.StoryID
		story.RunningOrderID = storySend.ROID
		if storySend.StorySlug != "" {
			story.Slug = storySend.StorySlug
		}
		if storySend.StoryNum != "" {
			story.Number = storySend.StoryNum
		}
		story.UpdatedAt = time.Now()

		if err := s.storyRepo.Update(ctx, story); err != nil {
			return fmt.Errorf("failed to update story from roStorySend: %w", err)
		}
	}

	// Publish event
	if s.eventBus != nil {
		s.eventBus.Publish(events.Event{
			Type:    events.StoryReceived,
			Payload: storySend.StoryID,
			Source:  "mos_service",
		})
	}

	logger.Infof("Processed roStorySend: RO %s, story %s", storySend.ROID, storySend.StoryID)
	return nil
}

// ProcessROReqStoryAction processes a roReqStoryAction message (Profile 6)
// MOS requests a story modification from NCS; forwards via event bus
func (s *MOSService) ProcessROReqStoryAction(ctx context.Context, reqAction xml.ROReqStoryAction) error {
	// Process based on the operation
	storySend := reqAction.ROStorySend

	// Store/update the story content
	err := s.ProcessROStorySend(ctx, storySend)
	if err != nil {
		return fmt.Errorf("failed to process story in roReqStoryAction: %w", err)
	}

	// Publish event for NCS clients to act upon
	if s.eventBus != nil {
		s.eventBus.Publish(events.Event{
			Type: events.StoryModified,
			Payload: map[string]string{
				"roID":      storySend.ROID,
				"storyID":   storySend.StoryID,
				"operation": reqAction.Operation,
				"username":  reqAction.Username,
			},
			Source: "mos_service",
		})
	}

	logger.Infof("Processed roReqStoryAction: operation=%s, RO %s, story %s, user=%s",
		reqAction.Operation, storySend.ROID, storySend.StoryID, reqAction.Username)
	return nil
}
