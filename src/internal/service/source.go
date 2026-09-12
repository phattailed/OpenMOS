package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"airshift/openmos/internal/model"
	"airshift/openmos/internal/repository"
	mosxml "airshift/openmos/internal/xml"
)

// CommittedSource owns one rundown and one durable revision stream. Its mutex serializes input
// and lifecycle decisions; HTTP delivery runs outside it against exact committed bytes.
type CommittedSource struct {
	mu       sync.Mutex
	store    *repository.Durable
	binding  repository.SourceBinding
	token    string
	timeout  time.Duration
	sessions map[string]time.Time
	owners   map[string]string
	wake     chan struct{}
}

type SourceInput struct {
	Transport string
	Scope     string
	NCSID     string
	MessageID string
	Session   string
	Content   []byte
}

type heldSourceStory struct {
	ID          string             `json:"id"`
	Fresh       bool               `json:"fresh"`
	Ambiguous   bool               `json:"ambiguous"`
	Occurrences []SourceOccurrence `json:"occurrences"`
	Raw         string             `json:"raw"`
}

type sourceState struct {
	Active      bool              `json:"active"`
	RosterFresh bool              `json:"rosterFresh"`
	Stories     []heldSourceStory `json:"stories"`
	NextCue     uint64            `json:"nextCue"`
	Halted      bool              `json:"halted"`
	RawRoster   string            `json:"rawRoster"`
	Problem     string            `json:"problem,omitempty"`
}

func ValidateSourceBinding(binding repository.SourceBinding) error {
	if !sourceText(binding.SourceID, 512, true) || !sourceText(binding.RundownID, 512, true) || !sourceText(binding.MosID, 512, true) || !sourceText(binding.NCSID, 512, true) {
		return errors.New("committed source requires bounded source, rundown and peer identities")
	}
	if binding.Transport != "tcp" && binding.Transport != "ws-server" && binding.Transport != "ws-client" {
		return errors.New("source transport must be tcp, ws-server or ws-client")
	}
	u, err := url.Parse(binding.Destination)
	if err != nil {
		return errors.New("invalid source destination")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "http" || ip == nil || !ip.IsLoopback() || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/v1/openmos-snapshots" || u.RawPath != "" {
		return errors.New("source destination must be numeric loopback HTTP at /v1/openmos-snapshots")
	}
	return nil
}

// NewCommittedSource invalidates persisted coverage before a publisher can renew it. Opening a
// file never proves that the sender still holds a current roster or fresh story bodies.
func NewCommittedSource(ctx context.Context, store *repository.Durable, binding repository.SourceBinding, token string, timeout time.Duration) (*CommittedSource, error) {
	if err := ValidateSourceBinding(binding); err != nil {
		return nil, err
	}
	if token == "" || strings.ContainsAny(token, "\r\n") || timeout <= 0 {
		return nil, errors.New("source credential and positive peer timeout are required")
	}
	s := &CommittedSource{store: store, binding: binding, token: token, timeout: timeout, sessions: make(map[string]time.Time), owners: make(map[string]string), wake: make(chan struct{}, 1)}
	if err := s.invalidate(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Observe is called only after transport envelope validation. A new connection invalidates the
// coverage of the prior connection; replaying old receipts cannot make that coverage fresh.
func (s *CommittedSource) Observe(ctx context.Context, input SourceInput) error {
	if input.Transport != s.binding.Transport {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.NCSID != s.binding.NCSID {
		// A foreign identity on our established connection makes its coverage uncertain.
		// Unrelated connections and transport scopes cannot invalidate the selected source.
		if input.Session != "" && s.owners[input.Scope] == input.Session {
			return s.invalidate(ctx)
		}
		return nil
	}
	if input.Session == "" {
		return errors.New("source session identity is required")
	}
	if _, seen := s.sessions[input.Session]; !seen {
		if err := s.invalidate(ctx); err != nil {
			return err
		}
		s.owners[input.Scope] = input.Session
	}
	if s.owners[input.Scope] != input.Session {
		return errors.New("source connection has been superseded")
	}
	s.sessions[input.Session] = time.Now()
	return nil
}

func (s *CommittedSource) Disconnected(session string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.sessions[session]; !exists {
		return
	}
	delete(s.sessions, session)
	if s.releaseSession(session) {
		_ = s.invalidate(context.Background())
	} // A failed checkpoint stops all delivery through Checkpoint.
}

func (s *CommittedSource) Uncertain(session string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, owner := range s.owners {
		if owner == session {
			_ = s.invalidate(context.Background())
			break
		}
	}
}

func (s *CommittedSource) releaseSession(session string) bool {
	released := false
	for scope, owner := range s.owners {
		if owner == session {
			delete(s.owners, scope)
			released = true
		}
	}
	return released
}

func (s *CommittedSource) invalidate(ctx context.Context) error {
	err := s.store.Commit(ctx, func(_ repository.Repository, cp *repository.SourceCheckpoint) error {
		state, err := readSourceState(cp.State)
		if err != nil {
			return err
		}
		if state.Halted {
			return errors.New("source is halted; automatic recovery is unsupported")
		}
		state.RosterFresh = false
		for i := range state.Stories {
			state.Stories[i].Fresh = false
		}
		return s.revise(cp, state)
	})
	if err == nil {
		s.notify()
	}
	return err
}

func readSourceState(raw []byte) (sourceState, error) {
	var state sourceState
	if len(raw) == 0 {
		return state, nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&state); err != nil {
		return state, fmt.Errorf("invalid retained source state: %w", err)
	}
	if state.NextCue > repository.MaxSourceRevision {
		return state, errors.New("retained source state exceeds bounds")
	}
	return state, nil
}

func (s *CommittedSource) revise(cp *repository.SourceCheckpoint, state sourceState) error {
	if cp.Revision == repository.MaxSourceRevision {
		return errors.New("source revision exhausted; reset is unsupported")
	}
	snapshot := SourceSnapshot{Version: 1, SourceID: s.binding.SourceID, RundownID: s.binding.RundownID, Revision: cp.Revision + 1, Active: state.Active, Complete: state.RosterFresh, Stories: []SourceStory{}}
	state.Problem = ""
	if !state.RosterFresh {
		state.Problem = "fresh_roster_required"
	}
	for _, story := range state.Stories {
		if !story.Fresh || story.Ambiguous {
			snapshot.Complete = false
			if state.Problem == "" {
				state.Problem = "fresh_story_bodies_required"
			}
			if story.Ambiguous {
				state.Problem = "anonymous_cue_identity_unresolved"
			}
		}
	}
	if snapshot.Complete && state.Active {
		for _, story := range state.Stories {
			snapshot.Stories = append(snapshot.Stories, SourceStory{ID: story.ID, Occurrences: story.Occurrences})
		}
	}
	pending, err := marshalSource(snapshot)
	if err != nil && snapshot.Complete {
		// Receiver limits cannot discard valid MOS content. Raw roster/body values and derived
		// source state stay committed; the neutral receiver gets a visible incomplete suspension.
		state.Problem = "source_projection_outside_limits"
		snapshot.Complete, snapshot.Stories = false, []SourceStory{}
		pending, err = marshalSource(snapshot)
	}
	if err != nil {
		return err
	}
	rawState, err := json.Marshal(state)
	if err != nil {
		return err
	}
	cp.Revision, cp.State, cp.Pending = snapshot.Revision, rawState, pending
	return nil
}

func (s *CommittedSource) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// SourceMessage reports mutations that must never reach a nontransactional legacy handler in
// source mode. Unsupported actions are retained as NACK receipts and invalidate coverage.
func SourceMessage(msg mosxml.MOSMessage) bool {
	switch msg.(type) {
	case mosxml.RunningOrderInfo, mosxml.ROReplace, mosxml.ROList, mosxml.RODelete,
		mosxml.ROStorySend, mosxml.ROReadyToAir, mosxml.ROMetadataReplace, mosxml.ROElementAction,
		mosxml.ROElementStat, mosxml.ROCtrl, mosxml.ROItemCue, mosxml.NCSReqStoryAction,
		mosxml.ROReqStoryAction, mosxml.MosItemReplace:
		return true
	}
	return false
}

// SourceRundown identifies the affected roster for binding checks and transport recovery.
func SourceRundown(msg mosxml.MOSMessage) string {
	switch m := msg.(type) {
	case mosxml.RunningOrderInfo:
		return m.ID
	case mosxml.ROReplace:
		return m.ID
	case mosxml.ROList:
		return m.ID
	case mosxml.RODelete:
		return m.ID
	case mosxml.ROStorySend:
		return m.ROID
	case mosxml.ROReadyToAir:
		return m.ROID
	case mosxml.ROMetadataReplace:
		return m.ID
	case mosxml.ROElementAction:
		return m.ROID
	case mosxml.ROElementStat:
		return m.ROID
	case mosxml.ROCtrl:
		return m.ROID
	case mosxml.ROItemCue:
		return m.ROID
	case mosxml.NCSReqStoryAction:
		return m.ROStorySend.ROID
	case mosxml.ROReqStoryAction:
		return m.StoryAction.ROID
	case mosxml.MosItemReplace:
		return m.ROID
	}
	return ""
}

// Apply commits retained content, coverage, revision, pending delivery and the transport-rendered
// reply together. The caller sends the returned bytes only after this method returns.
func (s *CommittedSource) Apply(ctx context.Context, input SourceInput, msg mosxml.MOSMessage, render func(mosxml.MOSMessage) ([]byte, error)) (reply []byte, recover bool, result error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	roID := SourceRundown(msg)
	respond := func(status string) ([]byte, error) {
		if _, silent := msg.(mosxml.ROList); silent {
			return nil, nil
		}
		return render(mosxml.CreateROAck(roID, status, nil))
	}
	if input.Transport != s.binding.Transport || input.NCSID != s.binding.NCSID || !sourceText(input.Scope, 512, true) || !sourceText(input.MessageID, 4<<20, false) || input.Session == "" || len(input.Content) == 0 || len(input.Content) > 6<<20 || roID != "" && roID != s.binding.RundownID {
		reply, result = respond("NACK: message is outside the committed source binding")
		return
	}
	if _, live := s.sessions[input.Session]; !live || s.owners[input.Scope] != input.Session {
		reply, result = respond("NACK: source connection has not been validated")
		return
	}
	cp, err := s.store.Checkpoint()
	if err != nil {
		reply, _ = respond("NACK: committed source storage is unavailable")
		return reply, false, err
	}
	state, err := readSourceState(cp.State)
	if err != nil {
		return nil, false, err
	}
	if state.Halted {
		reply, result = respond("NACK: source is halted; automatic recovery is unsupported")
		return
	}
	sum := sha256.Sum256(input.Content)
	hash := hex.EncodeToString(sum[:])
	conflict := false
	if input.MessageID != "" {
		for _, receipt := range cp.Receipts {
			if receipt.Scope != input.Scope || receipt.NCSID != input.NCSID || receipt.MessageID != input.MessageID {
				continue
			}
			if receipt.Hash == hash {
				return append([]byte(nil), receipt.Response...), false, nil
			}
			conflict = true
			break
		}
	}
	var applyErr error
	if conflict {
		applyErr = errors.New("messageID conflict")
	} else {
		applyErr = s.store.Commit(ctx, func(repos repository.Repository, cp *repository.SourceCheckpoint) error {
			state, err := readSourceState(cp.State)
			if err != nil {
				return err
			}
			staged := NewMOSService(repos.RunningOrders(), repos.Stories(), repos.Items(), repos.Objects(), nil)
			if err := s.applyMessage(ctx, staged, &state, msg, string(input.Content)); err != nil {
				return err
			}
			if err := s.revise(cp, state); err != nil {
				return err
			}
			reply, err = respond("OK")
			if err != nil {
				return err
			}
			rememberSource(cp, input, hash, reply)
			return nil
		})
	}
	if applyErr != nil {
		// A failed stage is discarded. Only the old repository state plus an incomplete source and
		// its negative receipt are committed; no partial application can escape behind an OK.
		reply, err = respond("NACK: committed source requires a fresh roster and complete bodies")
		if err != nil {
			return nil, false, err
		}
		err = s.store.Commit(ctx, func(_ repository.Repository, cp *repository.SourceCheckpoint) error {
			state, err := readSourceState(cp.State)
			if err != nil {
				return err
			}
			state.RosterFresh = false
			for i := range state.Stories {
				state.Stories[i].Fresh = false
			}
			if err := s.revise(cp, state); err != nil {
				return err
			}
			if !conflict {
				rememberSource(cp, input, hash, reply)
			}
			return nil
		})
		if err != nil {
			return reply, false, err
		}
		s.notify()
		return reply, true, applyErr
	}
	s.notify()
	return reply, false, nil
}

func rememberSource(cp *repository.SourceCheckpoint, input SourceInput, hash string, reply []byte) {
	// MOS 2.x permits absent messageID. Those messages get atomic replacement semantics, but
	// cannot claim transport retry deduplication without an identifier from their sender.
	if input.MessageID == "" {
		return
	}
	cp.Receipts = append(cp.Receipts, repository.InputReceipt{Scope: input.Scope, NCSID: input.NCSID, MessageID: input.MessageID, Hash: hash, Response: append([]byte(nil), reply...)})
}

func (s *CommittedSource) halt(ctx context.Context) error {
	return s.store.Commit(ctx, func(_ repository.Repository, cp *repository.SourceCheckpoint) error {
		state, err := readSourceState(cp.State)
		if err != nil {
			return err
		}
		state.Halted = true
		state.Problem = "receiver_revision_conflict"
		cp.State, err = json.Marshal(state)
		return err
	})
}

func (s *CommittedSource) applyMessage(ctx context.Context, staged *MOSService, state *sourceState, msg mosxml.MOSMessage, raw string) error {
	switch m := msg.(type) {
	case mosxml.RunningOrderInfo:
		return s.applyRoster(ctx, staged, state, mosxml.ROReplace{ID: m.ID, Slug: m.Slug, Channel: m.Channel, EdDur: m.Duration, Stories: m.Stories, MosExternalMetadata: m.MosExternalMetadata}, raw)
	case mosxml.ROReplace:
		return s.applyRoster(ctx, staged, state, m, raw)
	case mosxml.ROList:
		return s.applyRoster(ctx, staged, state, mosxml.ROReplace{ID: m.ID, Slug: m.Slug, Channel: m.Channel, EdDur: m.EdDur, Stories: m.Stories, MosExternalMetadata: m.MosExternalMetadata}, raw)
	case mosxml.RODelete:
		if err := staged.DeleteRunningOrder(ctx, m.ID); err != nil {
			return err
		}
		state.Active, state.RosterFresh, state.Stories = false, true, nil
		state.RawRoster = raw
		return nil
	case mosxml.ROStorySend:
		return s.applyBody(ctx, staged, state, m, raw)
	case mosxml.ROReadyToAir:
		return staged.SetReadyToAir(ctx, m.ROID, m.ROAir)
	case mosxml.ROMetadataReplace:
		return staged.ReplaceMetadata(ctx, m)
	case mosxml.ROElementStat:
		return staged.ProcessElementStatus(ctx, m)
	default:
		return errors.New("source-affecting operation is unsupported; full recovery is required")
	}
}

func (s *CommittedSource) applyRoster(ctx context.Context, staged *MOSService, state *sourceState, roster mosxml.ROReplace, raw string) error {
	seen := make(map[string]bool)
	stories := make([]heldSourceStory, 0, len(roster.Stories))
	for _, story := range roster.Stories {
		if story.ID == "" || seen[story.ID] {
			return errors.New("invalid or duplicate source story identity")
		}
		seen[story.ID] = true
		for _, item := range story.Items {
			if item.Source != nil {
				if err := item.Source.Validate(); err != nil {
					return err
				}
			}
		}
		held := heldSourceStory{ID: story.ID, Occurrences: []SourceOccurrence{}}
		for _, old := range state.Stories {
			if old.ID == story.ID {
				held = old
				held.Fresh = false
				break
			}
		}
		stories = append(stories, held)
	}
	if err := staged.ReplaceRunningOrder(ctx, roster); err != nil {
		return err
	}
	ro, err := staged.runningOrderRepo.Get(ctx, roster.ID)
	if err != nil {
		return err
	}
	ro.MosID = s.binding.MosID
	if err := staged.runningOrderRepo.Update(ctx, ro); err != nil {
		return err
	}
	state.Active, state.RosterFresh, state.Stories = true, true, stories
	state.RawRoster = raw
	return nil
}

func (s *CommittedSource) applyBody(ctx context.Context, staged *MOSService, state *sourceState, body mosxml.ROStorySend, raw string) error {
	if !state.Active || !state.RosterFresh || body.StoryBody.XMLName.Local != "storyBody" {
		return errors.New("fresh roster and explicit story body are required")
	}
	index := -1
	for i, story := range state.Stories {
		if story.ID == body.StoryID {
			index = i
			break
		}
	}
	if index < 0 {
		return errors.New("story is absent from the current source roster")
	}
	elements, err := body.StoryBody.OrderedSource()
	if err != nil {
		return err
	}
	occurrences := make([]SourceOccurrence, 0, len(elements))
	ids := make(map[string]bool)
	cues := make(map[string]bool)
	ambiguous := false
	for _, element := range elements {
		if element.Item != nil {
			item := element.Item
			if item.ID == "" || ids[item.ID] {
				return errors.New("source body item identity is invalid or repeated")
			}
			ids[item.ID] = true
			o := SourceOccurrence{ID: item.ID, Kind: "mos_item", MosID: item.MosID, ObjectID: item.ObjectID, ObjectType: item.ObjectType, Label: item.Label, Abstract: item.Abstract, ItemEdDur: item.ItemEdDur, ObjDur: item.ObjDur, ObjTB: item.ObjTB, Metadata: item.Metadata}
			if item.Media != nil {
				media := make([]SourceMedia, 0, len(*item.Media))
				for _, path := range *item.Media {
					media = append(media, SourceMedia{Role: path.Role, URL: path.URL, TechDescription: path.TechDescription})
				}
				o.Media = &media
			}
			occurrences = append(occurrences, o)
		} else if element.Cue != nil {
			cue := element.Cue
			o := SourceOccurrence{Kind: "cue", CueType: cue.Kind, Raw: &cue.Raw}
			if cue.Verb != "" {
				o.Verb = &cue.Verb
			}
			if cue.Target != "" || cue.Fields != nil || cue.Kind == mosxml.CuePrompter {
				o.Target = &cue.Target
			}
			if cue.Fields != nil {
				o.Fields = &cue.Fields
			}
			if cue.Params != nil {
				o.Params = &cue.Params
			}
			signature, _ := json.Marshal(cueSelector(o))
			if cues[string(signature)] {
				ambiguous = true
			}
			cues[string(signature)] = true
			occurrences = append(occurrences, o)
		}
	}
	old := state.Stories[index].Occurrences
	if state.Stories[index].Ambiguous && len(cues) > 0 {
		ambiguous = true
	}
	if len(cues) > 0 && hasSourceCues(old) {
		if sameCueLayout(old, occurrences) {
			for i := range occurrences {
				if occurrences[i].Kind == "cue" {
					occurrences[i].ID = old[i].ID
				}
			}
		} else {
			ambiguous = true
		}
	}
	if !ambiguous {
		for i := range occurrences {
			if occurrences[i].ID != "" {
				continue
			}
			for {
				if state.NextCue == repository.MaxSourceRevision {
					return errors.New("anonymous cue identity exhausted")
				}
				state.NextCue++
				id := "cue:" + strconv.FormatUint(state.NextCue, 10)
				if !ids[id] {
					occurrences[i].ID = id
					ids[id] = true
					break
				}
			}
		}
	}
	// The body is authoritative, including explicitly empty sections. Recreate its normalized
	// item view inside this detached stage so legacy merge-on-update cannot retain omitted fields.
	storyID := storyPersistenceID(body.ROID, body.StoryID)
	previousItems, err := staged.itemRepo.ListByStory(ctx, storyID)
	if err != nil {
		return err
	}
	for _, item := range previousItems {
		if err := staged.itemRepo.Delete(ctx, item.ID); err != nil {
			return err
		}
	}
	if err := staged.ProcessROStorySend(ctx, body); err != nil {
		return err
	}
	items, err := staged.itemRepo.ListByStory(ctx, storyID)
	if err != nil {
		return err
	}
	order := make(map[string]int)
	for i, occurrence := range occurrences {
		if occurrence.Kind == "mos_item" {
			order[occurrence.ID] = i + 1
		}
	}
	for _, item := range items {
		position, held := order[item.RawID]
		if !held {
			if err := staged.itemRepo.Delete(ctx, item.ID); err != nil {
				return err
			}
			continue
		}
		item.Order = position
		if err := staged.itemRepo.Update(ctx, item); err != nil {
			return err
		}
	}
	story, err := staged.storyRepo.Get(ctx, storyID)
	if err != nil {
		return err
	}
	story.Cues = nil
	for i, element := range elements {
		if cue := element.Cue; cue != nil {
			story.Cues = append(story.Cues, model.StoryCue{Kind: cue.Kind, Raw: cue.Raw, Verb: cue.Verb, Target: cue.Target, Fields: cue.Fields, Params: cue.Params, Paragraph: cue.Paragraph, Order: i})
		}
	}
	if err := staged.storyRepo.Update(ctx, story); err != nil {
		return err
	}
	state.Stories[index].Fresh = true
	state.Stories[index].Ambiguous = ambiguous
	state.Stories[index].Occurrences = occurrences
	state.Stories[index].Raw = raw
	return nil
}

func hasSourceCues(occurrences []SourceOccurrence) bool {
	for _, occurrence := range occurrences {
		if occurrence.Kind == "cue" {
			return true
		}
	}
	return false
}

func sameCueLayout(old, next []SourceOccurrence) bool {
	if len(old) != len(next) {
		return false
	}
	for i, a := range old {
		b := next[i]
		if a.Kind != b.Kind {
			return false
		}
		if a.Kind == "mos_item" {
			if a.ID != b.ID {
				return false
			}
			continue
		}
		if cueSelector(a) != cueSelector(b) {
			return false
		}
	}
	return true
}

// Parsed fields are payload, not identity. A unique unchanged selector in an unchanged mixed
// layout proves the one slot that an edit affects; repeated or reordered selectors do not.
func cueSelector(c SourceOccurrence) [3]string {
	value := func(v *string) string {
		if v == nil {
			return ""
		}
		return *v
	}
	return [3]string{c.CueType, value(c.Verb), value(c.Target)}
}
