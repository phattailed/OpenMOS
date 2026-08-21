package service

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"airshift/openmos/internal/events"
	"airshift/openmos/internal/model"
	"airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"
)

// ProcessElementAction dispatches a roElementAction based on the operation attribute (Profile 4)
func (s *MOSService) ProcessElementAction(ctx context.Context, action xml.ROElementAction) error {
	switch action.Operation {
	case "INSERT":
		return s.processInsert(ctx, action)
	case "REPLACE":
		return s.processReplace(ctx, action)
	case "MOVE":
		return s.processMove(ctx, action)
	case "DELETE":
		return s.processDelete(ctx, action)
	case "SWAP":
		return s.processSwap(ctx, action)
	default:
		return fmt.Errorf("unsupported roElementAction operation: %s", action.Operation)
	}
}

// processInsert handles INSERT operations for stories or items
func (s *MOSService) processInsert(ctx context.Context, action xml.ROElementAction) error {
	sourceType := action.Source.GetSourceType()

	switch sourceType {
	case "story":
		return s.InsertStories(ctx, action.ROID, action.Target, action.Source.Stories)
	case "item":
		if action.Target == nil || action.Target.StoryID == "" {
			return fmt.Errorf("INSERT item requires element_target with storyID")
		}
		return s.InsertItems(ctx, action.ROID, action.Target.StoryID, action.Target.ItemID, action.Source.Items)
	default:
		return fmt.Errorf("INSERT operation requires stories or items in element_source")
	}
}

// processReplace handles REPLACE operations for stories or items
func (s *MOSService) processReplace(ctx context.Context, action xml.ROElementAction) error {
	sourceType := action.Source.GetSourceType()

	switch sourceType {
	case "story":
		return s.ReplaceStories(ctx, action.ROID, action.Target, action.Source.Stories)
	case "item":
		if action.Target == nil || action.Target.StoryID == "" {
			return fmt.Errorf("REPLACE item requires element_target with storyID")
		}
		return s.ReplaceItems(ctx, action.ROID, action.Target.StoryID, action.Target.ItemID, action.Source.Items)
	default:
		return fmt.Errorf("REPLACE operation requires stories or items in element_source")
	}
}

// processMove handles MOVE operations for stories or items
func (s *MOSService) processMove(ctx context.Context, action xml.ROElementAction) error {
	sourceType := action.Source.GetSourceType()

	switch sourceType {
	case "storyID":
		return s.MoveStories(ctx, action.ROID, action.Target, action.Source.StoryIDs)
	case "itemID":
		if action.Target == nil || action.Target.StoryID == "" {
			return fmt.Errorf("MOVE item requires element_target with storyID")
		}
		return s.MoveItems(ctx, action.ROID, action.Target.StoryID, action.Target.ItemID, action.Source.ItemIDs)
	default:
		return fmt.Errorf("MOVE operation requires storyIDs or itemIDs in element_source")
	}
}

// processDelete handles DELETE operations for stories or items
func (s *MOSService) processDelete(ctx context.Context, action xml.ROElementAction) error {
	sourceType := action.Source.GetSourceType()

	switch sourceType {
	case "storyID":
		return s.DeleteStories(ctx, action.ROID, action.Source.StoryIDs)
	case "itemID":
		if action.Target == nil || action.Target.StoryID == "" {
			return fmt.Errorf("DELETE item requires element_target with storyID")
		}
		return s.DeleteItems(ctx, action.ROID, action.Target.StoryID, action.Source.ItemIDs)
	default:
		return fmt.Errorf("DELETE operation requires storyIDs or itemIDs in element_source")
	}
}

// processSwap handles SWAP operations for stories or items
func (s *MOSService) processSwap(ctx context.Context, action xml.ROElementAction) error {
	sourceType := action.Source.GetSourceType()

	switch sourceType {
	case "storyID":
		if len(action.Source.StoryIDs) != 2 {
			return fmt.Errorf("SWAP operation requires exactly 2 storyIDs")
		}
		return s.SwapStories(ctx, action.ROID, action.Source.StoryIDs[0], action.Source.StoryIDs[1])
	case "itemID":
		if len(action.Source.ItemIDs) != 2 {
			return fmt.Errorf("SWAP operation requires exactly 2 itemIDs")
		}
		if action.Target == nil || action.Target.StoryID == "" {
			return fmt.Errorf("SWAP item requires element_target with storyID")
		}
		return s.SwapItems(ctx, action.ROID, action.Target.StoryID, action.Source.ItemIDs[0], action.Source.ItemIDs[1])
	default:
		return fmt.Errorf("SWAP operation requires storyIDs or itemIDs in element_source")
	}
}

// InsertStories inserts stories into a running order at the position indicated by the target
func (s *MOSService) InsertStories(ctx context.Context, roID string, target *xml.ElementTarget, stories []xml.StoryInfo) error {
	// Determine insert position
	insertAfterOrder := 0
	if target != nil && target.StoryID != "" {
		targetStory, err := s.storyRepo.Get(ctx, target.StoryID)
		if err != nil {
			return fmt.Errorf("target story not found: %w", err)
		}
		insertAfterOrder = targetStory.Order
	}

	// Shift existing stories to make room
	existingStories, err := s.storyRepo.ListByRunningOrder(ctx, roID)
	if err != nil {
		return fmt.Errorf("failed to list stories: %w", err)
	}

	// Shift stories that come after the insert point
	for _, existing := range existingStories {
		if existing.Order > insertAfterOrder {
			existing.Order += len(stories)
			existing.UpdatedAt = time.Now()
			if err := s.storyRepo.Update(ctx, existing); err != nil {
				return fmt.Errorf("failed to shift story order: %w", err)
			}
		}
	}

	// Insert the new stories
	for i, storyInfo := range stories {
		story := &model.Story{
			ID:             storyInfo.ID,
			RunningOrderID: roID,
			Slug:           storyInfo.Slug,
			Number:         storyInfo.Number,
			Status:         model.StatusPending,
			Order:          insertAfterOrder + i + 1,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		}

		_, err := s.storyRepo.Create(ctx, story)
		if err != nil {
			return fmt.Errorf("failed to insert story %s: %w", storyInfo.ID, err)
		}

		// Insert items for this story
		for j, itemInfo := range storyInfo.Items {
			item := &model.Item{
				ID:        itemInfo.ID,
				StoryID:   storyInfo.ID,
				Slug:      itemInfo.Slug,
				ObjectID:  itemInfo.ObjectID,
				Order:     j + 1,
				Status:    model.StatusPending,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			if itemInfo.Duration != "" {
				if dur, parseErr := strconv.Atoi(itemInfo.Duration); parseErr == nil {
					item.Duration = dur
				}
			}
			if _, err := s.itemRepo.Create(ctx, item); err != nil {
				logger.Warningf("Failed to insert item %s for story %s: %v", itemInfo.ID, storyInfo.ID, err)
			}
		}
	}

	s.publishROUpdate(roID)
	logger.Infof("Inserted %d stories into RO %s after position %d", len(stories), roID, insertAfterOrder)
	return nil
}

// InsertItems inserts items into a story at the specified position
func (s *MOSService) InsertItems(ctx context.Context, roID, storyID, beforeItemID string, items []xml.ItemInfo) error {
	// Determine insert position
	insertAfterOrder := 0
	if beforeItemID != "" {
		targetItem, err := s.itemRepo.Get(ctx, beforeItemID)
		if err != nil {
			return fmt.Errorf("target item not found: %w", err)
		}
		insertAfterOrder = targetItem.Order
	}

	// Shift existing items
	existingItems, err := s.itemRepo.ListByStory(ctx, storyID)
	if err != nil {
		return fmt.Errorf("failed to list items: %w", err)
	}

	for _, existing := range existingItems {
		if existing.Order > insertAfterOrder {
			existing.Order += len(items)
			existing.UpdatedAt = time.Now()
			if err := s.itemRepo.Update(ctx, existing); err != nil {
				return fmt.Errorf("failed to shift item order: %w", err)
			}
		}
	}

	// Insert new items
	for i, itemInfo := range items {
		item := &model.Item{
			ID:        itemInfo.ID,
			StoryID:   storyID,
			Slug:      itemInfo.Slug,
			ObjectID:  itemInfo.ObjectID,
			Order:     insertAfterOrder + i + 1,
			Status:    model.StatusPending,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if itemInfo.Duration != "" {
			if dur, parseErr := strconv.Atoi(itemInfo.Duration); parseErr == nil {
				item.Duration = dur
			}
		}

		if _, err := s.itemRepo.Create(ctx, item); err != nil {
			return fmt.Errorf("failed to insert item %s: %w", itemInfo.ID, err)
		}
	}

	s.publishROUpdate(roID)
	logger.Infof("Inserted %d items into story %s in RO %s", len(items), storyID, roID)
	return nil
}

// ReplaceStories replaces the target story with the provided stories
func (s *MOSService) ReplaceStories(ctx context.Context, roID string, target *xml.ElementTarget, stories []xml.StoryInfo) error {
	if target == nil || target.StoryID == "" {
		return fmt.Errorf("REPLACE story requires element_target with storyID")
	}

	// Get the target story to determine its position
	targetStory, err := s.storyRepo.Get(ctx, target.StoryID)
	if err != nil {
		return fmt.Errorf("target story not found: %w", err)
	}
	replaceOrder := targetStory.Order

	// Delete the target story and its items
	items, err := s.itemRepo.ListByStory(ctx, target.StoryID)
	if err == nil {
		for _, item := range items {
			_ = s.itemRepo.Delete(ctx, item.ID)
		}
	}
	if err := s.storyRepo.Delete(ctx, target.StoryID); err != nil {
		return fmt.Errorf("failed to delete target story: %w", err)
	}

	// If replacing with multiple stories, shift others
	if len(stories) > 1 {
		existingStories, err := s.storyRepo.ListByRunningOrder(ctx, roID)
		if err != nil {
			return fmt.Errorf("failed to list stories: %w", err)
		}
		for _, existing := range existingStories {
			if existing.Order > replaceOrder {
				existing.Order += len(stories) - 1
				existing.UpdatedAt = time.Now()
				if err := s.storyRepo.Update(ctx, existing); err != nil {
					return fmt.Errorf("failed to shift story order: %w", err)
				}
			}
		}
	}

	// Insert replacement stories
	for i, storyInfo := range stories {
		story := &model.Story{
			ID:             storyInfo.ID,
			RunningOrderID: roID,
			Slug:           storyInfo.Slug,
			Number:         storyInfo.Number,
			Status:         model.StatusPending,
			Order:          replaceOrder + i,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		}

		if _, err := s.storyRepo.Create(ctx, story); err != nil {
			return fmt.Errorf("failed to create replacement story %s: %w", storyInfo.ID, err)
		}

		// Insert items for this story
		for j, itemInfo := range storyInfo.Items {
			item := &model.Item{
				ID:        itemInfo.ID,
				StoryID:   storyInfo.ID,
				Slug:      itemInfo.Slug,
				ObjectID:  itemInfo.ObjectID,
				Order:     j + 1,
				Status:    model.StatusPending,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			if itemInfo.Duration != "" {
				if dur, parseErr := strconv.Atoi(itemInfo.Duration); parseErr == nil {
					item.Duration = dur
				}
			}
			if _, err := s.itemRepo.Create(ctx, item); err != nil {
				logger.Warningf("Failed to insert item %s: %v", itemInfo.ID, err)
			}
		}
	}

	s.publishROUpdate(roID)
	logger.Infof("Replaced story %s in RO %s with %d stories", target.StoryID, roID, len(stories))
	return nil
}

// ReplaceItems replaces the target item with the provided items
func (s *MOSService) ReplaceItems(ctx context.Context, roID, storyID, targetItemID string, items []xml.ItemInfo) error {
	if targetItemID == "" {
		return fmt.Errorf("REPLACE item requires target itemID")
	}

	// Get the target item to determine its position
	targetItem, err := s.itemRepo.Get(ctx, targetItemID)
	if err != nil {
		return fmt.Errorf("target item not found: %w", err)
	}
	replaceOrder := targetItem.Order

	// Delete the target item
	if err := s.itemRepo.Delete(ctx, targetItemID); err != nil {
		return fmt.Errorf("failed to delete target item: %w", err)
	}

	// If replacing with multiple items, shift others
	if len(items) > 1 {
		existingItems, err := s.itemRepo.ListByStory(ctx, storyID)
		if err != nil {
			return fmt.Errorf("failed to list items: %w", err)
		}
		for _, existing := range existingItems {
			if existing.Order > replaceOrder {
				existing.Order += len(items) - 1
				existing.UpdatedAt = time.Now()
				if err := s.itemRepo.Update(ctx, existing); err != nil {
					return fmt.Errorf("failed to shift item order: %w", err)
				}
			}
		}
	}

	// Insert replacement items
	for i, itemInfo := range items {
		item := &model.Item{
			ID:        itemInfo.ID,
			StoryID:   storyID,
			Slug:      itemInfo.Slug,
			ObjectID:  itemInfo.ObjectID,
			Order:     replaceOrder + i,
			Status:    model.StatusPending,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if itemInfo.Duration != "" {
			if dur, parseErr := strconv.Atoi(itemInfo.Duration); parseErr == nil {
				item.Duration = dur
			}
		}

		if _, err := s.itemRepo.Create(ctx, item); err != nil {
			return fmt.Errorf("failed to create replacement item %s: %w", itemInfo.ID, err)
		}
	}

	s.publishROUpdate(roID)
	logger.Infof("Replaced item %s in story %s with %d items", targetItemID, storyID, len(items))
	return nil
}

// MoveStories moves stories to a new position in the running order
func (s *MOSService) MoveStories(ctx context.Context, roID string, target *xml.ElementTarget, storyIDs []string) error {
	// Determine the target position (insert after this story)
	targetOrder := 0
	if target != nil && target.StoryID != "" {
		targetStory, err := s.storyRepo.Get(ctx, target.StoryID)
		if err != nil {
			return fmt.Errorf("target story not found: %w", err)
		}
		targetOrder = targetStory.Order
	}

	// Get all stories in the RO
	allStories, err := s.storyRepo.ListByRunningOrder(ctx, roID)
	if err != nil {
		return fmt.Errorf("failed to list stories: %w", err)
	}

	// Build a set of stories being moved
	moveSet := make(map[string]bool)
	for _, id := range storyIDs {
		moveSet[id] = true
	}

	// Remove moved stories from their positions and reorder remaining
	var remaining []*model.Story
	var moving []*model.Story
	for _, story := range allStories {
		if moveSet[story.ID] {
			moving = append(moving, story)
		} else {
			remaining = append(remaining, story)
		}
	}

	// Determine the new insert position within remaining stories
	insertIdx := 0
	for i, story := range remaining {
		if story.Order <= targetOrder {
			insertIdx = i + 1
		}
	}

	// Build new order: remaining[:insertIdx] + moving + remaining[insertIdx:]
	newOrder := make([]*model.Story, 0, len(allStories))
	newOrder = append(newOrder, remaining[:insertIdx]...)
	newOrder = append(newOrder, moving...)
	newOrder = append(newOrder, remaining[insertIdx:]...)

	// Update orders
	for i, story := range newOrder {
		story.Order = i + 1
		story.UpdatedAt = time.Now()
		if err := s.storyRepo.Update(ctx, story); err != nil {
			return fmt.Errorf("failed to update story order: %w", err)
		}
	}

	s.publishROUpdate(roID)
	logger.Infof("Moved %d stories in RO %s to position after %d", len(storyIDs), roID, targetOrder)
	return nil
}

// MoveItems moves items to a new position within a story
func (s *MOSService) MoveItems(ctx context.Context, roID, storyID, targetItemID string, itemIDs []string) error {
	// Determine the target position
	targetOrder := 0
	if targetItemID != "" {
		targetItem, err := s.itemRepo.Get(ctx, targetItemID)
		if err != nil {
			return fmt.Errorf("target item not found: %w", err)
		}
		targetOrder = targetItem.Order
	}

	// Get all items in the story
	allItems, err := s.itemRepo.ListByStory(ctx, storyID)
	if err != nil {
		return fmt.Errorf("failed to list items: %w", err)
	}

	// Build a set of items being moved
	moveSet := make(map[string]bool)
	for _, id := range itemIDs {
		moveSet[id] = true
	}

	// Separate moving items from remaining
	var remaining []*model.Item
	var moving []*model.Item
	for _, item := range allItems {
		if moveSet[item.ID] {
			moving = append(moving, item)
		} else {
			remaining = append(remaining, item)
		}
	}

	// Determine insert position within remaining
	insertIdx := 0
	for i, item := range remaining {
		if item.Order <= targetOrder {
			insertIdx = i + 1
		}
	}

	// Build new order
	newOrder := make([]*model.Item, 0, len(allItems))
	newOrder = append(newOrder, remaining[:insertIdx]...)
	newOrder = append(newOrder, moving...)
	newOrder = append(newOrder, remaining[insertIdx:]...)

	// Update orders
	for i, item := range newOrder {
		item.Order = i + 1
		item.UpdatedAt = time.Now()
		if err := s.itemRepo.Update(ctx, item); err != nil {
			return fmt.Errorf("failed to update item order: %w", err)
		}
	}

	s.publishROUpdate(roID)
	logger.Infof("Moved %d items in story %s in RO %s", len(itemIDs), storyID, roID)
	return nil
}

// DeleteStories deletes stories from a running order by their IDs
func (s *MOSService) DeleteStories(ctx context.Context, roID string, storyIDs []string) error {
	// Collect all item IDs to delete across all stories being removed
	var allItemIDs []string
	for _, storyID := range storyIDs {
		items, err := s.itemRepo.ListByStory(ctx, storyID)
		if err == nil {
			for _, item := range items {
				allItemIDs = append(allItemIDs, item.ID)
			}
		}
	}

	// Batch delete items
	if len(allItemIDs) > 0 {
		if err := s.itemRepo.DeleteMultiple(ctx, allItemIDs); err != nil {
			return fmt.Errorf("failed to delete items for stories: %w", err)
		}
	}

	// Batch delete stories
	if err := s.storyRepo.DeleteMultiple(ctx, storyIDs); err != nil {
		return fmt.Errorf("failed to delete stories: %w", err)
	}

	// Reorder remaining stories
	remainingStories, err := s.storyRepo.ListByRunningOrder(ctx, roID)
	if err == nil {
		for i, story := range remainingStories {
			story.Order = i + 1
			story.UpdatedAt = time.Now()
			_ = s.storyRepo.Update(ctx, story)
		}
	}

	s.publishROUpdate(roID)
	logger.Infof("Deleted %d stories from RO %s", len(storyIDs), roID)
	return nil
}

// DeleteItems deletes items from a story by their IDs
func (s *MOSService) DeleteItems(ctx context.Context, roID, storyID string, itemIDs []string) error {
	if err := s.itemRepo.DeleteMultiple(ctx, itemIDs); err != nil {
		return fmt.Errorf("failed to delete items: %w", err)
	}

	// Reorder remaining items
	remainingItems, err := s.itemRepo.ListByStory(ctx, storyID)
	if err == nil {
		for i, item := range remainingItems {
			item.Order = i + 1
			item.UpdatedAt = time.Now()
			_ = s.itemRepo.Update(ctx, item)
		}
	}

	s.publishROUpdate(roID)
	logger.Infof("Deleted %d items from story %s in RO %s", len(itemIDs), storyID, roID)
	return nil
}

// SwapStories swaps the positions of two stories in a running order
func (s *MOSService) SwapStories(ctx context.Context, roID, storyID1, storyID2 string) error {
	story1, err := s.storyRepo.Get(ctx, storyID1)
	if err != nil {
		return fmt.Errorf("story %s not found: %w", storyID1, err)
	}

	story2, err := s.storyRepo.Get(ctx, storyID2)
	if err != nil {
		return fmt.Errorf("story %s not found: %w", storyID2, err)
	}

	// Swap orders
	story1.Order, story2.Order = story2.Order, story1.Order
	story1.UpdatedAt = time.Now()
	story2.UpdatedAt = time.Now()

	if err := s.storyRepo.Update(ctx, story1); err != nil {
		return fmt.Errorf("failed to update story %s: %w", storyID1, err)
	}
	if err := s.storyRepo.Update(ctx, story2); err != nil {
		return fmt.Errorf("failed to update story %s: %w", storyID2, err)
	}

	s.publishROUpdate(roID)
	logger.Infof("Swapped stories %s and %s in RO %s", storyID1, storyID2, roID)
	return nil
}

// SwapItems swaps the positions of two items within a story
func (s *MOSService) SwapItems(ctx context.Context, roID, storyID, itemID1, itemID2 string) error {
	item1, err := s.itemRepo.Get(ctx, itemID1)
	if err != nil {
		return fmt.Errorf("item %s not found: %w", itemID1, err)
	}

	item2, err := s.itemRepo.Get(ctx, itemID2)
	if err != nil {
		return fmt.Errorf("item %s not found: %w", itemID2, err)
	}

	// Swap orders
	item1.Order, item2.Order = item2.Order, item1.Order
	item1.UpdatedAt = time.Now()
	item2.UpdatedAt = time.Now()

	if err := s.itemRepo.Update(ctx, item1); err != nil {
		return fmt.Errorf("failed to update item %s: %w", itemID1, err)
	}
	if err := s.itemRepo.Update(ctx, item2); err != nil {
		return fmt.Errorf("failed to update item %s: %w", itemID2, err)
	}

	s.publishROUpdate(roID)
	logger.Infof("Swapped items %s and %s in story %s in RO %s", itemID1, itemID2, storyID, roID)
	return nil
}

// SetReadyToAir sets the ready-to-air status of a running order (Profile 4)
func (s *MOSService) SetReadyToAir(ctx context.Context, roID, roAir string) error {
	ro, err := s.runningOrderRepo.Get(ctx, roID)
	if err != nil {
		return fmt.Errorf("running order not found: %w", err)
	}

	if ro.Metadata == nil {
		ro.Metadata = make(map[string]string)
	}
	ro.Metadata["roAir"] = roAir
	ro.UpdatedAt = time.Now()

	if err := s.runningOrderRepo.Update(ctx, ro); err != nil {
		return fmt.Errorf("failed to update running order: %w", err)
	}

	s.publishROUpdate(roID)
	logger.Infof("Set RO %s ready-to-air status: %s", roID, roAir)
	return nil
}

// ReportElementStatus records the status of an element in a running order (Profile 4)
func (s *MOSService) ReportElementStatus(ctx context.Context, stat xml.ROElementStat) error {
	// Update the item status if the item exists
	if stat.ItemID != "" {
		item, err := s.itemRepo.Get(ctx, stat.ItemID)
		if err == nil {
			item.Status = model.StatusType(stat.Status)
			item.UpdatedAt = time.Now()
			if item.Metadata == nil {
				item.Metadata = make(map[string]string)
			}
			item.Metadata["lastStatusTime"] = stat.Time
			if stat.ItemChannel != "" {
				item.Metadata["itemChannel"] = stat.ItemChannel
			}
			if err := s.itemRepo.Update(ctx, item); err != nil {
				return fmt.Errorf("failed to update item status: %w", err)
			}
		}
	}

	// Publish event for status change
	if s.eventBus != nil {
		s.eventBus.Publish(events.Event{
			Type:    events.ItemChanged,
			Payload: stat.ItemID,
			Source:  "mos_service",
		})
	}

	logger.Infof("Element status report: RO %s, item %s, status %s", stat.ROID, stat.ItemID, stat.Status)
	return nil
}

// publishROUpdate publishes a running order update event
func (s *MOSService) publishROUpdate(roID string) {
	if s.eventBus != nil {
		s.eventBus.Publish(events.Event{
			Type:    events.RunningOrderUpdated,
			Payload: roID,
			Source:  "mos_service",
		})
	}
}
