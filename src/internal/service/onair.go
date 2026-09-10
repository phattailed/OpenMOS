package service

import (
	"context"
	"strings"
	"time"

	"airshift/openmos/internal/events"
	"airshift/openmos/internal/model"
	"airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"
)

// On-air tracking, driven by roElementStat.
//
// This is the only message that says NOW. Everything else in the running-order family describes what a
// rundown contains; roElementStat describes what is happening in it. It is also the most common
// non-heartbeat message in real traffic, and OpenMOS previously parsed, logged and acknowledged it
// without recording anything.
//
// Observed live: moving the timing bar in the ENPS client emits a PAIR per move -- STOP on the story
// being left, PLAY on the story being entered, roughly two seconds apart, each carrying the story's
// identifier and the time.
//
//	<roElementStat element="STORY">
//	  <roID>…</roID><storyID>…;DA7C2774-…</storyID>
//	  <status>PLAY</status><time>2026-09-10T20:20:30</time>
//	</roElementStat>
//
// So the on-air position is fully recoverable from the PLAY messages alone. For anything downstream --
// a graphics trigger, an automation cue, a monitoring display -- this is the signal that matters.
//
// It is a NOTIFICATION, not a command. The NCS is reporting where the bar is; nothing here asks us to
// play anything. Actual playout control is Profile 5 (roCtrl, roItemCue), which the reference NCS
// advertises as NO, so it never arrives from there.

// Status values that carry on-air meaning. The specification lists status as free-ish text drawn from
// "NEW", "UPDATED", "MOVED", "BUSY", "DELETED", "NCS CTRL", "MANUAL CTRL", "READY", "NOT READY", "PLAY",
// "STOP" -- so these are compared case-insensitively after trimming rather than treated as an enum.
const (
	statusPlay = "PLAY"
	statusStop = "STOP"
)

// ProcessElementStatus records a status report from the peer.
//
// Three levels, distinguished by the element attribute and by which identifiers are present. The
// attribute is preferred; the identifiers are the fallback, because the specification's own DTD makes
// itemID required for this message while its structural outline makes it optional, and real senders
// differ.
func (s *MOSService) ProcessElementStatus(ctx context.Context, stat xml.ROElementStat) error {
	level := strings.ToUpper(strings.TrimSpace(stat.Element))
	if level == "" {
		switch {
		case strings.TrimSpace(stat.ItemID) != "":
			level = "ITEM"
		case strings.TrimSpace(stat.StoryID) != "":
			level = "STORY"
		default:
			level = "RO"
		}
	}

	switch level {
	case "ITEM":
		return s.applyItemStatus(ctx, stat)
	case "STORY":
		return s.applyStoryStatus(ctx, stat)
	default:
		return s.applyROStatus(ctx, stat)
	}
}

// applyStoryStatus records a story's status and maintains the running order's on-air pointer.
func (s *MOSService) applyStoryStatus(ctx context.Context, stat xml.ROElementStat) error {
	story, err := s.resolveStory(ctx, stat.ROID, stat.StoryID)
	if err != nil {
		// Not held. Returning the error lets the caller treat it as lost synchronisation and pull a
		// rebuild, the same as any other message naming a story we do not have.
		return err
	}

	status := strings.ToUpper(strings.TrimSpace(stat.Status))
	story.Status = model.StatusType(status)
	story.UpdatedAt = time.Now()
	if story.Metadata == nil {
		story.Metadata = make(map[string]string)
	}
	if stat.Time != "" {
		story.Metadata["lastStatusTime"] = stat.Time
	}
	if err := s.storyRepo.Update(ctx, story); err != nil {
		return err
	}

	ro, err := s.runningOrderRepo.Get(ctx, stat.ROID)
	if err != nil {
		return err
	}

	switch status {
	case statusPlay:
		ro.OnAirStoryID = story.ID
		if when, ok := statusTime(stat.Time); ok {
			ro.OnAirSince = &when
		} else {
			now := time.Now()
			ro.OnAirSince = &now
		}
		logger.Infof("On air: RO %s story %q", ro.Slug, story.Slug)

	case statusStop:
		// Only clear when the STOP names the story we currently believe is on air.
		//
		// The order of the pair is not guaranteed. A STOP for the story being left can arrive AFTER the
		// PLAY for the story being entered, and clearing unconditionally would then blank a pointer
		// that had just been set correctly -- leaving nothing on air while the bar is plainly sitting
		// somewhere.
		if ro.OnAirStoryID == story.ID {
			ro.OnAirStoryID = ""
			ro.OnAirSince = nil
			logger.Infof("Off air: RO %s story %q", ro.Slug, story.Slug)
		}
	}

	ro.UpdatedAt = time.Now()
	if err := s.runningOrderRepo.Update(ctx, ro); err != nil {
		return err
	}

	if s.eventBus != nil {
		s.eventBus.Publish(events.Event{
			Type:    events.StoryModified,
			Payload: story.ID,
			Source:  "mos_service",
		})
	}
	return nil
}

// applyROStatus records a running-order level status, such as "MANUAL CTRL" or "NCS CTRL".
func (s *MOSService) applyROStatus(ctx context.Context, stat xml.ROElementStat) error {
	ro, err := s.runningOrderRepo.Get(ctx, stat.ROID)
	if err != nil {
		return err
	}
	ro.Status = model.StatusType(strings.ToUpper(strings.TrimSpace(stat.Status)))
	ro.UpdatedAt = time.Now()
	if ro.Metadata == nil {
		ro.Metadata = make(map[string]string)
	}
	if stat.Time != "" {
		ro.Metadata["lastStatusTime"] = stat.Time
	}
	logger.Infof("Running order %q status %s", ro.Slug, ro.Status)
	return s.runningOrderRepo.Update(ctx, ro)
}

// applyItemStatus records an item's status.
//
// The identifier is resolved rather than used directly. The previous version reached for
// itemRepo.Get(stat.ItemID) with the bare wire value, which never matches a composite storage key --
// the same defect that made MOVE and DELETE silent no-ops (doc/interop §42).
func (s *MOSService) applyItemStatus(ctx context.Context, stat xml.ROElementStat) error {
	storyKey, err := s.storyKeyFor(ctx, stat.ROID, stat.StoryID)
	if err != nil {
		return err
	}
	item, err := s.resolveItem(ctx, storyKey, stat.ItemID)
	if err != nil {
		return err
	}

	item.Status = model.StatusType(strings.ToUpper(strings.TrimSpace(stat.Status)))
	item.UpdatedAt = time.Now()
	if item.Metadata == nil {
		item.Metadata = make(map[string]string)
	}
	if stat.Time != "" {
		item.Metadata["lastStatusTime"] = stat.Time
	}
	if stat.ItemChannel != "" {
		item.Metadata["itemChannel"] = stat.ItemChannel
	}
	if err := s.itemRepo.Update(ctx, item); err != nil {
		return err
	}

	if s.eventBus != nil {
		s.eventBus.Publish(events.Event{
			Type:    events.ItemChanged,
			Payload: item.ID,
			Source:  "mos_service",
		})
	}
	logger.Infof("Item %q status %s", item.Slug, item.Status)
	return nil
}

// OnAirStory returns the story a running order currently has on air, or nil when none does.
//
// Provided as a first-class lookup because "what is on air right now" is the question a consumer of this
// data actually asks, and deriving it by scanning story statuses would give a different answer whenever
// a STOP was missed.
func (s *MOSService) OnAirStory(ctx context.Context, roID string) (*model.Story, *time.Time, error) {
	ro, err := s.runningOrderRepo.Get(ctx, roID)
	if err != nil {
		return nil, nil, err
	}
	if ro.OnAirStoryID == "" {
		return nil, nil, nil
	}
	story, err := s.storyRepo.Get(ctx, ro.OnAirStoryID)
	if err != nil {
		return nil, nil, err
	}
	return story, ro.OnAirSince, nil
}

// statusTime parses the time a status report carries.
//
// MOS timestamps use a COMMA decimal separator, which Go's stdlib cannot read, so this goes through
// ParseMOSTime rather than time.Parse. A value that will not parse is not an error worth failing the
// report over -- the status itself is the point -- so the caller substitutes arrival time.
func statusTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	parsed, err := xml.ParseMOSTime(raw)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
