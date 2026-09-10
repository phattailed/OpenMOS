package server

import (
	"context"
	stdxml "encoding/xml"
	"errors"
	"fmt"
	"strings"

	"airshift/openmos/internal/model"
	"airshift/openmos/internal/service"
	mosxml "airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"
)

// This file exists because the two transports had drifted apart.
//
// The project's central claim is one shared message core behind two transports, with
// the transports owning framing and envelope rules and nothing else. That was not true:
// the MOS 2.x socket path had fifteen running-order handlers and the MOS 4.0 WebSocket
// path had one, NACKing everything else as unimplemented. Adding fifteen more methods to
// the WebSocket server would have doubled the surface and guaranteed the two drifted
// again -- which is exactly how roElementStat came to be parseable on one transport and
// not the other.
//
// So the running-order handling lives here, transport-agnostic, and each transport
// supplies a peerResponder. The handlers are identical by construction rather than by
// discipline.

// peerResponder is a transport's ability to answer the peer that sent a message.
//
// Respond takes a MOS message and is responsible for whatever enveloping, encoding and
// retry bookkeeping that transport requires. Everything above this interface is shared.
type peerResponder interface {
	// peerLabel identifies the peer for logging. Not used for routing.
	peerLabel() string
	// respond sends a message back to the peer that sent the one being handled.
	respond(ctx context.Context, msg mosxml.MOSMessage) error
	// canOriginate reports whether this lane can carry a message the peer will treat as a REQUEST,
	// as opposed to a response to something the peer sent us.
	//
	// It exists because a MOS 4.0 passive connection cannot. MOS 4.0 §1 says it can -- "when the
	// 'external' device needs to originate a message sequence, for example an roReq message to the
	// 'internal' NCS, it will use this 'passive' connection" -- and the reference NOM does not
	// implement that. A passive connection becomes an output socket whose arrival handler feeds
	// everything to ProcessMOS4MessageResponse and never to the inbound request queue, so no frame
	// on it can reach request dispatch (doc/interop §43).
	//
	// Sending anyway is not merely futile, it is HARMFUL: the peer consumes the frame as the answer
	// to its own outstanding message, its send-complete bookkeeping throws, the queue entry survives,
	// and it re-sends that message every thirty seconds indefinitely.
	canOriginate() bool
}

// originator sends a message the peer will treat as a REQUEST, on a lane that can carry one.
//
// It exists because the lane a message arrives on is not necessarily a lane that can answer back with
// a request of our own. A device holding a passive connection learns about a divergence there, and must
// repair it somewhere else -- so recovery cannot simply reply to the sender.
type originator interface {
	originate(ctx context.Context, msg mosxml.MOSMessage) error
	// ready reports whether a request could be sent right now. A lane that is configured but not yet
	// connected is not ready, and recovery should be reported as deferred rather than attempted.
	ready() bool
}

// roDeps is what running-order handling needs beyond the responder.
type roDeps struct {
	service *service.MOSService
	// resync rate-limits outbound roReq so pull recovery cannot loop. May be nil, in
	// which case recovery is simply not attempted.
	resync *resyncGuard
	// mosID is this device's configured identity, needed when applying a roList.
	mosID string
	// origin is a lane that can carry our requests, when the lane a message arrived on cannot.
	// Nil means recovery is limited to the responding lane.
	origin originator
	// walk sequences roListAll -> roReq-per-running-order discovery. May be nil, in which
	// case an inbound roListAll is reported and no follow-up is made.
	walk *discoveryWalk
}

// dispatchRunningOrder handles the Profile 2 running-order family and the Profile 4
// messages that accompany it in practice.
//
// It reports whether the message was recognised. An unrecognised message is left to the
// caller, which knows what its transport should say about it -- the socket transport
// tolerates silence in places where MOS 4.0 requires a NACK.
func dispatchRunningOrder(ctx context.Context, deps roDeps, r peerResponder, msg mosxml.MOSMessage) (handled bool, err error) {
	// Any inbound traffic is an opportunity to unstick a discovery walk whose answer never
	// arrived. See discoveryWalk.nudge for why this is opportunistic rather than timer-driven.
	advanceWalk(ctx, deps, r)

	switch m := msg.(type) {
	case mosxml.ROReplace:
		return true, handleReplace(ctx, deps, r, m)
	case mosxml.RODelete:
		return true, handleDelete(ctx, deps, r, m)
	case mosxml.ROMetadataReplace:
		return true, handleMetadataReplace(ctx, deps, r, m)
	case mosxml.ROStorySend:
		return true, handleStorySend(ctx, deps, r, m)
	case mosxml.ROReadyToAir:
		return true, handleReadyToAir(ctx, deps, r, m)
	case mosxml.ROElementAction:
		return true, handleElementAction(ctx, deps, r, m)
	case mosxml.ROElementStat:
		return true, handleElementStat(ctx, deps, r, m)
	case mosxml.ROReq:
		return true, handleReq(ctx, deps, r, m)
	case mosxml.ROReqAll:
		return true, handleReqAll(ctx, deps, r)
	case mosxml.ROList:
		return true, handleList(ctx, deps, r, m)
	case mosxml.ROListAll:
		return true, handleListAll(ctx, deps, r, m)
	case mosxml.RunningOrderInfo:
		return true, handleCreate(ctx, deps, r, m)
	default:
		return false, nil
	}
}

func handleReplace(ctx context.Context, deps roDeps, r peerResponder, m mosxml.ROReplace) error {
	logger.Infof("Received roReplace from %s for RO %s", r.peerLabel(), m.ID)
	if err := deps.service.ReplaceRunningOrder(ctx, m); err != nil {
		logger.Errorf("Failed to replace running order %s: %v", m.ID, err)
		return r.respond(ctx, mosxml.CreateROAck(m.ID, "NACK: "+firstLine(err), nil))
	}
	return r.respond(ctx, mosxml.CreateROAck(m.ID, "OK", nil))
}

func handleDelete(ctx context.Context, deps roDeps, r peerResponder, m mosxml.RODelete) error {
	logger.Infof("Received roDelete from %s for RO %s", r.peerLabel(), m.ID)
	if err := deps.service.DeleteRunningOrder(ctx, m.ID); err != nil {
		logger.Errorf("Failed to delete running order %s: %v", m.ID, err)
		return r.respond(ctx, mosxml.CreateROAck(m.ID, "NACK: "+firstLine(err), nil))
	}
	return r.respond(ctx, mosxml.CreateROAck(m.ID, "OK", nil))
}

func handleMetadataReplace(ctx context.Context, deps roDeps, r peerResponder, m mosxml.ROMetadataReplace) error {
	logger.Infof("Received roMetadataReplace from %s for RO %s", r.peerLabel(), m.ID)
	// MOS 4.0 §3.4.4: "If the roID in the roMetadataReplace message does not match an
	// existing roID then no action will be taken and the roMetadataReplace message will
	// be replied to with an roAck message which carrying a status value of NACK."
	if err := deps.service.ReplaceMetadata(ctx, m); err != nil {
		logger.Errorf("Failed to replace metadata for RO %s: %v", m.ID, err)
		return r.respond(ctx, mosxml.CreateROAck(m.ID, "NACK: "+firstLine(err), nil))
	}
	return r.respond(ctx, mosxml.CreateROAck(m.ID, "OK", nil))
}

// handleStorySend applies a story, and on an unknown running order begins pull recovery.
//
// MOS 4.0 §2.3: on a message referencing an unknown roID the device "will assume there
// has been a prior error in communication with the NCS" and request a full rebuild via
// roReq. Refusing without asking leaves the disagreement in place, because the NCS has
// no reason to think anything is wrong.
func handleStorySend(ctx context.Context, deps roDeps, r peerResponder, m mosxml.ROStorySend) error {
	logger.Infof("Received roStorySend from %s: roID=%s storyID=%s", r.peerLabel(), m.ROID, m.StoryID)

	err := deps.service.ProcessROStorySend(ctx, m)
	if err == nil {
		return r.respond(ctx, mosxml.CreateROAck(m.ROID, "OK", nil))
	}

	var unknown *service.UnknownRunningOrderError
	if errors.As(err, &unknown) {
		logger.Warningf("Lost synchronisation on RO %s", unknown.ROID)
		// The NACK goes first: the peer is waiting for an answer to THIS message and
		// must know it was not applied. The roReq follows as a separate request.
		if ackErr := r.respond(ctx, mosxml.CreateROAck(m.ROID,
			"NACK: running order not held by this device, requesting resync", nil)); ackErr != nil {
			return ackErr
		}
		requestResync(ctx, deps, r, unknown.ROID)
		return nil
	}

	logger.Errorf("Failed to process roStorySend for story %s in RO %s: %v", m.StoryID, m.ROID, err)
	return r.respond(ctx, mosxml.CreateROAck(m.ROID, "NACK: "+firstLine(err), nil))
}

func handleReadyToAir(ctx context.Context, deps roDeps, r peerResponder, m mosxml.ROReadyToAir) error {
	logger.Infof("Received roReadyToAir from %s for RO %s (%s)", r.peerLabel(), m.ROID, m.ROAir)
	if err := deps.service.SetReadyToAir(ctx, m.ROID, m.ROAir); err != nil {
		logger.Errorf("Failed to set ready-to-air for RO %s: %v", m.ROID, err)
		return r.respond(ctx, mosxml.CreateROAck(m.ROID, "NACK: "+firstLine(err), nil))
	}
	return r.respond(ctx, mosxml.CreateROAck(m.ROID, "OK", nil))
}

func handleElementAction(ctx context.Context, deps roDeps, r peerResponder, m mosxml.ROElementAction) error {
	logger.Infof("Received roElementAction %q from %s for RO %s", m.Operation, r.peerLabel(), m.ROID)

	err := deps.service.ProcessElementAction(ctx, m)
	if err == nil {
		return r.respond(ctx, mosxml.CreateROAck(m.ROID, "OK", nil))
	}

	// The same lost-synchronisation rule applies here, and §2.3 names roElementAction
	// as its example: "if a MOS device receives an roElementAction message which
	// references an unknown roID, storyID or itemID, the MOS device will send an roReq".
	var unknown *service.UnknownRunningOrderError
	if errors.As(err, &unknown) {
		logger.Warningf("Lost synchronisation on RO %s via roElementAction", unknown.ROID)
		if ackErr := r.respond(ctx, mosxml.CreateROAck(m.ROID,
			"NACK: running order not held by this device, requesting resync", nil)); ackErr != nil {
			return ackErr
		}
		requestResync(ctx, deps, r, unknown.ROID)
		return nil
	}

	logger.Errorf("Failed to apply roElementAction for RO %s: %v", m.ROID, err)
	return r.respond(ctx, mosxml.CreateROAck(m.ROID, "NACK: "+firstLine(err), nil))
}

// handleElementStat records a status report from the peer.
//
// As of MOS 2.8.5 this is bidirectional, so an NCS may send it to us. We parse and
// acknowledge it but do not yet act on it, which the README states plainly.
func handleElementStat(ctx context.Context, deps roDeps, r peerResponder, m mosxml.ROElementStat) error {
	logger.Infof("Received roElementStat element=%s from %s: roID=%s status=%s",
		m.Element, r.peerLabel(), m.ROID, m.Status)

	err := deps.service.ProcessElementStatus(ctx, m)
	if err == nil {
		return r.respond(ctx, mosxml.CreateROAck(m.ROID, "OK", nil))
	}

	// A status report for something we do not hold is lost synchronisation like any other, and §2.3
	// names roElementStat's siblings as grounds for a rebuild. Acknowledge, then ask.
	var unknown *service.UnknownRunningOrderError
	if errors.As(err, &unknown) {
		logger.Warningf("Status report for RO %s names an element we do not hold", unknown.ROID)
		if ackErr := r.respond(ctx, mosxml.CreateROAck(m.ROID,
			"NACK: element not held by this device, requesting resync", nil)); ackErr != nil {
			return ackErr
		}
		requestResync(ctx, deps, r, unknown.ROID)
		return nil
	}

	logger.Errorf("Failed to record status for RO %s: %v", m.ROID, err)
	return r.respond(ctx, mosxml.CreateROAck(m.ROID, "NACK: "+firstLine(err), nil))
}

// handleReq answers a request for one running order with a full roList.
//
// MOS 4.0 §3.5.1: answered with roList, or "roAck is sent with the status value of NACK
// if the roID is not valid, or if the Running Order is not available".
func handleReq(ctx context.Context, deps roDeps, r peerResponder, m mosxml.ROReq) error {
	logger.Infof("Received roReq from %s for RO %s", r.peerLabel(), m.ROID)

	if strings.TrimSpace(m.ROID) == "" {
		return r.respond(ctx, mosxml.CreateROAck("", "NACK: roReq requires a roID", nil))
	}

	ro, stories, err := deps.service.GetRunningOrderWithStories(ctx, m.ROID)
	if err != nil {
		logger.Infof("roReq for unknown or unavailable RO %s: %v", m.ROID, err)
		return r.respond(ctx, mosxml.CreateROAck(m.ROID, "NACK: running order not available", nil))
	}

	storyInfos, err := storyInfosFor(ctx, deps.service, stories)
	if err != nil {
		return r.respond(ctx, mosxml.CreateROAck(m.ROID, "NACK: "+firstLine(err), nil))
	}

	return r.respond(ctx, mosxml.CreateROList(mosxml.ROListEntry{
		ID:      ro.ID,
		Slug:    ro.Slug,
		Channel: ro.Channel,
		EdDur:   fmt.Sprintf("%d", ro.Duration),
		Stories: storyInfos,
		// Running-order level, so only PLAYLIST-scoped blocks survive. CreateROList filters
		// again as a backstop.
		MosExternalMetadata: mosxml.FilterMetadataForLevel(
			service.ExternalMetadataToWire(ro.ExternalMetadata), mosxml.LevelRunningOrder),
	}))
}

// handleReqAll answers a request for all running orders with roListAll summaries.
//
// MOS 4.0 §3.5.4 carries summary fields only. Stories are deliberately absent: this is
// discovery, and a peer wanting content follows up with roReq per running order.
func handleReqAll(ctx context.Context, deps roDeps, r peerResponder) error {
	logger.Infof("Received roReqAll from %s", r.peerLabel())

	runningOrders, err := deps.service.ListRunningOrders(ctx)
	if err != nil {
		return r.respond(ctx, mosxml.CreateROAck("", "NACK: "+firstLine(err), nil))
	}

	entries := make([]mosxml.ROListAllItem, 0, len(runningOrders))
	for _, ro := range runningOrders {
		entries = append(entries, mosxml.ROListAllItem{
			ID:      ro.ID,
			Slug:    ro.Slug,
			Channel: ro.Channel,
			EdDur:   fmt.Sprintf("%d", ro.Duration),
		})
	}
	// An empty roListAll is a valid answer, observed from a real NCS.
	return r.respond(ctx, mosxml.CreateROListAll(entries))
}

// handleList applies an inbound roList, completing pull recovery. No response is
// defined for roList, so none is sent.
func handleList(ctx context.Context, deps roDeps, r peerResponder, m mosxml.ROList) error {
	logger.Infof("Received roList from %s for RO %s with %d stories", r.peerLabel(), m.ID, len(m.Stories))

	if err := deps.service.ApplyROList(ctx, m, deps.mosID); err != nil {
		logger.Errorf("Failed to apply roList for RO %s: %v", m.ID, err)
		return nil
	}

	// The disagreement is resolved, so a later one is new information rather than a
	// repeat, and should be actionable immediately.
	deps.resync.forget(m.ID)
	logger.Infof("Applied roList for RO %s; local state rebuilt", m.ID)

	// If this roList answered a discovery request, the walk moves on. This is what makes the
	// walk sequential: the next roReq is sent here, on the previous one completing, rather
	// than all of them being fired at once.
	if next, ok := deps.walk.resolved(m.ID); ok {
		sendDiscoveryReq(ctx, deps, r, next)
	}
	return nil
}

// handleCreate applies a roCreate.
//
// This was per-transport for longer than it should have been, on the reasoning that dedup scope
// differs between the transports. That reasoning confused two layers: deduplication happens in
// the transport ABOVE dispatch, using that transport's scope, and only then is the message
// applied. The application step itself is identical, so keeping it per-transport bought nothing
// and cost the client entirely.
//
// The cost was concrete. The MOS 4 client had no roCreate handler at all, so on a live passive
// connection ENPS delivered a running order and the client logged "received unhandled message
// type roCreate" and dropped it. Every roStorySend that followed then had no running order to
// attach to, and the appliance persisted nothing -- from a rundown that had arrived correctly.
// The comment in the client's dispatch path already claimed a pushed roCreate would be applied;
// this is what makes that true.
func handleCreate(ctx context.Context, deps roDeps, r peerResponder, m mosxml.RunningOrderInfo) error {
	logger.Infof("Received roCreate from %s for RO %s with %d stories",
		r.peerLabel(), m.ID, len(m.Stories))

	if err := deps.service.ProcessRunningOrderInfo(ctx, m, deps.mosID); err != nil {
		logger.Errorf("Failed to apply roCreate for RO %s: %v", m.ID, err)
		return r.respond(ctx, mosxml.CreateROAck(m.ID, "NACK: "+firstLine(err), nil))
	}

	// Ack only after persistence, per the ACK contract: an ACK asserts the metadata was saved.
	logger.Infof("Applied roCreate for RO %s", m.ID)
	return r.respond(ctx, mosxml.CreateROAck(m.ID, "OK", nil))
}

// handleListAll begins the second stage of discovery.
//
// roListAll is a summary and nothing more: identifiers, slugs and timings, with no stories or
// items. Applying it as though it were state would be wrong, and ignoring it -- which is what
// OpenMOS did until now -- leaves startup and reconnect recovery unfinished. MOS 4.0 §2.5: "for
// a full listing of the contents of the RO the MOS device must issue a subsequent roReq".
func handleListAll(ctx context.Context, deps roDeps, r peerResponder, m mosxml.ROListAll) error {
	roIDs := make([]string, 0, len(m.ROs))
	for _, ro := range m.ROs {
		roIDs = append(roIDs, ro.ID)
	}

	// An empty roListAll is a legitimate answer -- a real NCS sends self-closing ones -- and
	// means there is nothing to walk.
	if len(roIDs) == 0 {
		logger.Infof("Received empty roListAll from %s; no running orders to discover",
			r.peerLabel())
		return nil
	}

	next, ok, dropped := deps.walk.begin(roIDs)
	if dropped > 0 {
		logger.Warningf("roListAll from %s advertised %d running orders, which exceeds the %d the "+
			"discovery walk will queue; %d were not requested and local state for them stays "+
			"divergent until the next roListAll",
			r.peerLabel(), len(roIDs), defaultWalkMax, dropped)
	}
	if !ok {
		// A request is already outstanding; this list is queued behind it.
		logger.Infof("Received roListAll from %s with %d running orders; queued behind the "+
			"request already in flight", r.peerLabel(), len(roIDs))
		return nil
	}

	logger.Infof("Received roListAll from %s with %d running orders; beginning discovery walk",
		r.peerLabel(), len(roIDs))
	sendDiscoveryReq(ctx, deps, r, next)
	return nil
}

// advanceWalk sends the next roReq if the walk has work and nothing outstanding, including
// after an in-flight request has timed out.
func advanceWalk(ctx context.Context, deps roDeps, r peerResponder) {
	if abandoned, yes := deps.walk.timedOut(); yes {
		logger.Warningf("No roList arrived for RO %s within the discovery walk timeout; "+
			"continuing with the next running order. A roReq may be answered with a NACK "+
			"rather than a roList, so this is expected rather than exceptional.", abandoned)
	}
	if next, ok := deps.walk.nudge(); ok {
		sendDiscoveryReq(ctx, deps, r, next)
	}
}

// sendDiscoveryReq issues one roReq as part of the walk.
//
// It deliberately bypasses resyncGuard. That guard exists to stop a divergence turning into a
// request loop, keyed per running order with a thirty-second interval. A discovery walk is the
// opposite situation: each identifier is requested once, in sequence, because the NCS has just
// told us it holds them. Passing the walk through the loop-breaker would make a legitimate
// first-time walk suppress itself whenever it followed a recent divergence on the same RO.
func sendDiscoveryReq(ctx context.Context, deps roDeps, r peerResponder, roID string) {
	logger.Infof("Discovery walk: sending roReq for RO %s (%d remaining)",
		roID, deps.walk.remaining())
	if err := sendRequest(ctx, deps, r, mosxml.ROReq{ROID: roID}); err != nil {
		logger.Errorf("Discovery walk: failed to send roReq for RO %s: %v", roID, err)
	}
}

// requestResync sends a roReq for a running order we should hold but do not.
//
// Failures are logged and swallowed. Recovery is best-effort: the message that triggered
// it has already been answered, and turning a failed recovery attempt into a connection
// error would replace a recoverable disagreement with an outage.
func requestResync(ctx context.Context, deps roDeps, r peerResponder, roID string) {
	// No lane check here. sendRequest below picks a lane that can carry the request, so recovery
	// works the same whether the divergence was noticed on a lane that can originate or not. An
	// earlier version branched here and sent directly, which bypassed the walk's one-request-at-a-time
	// serialisation -- the very thing the walk exists to guarantee.
	if !deps.resync.shouldRequest(roID) {
		// Already asked recently. Declining is safe; asking on every refusal is how a loop starts.
		//
		// This is the common case in practice, not the exception: a resynchronising NCS sends every
		// story in the running order, so one divergence produces a dozen refusals within two seconds.
		// Logged at debug volume rather than warning, because the interesting event -- the divergence
		// itself -- has already been reported by the caller.
		logger.Infof("Rebuild for RO %s already requested recently; not asking again", roID)
		return
	}

	// Route through the discovery walk rather than sending directly, so there is exactly one
	// roReq outstanding on this lane. MOS 4.0 §4.1: a sender "must not send another message on
	// the same port until the previous message is acknowledged". Sending here directly meant a
	// divergence arriving mid-walk produced two concurrent requests.
	//
	// Recovery jumps the queue: the peer is actively sending us messages about a running order we
	// do not hold, while the walk is catching up on state nobody is asking for yet.
	next, ok := deps.walk.enqueueUrgent(roID)
	if !ok {
		logger.Infof("Queued roReq for RO %s behind the request already in flight", roID)
		return
	}
	logger.Infof("Sending roReq for RO %s to recover local state", roID)
	if err := sendRequest(ctx, deps, r, mosxml.ROReq{ROID: next}); err != nil {
		logger.Errorf("Failed to send roReq for RO %s: %v", next, err)
		// The identifier stays queued, and the walk's deadline releases the slot, so a lane that
		// recovers later can still make the request.
	}
}

// sendRequest sends a message the peer must treat as a REQUEST, choosing a lane that can carry one.
//
// The lane a triggering message arrived on is not necessarily a lane that can carry a request back. A
// device holding a passive connection learns about running orders there and must ASK about them
// somewhere else, because the peer consumes anything arriving on a passive link as the answer to its
// own last message (doc/interop §43).
//
// This is the single place that choice is made. Recovery and the discovery walk both route through it;
// having the walk reply to the sender was why a roListAll naming two running orders produced a roReq
// that was never answered, leaving the walk stalled behind an in-flight request that could not resolve.
func sendRequest(ctx context.Context, deps roDeps, r peerResponder, msg mosxml.MOSMessage) error {
	if r.canOriginate() {
		return r.respond(ctx, msg)
	}
	if deps.origin == nil {
		return fmt.Errorf("this lane cannot carry a %s and no request lane is configured; "+
			"set WS_CLIENT_REQUEST_LANE", msg.GetMessageType())
	}
	if !deps.origin.ready() {
		return fmt.Errorf("this lane cannot carry a %s and the request lane is not connected yet",
			msg.GetMessageType())
	}
	return deps.origin.originate(ctx, msg)
}

// storyInfosFor converts stored stories, with their items, into the wire shape shared by
// roList, roCreate and roReplace.
//
// It carries mosExternalMetadata back out, filtered by mosScope for the level it sits at. That
// emission was previously missing entirely: blocks were parsed and stored on ingest but the
// conversion back to wire form was dead code, so every roList OpenMOS built silently dropped all
// vendor metadata. Pull recovery hands a peer its state back, so the loss was invisible until
// something compared what went in with what came out.
func storyInfosFor(ctx context.Context, svc *service.MOSService, stories []*model.Story) ([]mosxml.StoryInfo, error) {
	storyInfos := make([]mosxml.StoryInfo, 0, len(stories))
	for _, story := range stories {
		items, err := svc.GetItemsForStory(ctx, story.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get items for story %s: %w", story.ID, err)
		}

		itemInfos := make([]mosxml.ItemInfo, 0, len(items))
		for _, item := range items {
			itemID := item.RawID
			if itemID == "" {
				itemID = item.ID
			}
			itemInfos = append(itemInfos, mosxml.ItemInfo{
				ID:       itemID,
				Slug:     item.Slug,
				Duration: fmt.Sprintf("%d", item.Duration),
				ObjectID: item.ObjectID,
				MosExternalMetadata: mosxml.FilterMetadataForLevel(
					service.ExternalMetadataToWire(item.ExternalMetadata), mosxml.LevelItem),
			})
		}

		storyID := story.RawID
		if storyID == "" {
			storyID = story.ID
		}
		storyInfos = append(storyInfos, mosxml.StoryInfo{
			ID:       storyID,
			Slug:     story.Slug,
			Number:   story.Number,
			Duration: fmt.Sprintf("%d", story.Duration),
			MosExternalMetadata: mosxml.FilterMetadataForLevel(
				service.ExternalMetadataToWire(story.ExternalMetadata), mosxml.LevelStory),
			Items: itemInfos,
		})
	}
	return storyInfos, nil
}

// firstLine trims an error to something that fits a roStatus.
//
// MOS 4.0 §6: roStatus is "OK" or an error description, 128 chars max. A wrapped Go
// error chain routinely exceeds that, and truncating mid-sentence is less useful than
// keeping the outermost cause.
func firstLine(err error) string {
	text := err.Error()
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		text = text[:idx]
	}
	const max = 100 // leaves room for the "NACK: " prefix within 128
	if len(text) > max {
		text = text[:max] + "..."
	}
	return text
}

// --- transport adapters ---

// tcpResponder adapts a MOS 2.x socket connection to peerResponder.
type tcpResponder struct{ conn *ClientConnection }

func (t tcpResponder) peerLabel() string { return t.conn.id }

func (t tcpResponder) respond(ctx context.Context, msg mosxml.MOSMessage) error {
	return t.conn.writeMessage(ctx, msg)
}

// canOriginate is true on the MOS 2.x socket. The NCS dials us, but the socket carries traffic in
// both directions as requests: real multi-vendor traffic shows a prompter sending roReq twelve times
// over three days on exactly this kind of link.
func (t tcpResponder) canOriginate() bool { return true }

// roDeps assembles the shared dependencies from a socket connection.
func (c *ClientConnection) roDeps() roDeps {
	return roDeps{
		service: c.server.service,
		resync:  c.server.resync,
		walk:    c.server.walk,
		mosID:   c.config.MOS.ID,
	}
}

// wsResponder adapts a MOS 4.0 WebSocket session to peerResponder.
//
// The MOS 4.0 envelope echoes the request's messageID on the response (§4.1.7), so the
// responder carries it. Marshalling failures are surfaced rather than swallowed: unlike a
// failed recovery attempt, an unanswerable message leaves the peer retrying.
type wsResponder struct {
	server    *WSServer
	sess      *WSSession
	messageID string
}

// canOriginate is true for a peer that connected to US in standard mode. The connection is the
// MOS 4.0 equivalent of the 2.x socket above, and the peer's own request queue is fed from it.
//
// Not yet exercised live in this direction, so this is the spec's model rather than an observation.
func (w wsResponder) canOriginate() bool { return true }

func (w wsResponder) peerLabel() string {
	return "ncsID=" + w.sess.ncsID + " channel=" + w.sess.channel
}

func (w wsResponder) respond(ctx context.Context, msg mosxml.MOSMessage) error {
	inner, err := stdxml.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal %s: %w", msg.GetMessageType(), err)
	}
	w.server.writeMessage(ctx, w.sess,
		mosxml.WrapEnvelope(w.server.config.MOS.ID, w.sess.ncsID, w.messageID, inner))
	return nil
}

// roDeps assembles the shared dependencies from a WebSocket server.
func (s *WSServer) roDeps() roDeps {
	return roDeps{
		service: s.service,
		resync:  s.resync,
		walk:    s.walk,
		mosID:   s.config.MOS.ID,
	}
}
