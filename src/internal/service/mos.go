package service

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"airshift/openmos/internal/events"
	"airshift/openmos/internal/model"
	"airshift/openmos/internal/repository"
	"airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"
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

// GetObject retrieves a MOS object by ID and returns it as an XML message struct (Profile 1)
func (s *MOSService) GetObject(ctx context.Context, objID string) (*xml.MosObj, error) {
	obj, err := s.objectRepo.Get(ctx, objID)
	if err != nil {
		return nil, fmt.Errorf("failed to get object: %w", err)
	}

	// Convert model to XML struct
	mosObj := &xml.MosObj{
		ObjID:       obj.ID,
		ObjSlug:     obj.Slug,
		MosAbstract: obj.MosAbstract,
		ObjType:     obj.ObjectType,
		ObjTB:       obj.TimeBase,
		ObjDur:      obj.Duration,
		Status:      string(obj.Status),
		CreatedBy:   obj.Metadata["createdBy"],
		Created:     obj.CreatedAt.Format(time.RFC3339),
		ChangedBy:   obj.Metadata["changedBy"],
		Changed:     obj.UpdatedAt.Format(time.RFC3339),
		Description: obj.Metadata["description"],
	}

	return mosObj, nil
}

// GetAllObjects retrieves all MOS objects and returns them as XML message structs (Profile 1)
func (s *MOSService) GetAllObjects(ctx context.Context) ([]xml.MosObj, error) {
	objects, err := s.objectRepo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list objects: %w", err)
	}

	mosObjs := make([]xml.MosObj, 0, len(objects))
	for _, obj := range objects {
		mosObj := xml.MosObj{
			ObjID:       obj.ID,
			ObjSlug:     obj.Slug,
			MosAbstract: obj.MosAbstract,
			ObjType:     obj.ObjectType,
			ObjTB:       obj.TimeBase,
			ObjDur:      obj.Duration,
			Status:      string(obj.Status),
			CreatedBy:   obj.Metadata["createdBy"],
			Created:     obj.CreatedAt.Format(time.RFC3339),
			ChangedBy:   obj.Metadata["changedBy"],
			Changed:     obj.UpdatedAt.Format(time.RFC3339),
			Description: obj.Metadata["description"],
		}
		mosObjs = append(mosObjs, mosObj)
	}

	return mosObjs, nil
}

// StoreObject stores or updates a MOS object from an XML message (Profile 1)
func (s *MOSService) StoreObject(ctx context.Context, mosObj xml.MosObj) error {
	// Check if object exists
	existingObj, err := s.objectRepo.Get(ctx, mosObj.ObjID)

	if err != nil {
		// Object does not exist, create it
		metadata := map[string]string{
			"createdBy":   mosObj.CreatedBy,
			"changedBy":   mosObj.ChangedBy,
			"description": mosObj.Description,
			"objGroup":    mosObj.ObjGroup,
			"objAir":      mosObj.ObjAir,
		}

		obj := &model.MOSObject{
			ID:          mosObj.ObjID,
			Slug:        mosObj.ObjSlug,
			MosAbstract: mosObj.MosAbstract,
			ObjectType:  mosObj.ObjType,
			TimeBase:    mosObj.ObjTB,
			Duration:    mosObj.ObjDur,
			Status:      model.StatusType(mosObj.Status),
			Metadata:    metadata,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}

		_, err = s.objectRepo.Create(ctx, obj)
		if err != nil {
			return fmt.Errorf("failed to create object: %w", err)
		}

		// Publish event
		if s.eventBus != nil {
			s.eventBus.Publish(events.Event{
				Type:    events.ObjectCreated,
				Payload: mosObj.ObjID,
				Source:  "mos_service",
			})
		}

		logger.Infof("Created MOS object %s", mosObj.ObjID)
	} else {
		// Object exists, update it
		existingObj.Slug = mosObj.ObjSlug
		existingObj.MosAbstract = mosObj.MosAbstract
		existingObj.ObjectType = mosObj.ObjType
		existingObj.TimeBase = mosObj.ObjTB
		existingObj.Duration = mosObj.ObjDur
		existingObj.UpdatedAt = time.Now()

		if mosObj.Status != "" {
			existingObj.Status = model.StatusType(mosObj.Status)
		}

		if existingObj.Metadata == nil {
			existingObj.Metadata = make(map[string]string)
		}
		if mosObj.ChangedBy != "" {
			existingObj.Metadata["changedBy"] = mosObj.ChangedBy
		}
		if mosObj.Description != "" {
			existingObj.Metadata["description"] = mosObj.Description
		}
		if mosObj.ObjGroup != "" {
			existingObj.Metadata["objGroup"] = mosObj.ObjGroup
		}

		err = s.objectRepo.Update(ctx, existingObj)
		if err != nil {
			return fmt.Errorf("failed to update object: %w", err)
		}

		// Publish event
		if s.eventBus != nil {
			s.eventBus.Publish(events.Event{
				Type:    events.ObjectUpdated,
				Payload: mosObj.ObjID,
				Source:  "mos_service",
			})
		}

		logger.Infof("Updated MOS object %s", mosObj.ObjID)
	}

	return nil
}

// ProcessRunningOrderInfo processes a running order creation/update message
func (s *MOSService) ProcessRunningOrderInfo(ctx context.Context, roInfo xml.RunningOrderInfo) error {
	// Check if running order exists
	existingRO, err := s.runningOrderRepo.Get(ctx, roInfo.ID)

	// Parse duration if provided
	var duration int
	if roInfo.Duration != "" {
		duration, err = strconv.Atoi(roInfo.Duration)
		if err != nil {
			duration = 0
		}
	}

	// Create or update running order
	if err != nil { // Running order doesn't exist
		// Create new running order
		ro := &model.RunningOrder{
			ID:        roInfo.ID,
			Slug:      roInfo.Slug,
			Status:    model.StatusPending,
			Duration:  duration,
			Channel:   roInfo.Channel,
			Version:   1,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
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

		err = s.runningOrderRepo.Update(ctx, existingRO)
		if err != nil {
			return fmt.Errorf("failed to update running order: %w", err)
		}
	}

	// Process stories (simplified - full implementation would handle deletions, etc.)
	for i, storyInfo := range roInfo.Stories {
		// Create or update each story
		story := &model.Story{
			ID:             storyInfo.ID,
			RunningOrderID: roInfo.ID,
			Slug:           storyInfo.Slug,
			Number:         storyInfo.Number,
			Status:         model.StatusPending,
			Order:          i + 1,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		}

		// Parse duration if provided
		if storyInfo.Duration != "" {
			if storyDuration, err := strconv.Atoi(storyInfo.Duration); err == nil {
				story.Duration = storyDuration
			}
		}

		// Create or update the story
		existingStory, err := s.storyRepo.Get(ctx, storyInfo.ID)
		if err != nil {
			// Story doesn't exist, create it
			_, err = s.storyRepo.Create(ctx, story)
			if err != nil {
				return fmt.Errorf("failed to create story: %w", err)
			}
		} else {
			// Story exists, update it
			existingStory.Slug = storyInfo.Slug
			existingStory.Number = storyInfo.Number
			existingStory.Order = i + 1
			existingStory.UpdatedAt = time.Now()

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
