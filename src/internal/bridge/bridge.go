package bridge

import (
	"context"
	"sort"
	"sync"
	"time"

	"airshift/openmos/internal/events"
	"airshift/openmos/internal/model"
	"airshift/openmos/pkg/logger"
)

// RundownReader is the read surface the bridge needs from the MOS core. It is a
// subset of *service.MOSService, declared here so the bridge depends on behaviour
// rather than the concrete service, which also lets tests supply a fake without
// standing up repositories or an event bus.
type RundownReader interface {
	ListRunningOrders(ctx context.Context) ([]*model.RunningOrder, error)
	GetRunningOrderWithStories(ctx context.Context, id string) (*model.RunningOrder, []*model.Story, error)
	GetItemsForStory(ctx context.Context, storyID string) ([]*model.Item, error)
}

// changeEvents are the event types that mean "the rundown the operator sees may
// have changed". Any of them triggers a rebuild. Object create/update events are
// deliberately excluded: they touch the object library, not running-order layout.
var changeEvents = []events.EventType{
	events.RunningOrderUpdated,
	events.StoryModified,
	events.StoryReceived,
	events.ItemChanged,
	events.ItemControlled,
	events.ItemCued,
}

// Bridge subscribes to rundown changes, rebuilds a snapshot from the reader and
// holds the latest one for the renderers to serve. It owns no MOS state; the
// snapshot is derived data that can be discarded and rebuilt at any time.
type Bridge struct {
	reader RundownReader
	fields []string

	mu       sync.RWMutex
	snapshot Snapshot

	// onChange is invoked with each new snapshot after it is stored, used to drive
	// side effects such as writing the CSV file. Optional.
	onChange func(Snapshot)
}

// New builds a bridge over the given reader, rendering the given field list
// (falling back to the default set when empty).
func New(reader RundownReader, fields []string) *Bridge {
	return &Bridge{
		reader: reader,
		fields: resolveFields(fields),
		snapshot: Snapshot{
			GeneratedAt: time.Now(),
			Rows:        []Row{},
		},
	}
}

// Fields returns the resolved output column list.
func (b *Bridge) Fields() []string { return b.fields }

// SetOnChange registers a callback invoked with every new snapshot. Set it before
// Run so the initial build is delivered too.
func (b *Bridge) SetOnChange(fn func(Snapshot)) { b.onChange = fn }

// Snapshot returns the most recently built snapshot.
func (b *Bridge) Snapshot() Snapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.snapshot
}

// Run subscribes to change events and rebuilds the snapshot on each, until the
// context is cancelled. It performs one build immediately so a freshly started
// bridge serves current data rather than an empty table while it waits for the
// first change. Intended to run in its own goroutine.
func (b *Bridge) Run(ctx context.Context, bus *events.EventBus) {
	// One channel per event type. A modest buffer absorbs bursts (a roReplace can
	// fan out into many item events) without the event bus dropping our delivery.
	chans := make([]<-chan events.Event, 0, len(changeEvents))
	for _, et := range changeEvents {
		chans = append(chans, bus.Subscribe(et, 64))
	}

	// Fan the per-type channels into one so the select below stays fixed-size.
	merged := make(chan events.Event, 128)
	for _, ch := range chans {
		go func(c <-chan events.Event) {
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-c:
					if !ok {
						return
					}
					select {
					case merged <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
		}(ch)
	}

	b.rebuild(ctx)

	// Coalesce a burst of events into a single rebuild. ENPS sending a full
	// running order produces many events in quick succession; rebuilding once
	// after they settle is both correct and far cheaper than once per event.
	const debounce = 150 * time.Millisecond
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-merged:
			if timer == nil {
				timer = time.NewTimer(debounce)
				timerC = timer.C
			} else {
				timer.Reset(debounce)
			}
		case <-timerC:
			b.rebuild(ctx)
		}
	}
}

// rebuild reads the whole current rundown and replaces the stored snapshot.
//
// It reads everything rather than patching the changed running order because the
// snapshot is small (a rundown is hundreds of rows, not millions) and a full read
// is immune to the desynchronisation risks the MOS core itself warns about: there
// is no incremental state to drift.
func (b *Bridge) rebuild(ctx context.Context) {
	rows, err := b.build(ctx)
	if err != nil {
		logger.Errorf("bridge: failed to rebuild rundown snapshot: %v", err)
		return
	}

	snap := Snapshot{GeneratedAt: time.Now(), Rows: rows}

	b.mu.Lock()
	b.snapshot = snap
	b.mu.Unlock()

	if b.onChange != nil {
		b.onChange(snap)
	}
	logger.Infof("bridge: rebuilt rundown snapshot with %d rows", len(rows))
}

// build assembles the flat row list from the reader. Ordering is deterministic:
// running orders by ID, stories by their Order then ID, items by Order then ID,
// so vMix sees a stable table across refreshes.
func (b *Bridge) build(ctx context.Context) ([]Row, error) {
	ros, err := b.reader.ListRunningOrders(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(ros, func(i, j int) bool { return ros[i].ID < ros[j].ID })

	var rows []Row
	for _, roMeta := range ros {
		ro, stories, err := b.reader.GetRunningOrderWithStories(ctx, roMeta.ID)
		if err != nil {
			logger.Warningf("bridge: skipping running order %s: %v", roMeta.ID, err)
			continue
		}
		sort.Slice(stories, func(i, j int) bool {
			if stories[i].Order != stories[j].Order {
				return stories[i].Order < stories[j].Order
			}
			return stories[i].ID < stories[j].ID
		})

		for _, story := range stories {
			items, err := b.reader.GetItemsForStory(ctx, story.ID)
			if err != nil {
				logger.Warningf("bridge: skipping items for story %s: %v", story.ID, err)
				items = nil
			}
			sort.Slice(items, func(i, j int) bool {
				if items[i].Order != items[j].Order {
					return items[i].Order < items[j].Order
				}
				return items[i].ID < items[j].ID
			})

			if len(items) == 0 {
				// Emit the story even with no items, so an empty story is visible.
				rows = append(rows, newRow(ro, story, nil))
				continue
			}
			for _, item := range items {
				rows = append(rows, newRow(ro, story, item))
			}
		}
	}
	return rows, nil
}
