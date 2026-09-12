package repository

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
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
	clone := cloneRunningOrder(ro)
	r.data[ro.ID] = clone
	return cloneRunningOrder(clone), nil
}

func (r *MemoryRunningOrderRepository) Get(_ context.Context, id string) (*model.RunningOrder, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ro, ok := r.data[id]
	if !ok {
		return nil, fmt.Errorf("running order %s not found", id)
	}
	return cloneRunningOrder(ro), nil
}

func (r *MemoryRunningOrderRepository) Update(_ context.Context, ro *model.RunningOrder) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data[ro.ID]; !ok {
		return fmt.Errorf("running order %s not found", ro.ID)
	}
	r.data[ro.ID] = cloneRunningOrder(ro)
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
		result = append(result, cloneRunningOrder(ro))
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
	clone := cloneStory(story)
	r.data[story.ID] = clone
	return cloneStory(clone), nil
}

func (r *MemoryStoryRepository) Get(_ context.Context, id string) (*model.Story, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.data[id]
	if !ok {
		return nil, fmt.Errorf("story %s not found", id)
	}
	return cloneStory(s), nil
}

func (r *MemoryStoryRepository) Update(_ context.Context, story *model.Story) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data[story.ID]; !ok {
		return fmt.Errorf("story %s not found", story.ID)
	}
	r.data[story.ID] = cloneStory(story)
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
			result = append(result, cloneStory(s))
		}
	}
	// Return stories in the order the NCS supplied, which for a rundown is play
	// order. This backend stores them in a map, so without an explicit sort the
	// order is whatever Go's map iteration produces -- different on every call.
	//
	// MOS 3.8.4: "Element order is significant. Items arrive in intended play order,
	// and a MOS device must retain the sequence supplied by the NCS even if it
	// executes items out of order." The Mongo backend already sorted by this field;
	// the in-memory one did not, which meant the DEFAULT backend silently reordered
	// rundowns and no test could see it.
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Order != result[j].Order {
			return result[i].Order < result[j].Order
		}
		// Ties should not happen, but if they do, stay deterministic rather than
		// falling back to map order.
		return result[i].ID < result[j].ID
	})
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
	clone := cloneItem(item)
	r.data[item.ID] = clone
	return cloneItem(clone), nil
}

func (r *MemoryItemRepository) Get(_ context.Context, id string) (*model.Item, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	i, ok := r.data[id]
	if !ok {
		return nil, fmt.Errorf("item %s not found", id)
	}
	return cloneItem(i), nil
}

func (r *MemoryItemRepository) Update(_ context.Context, item *model.Item) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data[item.ID]; !ok {
		return fmt.Errorf("item %s not found", item.ID)
	}
	r.data[item.ID] = cloneItem(item)
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
			result = append(result, cloneItem(i))
		}
	}
	// Items are ordered within their story for the same reason stories are ordered
	// within the running order: play order is meaning, not presentation.
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Order != result[j].Order {
			return result[i].Order < result[j].Order
		}
		return result[i].ID < result[j].ID
	})
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
	clone := cloneObject(obj)
	r.data[obj.ID] = clone
	return cloneObject(clone), nil
}

func (r *MemoryObjectRepository) Get(_ context.Context, id string) (*model.MOSObject, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	o, ok := r.data[id]
	if !ok {
		return nil, fmt.Errorf("object %s not found", id)
	}
	return cloneObject(o), nil
}

func (r *MemoryObjectRepository) Update(_ context.Context, obj *model.MOSObject) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data[obj.ID]; !ok {
		return fmt.Errorf("object %s not found", obj.ID)
	}
	r.data[obj.ID] = cloneObject(obj)
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
		result = append(result, cloneObject(o))
	}
	return result, nil
}

// Repository values are detached: callers may stage changes before Update or discard them.
func cloneRunningOrder(in *model.RunningOrder) *model.RunningOrder {
	out := *in
	out.Metadata = maps.Clone(in.Metadata)
	out.ExternalMetadata = slices.Clone(in.ExternalMetadata)
	if in.AirTime != nil {
		at := *in.AirTime
		out.AirTime = &at
	}
	if in.OnAirSince != nil {
		at := *in.OnAirSince
		out.OnAirSince = &at
	}
	return &out
}

func cloneStory(in *model.Story) *model.Story {
	out := *in
	out.Metadata = maps.Clone(in.Metadata)
	out.ExternalMetadata = slices.Clone(in.ExternalMetadata)
	out.Cues = slices.Clone(in.Cues)
	for i := range out.Cues {
		out.Cues[i].Fields = slices.Clone(in.Cues[i].Fields)
		out.Cues[i].Params = maps.Clone(in.Cues[i].Params)
	}
	return &out
}

func cloneItem(in *model.Item) *model.Item {
	out := *in
	out.Metadata = maps.Clone(in.Metadata)
	out.ExternalMetadata = slices.Clone(in.ExternalMetadata)
	if in.Media != nil {
		out.Media = &model.MediaPaths{
			Essence: slices.Clone(in.Media.Essence), Proxy: slices.Clone(in.Media.Proxy),
			Metadata: slices.Clone(in.Media.Metadata),
		}
	}
	return &out
}

func cloneObject(in *model.MOSObject) *model.MOSObject {
	out := *in
	out.Metadata = maps.Clone(in.Metadata)
	out.ExternalMetadata = slices.Clone(in.ExternalMetadata)
	return &out
}
