package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	stdxml "encoding/xml"
	"errors"
	"strings"
	"sync"
	"time"

	"airshift/openmos/internal/repository"
	mosxml "airshift/openmos/internal/xml"
)

const cataloguePath = "/v1/openmos-catalogue"

type SourceCatalogue struct {
	Version  int                      `json:"version"`
	SourceID string                   `json:"sourceId"`
	Revision uint64                   `json:"revision"`
	Complete bool                     `json:"complete"`
	Rundowns []SourceCatalogueRundown `json:"rundowns"`
}

type SourceCatalogueRundown struct {
	ID             string  `json:"id" xml:"roID"`
	Active         bool    `json:"active" xml:"-"`
	Label          *string `json:"label,omitempty" xml:"roSlug"`
	ScheduledStart *string `json:"scheduledStart,omitempty" xml:"roEdStart"`
}

func SourceCatalogueBinding(source repository.SourceBinding) repository.SourceBinding {
	source.RundownID = ""
	if !strings.HasSuffix(source.Destination, "/v2/source-sync") {
		source.Destination = strings.TrimSuffix(source.Destination, "/v1/openmos-snapshots") + cataloguePath
	}
	return source
}

// CommittedSourceSet routes every configured rundown continuously, independently of the
// receiving application's selected show. Native identity/counters still belong to one transport.
type CommittedSourceSet struct {
	mu        sync.Mutex
	members   []*CommittedSource
	byRundown map[string]*CommittedSource
	catalogue *repository.Catalogue
	binding   repository.SourceBinding
	sessions  map[string]time.Time
	owners    map[string]string
	wake      chan struct{}
	transfer  *sourceTransfer
}

func (*CommittedSourceSet) CatalogueEnabled() bool          { return true }
func (g *CommittedSourceSet) RetainsRundown(id string) bool { return g.byRundown[id] != nil }

func (g *CommittedSourceSet) CatalogueNeedsRefresh() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	cp, err := g.catalogue.Checkpoint()
	if err != nil || cp.Halted {
		return false
	}
	var snapshot SourceCatalogue
	return json.Unmarshal(cp.Pending, &snapshot) == nil && !snapshot.Complete
}

type SourceRundownStore struct {
	Store   *repository.Durable
	Binding repository.SourceBinding
}

func NewCommittedSourceSet(ctx context.Context, retained []SourceRundownStore, catalogue *repository.Catalogue, token string, timeout time.Duration) (*CommittedSourceSet, error) {
	if len(retained) == 0 || (len(retained) > 100 && !strings.HasSuffix(retained[0].Binding.Destination, "/v2/source-sync")) || catalogue == nil {
		return nil, errors.New("source set requires 1-100 retained rundowns and a catalogue store")
	}
	g := &CommittedSourceSet{byRundown: make(map[string]*CommittedSource), catalogue: catalogue,
		binding: SourceCatalogueBinding(retained[0].Binding), sessions: make(map[string]time.Time), owners: make(map[string]string), wake: make(chan struct{}, 1)}
	seen := make(map[string]bool)
	for _, entry := range retained {
		if entry.Store == nil || ValidateSourceBinding(entry.Binding) != nil || SourceCatalogueBinding(entry.Binding) != g.binding || seen[entry.Binding.RundownID] {
			return nil, errors.New("source set members require distinct rundown IDs and the same peer, transport, destination and credential")
		}
		seen[entry.Binding.RundownID] = true
	}
	for _, entry := range retained {
		member, err := newCommittedSource(ctx, entry.Store, entry.Binding, token, timeout, true)
		if err != nil {
			return nil, err
		}
		g.members = append(g.members, member)
		g.byRundown[member.binding.RundownID] = member
	}
	if err := g.invalidateCatalogue(); err != nil {
		return nil, err
	}
	return g, nil
}

func marshalCatalogue(snapshot SourceCatalogue) ([]byte, error) {
	if (snapshot.Version != 1 && snapshot.Version != 2) || !sourceText(snapshot.SourceID, 512, true) || snapshot.Revision == 0 || snapshot.Revision > repository.MaxSourceRevision || snapshot.Rundowns == nil || (snapshot.Version == 1 && len(snapshot.Rundowns) > 100) {
		return nil, errors.New("invalid catalogue header or capacity")
	}
	seen := make(map[string]bool)
	for _, ro := range snapshot.Rundowns {
		if !sourceText(ro.ID, 512, true) || seen[ro.ID] {
			return nil, errors.New("catalogue requires unique bounded rundown IDs")
		}
		seen[ro.ID] = true
		for _, value := range []*string{ro.Label, ro.ScheduledStart} {
			if value != nil && !sourceText(*value, 512, false) {
				return nil, errors.New("catalogue display field exceeds its bound")
			}
		}
	}
	raw, err := json.Marshal(snapshot)
	if err == nil && snapshot.Version == 1 && len(raw) > 64<<10 {
		err = errors.New("catalogue exceeds 64 KiB")
	}
	return raw, err
}

func (g *CommittedSourceSet) reviseCatalogue(cp *repository.CatalogueCheckpoint, complete bool, rows []SourceCatalogueRundown) error {
	if cp.Halted {
		return errSourceHalted
	}
	proposed := SourceCatalogue{Version: 1, SourceID: g.binding.SourceID, Revision: cp.Revision, Complete: complete, Rundowns: rows}
	if strings.HasSuffix(g.binding.Destination, "/v2/source-sync") {
		proposed.Version = 2
	}
	if proposed.Revision == 0 {
		proposed.Revision = 1
	}
	raw, err := marshalCatalogue(proposed)
	if err != nil {
		return err
	}
	if bytes.Equal(raw, cp.Pending) {
		return nil
	}
	if cp.Revision == repository.MaxSourceRevision {
		return errors.New("catalogue revision exhausted; reset is unsupported")
	}
	proposed.Revision = cp.Revision + 1
	cp.Pending, err = marshalCatalogue(proposed)
	cp.Revision = proposed.Revision
	return err
}

func (g *CommittedSourceSet) invalidateCatalogue() error {
	err := g.catalogue.Commit(func(cp *repository.CatalogueCheckpoint) error {
		return g.reviseCatalogue(cp, false, []SourceCatalogueRundown{})
	})
	if err == nil {
		g.notifyCatalogue()
	}
	return err
}

func (g *CommittedSourceSet) notifyCatalogue() {
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

func (g *CommittedSourceSet) Observe(ctx context.Context, input SourceInput) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	var result error
	for _, member := range g.members {
		if err := member.Observe(ctx, input); !errors.Is(err, errSourceHalted) {
			result = errors.Join(result, err)
		}
	}
	if input.Transport != g.binding.Transport {
		return result
	}
	if input.NCSID != g.binding.NCSID {
		if input.Session != "" && g.owners[input.Scope] == input.Session {
			result = errors.Join(result, g.invalidateCatalogue())
		}
		return result
	}
	if input.Session == "" || !sourceText(input.Scope, 512, true) {
		return errors.Join(result, errors.New("catalogue requires validated connection provenance"))
	}
	last, seen := g.sessions[input.Session]
	if seen && g.owners[input.Scope] != input.Session {
		return errors.Join(result, errors.New("catalogue connection has been superseded"))
	}
	if !seen || time.Since(last) > g.members[0].timeout {
		if err := g.invalidateCatalogue(); err != nil {
			return errors.Join(result, err)
		}
		g.owners[input.Scope] = input.Session
	}
	if g.owners[input.Scope] != input.Session {
		return errors.Join(result, errors.New("catalogue connection has been superseded"))
	}
	g.sessions[input.Session] = time.Now()
	return result
}

func (g *CommittedSourceSet) RefreshSession(session string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, member := range g.members {
		member.RefreshSession(session)
	}
	if seen, exists := g.sessions[session]; exists && time.Since(seen) <= g.members[0].timeout {
		for _, owner := range g.owners {
			if owner == session {
				g.sessions[session] = time.Now()
				break
			}
		}
	}
}

func (g *CommittedSourceSet) Disconnected(session string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, member := range g.members {
		member.Disconnected(session)
	}
	delete(g.sessions, session)
	if g.releaseSession(session) {
		_ = g.invalidateCatalogue() // Uncertain storage makes every later read/publication fail.
	}
}

func (g *CommittedSourceSet) releaseSession(session string) bool {
	owned := false
	for scope, owner := range g.owners {
		if owner == session {
			delete(g.owners, scope)
			owned = true
		}
	}
	return owned
}

func (g *CommittedSourceSet) Uncertain(session string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.uncertain(session)
}

func (g *CommittedSourceSet) uncertain(session string) {
	for _, member := range g.members {
		member.Uncertain(session)
	}
	for _, owner := range g.owners {
		if owner == session {
			_ = g.invalidateCatalogue()
			break
		}
	}
}

// Receipts are retained per rundown, but a peer's messageID sequence spans the whole source.
// Check other stores before routing so a reused ID cannot escape conflict detection by changing RO.
// ponytail: scan the bounded source set; index receipts if measured retained volume warrants it.
func (g *CommittedSourceSet) crossReceipt(input SourceInput, response bool, target *CommittedSource) error {
	if input.MessageID == "" {
		return nil
	}
	conflict := func(receipts []repository.InputReceipt) bool {
		for _, receipt := range receipts {
			if receipt.Scope == input.Scope && receipt.NCSID == input.NCSID && receipt.MessageID == input.MessageID && (len(receipt.Response) == 0) == response {
				return true
			}
		}
		return false
	}
	for _, member := range g.members {
		if member == target {
			continue
		}
		cp, err := member.store.Checkpoint()
		if err != nil {
			return err
		}
		if conflict(cp.Receipts) {
			return errors.New("messageID conflicts with another rundown's retained receipt")
		}
	}
	if target != nil {
		cp, err := g.catalogue.Checkpoint()
		if err != nil {
			return err
		}
		if conflict(cp.Receipts) {
			return errors.New("messageID conflicts with a retained catalogue receipt")
		}
	}
	return nil
}

func (g *CommittedSourceSet) Apply(ctx context.Context, input SourceInput, msg mosxml.MOSMessage, render func(mosxml.MOSMessage) ([]byte, error)) (SourceResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, catalogue := msg.(mosxml.ROListAll)
	_, roster := msg.(mosxml.ROList)
	roID := SourceRundown(msg)
	reject := func(reason string) (SourceResult, error) {
		if catalogue || roster {
			return SourceResult{}, errors.New(reason)
		}
		reply, err := render(mosxml.CreateROAck(roID, "NACK: "+reason, nil))
		return SourceResult{Reply: reply}, err
	}
	seen, live := g.sessions[input.Session]
	if input.Transport != g.binding.Transport || input.NCSID != g.binding.NCSID || input.Session == "" || !live || time.Since(seen) > g.members[0].timeout || g.owners[input.Scope] != input.Session || !sourceText(input.Scope, 512, true) || !sourceText(input.MessageID, 4<<20, false) || len(input.Content) == 0 || len(input.Content) > 6<<20 {
		return reject("message is outside the configured source set or current connection")
	}
	target := g.byRundown[roID]
	if !catalogue && target == nil {
		if roster {
			return SourceResult{}, nil // A discovery response outside the explicit set is not retained.
		}
		return reject("rundown is outside the configured source set")
	}
	if err := g.crossReceipt(input, catalogue || roster, target); err != nil {
		g.uncertain(input.Session)
		return reject(err.Error())
	}
	if catalogue {
		fresh, err := g.applyCatalogue(input)
		return SourceResult{Applied: fresh}, err
	}
	out, err := target.Apply(ctx, input, msg, render)
	if err != nil || out.Recover {
		return out, err
	}
	if out.Applied {
		err = g.updateCatalogue(msg, input.Content)
	}
	return out, err
}

func (g *CommittedSourceSet) applyCatalogue(input SourceInput) (bool, error) {
	sum := sha256.Sum256(input.Content)
	hash := hex.EncodeToString(sum[:])
	cp, err := g.catalogue.Checkpoint()
	if err != nil {
		return false, err
	}
	for _, receipt := range cp.Receipts {
		if input.MessageID != "" && receipt.Scope == input.Scope && receipt.NCSID == input.NCSID && receipt.MessageID == input.MessageID {
			if receipt.Hash == hash {
				return false, nil // A replay must never renew coverage or advance discovery.
			}
			g.uncertain(input.Session)
			return false, errors.New("catalogue messageID conflict")
		}
	}
	var listing struct {
		Rundowns []SourceCatalogueRundown `xml:"ro"`
	}
	if err := stdxml.Unmarshal(input.Content, &listing); err != nil {
		return false, g.rejectCatalogue(input, hash, err)
	}
	rows := []SourceCatalogueRundown{}
	seen := make(map[string]bool)
	for _, ro := range listing.Rundowns {
		if !sourceText(ro.ID, 512, true) || seen[ro.ID] {
			return false, g.rejectCatalogue(input, hash, errors.New("catalogue contains an invalid or repeated rundown identity"))
		}
		seen[ro.ID] = true
		if g.byRundown[ro.ID] != nil {
			ro.Active = true
			rows = append(rows, ro)
		}
	}
	err = g.catalogue.Commit(func(cp *repository.CatalogueCheckpoint) error {
		cp.Raw = string(input.Content)
		if err := g.reviseCatalogue(cp, true, rows); err != nil {
			return err
		}
		if input.MessageID != "" {
			cp.Receipts = append(cp.Receipts, repository.InputReceipt{Scope: input.Scope, NCSID: input.NCSID, MessageID: input.MessageID, Hash: hash})
		}
		return nil
	})
	if err != nil {
		return false, g.rejectCatalogue(input, hash, err)
	}
	g.notifyCatalogue()
	return true, nil
}

func (g *CommittedSourceSet) rejectCatalogue(input SourceInput, hash string, cause error) error {
	err := g.catalogue.Commit(func(cp *repository.CatalogueCheckpoint) error {
		if err := g.reviseCatalogue(cp, false, []SourceCatalogueRundown{}); err != nil {
			return err
		}
		cp.Raw = string(input.Content)
		if input.MessageID != "" {
			cp.Receipts = append(cp.Receipts, repository.InputReceipt{Scope: input.Scope, NCSID: input.NCSID, MessageID: input.MessageID, Hash: hash})
		}
		return nil
	})
	g.notifyCatalogue()
	return errors.Join(cause, err)
}

func (g *CommittedSourceSet) updateCatalogue(msg mosxml.MOSMessage, raw []byte) error {
	_, create := msg.(mosxml.RunningOrderInfo)
	_, remove := msg.(mosxml.RODelete)
	_, metadata := msg.(mosxml.ROMetadataReplace)
	switch msg.(type) {
	case mosxml.RunningOrderInfo, mosxml.ROReplace, mosxml.ROList, mosxml.RODelete, mosxml.ROMetadataReplace:
	default:
		return nil
	}
	var row SourceCatalogueRundown
	if err := stdxml.Unmarshal(raw, &row); err != nil {
		return err
	}
	err := g.catalogue.Commit(func(cp *repository.CatalogueCheckpoint) error {
		var snapshot SourceCatalogue
		if err := json.Unmarshal(cp.Pending, &snapshot); err != nil {
			return err
		}
		if !snapshot.Complete {
			return nil // Only a fresh full enumeration can restore catalogue coverage.
		}
		rows := []SourceCatalogueRundown{}
		found := false
		for _, prior := range snapshot.Rundowns {
			if prior.ID != row.ID {
				rows = append(rows, prior)
				continue
			}
			found = true
			if remove {
				continue
			}
			if metadata {
				if row.Label == nil {
					row.Label = prior.Label
				}
				if row.ScheduledStart == nil {
					row.ScheduledStart = prior.ScheduledStart
				}
			}
			row.Active = true
			rows = append(rows, row)
		}
		if create && !found {
			row.Active = true
			rows = append(rows, row)
		}
		return g.reviseCatalogue(cp, true, rows)
	})
	if err != nil {
		_ = g.invalidateCatalogue()
	}
	g.notifyCatalogue()
	return err
}

var _ SourceReceiver = (*CommittedSourceSet)(nil)
var _ SourceReceiver = (*CommittedSource)(nil)
