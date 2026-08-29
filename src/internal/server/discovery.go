package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"airshift/openmos/pkg/logger"
)

// discoveryWalk sequences the two-stage recovery the specification actually describes.
//
// roReqAll returns a roListAll, which is a SUMMARY: identifiers, slugs and timings, with no
// stories or items. MOS 4.0 §2.5 is explicit that it does not end there -- "for a full listing
// of the contents of the RO the MOS device must issue a subsequent roReq". Until now OpenMOS
// logged the summary and stopped, so an inbound roListAll changed nothing at all. The message
// inventory test classified it parsed-only for exactly that reason.
//
// The walk is deliberately SEQUENTIAL rather than a burst. Firing one roReq per advertised
// running order would be simpler and is wrong twice over: MOS 4.0 §4.1 requires that a sender
// "must not send another message on the same port until the previous message is acknowledged",
// and a real NCS can advertise far more running orders than it is reasonable to demand at once.
// So one request is outstanding at a time, and the next is sent when the previous resolves.
//
// Scope matches resyncGuard: one walk per transport instance, not per connection. Discovery is
// about our own local state, and a reconnect should resume the same walk rather than restart it.
type discoveryWalk struct {
	mu sync.Mutex

	// pending holds advertised running orders not yet requested, in the order the NCS listed
	// them. Order is preserved because the NCS's ordering is meaningful elsewhere in the
	// protocol and there is no reason to discard it here.
	pending []string

	// inFlight is the running order whose roReq has been sent and not yet resolved. Empty
	// when the walk is idle.
	inFlight string

	// deadline bounds how long to wait for the in-flight answer before moving on.
	deadline time.Time

	timeout time.Duration
	max     int

	// path is where pending work is persisted, empty when durability is disabled.
	//
	// A walk interrupted by a restart is the case worth surviving. The NCS told us once which
	// running orders it holds; if the process stops halfway through fetching them, nothing will
	// say so again until something else provokes a roListAll. Without this the remaining
	// running orders stay silently divergent -- present on the NCS, absent locally, with no
	// error anywhere. That is the same class of silent gap the walk was written to close.
	path     string
	degraded bool
}

// walkState is the persisted shape. The in-flight identifier is written back to the front of
// pending on load rather than kept separate, because after a restart no answer is coming for it.
type walkState struct {
	Pending []string `json:"pending"`
}

const (
	// defaultWalkTimeout is how long a single roReq may remain outstanding before the walk
	// gives up on it and continues.
	//
	// This exists because roReq is not guaranteed a roList. The specification allows it to be
	// answered with a NACK-bearing roAck instead, and a real ENPS buddy server NACKs
	// everything (doc/interop §16). Without a deadline one refusal would stall the walk
	// permanently, leaving every later running order unrequested and the divergence silent --
	// which is worse than the gap this walk was written to close.
	defaultWalkTimeout = 15 * time.Second

	// defaultWalkMax bounds the queue. A peer advertising an implausible number of running
	// orders should not be able to commit us to an unbounded sequence of requests.
	defaultWalkMax = 512
)

func newDiscoveryWalk() *discoveryWalk {
	return &discoveryWalk{timeout: defaultWalkTimeout, max: defaultWalkMax, degraded: true}
}

// openDiscoveryWalk returns a walk that persists its pending queue under dir, reloading any work
// left unfinished by a previous run.
//
// An unusable directory degrades to in-memory rather than refusing to start, matching
// internal/messageid and FileDedupStore: losing the queue costs an unfinished recovery, which is
// the behaviour before this existed, whereas failing to start costs everything.
func openDiscoveryWalk(dir string) *discoveryWalk {
	w := newDiscoveryWalk()
	if dir == "" {
		return w
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.Warningf("Discovery walk progress will NOT survive a restart: cannot create state "+
			"directory %s: %v. An interrupted walk will leave the remaining running orders "+
			"divergent until the next roListAll.", dir, err)
		return w
	}

	w.path = filepath.Join(dir, "discovery.json")
	w.degraded = false

	raw, err := os.ReadFile(w.path)
	if err != nil {
		return w // no saved work is the normal case
	}
	var state walkState
	if err := json.Unmarshal(raw, &state); err != nil {
		logger.Warningf("Ignoring unreadable discovery walk state at %s: %v", w.path, err)
		return w
	}
	if len(state.Pending) > 0 {
		if len(state.Pending) > w.max {
			state.Pending = state.Pending[:w.max]
		}
		w.pending = state.Pending
		logger.Infof("Resuming an interrupted discovery walk: %d running orders were advertised "+
			"but never fetched", len(w.pending))
	}
	return w
}

// persistLocked writes the outstanding work. Caller holds the lock.
//
// The in-flight identifier is written as part of pending, because a restart means its answer will
// never arrive and it must be requested again.
func (w *discoveryWalk) persistLocked() {
	if w.degraded || w.path == "" {
		return
	}

	pending := w.pending
	if w.inFlight != "" {
		pending = append([]string{w.inFlight}, pending...)
	}

	// An empty queue is written as an empty file rather than deleted, so a completed walk is
	// distinguishable from one that never ran.
	raw, err := json.Marshal(walkState{Pending: pending})
	if err != nil {
		return
	}

	tmp := w.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		logger.Warningf("Failed to persist discovery walk progress to %s: %v", w.path, err)
		w.degraded = true
		return
	}
	if err := os.Rename(tmp, w.path); err != nil {
		_ = os.Remove(tmp)
		logger.Warningf("Failed to replace discovery walk progress at %s: %v", w.path, err)
		w.degraded = true
	}
}

// begin seeds the walk from a roListAll and returns the first running order to request.
//
// A fresh roListAll supersedes whatever was queued: it is a newer statement of what the NCS
// holds, so continuing to work through a stale list would request running orders the NCS may
// no longer have. Anything already in flight is left alone, because its answer is still coming.
//
// dropped reports how many advertised identifiers did not fit the bound, so the caller can say
// so rather than silently under-recovering.
func (w *discoveryWalk) begin(roIDs []string) (next string, ok bool, dropped int) {
	if w == nil {
		return "", false, 0
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	// Deduplicate while preserving order. A malformed or duplicated list should not produce
	// duplicate requests.
	seen := make(map[string]bool, len(roIDs))
	queue := make([]string, 0, len(roIDs))
	for _, id := range roIDs {
		if id == "" || seen[id] || id == w.inFlight {
			continue
		}
		seen[id] = true
		queue = append(queue, id)
	}

	if len(queue) > w.max {
		dropped = len(queue) - w.max
		queue = queue[:w.max]
	}
	w.pending = queue

	next, ok = w.takeLocked()
	return next, ok, dropped
}

// enqueueUrgent puts a running order at the FRONT of the queue and returns it if it can be
// requested immediately.
//
// This exists so that pull recovery and the discovery walk cannot both have a roReq outstanding on
// the same lane. MOS 4.0 §4.1: a sender "must not send another message on the same port until the
// previous message is acknowledged". Recovery used to send directly, so a divergence arriving
// mid-walk produced two concurrent requests.
//
// Front rather than back because recovery is the more urgent of the two: the peer is actively
// sending us messages about a running order we do not hold, whereas the walk is catching up on
// state nobody is asking for yet.
//
// If a request is already in flight this returns false and the identifier waits. Nothing is lost:
// the in-flight request either completes, which drains the queue, or times out, which also drains
// it. That deadline is what makes serialising safe -- a gate with no timeout would turn one lost
// acknowledgement into a permanently stuck lane, which is worse than the overlap it prevents.
func (w *discoveryWalk) enqueueUrgent(roID string) (next string, ok bool) {
	if w == nil || roID == "" {
		return "", false
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.inFlight == roID {
		return "", false // already asked, still waiting
	}
	for _, id := range w.pending {
		if id == roID {
			return "", false // already queued
		}
	}

	if len(w.pending) >= w.max {
		// Bounded, as elsewhere. Dropping a recovery request is acceptable because recovery is
		// best-effort and the peer will keep telling us about the divergence.
		return "", false
	}
	w.pending = append([]string{roID}, w.pending...)

	return w.takeLocked()
}

// resolved records that the in-flight running order is settled -- its roList arrived and was
// applied -- and returns the next one to request.
//
// A roID that is not the one in flight is ignored for sequencing purposes: an unsolicited
// roList is perfectly legal and must not be allowed to advance somebody else's queue.
func (w *discoveryWalk) resolved(roID string) (next string, ok bool) {
	if w == nil || roID == "" {
		return "", false
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.inFlight != roID {
		return "", false
	}
	w.inFlight = ""
	next, ok = w.takeLocked()
	if !ok {
		// Nothing left to request: record the completed state so a restart does not repeat it.
		w.persistLocked()
	}
	return next, ok
}

// nudge advances the walk when the in-flight request has outlived its deadline.
//
// It is called opportunistically from message dispatch rather than from a timer, which keeps
// the walk free of goroutine lifecycle. The consequence is honest and worth stating: if the
// peer falls completely silent mid-walk, the walk pauses until any message arrives. In
// practice Profile 0 traffic flows at least every thirty seconds, so this is a pause and not a
// deadlock. A peer that sends nothing at all has a larger problem than an unfinished walk.
func (w *discoveryWalk) nudge() (next string, ok bool) {
	if w == nil {
		return "", false
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.inFlight == "" {
		// Idle, or already waiting on nothing. If work remains, start it.
		return w.takeLocked()
	}
	if time.Now().Before(w.deadline) {
		return "", false
	}

	// The answer never came, or came as a refusal the transport logged rather than routed.
	// Abandon this one and continue; recovery is best-effort by nature.
	w.inFlight = ""
	next, ok = w.takeLocked()
	if !ok {
		w.persistLocked()
	}
	return next, ok
}

// abandoned reports the running order the walk has just given up waiting for, for logging.
func (w *discoveryWalk) timedOut() (roID string, yes bool) {
	if w == nil {
		return "", false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.inFlight != "" && !time.Now().Before(w.deadline) {
		return w.inFlight, true
	}
	return "", false
}

// remaining reports how many running orders are still queued, for logging and tests.
func (w *discoveryWalk) remaining() int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.pending)
}

// inFlightID reports the outstanding request, for tests.
func (w *discoveryWalk) inFlightID() string {
	if w == nil {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.inFlight
}

// takeLocked pops the next pending identifier and marks it in flight. Caller holds the lock.
func (w *discoveryWalk) takeLocked() (string, bool) {
	if w.inFlight != "" || len(w.pending) == 0 {
		return "", false
	}
	next := w.pending[0]
	w.pending = w.pending[1:]
	w.inFlight = next
	w.deadline = time.Now().Add(w.timeout)
	w.persistLocked()
	return next, true
}

// stateSubdir gives each transport its own directory under the configured state directory.
//
// The transports keep separate dedup scopes already -- they run concurrently, and MOS 4
// multiplexes three channels with independent messageID sequences -- so mixing their durable
// state in one file would invite exactly the cross-talk the scoping prevents. An empty state
// directory means durability is disabled, and must stay empty rather than becoming "./name".
func stateSubdir(base, name string) string {
	if base == "" {
		return ""
	}
	return filepath.Join(base, name)
}

// StateSubdir is the exported form, for main to place a store alongside the transports'.
func StateSubdir(base, name string) string { return stateSubdir(base, name) }
