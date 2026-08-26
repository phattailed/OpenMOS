package server

import (
	"sync"
	"time"
)

// resyncGuard rate-limits outbound roReq so that recovery cannot become a loop.
//
// The hazard is concrete. When the two sides disagree about a running order, the NCS
// keeps sending roStorySend for it -- a live ENPS sent ten in a row. If each refusal
// triggered a roReq, and the NCS answered each roReq with a NACK because the running
// order is genuinely gone on its side too, the pair would trade messages indefinitely.
// The specification warns about exactly this shape for heartbeat: "care should be taken
// in implementation of this message to avoid an endless looping condition on response."
//
// So a roID may be requested at most once per interval, and the set of in-flight
// identifiers is bounded. Recovery is best-effort by nature: if the first request does
// not fix things, requesting harder will not either.
type resyncGuard struct {
	mu        sync.Mutex
	requested map[string]time.Time
	interval  time.Duration
	max       int
}

const (
	// defaultResyncInterval is how long to wait before asking for the same running
	// order again. Long enough that a burst of roStorySend produces one request, short
	// enough that a genuine later divergence is still recovered.
	defaultResyncInterval = 30 * time.Second

	// defaultResyncTracked bounds the identifier set, so a peer sending endless
	// distinct unknown roIDs cannot grow it without limit.
	defaultResyncTracked = 256
)

func newResyncGuard() *resyncGuard {
	return &resyncGuard{
		requested: make(map[string]time.Time),
		interval:  defaultResyncInterval,
		max:       defaultResyncTracked,
	}
}

// shouldRequest reports whether a roReq for this running order should be sent now,
// and records the decision. It returns false while a previous request is still within
// the interval.
func (g *resyncGuard) shouldRequest(roID string) bool {
	if g == nil || roID == "" {
		return false
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now()
	if last, seen := g.requested[roID]; seen && now.Sub(last) < g.interval {
		return false
	}

	// Drop entries that have aged out before considering the bound, so ordinary
	// long-running operation does not hit it.
	for id, at := range g.requested {
		if now.Sub(at) >= g.interval {
			delete(g.requested, id)
		}
	}

	// Still full: refuse rather than evict. Declining to ask is safe; forgetting that
	// we asked is what re-opens the loop.
	if len(g.requested) >= g.max {
		if _, seen := g.requested[roID]; !seen {
			return false
		}
	}

	g.requested[roID] = now
	return true
}

// forget clears the record for a running order, so a subsequent divergence is
// requested immediately rather than waiting out the interval. Called once a roList has
// been applied successfully: at that point the disagreement is resolved, and any later
// one is new information.
func (g *resyncGuard) forget(roID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.requested, roID)
}
