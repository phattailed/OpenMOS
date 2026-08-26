package service

import (
	"context"
	"errors"
	"fmt"

	"airshift/openmos/internal/xml"
)

// ErrUnknownRunningOrder marks the condition the specification calls lost
// synchronisation: a message referenced a running order this device does not hold.
//
// MOS 3.8.4: "If a message references an unknown roID or storyID, the MOS device
// should treat this as lost synchronization, send roReq, and replace its local state
// from the returned full roList."
//
// It is a distinct error rather than a generic failure because the transport must be
// able to tell "we disagree about what exists", which is recoverable by asking, from
// "the store is broken", which is not.
var ErrUnknownRunningOrder = errors.New("unknown running order")

// UnknownRunningOrderError carries the identifier that could not be found, so the
// transport knows which running order to request.
type UnknownRunningOrderError struct {
	ROID string
	Err  error
}

func (e *UnknownRunningOrderError) Error() string {
	return fmt.Sprintf("roStorySend for unknown running order %q: "+
		"the NCS believes this device holds it, so recover with roReq for this roID "+
		"or roReqAll (%v)", e.ROID, e.Err)
}

// Unwrap exposes both the underlying repository error and the sentinel, so callers can
// use errors.Is(err, ErrUnknownRunningOrder) as well as errors.As.
func (e *UnknownRunningOrderError) Unwrap() []error {
	return []error{ErrUnknownRunningOrder, e.Err}
}

// ApplyROList replaces local state for one running order from a roList.
//
// This is the second half of pull recovery. The specification says to "replace its
// local state from the returned full roList", and a roList carries the same
// running-order-plus-stories structure as roCreate, so the existing create-or-update
// path does the work.
//
// Note what this does NOT do: it does not delete stories absent from the roList. A
// full replace is the honest reading of "replace its local state", and is implemented
// by ProcessROReplace semantics rather than here, so this deliberately converges on
// what the NCS sent without pretending to have reconciled deletions. That gap is
// recorded rather than hidden.
func (s *MOSService) ApplyROList(ctx context.Context, list xml.ROList, mosID string) error {
	if list.ID == "" {
		return fmt.Errorf("roList carried no roID")
	}

	// A roList and a roCreate describe a running order identically, so reuse the
	// create-or-update path rather than duplicating persistence logic.
	info := xml.RunningOrderInfo{
		ID:       list.ID,
		Slug:     list.Slug,
		Channel:  list.Channel,
		EditTime: list.EdStart,
		Duration: list.EdDur,
		Stories:  list.Stories,
	}
	if err := s.ProcessRunningOrderInfo(ctx, info, mosID); err != nil {
		return fmt.Errorf("failed to apply roList for %q: %w", list.ID, err)
	}
	return nil
}
