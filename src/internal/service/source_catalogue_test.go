package service

import (
	"bytes"
	"context"
	"encoding/json"
	stdxml "encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"airshift/openmos/internal/repository"
	mosxml "airshift/openmos/internal/xml"
)

type sourceSetFixture struct {
	group        *CommittedSourceSet
	seq          int
	session      string
	dirs         map[string]string
	catalogueDir string
}

func newSourceSetFixture(t *testing.T, destination string) *sourceSetFixture {
	t.Helper()
	if destination == "" {
		destination = "http://127.0.0.1:1234/v1/openmos-snapshots"
	}
	binding := repository.SourceBinding{SourceID: "source", MosID: "device", NCSID: "newsroom", Transport: "tcp", Destination: destination}
	f := &sourceSetFixture{session: "connection", dirs: make(map[string]string), catalogueDir: t.TempDir()}
	var sources []SourceRundownStore
	for _, id := range []string{"rundown", "other"} {
		binding.RundownID = id
		f.dirs[id] = t.TempDir()
		store, err := repository.OpenCommitted(f.dirs[id], binding, true)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		sources = append(sources, SourceRundownStore{Store: store, Binding: binding})
	}
	catalogue, err := repository.OpenCatalogue(f.catalogueDir, SourceCatalogueBinding(binding), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalogue.Close() })
	group, err := NewCommittedSourceSet(context.Background(), sources, catalogue, "synthetic-token", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	f.group = group
	return f
}

func (f *sourceSetFixture) send(t *testing.T, id, operation string) ([]byte, error) {
	t.Helper()
	if id == "" {
		f.seq++
		id = fmt.Sprint(f.seq)
	}
	msg, err := mosxml.ParseMessage(operation)
	if err != nil {
		t.Fatal(err)
	}
	input := SourceInput{Transport: "tcp", NCSID: "newsroom", Scope: "tcp:ro", Session: f.session, MessageID: id, Content: []byte(operation)}
	if err := f.group.Observe(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	out, err := f.group.Apply(context.Background(), input, msg, func(message mosxml.MOSMessage) ([]byte, error) { return stdxml.Marshal(message) })
	return out.Reply, err
}

func (f *sourceSetFixture) accept(t *testing.T, operation string) {
	t.Helper()
	reply, err := f.send(t, "", operation)
	if err != nil || len(reply) > 0 && !bytes.Contains(reply, []byte("<roStatus>OK</roStatus>")) {
		t.Fatalf("source set refused input: %v %s", err, reply)
	}
}

func (f *sourceSetFixture) snapshot(t *testing.T, id string) SourceSnapshot {
	t.Helper()
	cp, err := f.group.byRundown[id].store.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot SourceSnapshot
	if err := json.Unmarshal(cp.Pending, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func (f *sourceSetFixture) catalogue(t *testing.T) SourceCatalogue {
	t.Helper()
	cp, err := f.group.catalogue.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot SourceCatalogue
	if err := json.Unmarshal(cp.Pending, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

const twoShowCatalogue = `<roListAll><ro><roID>rundown</roID><roSlug> Show A </roSlug><roEdStart>2030-01-02T10:00:00</roEdStart></ro><ro><roID>other</roID><roSlug/></ro><ro><roID>outside</roID><roSlug>Outside configured set</roSlug></ro></roListAll>`

func TestSourceSetRetainsUnselectedEditsAndExactDisplayProperties(t *testing.T) {
	f := newSourceSetFixture(t, "")
	f.accept(t, twoShowCatalogue)
	cat := f.catalogue(t)
	if !cat.Complete || len(cat.Rundowns) != 2 || *cat.Rundowns[0].Label != " Show A " || *cat.Rundowns[0].ScheduledStart != "2030-01-02T10:00:00" || cat.Rundowns[1].Label == nil || *cat.Rundowns[1].Label != "" || cat.Rundowns[1].ScheduledStart != nil {
		t.Fatalf("catalogue changed scope, source strings or optional presence: %+v", cat)
	}
	f.accept(t, `<roCreate><roID>rundown</roID><roSlug> Show A </roSlug><story><storyID>same</storyID><storyNum>A1</storyNum><storySlug>Roster A</storySlug></story></roCreate>`)
	f.accept(t, `<roCreate><roID>other</roID><roSlug>Show B</roSlug><story><storyID>same</storyID><storySlug>Roster B</storySlug></story></roCreate>`)
	bodyA := `<roStorySend><roID>rundown</roID><storyID>same</storyID><storyNum/><storySlug> Body A </storySlug><storyBody><p>[CG :title\first]</p></storyBody><mosExternalMetadata><mosPayload><segment>not interpreted</segment></mosPayload></mosExternalMetadata></roStorySend>`
	bodyB := `<roStorySend><roID>other</roID><storyID>same</storyID><storyBody><p>[CG :title\second]</p></storyBody></roStorySend>`
	f.accept(t, bodyA)
	f.accept(t, bodyB)
	a, b := f.snapshot(t, "rundown"), f.snapshot(t, "other")
	if !a.Complete || !b.Complete || a.Stories[0].Page == nil || *a.Stories[0].Page != "" || *a.Stories[0].Slug != " Body A " || b.Stories[0].Page != nil || *b.Stories[0].Slug != "Roster B" {
		t.Fatalf("story display presence or complete coverage changed: A=%+v B=%+v", a, b)
	}
	if a.Stories[0].ID != b.Stories[0].ID || a.Stories[0].Occurrences[0].ID != b.Stories[0].Occurrences[0].ID {
		t.Fatal("fixture must exercise colliding story and local cue identities across shows")
	}
	catRevision := f.catalogue(t).Revision
	f.accept(t, strings.Replace(bodyA, "first", "latest while unselected", 1))
	latestA, unchangedB := f.snapshot(t, "rundown"), f.snapshot(t, "other")
	if latestA.Revision <= a.Revision || (*latestA.Stories[0].Occurrences[0].Fields)[0] != "latest while unselected" || !reflect.DeepEqual(unchangedB, b) || f.catalogue(t).Revision != catRevision {
		t.Fatal("an A edit was lost, changed B, or incorrectly advanced catalogue authority")
	}
	cp, _ := f.group.byRundown["rundown"].store.Checkpoint()
	if bytes.Contains(cp.Pending, []byte(`"segment"`)) {
		t.Fatal("external metadata was interpreted as an authoritative display property")
	}
	// Float/removal is a real roster replacement, not a fabricated zero-content snapshot.
	f.accept(t, `<roReplace><roID>rundown</roID><roSlug> Show A </roSlug></roReplace>`)
	if empty := f.snapshot(t, "rundown"); !empty.Complete || len(empty.Stories) != 0 || !reflect.DeepEqual(f.snapshot(t, "other"), b) {
		t.Fatal("authoritative A removal was not isolated from B")
	}
	f.accept(t, `<roDelete><roID>other</roID></roDelete>`)
	if got := f.catalogue(t); !got.Complete || len(got.Rundowns) != 1 || got.Rundowns[0].ID != "rundown" {
		t.Fatal("authoritative deletion did not remove catalogue membership")
	}
}

func TestSourceSetReplayCannotRefreshCatalogueAndCrossShowIDsConflict(t *testing.T) {
	f := newSourceSetFixture(t, "")
	if _, err := f.send(t, "list-1", twoShowCatalogue); err != nil {
		t.Fatal(err)
	}
	f.group.Disconnected("connection")
	before := f.catalogue(t)
	if _, err := f.send(t, "list-1", twoShowCatalogue); err != nil {
		t.Fatal(err)
	}
	if got := f.catalogue(t); got.Complete || got.Revision != before.Revision {
		t.Fatal("replayed enumeration restored coverage on a new connection")
	}
	f.accept(t, twoShowCatalogue)
	a := `<roCreate><roID>rundown</roID><roSlug>Show A</roSlug><story><storyID>same</storyID></story></roCreate>`
	ack, err := f.send(t, "peer-1", a)
	if err != nil || !bytes.Contains(ack, []byte("<roStatus>OK</roStatus>")) {
		t.Fatal("initial peer request was not retained")
	}
	f.accept(t, `<roStorySend><roID>rundown</roID><storyID>same</storyID><storyBody/></roStorySend>`)
	if !f.snapshot(t, "rundown").Complete {
		t.Fatal("fixture did not establish complete coverage before the conflict")
	}
	reply, _ := f.send(t, "peer-1", strings.Replace(a, "rundown", "other", 1))
	if !bytes.Contains(reply, []byte("NACK")) || f.catalogue(t).Complete {
		t.Fatal("a peer messageID escaped conflict detection by changing rundown")
	}
	replayed, err := f.send(t, "peer-1", a)
	if err != nil || !bytes.Equal(replayed, ack) || f.snapshot(t, "rundown").Complete {
		t.Fatal("original acknowledgement changed or a replay restored source coverage")
	}
	// Our request-response sequence is independent of the peer's request sequence.
	if _, err := f.send(t, "peer-1", twoShowCatalogue); err != nil {
		t.Fatalf("independent response sequence was rejected: %v", err)
	}
}

func TestSourceSetHTTPReceiptsAreIndependentAndIdentityBound(t *testing.T) {
	var wrongIdentity atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var posted map[string]any
		if r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("producer credential was not retained")
		}
		if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
			t.Error(err)
		}
		receipt := map[string]any{"sourceId": posted["sourceId"], "acceptedRevision": posted["revision"], "duplicate": false, "destinationApplied": false}
		if id, ok := posted["rundownId"]; ok {
			receipt["rundownId"] = id
		}
		if wrongIdentity.Load() == 1 {
			receipt["sourceId"] = "different-source"
		}
		if wrongIdentity.Load() == 2 {
			receipt["rundownId"] = "different-rundown"
		}
		_ = json.NewEncoder(w).Encode(receipt)
	}))
	defer receiver.Close()
	f := newSourceSetFixture(t, receiver.URL+"/v1/openmos-snapshots")
	f.accept(t, twoShowCatalogue)
	for _, id := range []string{"rundown", "other"} {
		f.accept(t, `<roCreate><roID>`+id+`</roID><roSlug>Show</roSlug></roCreate>`)
	}
	ctx := context.Background()
	client := receiver.Client()
	wrongIdentity.Store(1)
	if err := f.group.publishCatalogueOnce(ctx, client); err == nil {
		t.Fatal("wrong-source catalogue receipt was accepted")
	}
	if err := f.group.members[0].publishOnce(ctx, client); err == nil {
		t.Fatal("wrong-source snapshot receipt was accepted")
	}
	wrongIdentity.Store(2)
	if err := f.group.members[0].publishOnce(ctx, client); err == nil {
		t.Fatal("wrong-rundown snapshot receipt was accepted")
	}
	wrongIdentity.Store(0)
	if err := f.group.publishCatalogueOnce(ctx, client); err != nil {
		t.Fatal(err)
	}
	cat, _ := f.group.catalogue.Checkpoint()
	a, _ := f.group.members[0].store.Checkpoint()
	b, _ := f.group.members[1].store.Checkpoint()
	if cat.AcceptedRevision != cat.Revision || a.AcceptedRevision != 0 || b.AcceptedRevision != 0 {
		t.Fatal("catalogue acknowledgement renewed a rundown")
	}
	if err := f.group.members[0].publishOnce(ctx, client); err != nil {
		t.Fatal(err)
	}
	a, _ = f.group.members[0].store.Checkpoint()
	b, _ = f.group.members[1].store.Checkpoint()
	if a.AcceptedRevision != a.Revision || b.AcceptedRevision != 0 {
		t.Fatal("one rundown acknowledgement renewed another rundown")
	}
	f.group.mu.Lock()
	f.group.sessions["connection"] = time.Now().Add(-2 * time.Minute)
	f.group.mu.Unlock()
	if err := f.group.publishCatalogueOnce(ctx, client); err != nil {
		t.Fatal(err)
	}
	if f.catalogue(t).Complete || f.snapshot(t, "rundown").Complete || f.snapshot(t, "other").Complete {
		t.Fatal("expired transport authority survived a catalogue renewal")
	}
}

func TestSourceSetCatalogueBoundsNeverTruncate(t *testing.T) {
	f := newSourceSetFixture(t, "")
	f.accept(t, twoShowCatalogue)
	invalid := `<roListAll><ro><roID>rundown</roID><roSlug>` + strings.Repeat("x", 513) + `</roSlug></ro></roListAll>`
	if _, err := f.send(t, "bad-enumeration", invalid); err == nil || f.catalogue(t).Complete {
		t.Fatal("over-limit source text produced an eligible or truncated catalogue")
	}
	if _, err := f.send(t, "bad-enumeration", twoShowCatalogue); err == nil || f.catalogue(t).Complete {
		t.Fatal("changed content reused a rejected catalogue response ID")
	}
	if _, err := f.send(t, "", `<roListAll><ro><roID>rundown</roID></ro><ro><roID>rundown</roID></ro></roListAll>`); err == nil || f.catalogue(t).Complete {
		t.Fatal("duplicate catalogue identities were accepted")
	}
	f.accept(t, `<roListAll/>`)
	if got := f.catalogue(t); !got.Complete || len(got.Rundowns) != 0 {
		t.Fatal("an authoritative empty enumeration was not retained")
	}
}

func TestSourceSetLateCatalogueReceiptCannotAcceptNewerBody(t *testing.T) {
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var snapshot SourceCatalogue
		if err := json.NewDecoder(r.Body).Decode(&snapshot); err != nil {
			t.Error(err)
			return
		}
		if calls.Add(1) == 1 {
			arrived <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sourceId": snapshot.SourceID, "acceptedRevision": snapshot.Revision, "duplicate": false, "destinationApplied": false})
	}))
	defer receiver.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	f := newSourceSetFixture(t, receiver.URL+"/v1/openmos-snapshots")
	f.accept(t, twoShowCatalogue)
	done := make(chan error, 1)
	go func() { done <- f.group.publishCatalogueOnce(ctx, receiver.Client()) }()
	select {
	case <-arrived:
	case <-ctx.Done():
		t.Fatal("catalogue publisher did not reach the receiver")
	}
	f.accept(t, `<roListAll><ro><roID>rundown</roID><roSlug>Changed membership</roSlug></ro></roListAll>`)
	current := f.catalogue(t)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cp, _ := f.group.catalogue.Checkpoint()
	if cp.Revision != current.Revision || cp.AcceptedRevision != 0 {
		t.Fatal("a late receipt accepted the newer catalogue")
	}
	if err := f.group.publishCatalogueOnce(ctx, receiver.Client()); err != nil {
		t.Fatal(err)
	}
	cp, _ = f.group.catalogue.Checkpoint()
	if cp.AcceptedRevision != current.Revision {
		t.Fatal("the current catalogue receipt was not retained")
	}
}

func TestSourceSetObservedInputCannotEraseAnExpiredInterval(t *testing.T) {
	f := newSourceSetFixture(t, "")
	f.accept(t, twoShowCatalogue)
	f.accept(t, sourceRoster("story"))
	f.accept(t, sourceBody("story", sourceItem("video")))
	f.accept(t, `<roCreate><roID>other</roID><roSlug>Second</roSlug></roCreate>`)
	before := f.catalogue(t)
	for _, member := range f.group.members {
		member.sessions[f.session] = time.Now().Add(-2 * member.timeout)
	}
	f.group.sessions[f.session] = time.Now().Add(-2 * time.Minute)
	input := SourceInput{Transport: "tcp", Scope: "tcp:ro", NCSID: "newsroom", Session: f.session}
	if err := f.group.Observe(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if f.catalogue(t).Complete || f.catalogue(t).Revision <= before.Revision || f.snapshot(t, "rundown").Complete || f.snapshot(t, "other").Complete {
		t.Fatal("validated input renewed expired catalogue or rundown authority before the publisher sweep")
	}
}

func TestSourceSetHaltedMemberDoesNotBlockHealthyReconnectOrRestart(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			SourceID  string `json:"sourceId"`
			RundownID string `json:"rundownId"`
			Revision  uint64 `json:"revision"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		if input.RundownID == "rundown" {
			w.WriteHeader(http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sourceId": input.SourceID, "rundownId": input.RundownID, "acceptedRevision": input.Revision, "duplicate": false, "destinationApplied": false})
	}))
	defer receiver.Close()
	ctx := context.Background()
	f := newSourceSetFixture(t, receiver.URL+"/v1/openmos-snapshots")
	f.accept(t, twoShowCatalogue)
	f.accept(t, sourceRoster("story"))
	f.accept(t, sourceBody("story", sourceItem("video")))
	if err := f.group.members[0].publishOnce(ctx, receiver.Client()); !errors.Is(err, errSourceHalted) {
		t.Fatalf("fixture did not halt the first rundown on HTTP 409: %v", err)
	}
	halted, _ := f.group.members[0].store.Checkpoint()
	for _, phase := range []string{"reconnect", "restart"} {
		if phase == "reconnect" {
			f.group.Disconnected(f.session)
		} else {
			var retained []SourceRundownStore
			for _, member := range f.group.members {
				if err := member.store.Close(); err != nil {
					t.Fatal(err)
				}
				store, err := repository.OpenCommitted(f.dirs[member.binding.RundownID], member.binding, false)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				retained = append(retained, SourceRundownStore{Store: store, Binding: member.binding})
			}
			if err := f.group.catalogue.Close(); err != nil {
				t.Fatal(err)
			}
			catalogue, err := repository.OpenCatalogue(f.catalogueDir, f.group.binding, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = catalogue.Close() })
			f.group, err = NewCommittedSourceSet(ctx, retained, catalogue, "synthetic-token", time.Minute)
			if err != nil {
				t.Fatalf("one halted member prevented healthy startup: %v", err)
			}
		}
		f.session = phase
		f.accept(t, twoShowCatalogue)
		f.accept(t, `<roCreate><roID>other</roID><roSlug>Second after recovery</roSlug></roCreate>`)
		if !f.snapshot(t, "other").Complete || !f.catalogue(t).Complete {
			t.Fatalf("healthy source coverage did not recover after %s", phase)
		}
		if err := f.group.byRundown["other"].publishOnce(ctx, receiver.Client()); err != nil {
			t.Fatal(err)
		}
		if err := f.group.publishCatalogueOnce(ctx, receiver.Client()); err != nil {
			t.Fatal(err)
		}
		current, _ := f.group.byRundown["rundown"].store.Checkpoint()
		if !reflect.DeepEqual(current, halted) {
			t.Fatalf("%s changed the halted rundown's retained counter, content or receipts", phase)
		}
		if err := f.group.byRundown["rundown"].publishOnce(ctx, receiver.Client()); !errors.Is(err, errSourceHalted) {
			t.Fatal("healthy recovery silently resumed the halted publisher")
		}
	}
}
