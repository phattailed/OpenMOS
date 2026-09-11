package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"airshift/openmos/internal/events"
	"airshift/openmos/internal/model"
	"airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"
)

// ProcessStoryAction processes a story action request from an NCS
func (s *MOSService) ProcessStoryAction(ctx context.Context, action xml.NCSReqStoryAction) error {
	logger.Infof("Processing story action: %s", action.Operation)

	switch strings.ToUpper(action.Operation) {
	case "NEW":
		return s.createNewStory(ctx, action.ROStorySend)
	case "UPDATE":
		return s.updateStory(ctx, action.ROStorySend)
	case "REPLACE":
		return s.replaceStory(ctx, action.ROStorySend)
	default:
		return fmt.Errorf("unsupported story operation: %s", action.Operation)
	}
}

// createNewStory creates a new story from the provided ROStorySend.
//
// The running order must already exist. roStorySend adds a story to a running
// order; it is not a way to bring one into being. OpenMOS used to fabricate a
// missing running order here, with the slug "Auto-created RO", which was wrong in
// two ways: it invented a running order the NCS never asked for, carrying no slug,
// start time or duration that anyone had supplied, and it silently concealed the
// one condition worth surfacing.
//
// That condition is real and was observed live. After OpenMOS restarted with
// in-memory storage, a live ENPS resynchronised by sending ten roStorySend messages
// and no roCreate, because from its side the device already held the running order.
// Every one was accepted and a shell running order was fabricated. Answering with
// roAck roStatus=ERROR instead tells the NCS its assumption is stale, which is the
// only way it can know to send a roCreate.
func (s *MOSService) createNewStory(ctx context.Context, storySend xml.ROStorySend) error {
	if storySend.ROID == "" {
		return fmt.Errorf("no running order ID specified")
	}

	ro, err := s.runningOrderRepo.Get(ctx, storySend.ROID)
	if err != nil {
		return fmt.Errorf("roStorySend for unknown running order %q: send roCreate first (%w)",
			storySend.ROID, err)
	}

	// Create a story ID if not provided
	storyID := storySend.StoryID
	if storyID == "" {
		storyID = fmt.Sprintf("S%d", time.Now().UnixNano())
	}

	// Create the new story
	story := &model.Story{
		ID:             storyID,
		RunningOrderID: ro.ID,
		Slug:           storySend.StorySlug,
		Number:         storySend.StoryNum,
		Status:         model.StatusPending,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	// Try to determine the story's order in the running order
	stories, err := s.storyRepo.ListByRunningOrder(ctx, ro.ID)
	if err != nil {
		return fmt.Errorf("failed to list stories: %w", err)
	}

	// New story goes at the end
	story.Order = len(stories) + 1

	// Process story body
	err = s.processStoryBody(ctx, story, &storySend.StoryBody)
	if err != nil {
		return fmt.Errorf("failed to process story body: %w", err)
	}

	// Create the story
	_, err = s.storyRepo.Create(ctx, story)
	if err != nil {
		return fmt.Errorf("failed to create story: %w", err)
	}

	// Publish event after successful creation
	if s.eventBus != nil {
		s.eventBus.Publish(events.Event{
			Type:    events.StoryModified,
			Payload: story.ID,
			Source:  "mos_service",
		})
	}

	logger.Infof("Created new story %s in running order %s", story.ID, ro.ID)
	return nil
}

// updateStory updates an existing story from the provided ROStorySend
func (s *MOSService) updateStory(ctx context.Context, storySend xml.ROStorySend) error {
	// Check if the story exists
	story, err := s.storyRepo.Get(ctx, storySend.StoryID)
	if err != nil {
		return fmt.Errorf("story not found: %w", err)
	}

	// Update story fields
	story.Slug = storySend.StorySlug
	story.Number = storySend.StoryNum
	story.UpdatedAt = time.Now()

	// Process story body
	err = s.processStoryBody(ctx, story, &storySend.StoryBody)
	if err != nil {
		return fmt.Errorf("failed to process story body: %w", err)
	}

	// Update the story
	err = s.storyRepo.Update(ctx, story)
	if err != nil {
		return fmt.Errorf("failed to update story: %w", err)
	}

	// Publish event after successful update
	if s.eventBus != nil {
		s.eventBus.Publish(events.Event{
			Type:    events.StoryModified,
			Payload: story.ID,
			Source:  "mos_service",
		})
	}

	logger.Infof("Updated story %s in running order %s", story.ID, story.RunningOrderID)
	return nil
}

// replaceStory replaces an existing story from the provided ROStorySend
func (s *MOSService) replaceStory(ctx context.Context, storySend xml.ROStorySend) error {
	// For now, implement as delete + create
	// Delete existing story
	err := s.storyRepo.Delete(ctx, storySend.StoryID)
	if err != nil {
		return fmt.Errorf("failed to delete existing story: %w", err)
	}

	// Create new story
	return s.createNewStory(ctx, storySend)
}

// collectCues converts the body's non-MOS instructions into the storage model.
//
// The conversion is deliberately thin: this is the seam that silently dropped objDur/objTB and
// then objPaths after both were correctly added to the wire types and correctly stored, with unit
// tests passing on either side and nothing crossing (doc/interop §§48, 49). Keeping it to a direct
// field-for-field copy is what makes that failure visible here rather than plausible.
func collectCues(storyBody *xml.StoryBody) []model.StoryCue {
	parsed := storyBody.Cues()
	if len(parsed) == 0 {
		return nil
	}
	cues := make([]model.StoryCue, 0, len(parsed))
	for i, c := range parsed {
		cues = append(cues, model.StoryCue{
			Kind:      c.Kind,
			Raw:       c.Raw,
			Verb:      c.Verb,
			Target:    c.Target,
			Fields:    c.Fields,
			Params:    c.Params,
			Order:     i,
			Paragraph: c.Paragraph,
		})
	}
	return cues
}

// processStoryBody extracts the story's items and persists them.
//
// It used to build the item slice and then throw it away, with a comment saying persistence
// would come "in a later step". So items were only ever logged: a running order arrived, stories
// were stored, and every item silently vanished. That is the whole point of a MOS device -- the
// objID, the channel and the graphics payload all live on the item -- so a rundown without items
// is a list of headlines (doc/interop §40).
//
// Items are collected from both shapes MOS traffic uses: storyItem elements that are direct
// children of storyBody, which is what a live ENPS sends, and storyItem elements nested inside a
// paragraph, which the specification's examples show. Order follows document order across both,
// because element order is significant and the NCS-supplied sequence must be retained.
//
// Persistence delegates to storeItems, the same routine the roCreate path uses, so the two
// cannot drift apart in how they create, update, order or preserve metadata.
func (s *MOSService) processStoryBody(ctx context.Context, story *model.Story, storyBody *xml.StoryBody) error {
	if storyBody == nil {
		return nil
	}

	// Non-MOS instructions from the body text: production commands and legacy serial CG.
	//
	// Replaced wholesale rather than merged, which is the opposite of how item metadata is
	// handled. Every roStorySend carries the complete body, so a cue the journalist deleted is
	// absent from the resend and must disappear here too; merging would accumulate commands
	// that no longer exist and put a stale graphic on air.
	story.Cues = collectCues(storyBody)

	infos := make([]xml.ItemInfo, 0, len(storyBody.Items))

	appendItem := func(si xml.StoryItem) {
		f := si.ItemFields()
		if f == nil {
			return
		}
		info := xml.ItemInfo{
			ID:                  f.ItemID,
			Slug:                f.ItemSlug,
			Abstract:            f.MosAbstract,
			ObjectID:            f.ObjID,
			MosID:               f.MosID,
			Channel:             f.ItemChannel,
			MosExternalMetadata: f.ExternalMeta,
		}
		if f.ItemEdDur > 0 {
			info.Duration = strconv.Itoa(f.ItemEdDur)
		}
		// The object's duration and time base, which for some estates are the ONLY timing an item
		// carries: a production customer rundown supplied objDur and objTB on all 93 of its items and
		// itemEdDur on only some (doc/interop §48). Adding the fields to the wire types without
		// carrying them through this conversion left every one of those durations at zero.
		info.ObjDur = f.ObjDur
		info.ObjTB = f.ObjTB
		// And the media pointers. This is the same seam that dropped objDur and objTB after they were
		// added to the wire types (doc/interop §48): work correct at both ends, nothing crossing.
		info.ObjPaths = f.ObjPaths
		infos = append(infos, info)
	}

	// Direct children first: that is the live ENPS shape and the common case.
	for _, si := range storyBody.Items {
		appendItem(si)
	}
	for _, paragraph := range storyBody.Paragraphs {
		for _, si := range paragraph.Items {
			appendItem(si)
		}
	}

	if len(infos) == 0 {
		return nil
	}

	if err := s.storeItems(ctx, story.ID, infos); err != nil {
		return fmt.Errorf("failed to store items for story %s: %w", story.ID, err)
	}
	logger.Infof("Stored %d items for story %s", len(infos), story.ID)
	return nil
}
