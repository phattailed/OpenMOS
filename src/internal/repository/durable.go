package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"airshift/openmos/internal/model"
	"airshift/openmos/pkg/logger"
)

// File-backed running orders, by writing through to a snapshot rather than reimplementing storage.
//
// Why the rundown needs to survive a restart: protocol state already does -- the outbound
// messageID, deduplication receipts and unfinished discovery work -- but the running orders
// themselves did not. A restart therefore left OpenMOS silently disagreeing with the NCS about
// what it holds, and the NCS has no reason to tell us again. That is precisely how the
// roStorySend defect in doc/interop §13 stayed hidden: the running order was gone from our side
// and the fabrication looked like success.
//
// Why a decorator instead of a third backend: the in-memory repositories already implement every
// method correctly, including the element ordering that was itself a defect once -- the in-memory
// backend used to return Go map order while Mongo sorted properly. Reimplementing 22 methods of
// query logic would invite the two to diverge again. This composes them, exactly as
// FileDedupStore composes MemoryDedupStore.
//
// Why a snapshot instead of an append log: a rundown changes far less often than a message
// arrives, and interop-scale rundowns are tens of stories rather than thousands. The deduplication
// store needed a log because it writes on every inbound message; this does not. Temp-file-and-
// rename, as with the messageID mark.

// snapshot is the persisted shape. Slices, not maps, because element order is significant in MOS
// and a map would discard it -- the same trap the in-memory backend fell into.
type snapshot struct {
	RunningOrders []*model.RunningOrder `json:"runningOrders"`
	Stories       []*model.Story        `json:"stories"`
	Items         []*model.Item         `json:"items"`
}

// Durable wraps in-memory repositories and persists their contents after every mutation.
type Durable struct {
	runningOrders RunningOrderRepository
	stories       StoryRepository
	items         ItemRepository
	objects       ObjectRepository

	mu       sync.Mutex
	path     string
	degraded bool
}

// OpenDurable builds in-memory repositories, loads any previous snapshot from dir, and returns
// wrappers that persist after each change.
//
// An unusable directory degrades to plain in-memory with a loud warning rather than refusing to
// start, matching internal/messageid and FileDedupStore. Losing durability costs a
// resynchronisation, which the protocol can recover by asking; failing to start costs everything.
func OpenDurable(dir string) *Durable {
	d := &Durable{
		runningOrders: NewMemoryRunningOrderRepository(),
		stories:       NewMemoryStoryRepository(),
		items:         NewMemoryItemRepository(),
		objects:       NewMemoryObjectRepository(),
	}

	if dir == "" {
		d.degraded = true
		return d
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.Warningf("Running orders will NOT survive a restart: cannot create state directory "+
			"%s: %v. After a restart OpenMOS will disagree with the NCS about what it holds, and "+
			"the NCS has no reason to say again.", dir, err)
		d.degraded = true
		return d
	}
	d.path = filepath.Join(dir, "runningorders.json")

	if loaded, err := d.load(); err != nil {
		// A corrupt snapshot is reported and skipped rather than fatal. Recovery by asking the
		// NCS is a supported path; refusing to start is not.
		logger.Warningf("Ignoring unreadable running-order snapshot at %s: %v. Local state starts "+
			"empty and will be rebuilt by pull recovery.", d.path, err)
	} else if loaded > 0 {
		logger.Infof("Recovered %d running orders from %s", loaded, d.path)
	}
	return d
}

func (d *Durable) RunningOrders() RunningOrderRepository { return &durableRunningOrders{d} }
func (d *Durable) Stories() StoryRepository              { return &durableStories{d} }
func (d *Durable) Items() ItemRepository                 { return &durableItems{d} }

// Objects is not persisted. OpenMOS implements no object workflow -- Profiles 1 and 3 are not
// claimed -- so there is nothing durable to keep, and pretending otherwise would imply support
// that does not exist.
func (d *Durable) Objects() ObjectRepository { return d.objects }

// Degraded reports that changes are not being persisted.
func (d *Durable) Degraded() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.degraded
}

// load replays a snapshot into the in-memory repositories through their public constructors, so
// recovered state goes through exactly the same code path as live traffic.
func (d *Durable) load() (int, error) {
	raw, err := os.ReadFile(d.path)
	if err != nil {
		return 0, nil // absent is the normal first-run case
	}

	var snap snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return 0, fmt.Errorf("parse snapshot: %w", err)
	}

	ctx := context.Background()
	for _, ro := range snap.RunningOrders {
		if _, err := d.runningOrders.Create(ctx, ro); err != nil {
			return 0, fmt.Errorf("restore running order %s: %w", ro.ID, err)
		}
	}
	// Stories and items are restored in snapshot order, which is the order they were listed in,
	// which is the NCS-supplied order. That ordering is required to be retained.
	for _, story := range snap.Stories {
		if _, err := d.stories.Create(ctx, story); err != nil {
			return 0, fmt.Errorf("restore story %s: %w", story.ID, err)
		}
	}
	for _, item := range snap.Items {
		if _, err := d.items.Create(ctx, item); err != nil {
			return 0, fmt.Errorf("restore item %s: %w", item.ID, err)
		}
	}
	return len(snap.RunningOrders), nil
}

// persist walks current state through the public list methods and writes it.
//
// Walking rather than reaching into the memory repositories keeps this honest: whatever the
// repositories will actually return to a peer is what gets saved.
func (d *Durable) persist(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.degraded || d.path == "" {
		return
	}

	ros, err := d.runningOrders.List(ctx)
	if err != nil {
		logger.Errorf("Cannot snapshot running orders: %v", err)
		return
	}

	snap := snapshot{RunningOrders: ros}
	for _, ro := range ros {
		stories, err := d.stories.ListByRunningOrder(ctx, ro.ID)
		if err != nil {
			logger.Errorf("Cannot snapshot stories for %s: %v", ro.ID, err)
			return
		}
		snap.Stories = append(snap.Stories, stories...)
		for _, story := range stories {
			items, err := d.items.ListByStory(ctx, story.ID)
			if err != nil {
				logger.Errorf("Cannot snapshot items for %s: %v", story.ID, err)
				return
			}
			snap.Items = append(snap.Items, items...)
		}
	}

	raw, err := json.Marshal(snap)
	if err != nil {
		logger.Errorf("Cannot encode running-order snapshot: %v", err)
		return
	}

	tmp := d.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		logger.Warningf("Running orders stopped persisting: cannot write %s: %v", tmp, err)
		d.degraded = true
		return
	}
	if err := os.Rename(tmp, d.path); err != nil {
		_ = os.Remove(tmp)
		logger.Warningf("Running orders stopped persisting: cannot replace %s: %v", d.path, err)
		d.degraded = true
	}
}

// The wrappers below delegate reads straight through and snapshot after successful writes. A
// failed write is not persisted, so the snapshot never records a change that did not happen.

type durableRunningOrders struct{ d *Durable }

func (w *durableRunningOrders) Create(ctx context.Context, ro *model.RunningOrder) (*model.RunningOrder, error) {
	out, err := w.d.runningOrders.Create(ctx, ro)
	if err == nil {
		w.d.persist(ctx)
	}
	return out, err
}

func (w *durableRunningOrders) Get(ctx context.Context, id string) (*model.RunningOrder, error) {
	return w.d.runningOrders.Get(ctx, id)
}

func (w *durableRunningOrders) Update(ctx context.Context, ro *model.RunningOrder) error {
	err := w.d.runningOrders.Update(ctx, ro)
	if err == nil {
		w.d.persist(ctx)
	}
	return err
}

func (w *durableRunningOrders) Delete(ctx context.Context, id string) error {
	err := w.d.runningOrders.Delete(ctx, id)
	if err == nil {
		w.d.persist(ctx)
	}
	return err
}

func (w *durableRunningOrders) List(ctx context.Context) ([]*model.RunningOrder, error) {
	return w.d.runningOrders.List(ctx)
}

type durableStories struct{ d *Durable }

func (w *durableStories) Create(ctx context.Context, story *model.Story) (*model.Story, error) {
	out, err := w.d.stories.Create(ctx, story)
	if err == nil {
		w.d.persist(ctx)
	}
	return out, err
}

func (w *durableStories) Get(ctx context.Context, id string) (*model.Story, error) {
	return w.d.stories.Get(ctx, id)
}

func (w *durableStories) Update(ctx context.Context, story *model.Story) error {
	err := w.d.stories.Update(ctx, story)
	if err == nil {
		w.d.persist(ctx)
	}
	return err
}

func (w *durableStories) Delete(ctx context.Context, id string) error {
	err := w.d.stories.Delete(ctx, id)
	if err == nil {
		w.d.persist(ctx)
	}
	return err
}

func (w *durableStories) DeleteMultiple(ctx context.Context, ids []string) error {
	err := w.d.stories.DeleteMultiple(ctx, ids)
	if err == nil {
		w.d.persist(ctx)
	}
	return err
}

func (w *durableStories) ListByRunningOrder(ctx context.Context, roID string) ([]*model.Story, error) {
	return w.d.stories.ListByRunningOrder(ctx, roID)
}

type durableItems struct{ d *Durable }

func (w *durableItems) Create(ctx context.Context, item *model.Item) (*model.Item, error) {
	out, err := w.d.items.Create(ctx, item)
	if err == nil {
		w.d.persist(ctx)
	}
	return out, err
}

func (w *durableItems) Get(ctx context.Context, id string) (*model.Item, error) {
	return w.d.items.Get(ctx, id)
}

func (w *durableItems) Update(ctx context.Context, item *model.Item) error {
	err := w.d.items.Update(ctx, item)
	if err == nil {
		w.d.persist(ctx)
	}
	return err
}

func (w *durableItems) Delete(ctx context.Context, id string) error {
	err := w.d.items.Delete(ctx, id)
	if err == nil {
		w.d.persist(ctx)
	}
	return err
}

func (w *durableItems) DeleteMultiple(ctx context.Context, ids []string) error {
	err := w.d.items.DeleteMultiple(ctx, ids)
	if err == nil {
		w.d.persist(ctx)
	}
	return err
}

func (w *durableItems) ListByStory(ctx context.Context, storyID string) ([]*model.Item, error) {
	return w.d.items.ListByStory(ctx, storyID)
}
