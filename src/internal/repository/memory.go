package repository

import (
	"context"
	"fmt"
	"sync"

	"airshift/openmos/internal/model"
)

// MemoryRunningOrderRepository is an in-memory implementation of RunningOrderRepository.
type MemoryRunningOrderRepository struct {
	mu   sync.RWMutex
	data map[string]*model.RunningOrder
}

func NewMemoryRunningOrderRepository() *MemoryRunningOrderRepository {
	return &MemoryRunningOrderRepository{data: make(map[string]*model.RunningOrder)}
}

func (r *MemoryRunningOrderRepository) Create(_ context.Context, ro *model.RunningOrder) (*model.RunningOrder, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.data[ro.ID]; exists {
		return nil, fmt.Errorf("running order %s already exists", ro.ID)
	}
	clone := *ro
	r.data[ro.ID] = &clone
	return &clone, nil
}

func (r *MemoryRunningOrderRepository) Get(_ context.Context, id string) (*model.RunningOrder, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ro, ok := r.data[id]
	if !ok {
		return nil, fmt.Errorf("running order %s not found", id)
	}
	clone := *ro
	return &clone, nil
}

func (r *MemoryRunningOrderRepository) Update(_ context.Context, ro *model.RunningOrder) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data[ro.ID]; !ok {
		return fmt.Errorf("running order %s not found", ro.ID)
	}
	clone := *ro
	r.data[ro.ID] = &clone
	return nil
}

func (r *MemoryRunningOrderRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.data, id)
	return nil
}

func (r *MemoryRunningOrderRepository) List(_ context.Context) ([]*model.RunningOrder, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*model.RunningOrder, 0, len(r.data))
	for _, ro := range r.data {
		clone := *ro
		result = append(result, &clone)
	}
	return result, nil
}

// MemoryStoryRepository is an in-memory implementation of StoryRepository.
type MemoryStoryRepository struct {
	mu   sync.RWMutex
	data map[string]*model.Story
}

func NewMemoryStoryRepository() *MemoryStoryRepository {
	return &MemoryStoryRepository{data: make(map[string]*model.Story)}
}

func (r *MemoryStoryRepository) Create(_ context.Context, story *model.Story) (*model.Story, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.data[story.ID]; exists {
		return nil, fmt.Errorf("story %s already exists", story.ID)
	}
	clone := *story
	r.data[story.ID] = &clone
	return &clone, nil
}

func (r *MemoryStoryRepository) Get(_ context.Context, id string) (*model.Story, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.data[id]
	if !ok {
		return nil, fmt.Errorf("story %s not found", id)
	}
	clone := *s
	return &clone, nil
}

func (r *MemoryStoryRepository) Update(_ context.Context, story *model.Story) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data[story.ID]; !ok {
		return fmt.Errorf("story %s not found", story.ID)
	}
	clone := *story
	r.data[story.ID] = &clone
	return nil
}

func (r *MemoryStoryRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.data, id)
	return nil
}

func (r *MemoryStoryRepository) DeleteMultiple(_ context.Context, ids []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		delete(r.data, id)
	}
	return nil
}

func (r *MemoryStoryRepository) ListByRunningOrder(_ context.Context, roID string) ([]*model.Story, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.Story
	for _, s := range r.data {
		if s.RunningOrderID == roID {
			clone := *s
			result = append(result, &clone)
		}
	}
	return result, nil
}

// MemoryItemRepository is an in-memory implementation of ItemRepository.
type MemoryItemRepository struct {
	mu   sync.RWMutex
	data map[string]*model.Item
}

func NewMemoryItemRepository() *MemoryItemRepository {
	return &MemoryItemRepository{data: make(map[string]*model.Item)}
}

func (r *MemoryItemRepository) Create(_ context.Context, item *model.Item) (*model.Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.data[item.ID]; exists {
		return nil, fmt.Errorf("item %s already exists", item.ID)
	}
	clone := *item
	r.data[item.ID] = &clone
	return &clone, nil
}

func (r *MemoryItemRepository) Get(_ context.Context, id string) (*model.Item, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	i, ok := r.data[id]
	if !ok {
		return nil, fmt.Errorf("item %s not found", id)
	}
	clone := *i
	return &clone, nil
}

func (r *MemoryItemRepository) Update(_ context.Context, item *model.Item) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data[item.ID]; !ok {
		return fmt.Errorf("item %s not found", item.ID)
	}
	clone := *item
	r.data[item.ID] = &clone
	return nil
}

func (r *MemoryItemRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.data, id)
	return nil
}

func (r *MemoryItemRepository) DeleteMultiple(_ context.Context, ids []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		delete(r.data, id)
	}
	return nil
}

func (r *MemoryItemRepository) ListByStory(_ context.Context, storyID string) ([]*model.Item, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.Item
	for _, i := range r.data {
		if i.StoryID == storyID {
			clone := *i
			result = append(result, &clone)
		}
	}
	return result, nil
}

// MemoryObjectRepository is an in-memory implementation of ObjectRepository.
type MemoryObjectRepository struct {
	mu   sync.RWMutex
	data map[string]*model.MOSObject
}

func NewMemoryObjectRepository() *MemoryObjectRepository {
	return &MemoryObjectRepository{data: make(map[string]*model.MOSObject)}
}

func (r *MemoryObjectRepository) Create(_ context.Context, obj *model.MOSObject) (*model.MOSObject, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.data[obj.ID]; exists {
		return nil, fmt.Errorf("object %s already exists", obj.ID)
	}
	clone := *obj
	r.data[obj.ID] = &clone
	return &clone, nil
}

func (r *MemoryObjectRepository) Get(_ context.Context, id string) (*model.MOSObject, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	o, ok := r.data[id]
	if !ok {
		return nil, fmt.Errorf("object %s not found", id)
	}
	clone := *o
	return &clone, nil
}

func (r *MemoryObjectRepository) Update(_ context.Context, obj *model.MOSObject) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data[obj.ID]; !ok {
		return fmt.Errorf("object %s not found", obj.ID)
	}
	clone := *obj
	r.data[obj.ID] = &clone
	return nil
}

func (r *MemoryObjectRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.data, id)
	return nil
}

func (r *MemoryObjectRepository) List(_ context.Context) ([]*model.MOSObject, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*model.MOSObject, 0, len(r.data))
	for _, o := range r.data {
		clone := *o
		result = append(result, &clone)
	}
	return result, nil
}
