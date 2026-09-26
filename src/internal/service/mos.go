package service

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"airshift/openmos/internal/events"
	"airshift/openmos/internal/model"
	"airshift/openmos/internal/repository"
	"airshift/openmos/internal/xml"
)

// MOSService provides business logic for MOS operations
type MOSService struct {
	runningOrderRepo repository.RunningOrderRepository
	storyRepo        repository.StoryRepository
	itemRepo         repository.ItemRepository
	objectRepo       repository.ObjectRepository
	eventBus         *events.EventBus
}

// NewMOSService creates a new MOS service
func NewMOSService(
	runningOrderRepo repository.RunningOrderRepository,
	storyRepo repository.StoryRepository,
	itemRepo repository.ItemRepository,
	objectRepo repository.ObjectRepository,
	eventBus *events.EventBus,
) *MOSService {
	return &MOSService{
		runningOrderRepo: runningOrderRepo,
		storyRepo:        storyRepo,
		itemRepo:         itemRepo,
		objectRepo:       objectRepo,
		eventBus:         eventBus,
	}
}

// ListRunningOrders returns all running orders
func (s *MOSService) ListRunningOrders(ctx context.Context) ([]*model.RunningOrder, error) {
	return s.runningOrderRepo.List(ctx)
}

// GetRunningOrderWithStories retrieves a running order with all its stories
func (s *MOSService) GetRunningOrderWithStories(ctx context.Context, id string) (*model.RunningOrder, []*model.Story, error) {
	// Get the running order
	ro, err := s.runningOrderRepo.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}

	// Get all stories for this running order
	stories, err := s.storyRepo.ListByRunningOrder(ctx, id)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get stories: %w", err)
	}

	return ro, stories, nil
}

// GetItemsForStory retrieves all items for a story
func (s *MOSService) GetItemsForStory(ctx context.Context, storyID string) ([]*model.Item, error) {
	return s.itemRepo.ListByStory(ctx, storyID)
}

// ProcessRunningOrderInfo processes a running order creation/update message.
//
// mosID is the MOS ID from the enclosing envelope. roCreate carries no MOS ID of
// its own at running-order level, so it has to be supplied by the transport.
// Store the transport identity with the running order so later responses retain
// the same MOS identity.
func (s *MOSService) ProcessRunningOrderInfo(ctx context.Context, roInfo xml.RunningOrderInfo, mosID string) error {
	if err := validateRunningOrder(roInfo.ID, roInfo.Slug, roInfo.Stories); err != nil {
		return err
	}
	// Check if running order exists
	existingRO, err := s.runningOrderRepo.Get(ctx, roInfo.ID)

	// Parse duration if provided
	var duration int
	if roInfo.Duration != "" {
		duration = durationSeconds(roInfo.Duration)
	}

	// Create or update running order
	if err != nil { // Running order doesn't exist
		// Create new running order
		ro := &model.RunningOrder{
			ID:               roInfo.ID,
			MosID:            mosID,
			Slug:             roInfo.Slug,
			Status:           model.StatusPending,
			Duration:         duration,
			Channel:          roInfo.Channel,
			Version:          1,
			CreatedAt:        time.Now(),
			UpdatedAt:        time.Now(),
			ExternalMetadata: preserveExternalMetadata(roInfo.MosExternalMetadata),
		}

		_, err = s.runningOrderRepo.Create(ctx, ro)
		if err != nil {
			return fmt.Errorf("failed to create running order: %w", err)
		}
	} else {
		// Update existing running order
		existingRO.Slug = roInfo.Slug
		existingRO.Channel = roInfo.Channel
		existingRO.Duration = duration
		existingRO.UpdatedAt = time.Now()
		existingRO.ExternalMetadata = preserveExternalMetadata(roInfo.MosExternalMetadata)
		// Only overwrite when the transport supplied one, so an update carrying no
		// MOS ID cannot erase a value recorded earlier.
		if mosID != "" {
			existingRO.MosID = mosID
		}

		err = s.runningOrderRepo.Update(ctx, existingRO)
		if err != nil {
			return fmt.Errorf("failed to update running order: %w", err)
		}
	}

	// Process stories (simplified - full implementation would handle deletions, etc.)
	for i, storyInfo := range roInfo.Stories {
		storyID := storyPersistenceID(roInfo.ID, storyInfo.ID)
		// Create or update each story
		story := &model.Story{
			ID:               storyID,
			RawID:            storyInfo.ID,
			RunningOrderID:   roInfo.ID,
			Slug:             storyInfo.Slug,
			Number:           storyInfo.Number,
			Status:           model.StatusPending,
			Order:            i + 1,
			CreatedAt:        time.Now(),
			UpdatedAt:        time.Now(),
			ExternalMetadata: preserveExternalMetadata(storyInfo.MosExternalMetadata),
		}

		// Parse duration if provided
		if storyInfo.Duration != "" {
			if storyDuration, err := strconv.Atoi(storyInfo.Duration); err == nil {
				story.Duration = storyDuration
			}
		}

		// Create or update the story
		existingStory, err := s.storyRepo.Get(ctx, storyID)
		if err != nil {
			// Story doesn't exist, create it
			_, err = s.storyRepo.Create(ctx, story)
			if err != nil {
				return fmt.Errorf("failed to create story: %w", err)
			}
		} else {
			// Story exists, update it
			existingStory.RawID = story.RawID
			existingStory.RunningOrderID = story.RunningOrderID
			existingStory.Slug = storyInfo.Slug
			existingStory.Number = storyInfo.Number
			existingStory.Order = i + 1
			existingStory.UpdatedAt = time.Now()
			existingStory.ExternalMetadata = story.ExternalMetadata

			if storyInfo.Duration != "" {
				if storyDuration, err := strconv.Atoi(storyInfo.Duration); err == nil {
					existingStory.Duration = storyDuration
				}
			}

			err = s.storyRepo.Update(ctx, existingStory)
			if err != nil {
				return fmt.Errorf("failed to update story: %w", err)
			}
		}

		if err := s.storeItems(ctx, storyID, storyInfo.Items); err != nil {
			return err
		}
	}

	// Publish event after successful update
	if s.eventBus != nil {
		s.eventBus.Publish(events.Event{
			Type:    events.RunningOrderUpdated,
			Payload: roInfo.ID,
			Source:  "mos_service",
		})
	}

	return nil
}

func (s *MOSService) storeItems(ctx context.Context, storyID string, infos []xml.ItemInfo) error {
	for order, info := range infos {
		item := &model.Item{
			ID:               itemPersistenceID(storyID, info.ID),
			RawID:            info.ID,
			StoryID:          storyID,
			Slug:             info.Slug,
			ObjectID:         info.ObjectID,
			Status:           model.StatusPending,
			Order:            order + 1,
			CreatedAt:        time.Now(),
			UpdatedAt:        time.Now(),
			ExternalMetadata: preserveExternalMetadata(info.MosExternalMetadata),
		}
		if info.Duration != "" {
			// MOS itemEdDur is samples; Item.Duration is seconds.
			item.EditorialDuration, _ = strconv.Atoi(info.Duration)
		}
		// Keep the object's MOS identity separate from the receiving MOS identity.
		if info.MosID != "" {
			if item.Metadata == nil {
				item.Metadata = make(map[string]string, 1)
			}
			item.Metadata["mosID"] = info.MosID
		}

		existing, err := s.itemRepo.Get(ctx, item.ID)
		if err != nil {
			if _, err := s.itemRepo.Create(ctx, item); err != nil {
				return fmt.Errorf("failed to create item %s: %w", info.ID, err)
			}
			continue
		}
		existing.StoryID = item.StoryID
		existing.RawID = item.RawID
		existing.Slug = item.Slug
		existing.ObjectID = item.ObjectID
		if info.Duration != "" {
			existing.EditorialDuration = item.EditorialDuration
		}
		existing.Order = item.Order
		existing.ExternalMetadata = item.ExternalMetadata
		if info.MosID != "" {
			if existing.Metadata == nil {
				existing.Metadata = make(map[string]string, 1)
			}
			existing.Metadata["mosID"] = info.MosID
		}
		if err := s.itemRepo.Update(ctx, existing); err != nil {
			return fmt.Errorf("failed to update item %s: %w", info.ID, err)
		}
	}
	return nil
}

func preserveExternalMetadata(blocks []xml.MosExternalMetadata) []model.ExternalMetadata {
	if len(blocks) == 0 {
		return nil
	}
	stored := make([]model.ExternalMetadata, 0, len(blocks))
	for _, block := range blocks {
		stored = append(stored, model.ExternalMetadata{
			Scope: block.MosScope, Schema: block.MosSchema, Payload: block.MosPayload.Raw,
		})
	}
	return stored
}

func storyPersistenceID(roID, storyID string) string {
	return url.PathEscape(roID) + "/" + url.PathEscape(storyID)
}

func itemPersistenceID(storyID, itemID string) string {
	return storyID + "/" + url.PathEscape(itemID)
}

func durationSeconds(value string) int {
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return seconds
	}
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return 0
	}
	hours, hourErr := strconv.Atoi(parts[0])
	minutes, minuteErr := strconv.Atoi(parts[1])
	seconds, secondErr := strconv.Atoi(parts[2])
	if hourErr != nil || minuteErr != nil || secondErr != nil || hours < 0 || minutes < 0 || minutes > 59 || seconds < 0 || seconds > 59 {
		return 0
	}
	return hours*3600 + minutes*60 + seconds
}

func validateRunningOrder(roID, roSlug string, stories []xml.StoryInfo) error {
	if strings.TrimSpace(roID) == "" {
		return fmt.Errorf("roID is required")
	}
	if strings.TrimSpace(roSlug) == "" {
		return fmt.Errorf("roSlug is required")
	}
	for storyIndex, story := range stories {
		if strings.TrimSpace(story.ID) == "" {
			return fmt.Errorf("story %d: storyID is required", storyIndex+1)
		}
		for itemIndex, item := range story.Items {
			if strings.TrimSpace(item.ID) == "" {
				return fmt.Errorf("story %d item %d: itemID is required", storyIndex+1, itemIndex+1)
			}
			if strings.TrimSpace(item.ObjectID) == "" {
				return fmt.Errorf("story %d item %d: objID is required", storyIndex+1, itemIndex+1)
			}
			if strings.TrimSpace(item.MosID) == "" {
				return fmt.Errorf("story %d item %d: mosID is required", storyIndex+1, itemIndex+1)
			}
		}
	}
	return nil
}
