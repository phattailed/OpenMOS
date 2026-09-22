package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	stdxml "encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	return newSourceSetWithIDs(t, destination, "rundown", "other")
}

func newSourceSetWithIDs(t *testing.T, destination string, ids ...string) *sourceSetFixture {
	t.Helper()
	if destination == "" {
		destination = "http://127.0.0.1:1234/v1/openmos-snapshots"
	}
	binding := repository.SourceBinding{SourceID: "source", MosID: "device", NCSID: "newsroom", Transport: "tcp", Destination: destination}
	f := &sourceSetFixture{session: "connection", dirs: make(map[string]string), catalogueDir: t.TempDir()}
	var sources []SourceRundownStore
	for _, id := range ids {
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

const twoShowCatalogue = `<roListAll><ro><roID>rundown</roID><roSlug> Show A </roSlug><roEdStart>2030-01-02T10:00:00</roEdStart></ro><ro><roID>other</roID><roSlug/></ro></roListAll>`

func sourceCatalogueXML(t *testing.T, ids ...string) string {
	t.Helper()
	listing := mosxml.ROListAll{}
	for _, id := range ids {
		listing.ROs = append(listing.ROs, mosxml.ROListAllItem{ID: id, Slug: "Synthetic show"})
	}
	raw, err := stdxml.Marshal(listing)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSourceSetEnrollsUnknownOpaqueMembers(t *testing.T) {
	f := newSourceSetFixture(t, "")
	ids := []string{"../unconfigured/show", `..\unconfigured\show`}
	f.accept(t, sourceCatalogueXML(t, ids...))
	got := f.catalogue(t)
	if !got.Complete || len(got.Rundowns) != len(ids) {
		t.Fatalf("authoritative enumeration omitted unconfigured members: %+v", got)
	}
	for i, id := range ids {
		if got.Rundowns[i].ID != id || !f.group.RetainsRundown(id) {
			t.Fatalf("opaque member was changed or not retained: %q", id)
		}
		if f.snapshot(t, id).Complete {
			t.Fatal("membership alone certified a rundown snapshot")
		}
		path := filepath.Join(f.catalogueDir, "rundowns", fmt.Sprintf("%x", sha256.Sum256([]byte(id))), "source-checkpoint.json")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("opaque identity did not retain a digest-keyed checkpoint under the root: %v", err)
		}
	}
}

func TestSourceSetRestartReopensEnrolledContentAndOriginalReceipts(t *testing.T) {
	f := newSourceSetWithIDs(t, "") // No per-rundown binding or directory exists in configuration.
	listing := sourceCatalogueXML(t, "rundown", "other")
	if reply, err := f.send(t, "incidental", sourceRoster("story")); err != nil || !bytes.Contains(reply, []byte("NACK")) || f.group.RetainsRundown("rundown") {
		t.Fatal("incidental traffic enrolled a member without catalogue authority")
	}
	if _, err := f.send(t, "enumeration", listing); err != nil {
		t.Fatal(err)
	}
	ack, err := f.send(t, "original", sourceRoster("story"))
	if err != nil {
		t.Fatal(err)
	}
	f.accept(t, sourceBody("story", `<p>[CG :title\original]</p>`))
	f.accept(t, `<roCreate><roID>other</roID><roSlug>Second</roSlug></roCreate>`)
	before, _ := f.group.byRundown["rundown"].store.Checkpoint()
	stateBefore, _ := readSourceState(before.State)
	if !f.snapshot(t, "rundown").Complete || stateBefore.NextCue == 0 {
		t.Fatal("fixture did not establish independent body coverage and cue identity")
	}
	if err := f.group.catalogue.Close(); err != nil {
		t.Fatal(err)
	}
	catalogue, err := repository.OpenCatalogue(f.catalogueDir, f.group.binding, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalogue.Close() })
	f.group, err = NewCommittedSourceSet(context.Background(), nil, catalogue, "synthetic-token", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := f.group.byRundown["rundown"].store.Checkpoint()
	stateAfter, _ := readSourceState(after.State)
	if after.Revision <= before.Revision || !reflect.DeepEqual(after.Receipts, before.Receipts) || stateAfter.NextCue != stateBefore.NextCue || stateAfter.RawRoster != stateBefore.RawRoster || stateAfter.Stories[0].Raw != stateBefore.Stories[0].Raw {
		t.Fatal("restart lost accepted content, identities, counters or original receipts")
	}
	if cat := f.catalogue(t); cat.Complete || len(cat.Rundowns) != 2 || f.snapshot(t, "rundown").Complete || f.snapshot(t, "other").Complete {
		t.Fatal("restart dropped retained membership or reused old coverage")
	}
	f.session = "replacement"
	replayed, err := f.send(t, "original", sourceRoster("story"))
	if err != nil || !bytes.Equal(replayed, ack) {
		t.Fatal("restart changed the original acknowledgement")
	}
	if _, err := f.send(t, "enumeration", listing); err != nil || f.catalogue(t).Complete {
		t.Fatal("replayed enumeration restored authority after restart")
	}
	f.accept(t, listing)
	if !f.catalogue(t).Complete || f.snapshot(t, "rundown").Complete {
		t.Fatal("fresh membership certified retained body coverage")
	}
	f.accept(t, sourceRoster("story"))
	f.accept(t, sourceBody("story", `<p>[CG :title\updated while unselected]</p>`))
	current := f.snapshot(t, "rundown")
	if !current.Complete || current.Stories[0].Occurrences[0].ID != stateBefore.Stories[0].Occurrences[0].ID || (*current.Stories[0].Occurrences[0].Fields)[0] != "updated while unselected" {
		t.Fatal("fresh recovery lost the retained member's independent update or cue identity")
	}
}

func TestSourceSetUncertaintyAbsenceDeletionAndReappearanceAreDistinct(t *testing.T) {
	f := newSourceSetFixture(t, "")
	f.accept(t, twoShowCatalogue)
	f.accept(t, sourceRoster("story"))
	f.accept(t, sourceBody("story", sourceItem("video")))
	f.accept(t, `<roCreate><roID>other</roID><roSlug>Second</roSlug></roCreate>`)
	foreign := SourceInput{Transport: "tcp", NCSID: "foreign", Scope: "tcp:ro", Session: "foreign-session", MessageID: "foreign-enumeration", Content: []byte(sourceCatalogueXML(t, "foreign-member"))}
	if err := f.group.Observe(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := f.group.Apply(context.Background(), foreign, mosxml.ROListAll{}, func(msg mosxml.MOSMessage) ([]byte, error) { return stdxml.Marshal(msg) }); err == nil || f.group.RetainsRundown("foreign-member") || len(f.catalogue(t).Rundowns) != 2 {
		t.Fatal("wrong-peer enumeration changed membership or enrolled a member")
	}
	f.group.Uncertain(f.session)
	if cat := f.catalogue(t); cat.Complete || len(cat.Rundowns) != 2 {
		t.Fatal("uncertainty removed retained members")
	}
	input := SourceInput{Transport: "tcp", NCSID: "newsroom", Scope: "tcp:ro", Session: f.session, MessageID: "partial", Content: []byte(`<roListAll><ro><roID>rundown</roID></ro>`)}
	if _, err := f.group.Apply(context.Background(), input, mosxml.ROListAll{}, func(msg mosxml.MOSMessage) ([]byte, error) { return stdxml.Marshal(msg) }); err == nil || f.catalogue(t).Complete || len(f.catalogue(t).Rundowns) != 2 {
		t.Fatal("partial enumeration removed members or restored completeness")
	}
	f.accept(t, twoShowCatalogue)
	f.accept(t, sourceRoster("story"))
	f.accept(t, sourceBody("story", sourceItem("video")))
	retained, _ := f.group.byRundown["rundown"].store.Checkpoint()
	f.accept(t, sourceCatalogueXML(t, "other"))
	if cat := f.catalogue(t); !cat.Complete || len(cat.Rundowns) != 1 || cat.Rundowns[0].ID != "other" || f.snapshot(t, "rundown").Complete {
		t.Fatal("fresh complete absence did not remove membership and invalidate its old snapshot")
	}
	absent, _ := f.group.byRundown["rundown"].store.Checkpoint()
	oldState, _ := readSourceState(retained.State)
	absentState, _ := readSourceState(absent.State)
	if !reflect.DeepEqual(retained.Receipts, absent.Receipts) || oldState.RawRoster != absentState.RawRoster || oldState.Stories[0].Raw != absentState.Stories[0].Raw {
		t.Fatal("absence destroyed retained content or acknowledgement history")
	}
	// A retained member still accepts updates while absent; these cannot establish membership.
	f.accept(t, sourceRoster("story"))
	f.accept(t, sourceBody("story", sourceItem("video")))
	if cat := f.catalogue(t); cat.Complete || len(cat.Rundowns) != 1 {
		t.Fatal("incidental roster traffic became a full membership enumeration")
	}
	f.accept(t, twoShowCatalogue)
	if !f.catalogue(t).Complete || f.snapshot(t, "rundown").Complete {
		t.Fatal("reappearance reused a snapshot from outside current membership")
	}
	f.accept(t, `<roListAll/>`)
	if cat := f.catalogue(t); !cat.Complete || len(cat.Rundowns) != 0 || !f.group.RetainsRundown("rundown") || !f.group.RetainsRundown("other") {
		t.Fatal("complete empty membership was confused with uncertainty or deleted retained stores")
	}
	f.accept(t, twoShowCatalogue)
	deletion := `<roDelete><roID>other</roID></roDelete>`
	ack, err := f.send(t, "delete", deletion)
	if err != nil || !bytes.Contains(ack, []byte("<roStatus>OK</roStatus>")) {
		t.Fatal("validated delete was not retained")
	}
	cat := f.catalogue(t)
	if !cat.Complete || len(cat.Rundowns) != 1 || f.snapshot(t, "other").Active {
		t.Fatal("validated delete did not remove membership")
	}
	replay, err := f.send(t, "delete", deletion)
	if err != nil || !bytes.Equal(ack, replay) || f.catalogue(t).Revision != cat.Revision {
		t.Fatal("delete replay changed the original receipt or membership revision")
	}
	f.accept(t, twoShowCatalogue)
	if !f.catalogue(t).Complete || f.snapshot(t, "other").Complete {
		t.Fatal("deleted member's reappearance bypassed independent snapshot recovery")
	}
}

func TestSourceSetCapacityAndInterruptedEnrollmentWithholdAuthority(t *testing.T) {
	f := newSourceSetFixture(t, "")
	f.accept(t, twoShowCatalogue)
	tooMany := make([]string, 101)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("new-%d", i)
	}
	if _, err := f.send(t, "over-capacity", sourceCatalogueXML(t, tooMany...)); err == nil || f.catalogue(t).Complete || len(f.group.members) != 2 {
		t.Fatal("over-capacity enumeration partially enrolled or became complete")
	}
	// Fail the second enrollment after the first has become durable. The current catalogue
	// must remain uncertain, and a restart must reopen that first store without inventing coverage.
	root := filepath.Join(f.catalogueDir, "rundowns")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(root, fmt.Sprintf("%x", sha256.Sum256([]byte("new-second"))))
	if err := os.WriteFile(blocked, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	listing := sourceCatalogueXML(t, "rundown", "other", "new-first", "new-second")
	if _, err := f.send(t, "interrupted", listing); err == nil || f.catalogue(t).Complete || !f.group.RetainsRundown("new-first") || f.group.RetainsRundown("new-second") {
		t.Fatal("interrupted enrollment was treated as complete or lost the completed member")
	}
	first, _ := f.group.byRundown["new-first"].store.Checkpoint()
	if err := f.group.catalogue.Close(); err != nil {
		t.Fatal(err)
	}
	catalogue, err := repository.OpenCatalogue(f.catalogueDir, f.group.binding, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalogue.Close() })
	var configured []SourceRundownStore
	for _, id := range []string{"rundown", "other"} {
		member := f.group.byRundown[id]
		configured = append(configured, SourceRundownStore{Store: member.store, Binding: member.binding})
	}
	f.group, err = NewCommittedSourceSet(context.Background(), configured, catalogue, "synthetic-token", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := f.group.byRundown["new-first"].store.Checkpoint()
	if after.Revision <= first.Revision || !reflect.DeepEqual(after.Receipts, first.Receipts) || f.catalogue(t).Complete {
		t.Fatal("interrupted enrollment restart reused authority or reset a retained stream")
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	f.session = "replacement"
	if _, err := f.send(t, "interrupted", listing); err != nil || f.catalogue(t).Complete || f.group.RetainsRundown("new-second") {
		t.Fatal("replayed failed enumeration enrolled a member or restored authority")
	}
	f.accept(t, listing)
	if cat := f.catalogue(t); !cat.Complete || len(cat.Rundowns) != 4 || f.snapshot(t, "new-second").Complete {
		t.Fatal("fresh enumeration did not finish interrupted enrollment independently of snapshot readiness")
	}
	// Count all retained members, even after authoritative empty membership.
	f.accept(t, `<roListAll/>`)
	f.group.limit = len(f.group.members)
	if _, err := f.send(t, "retained-capacity", sourceCatalogueXML(t, "another")); err == nil || f.catalogue(t).Complete || f.group.RetainsRundown("another") {
		t.Fatal("inactive retained stores were evicted or ignored to make room")
	}
}

func TestSourceSetPublisherIncludesMembersEnrolledDuringDelivery(t *testing.T) {
	for _, endpoint := range []string{"/v1/openmos-snapshots", "/v2/source-sync"} {
		t.Run(endpoint, func(t *testing.T) {
			arrived, release := make(chan struct{}), make(chan struct{})
			var first atomic.Bool
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if first.CompareAndSwap(false, true) {
					close(arrived)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				response := map[string]any{"sourceId": body["sourceId"], "rundownId": body["rundownId"], "revision": body["revision"], "acceptedRevision": body["revision"], "duplicate": false, "destinationApplied": false}
				switch filepath.Base(r.URL.Path) {
				case "start":
					response["published"] = false
				case "missing":
					response["missing"] = body["hashes"]
				case "parts":
					response["hash"], response["stored"] = body["hash"], true
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer receiver.Close()
			f := newSourceSetWithIDs(t, receiver.URL+endpoint)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			done := make(chan struct{})
			go func() { f.group.RunPublisher(ctx); close(done) }()
			defer func() { cancel(); <-done }()
			select {
			case <-arrived:
			case <-ctx.Done():
				t.Fatal("empty source set did not publish its catalogue")
			}
			f.accept(t, twoShowCatalogue)
			f.accept(t, sourceRoster("story"))
			f.accept(t, sourceBody("story", sourceItem("video")))
			f.accept(t, `<roCreate><roID>other</roID><roSlug>Second</roSlug></roCreate>`)
			close(release)
			for {
				cat, err := f.group.catalogue.Checkpoint()
				a, aErr := f.group.byRundown["rundown"].store.Checkpoint()
				b, bErr := f.group.byRundown["other"].store.Checkpoint()
				if err != nil || aErr != nil || bErr != nil {
					t.Fatalf("publisher lost retained state: %v %v %v", err, aErr, bErr)
				}
				if cat.AcceptedRevision == cat.Revision && a.AcceptedRevision == a.Revision && b.AcceptedRevision == b.Revision {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("newly enrolled streams did not each receive their exact latest receipt")
				case <-time.After(10 * time.Millisecond):
				}
			}
			// A live connection does not make an old membership enumeration fresh forever.
			cancel()
			<-done
			f.group.enumerated = time.Now().Add(-2 * time.Minute)
			if !f.group.CatalogueNeedsRefresh() {
				t.Fatal("live heartbeats hid an expired catalogue enumeration")
			}
			if err := f.group.publishCatalogueOnce(context.Background(), receiver.Client()); err != nil && !errors.Is(err, errSourceContinue) {
				t.Fatal(err)
			}
			if cat := f.catalogue(t); cat.Complete || len(cat.Rundowns) != 2 || !f.snapshot(t, "rundown").Complete {
				t.Fatal("expired catalogue destroyed membership or changed independent snapshot readiness")
			}
		})
	}
}

func TestSourceSetV2CapacityIsBoundedWithoutTruncation(t *testing.T) {
	f := newSourceSetWithIDs(t, "http://127.0.0.1:1234/v2/source-sync")
	ids := make([]string, repository.MaxSourceMembers+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("rundown-%d", i)
	}
	if _, err := f.send(t, "over-limit", sourceCatalogueXML(t, ids...)); err == nil || f.catalogue(t).Complete || len(f.group.members) != 0 {
		t.Fatal("v2 capacity failure silently omitted or partially enrolled members")
	}
}

func TestSourceSetExpiredCatalogueReturnsToNormalRenewal(t *testing.T) {
	var posts atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body SourceCatalogue
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		posts.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"sourceId": body.SourceID, "acceptedRevision": body.Revision, "duplicate": false, "destinationApplied": false})
	}))
	defer receiver.Close()
	f := newSourceSetWithIDs(t, receiver.URL+"/v1/openmos-snapshots")
	f.accept(t, `<roListAll/>`)
	f.group.enumerated = time.Now().Add(-2 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	f.group.RunPublisher(ctx)
	cp, err := f.group.catalogue.Checkpoint()
	if err != nil || f.catalogue(t).Complete || cp.AcceptedRevision != cp.Revision || posts.Load() == 0 {
		t.Fatal("expired catalogue did not publish its incomplete revision")
	}
	if posts.Load() > 2 {
		t.Fatal("expired catalogue repeatedly woke itself instead of waiting for normal renewal")
	}
}

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
	f.accept(t, metadataReplacement(rundownMetadata))
	assertSourceMetadata(t, &sourceFixture{store: f.group.byRundown["rundown"].store}, []string{rundownMetadata})
	if !reflect.DeepEqual(f.snapshot(t, "other"), b) || f.catalogue(t).Revision != catRevision {
		t.Fatal("rundown metadata changed another snapshot or catalogue authority")
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
