package service

import (
	"context"
	"fmt"

	"airshift/openmos/internal/model"
)

// Identity translation between the wire and storage.
//
// Stories and items are stored under a COMPOSITE key -- storyPersistenceID(roID, storyID) -- because
// the protocol only guarantees a storyID is unique within a running order, not across the NCS. A
// peer, naturally, sends the bare storyID.
//
// The roElementAction family used those bare wire identifiers directly as storage keys while the
// roCreate and roStorySend family used the composite. Two consequences, both observed live against a
// real NCS (doc/interop §42):
//
//  1. Lookups missed, so MOVE, DELETE and SWAP silently did nothing and still acknowledged OK. Our
//     copy of the running order diverged from the NCS's while we reported success -- the one outcome
//     the spec is most emphatic about avoiding, since "if a message in a sequence is not applied or
//     'missed' then it is guaranteed that all subsequent messages will cause the sequence in the MOS
//     to be even further out of sequence".
//  2. Creations minted a second, phantom record for a story that already existed under the composite
//     key, so a rundown accumulated duplicates with inconsistent ordering.
//
// These helpers are the single place the translation happens. Adding a new element-action operation
// that reaches for a repository directly with a wire identifier is the mistake to look for.

// resolveStory finds a story by the identifier a peer used on the wire.
//
// The composite key is tried first. The fallback scan by RawID exists because records created under
// the old convention are keyed by the bare wire ID, so without it a rundown that predates this fix
// would stay permanently unaddressable.
func (s *MOSService) resolveStory(ctx context.Context, roID, wireStoryID string) (*model.Story, error) {
	if wireStoryID == "" {
		return nil, fmt.Errorf("empty storyID")
	}
	if story, err := s.storyRepo.Get(ctx, storyPersistenceID(roID, wireStoryID)); err == nil {
		return story, nil
	}
	stories, err := s.storyRepo.ListByRunningOrder(ctx, roID)
	if err != nil {
		return nil, fmt.Errorf("story %s not found and running order unreadable: %w", wireStoryID, err)
	}
	for _, story := range stories {
		if story.RawID == wireStoryID || story.ID == wireStoryID {
			return story, nil
		}
	}
	// A story the NCS believes we hold and we do not is lost synchronisation, and the recovery is
	// normative rather than optional. MOS 4.0 §2.3: "if a MOS device receives an roElementAction
	// message which references an unknown roID, storyID or itemID, the MOS device will send an roReq
	// message to the NCS which includes the roID."
	//
	// Returning a plain error here meant the caller NACKed and stopped. It refused correctly -- which
	// is already better than the silent no-op it replaced -- but left the divergence in place with
	// nothing scheduled to repair it.
	return nil, &UnknownRunningOrderError{ROID: roID,
		Err: fmt.Errorf("story %s is not held in this running order", wireStoryID)}
}

// resolveStoryKeys maps wire story identifiers onto the storage keys of the stories actually held.
//
// Unresolvable identifiers are returned separately rather than skipped. An operation that acts on a
// subset of what was asked has not been applied, and the caller must be able to tell -- silently
// dropping them is precisely how the divergence above went unnoticed.
func (s *MOSService) resolveStoryKeys(ctx context.Context, roID string, wireIDs []string) (keys []string, missing []string) {
	for _, wire := range wireIDs {
		story, err := s.resolveStory(ctx, roID, wire)
		if err != nil {
			missing = append(missing, wire)
			continue
		}
		keys = append(keys, story.ID)
	}
	return keys, missing
}

// resolveItem finds an item within a story by the itemID a peer used on the wire.
func (s *MOSService) resolveItem(ctx context.Context, storyKey, wireItemID string) (*model.Item, error) {
	if wireItemID == "" {
		return nil, fmt.Errorf("empty itemID")
	}
	if item, err := s.itemRepo.Get(ctx, itemPersistenceID(storyKey, wireItemID)); err == nil {
		return item, nil
	}
	items, err := s.itemRepo.ListByStory(ctx, storyKey)
	if err != nil {
		return nil, fmt.Errorf("item %s not found and story unreadable: %w", wireItemID, err)
	}
	for _, item := range items {
		if item.RawID == wireItemID || item.ID == wireItemID {
			return item, nil
		}
	}
	return nil, fmt.Errorf("item %s is not held in story %s", wireItemID, storyKey)
}

// resolveItemKeys maps wire item identifiers onto storage keys, reporting those not held.
func (s *MOSService) resolveItemKeys(ctx context.Context, storyKey string, wireIDs []string) (keys []string, missing []string) {
	for _, wire := range wireIDs {
		item, err := s.resolveItem(ctx, storyKey, wire)
		if err != nil {
			missing = append(missing, wire)
			continue
		}
		keys = append(keys, item.ID)
	}
	return keys, missing
}

// storyKeyFor returns the storage key for a story a peer named on the wire.
//
// A thin wrapper over resolveStory, kept because most call sites want only the key and reading
// `.ID` off a resolved story at each one invites using the wire value by mistake.
func (s *MOSService) storyKeyFor(ctx context.Context, roID, wireStoryID string) (string, error) {
	story, err := s.resolveStory(ctx, roID, wireStoryID)
	if err != nil {
		return "", err
	}
	return story.ID, nil
}
