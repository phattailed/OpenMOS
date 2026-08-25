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
	"airshift/openmos/pkg/logger"
	"airshift/openmos/pkg/utils"
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
		storyID := storyPersistenceID(roInfo.ID, storyInfo.ID)
		// Create or update each story
		story := &model.Story{
			ID:             storyID,
			RawID:          storyInfo.ID,
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

// --- Profile 2: Basic Running Order Workflow ---

// ReplaceRunningOrder replaces an entire running order (Profile 2)
// Deletes all existing stories for the RO and recreates from the replacement message
func (s *MOSService) ReplaceRunningOrder(ctx context.Context, roReplace xml.ROReplace) error {
	if err := validateRunningOrder(roReplace.ID, roReplace.Slug, roReplace.Stories); err != nil {
		return err
	}
	// Get existing stories for this RO and delete them
	existingStories, err := s.storyRepo.ListByRunningOrder(ctx, roReplace.ID)
	if err != nil {
		return fmt.Errorf("failed to list existing stories: %w", err)
	}
	for _, story := range existingStories {
		items, err := s.itemRepo.ListByStory(ctx, story.ID)
		if err != nil {
			return fmt.Errorf("failed to list items for story %s: %w", story.ID, err)
		}
		for _, item := range items {
			if err := s.itemRepo.Delete(ctx, item.ID); err != nil {
				return fmt.Errorf("failed to delete item %s: %w", item.ID, err)
			}
		}
		if err := s.storyRepo.Delete(ctx, story.ID); err != nil {
			return fmt.Errorf("failed to delete story %s: %w", story.ID, err)
		}
	}

	// Parse duration if provided
	var duration int
	if roReplace.EdDur != "" {
		duration = durationSeconds(roReplace.EdDur)
	}

	// Get or create the running order
	existingRO, err := s.runningOrderRepo.Get(ctx, roReplace.ID)
	if err != nil {
		// Create new running order
		ro := &model.RunningOrder{
			ID:        roReplace.ID,
			Slug:      roReplace.Slug,
			Status:    model.StatusPending,
			Duration:  duration,
			Channel:   roReplace.Channel,
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
		existingRO.Slug = roReplace.Slug
		existingRO.Channel = roReplace.Channel
		existingRO.Duration = duration
		existingRO.Version++
		existingRO.UpdatedAt = time.Now()

		err = s.runningOrderRepo.Update(ctx, existingRO)
		if err != nil {
			return fmt.Errorf("failed to update running order: %w", err)
		}
	}

	// Create new stories from the replacement
	for i, storyInfo := range roReplace.Stories {
		storyID := storyPersistenceID(roReplace.ID, storyInfo.ID)
		story := &model.Story{
			ID:             storyID,
			RawID:          storyInfo.ID,
			RunningOrderID: roReplace.ID,
			Slug:           storyInfo.Slug,
			Number:         storyInfo.Number,
			Status:         model.StatusPending,
			Order:          i + 1,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		}

		if storyInfo.Duration != "" {
			if storyDuration, err := strconv.Atoi(storyInfo.Duration); err == nil {
				story.Duration = storyDuration
			}
		}

		_, err = s.storyRepo.Create(ctx, story)
		if err != nil {
			return fmt.Errorf("failed to create replacement story: %w", err)
		}
		if err := s.storeItems(ctx, storyID, storyInfo.Items); err != nil {
			return err
		}
	}

	// Publish event
	if s.eventBus != nil {
		s.eventBus.Publish(events.Event{
			Type:    events.RunningOrderUpdated,
			Payload: roReplace.ID,
			Source:  "mos_service",
		})
	}

	logger.Infof("Replaced running order %s with %d stories", roReplace.ID, len(roReplace.Stories))
	return nil
}

func (s *MOSService) storeItems(ctx context.Context, storyID string, infos []xml.ItemInfo) error {
	for order, info := range infos {
		item := &model.Item{
			ID:        itemPersistenceID(storyID, info.ID),
			RawID:     info.ID,
			StoryID:   storyID,
			Slug:      info.Slug,
			ObjectID:  info.ObjectID,
			Status:    model.StatusPending,
			Order:     order + 1,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if info.Duration != "" {
			item.Duration, _ = strconv.Atoi(info.Duration)
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
		existing.Duration = item.Duration
		existing.Order = item.Order
		if err := s.itemRepo.Update(ctx, existing); err != nil {
			return fmt.Errorf("failed to update item %s: %w", info.ID, err)
		}
	}
	return nil
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

// DeleteRunningOrder deletes a running order and all associated stories/items (Profile 2)
func (s *MOSService) DeleteRunningOrder(ctx context.Context, roID string) error {
	if strings.TrimSpace(roID) == "" {
		return fmt.Errorf("roID is required")
	}

	// Delete all stories and their items
	stories, err := s.storyRepo.ListByRunningOrder(ctx, roID)
	if err != nil {
		return fmt.Errorf("failed to list stories for running order: %w", err)
	}
	for _, story := range stories {
		items, err := s.itemRepo.ListByStory(ctx, story.ID)
		if err != nil {
			return fmt.Errorf("failed to list items for story %s: %w", story.ID, err)
		}
		for _, item := range items {
			if err := s.itemRepo.Delete(ctx, item.ID); err != nil {
				return fmt.Errorf("failed to delete item %s: %w", item.ID, err)
			}
		}
		if err := s.storyRepo.Delete(ctx, story.ID); err != nil {
			return fmt.Errorf("failed to delete story %s: %w", story.ID, err)
		}
	}

	// Delete the running order itself
	if err := s.runningOrderRepo.Delete(ctx, roID); err != nil {
		return fmt.Errorf("failed to delete running order: %w", err)
	}

	// Publish event
	if s.eventBus != nil {
		s.eventBus.Publish(events.Event{
			Type:    events.RunningOrderUpdated,
			Payload: roID,
			Source:  "mos_service",
		})
	}

	logger.Infof("Deleted running order %s", roID)
	return nil
}

// ReplaceMetadata updates only the metadata of a running order without touching stories (Profile 2)
func (s *MOSService) ReplaceMetadata(ctx context.Context, roMeta xml.ROMetadataReplace) error {
	ro, err := s.runningOrderRepo.Get(ctx, roMeta.ID)
	if err != nil {
		return fmt.Errorf("running order not found: %w", err)
	}

	// Update metadata fields
	if roMeta.Slug != "" {
		ro.Slug = roMeta.Slug
	}
	if roMeta.Channel != "" {
		ro.Channel = roMeta.Channel
	}

	// Store additional metadata
	if ro.Metadata == nil {
		ro.Metadata = make(map[string]string)
	}
	if roMeta.EdStart != "" {
		ro.Metadata["edStart"] = roMeta.EdStart
	}
	if roMeta.EdDur != "" {
		ro.Metadata["edDur"] = roMeta.EdDur
		if dur, parseErr := strconv.Atoi(roMeta.EdDur); parseErr == nil {
			ro.Duration = dur
		}
	}
	if roMeta.Trigger != "" {
		ro.Metadata["trigger"] = roMeta.Trigger
	}
	if roMeta.MacroIn != "" {
		ro.Metadata["macroIn"] = roMeta.MacroIn
	}
	if roMeta.MacroOut != "" {
		ro.Metadata["macroOut"] = roMeta.MacroOut
	}

	ro.UpdatedAt = time.Now()

	err = s.runningOrderRepo.Update(ctx, ro)
	if err != nil {
		return fmt.Errorf("failed to update running order metadata: %w", err)
	}

	// Publish event
	if s.eventBus != nil {
		s.eventBus.Publish(events.Event{
			Type:    events.RunningOrderUpdated,
			Payload: roMeta.ID,
			Source:  "mos_service",
		})
	}

	logger.Infof("Replaced metadata for running order %s", roMeta.ID)
	return nil
}

// ListAllRunningOrdersCompact returns all running orders in compact format for roListAll (Profile 2)
func (s *MOSService) ListAllRunningOrdersCompact(ctx context.Context) ([]xml.ROListAllItem, error) {
	runningOrders, err := s.runningOrderRepo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list running orders: %w", err)
	}

	items := make([]xml.ROListAllItem, 0, len(runningOrders))
	for _, ro := range runningOrders {
		item := xml.ROListAllItem{
			ID:      ro.ID,
			Slug:    ro.Slug,
			Channel: ro.Channel,
		}

		// Include metadata fields if available
		if ro.Metadata != nil {
			item.EdStart = ro.Metadata["edStart"]
			item.EdDur = ro.Metadata["edDur"]
			item.Trigger = ro.Metadata["trigger"]
		}

		items = append(items, item)
	}

	return items, nil
}

// --- Profile 3: Advanced Object Based Workflow ---

// CreateObjectFromNCS creates a new MOS object from an NCS request (Profile 3)
// Returns the new object ID
func (s *MOSService) CreateObjectFromNCS(ctx context.Context, mosObjCreate xml.MosObjCreate) (string, error) {
	// Generate a unique object ID using crypto/rand
	objID, err := utils.GenerateID("OBJ")
	if err != nil {
		return "", fmt.Errorf("failed to generate object ID: %w", err)
	}

	metadata := map[string]string{
		"createdBy":   mosObjCreate.CreatedBy,
		"description": mosObjCreate.Description,
		"objGroup":    mosObjCreate.ObjGroup,
	}

	obj := &model.MOSObject{
		ID:         objID,
		Slug:       mosObjCreate.ObjSlug,
		ObjectType: mosObjCreate.ObjType,
		TimeBase:   mosObjCreate.ObjTB,
		Duration:   mosObjCreate.ObjDur,
		Status:     model.StatusPending,
		Metadata:   metadata,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	_, err = s.objectRepo.Create(ctx, obj)
	if err != nil {
		return "", fmt.Errorf("failed to create object: %w", err)
	}

	// Publish event
	if s.eventBus != nil {
		s.eventBus.Publish(events.Event{
			Type:    events.ObjectCreated,
			Payload: objID,
			Source:  "mos_service",
		})
	}

	logger.Infof("Created MOS object %s from NCS request (slug=%s)", objID, mosObjCreate.ObjSlug)
	return objID, nil
}

// ReplaceItemInStory replaces a specific item within a story (Profile 3)
func (s *MOSService) ReplaceItemInStory(ctx context.Context, mosItemReplace xml.MosItemReplace) error {
	// Verify the story exists
	_, err := s.storyRepo.Get(ctx, mosItemReplace.StoryID)
	if err != nil {
		return fmt.Errorf("story not found: %w", err)
	}

	// Get the existing item
	existingItem, err := s.itemRepo.Get(ctx, mosItemReplace.Item.ItemID)
	if err != nil {
		// Item does not exist, create it
		newItem := &model.Item{
			ID:        mosItemReplace.Item.ItemID,
			StoryID:   mosItemReplace.StoryID,
			Slug:      mosItemReplace.Item.ItemSlug,
			ObjectID:  mosItemReplace.Item.ObjID,
			Duration:  mosItemReplace.Item.ItemEdDur,
			Status:    model.StatusPending,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}

		_, err = s.itemRepo.Create(ctx, newItem)
		if err != nil {
			return fmt.Errorf("failed to create replacement item: %w", err)
		}
	} else {
		// Update existing item
		existingItem.Slug = mosItemReplace.Item.ItemSlug
		existingItem.ObjectID = mosItemReplace.Item.ObjID
		existingItem.Duration = mosItemReplace.Item.ItemEdDur
		existingItem.UpdatedAt = time.Now()

		if existingItem.Metadata == nil {
			existingItem.Metadata = make(map[string]string)
		}
		if mosItemReplace.Item.MosID != "" {
			existingItem.Metadata["mosID"] = mosItemReplace.Item.MosID
		}
		if mosItemReplace.Item.ItemChannel != "" {
			existingItem.Metadata["itemChannel"] = mosItemReplace.Item.ItemChannel
		}

		err = s.itemRepo.Update(ctx, existingItem)
		if err != nil {
			return fmt.Errorf("failed to update item: %w", err)
		}
	}

	// Publish event
	if s.eventBus != nil {
		s.eventBus.Publish(events.Event{
			Type:    events.ItemChanged,
			Payload: mosItemReplace.Item.ItemID,
			Source:  "mos_service",
		})
	}

	logger.Infof("Replaced item %s in story %s (RO %s)",
		mosItemReplace.Item.ItemID, mosItemReplace.StoryID, mosItemReplace.ROID)
	return nil
}
