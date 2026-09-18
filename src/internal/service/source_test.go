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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"airshift/openmos/internal/model"
	"airshift/openmos/internal/repository"
	mosxml "airshift/openmos/internal/xml"
)

type sourceFixture struct {
	source  *CommittedSource
	store   *repository.Durable
	dir     string
	binding repository.SourceBinding
	next    int
}

func newSourceFixture(t *testing.T, destination string) *sourceFixture {
	t.Helper()
	if destination == "" {
		destination = "http://127.0.0.1:1234/v1/openmos-snapshots"
	}
	f := &sourceFixture{dir: t.TempDir(), binding: repository.SourceBinding{SourceID: "source", RundownID: "rundown", MosID: "device", NCSID: "newsroom", Transport: "tcp", Destination: destination}}
	var err error
	f.store, err = repository.OpenCommitted(f.dir, f.binding, true)
	if err != nil {
		t.Fatal(err)
	}
	f.source, err = NewCommittedSource(context.Background(), f.store, f.binding, "synthetic-token", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	return f
}

func (f *sourceFixture) send(t *testing.T, id, operation string) ([]byte, error) {
	t.Helper()
	msg, err := mosxml.ParseMessage(operation)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		f.next++
		id = fmt.Sprint(f.next)
	}
	input := SourceInput{Transport: "tcp", Scope: "tcp:ro", NCSID: "newsroom", MessageID: id, Session: "connection", Content: []byte(operation)}
	if err := f.source.Observe(context.Background(), input); err != nil {
		return nil, err
	}
	out, err := f.source.Apply(context.Background(), input, msg, func(msg mosxml.MOSMessage) ([]byte, error) { return stdxml.Marshal(msg) })
	return out.Reply, err
}

func (f *sourceFixture) accept(t *testing.T, operation string) []byte {
	t.Helper()
	reply, err := f.send(t, "", operation)
	if err != nil {
		t.Fatalf("source rejected %s: %v", operation, err)
	}
	if len(reply) > 0 && !bytes.Contains(reply, []byte("<roStatus>OK</roStatus>")) {
		t.Fatalf("source did not retain message: %s", reply)
	}
	return reply
}

func (f *sourceFixture) snapshot(t *testing.T) SourceSnapshot {
	t.Helper()
	cp, err := f.store.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot SourceSnapshot
	if err := json.Unmarshal(cp.Pending, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func sourceRoster(ids ...string) string {
	var stories strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&stories, "<story><storyID>%s</storyID></story>", id)
	}
	return "<roCreate><roID>rundown</roID><roSlug>Synthetic rundown</roSlug>" + stories.String() + "</roCreate>"
}

func sourceBody(id, body string) string {
	return "<roStorySend><roID>rundown</roID><storyID>" + id + "</storyID><storyBody>" + body + "</storyBody></roStorySend>"
}
func sourceItem(id string) string {
	return `<storyItem><mosItem><itemID>` + id + `</itemID><objID>object</objID><mosID>media</mosID><objType>RAW_TYPE</objType><itemSlug></itemSlug><mosAbstract> Full text </mosAbstract><itemEdDur>00:00:01.25</itemEdDur><objDur></objDur><objTB>59.940</objTB><objPaths><objPath techDescription="">https://media.invalid/clip.mp4</objPath></objPaths><mosExternalMetadata><mosScope>STORY</mosScope><mosSchema>opaque</mosSchema><mosPayload><duration>unchanged</duration></mosPayload></mosExternalMetadata></mosItem></storyItem>`
}

func TestCommittedSourceCoverageOrderClearingAndLifecycle(t *testing.T) {
	f := newSourceFixture(t, "")
	f.accept(t, sourceRoster("first", "second"))
	firstBody := sourceItem("video") + `<p>[CG L3 MAIN\First\Detail\Tab]</p>` + sourceItem("last")
	f.accept(t, sourceBody("first", firstBody))
	if got := f.snapshot(t); got.Complete || len(got.Stories) != 0 {
		t.Fatalf("partial roster advertised completeness: %+v", got)
	}
	f.accept(t, sourceBody("second", ""))
	got := f.snapshot(t)
	if !got.Complete || !got.Active || len(got.Stories) != 2 {
		t.Fatalf("fresh bodies did not complete roster: %+v", got)
	}
	occ := got.Stories[0].Occurrences
	if len(occ) != 3 || occ[0].ID != "video" || occ[1].Kind != "cue" || occ[2].ID != "last" {
		t.Fatalf("mixed occurrence order changed: %+v", occ)
	}
	if occ[0].Label == nil || *occ[0].Label != "" || occ[0].ItemEdDur == nil || *occ[0].ItemEdDur != "00:00:01.25" || occ[0].ObjDur == nil || *occ[0].ObjDur != "" || *occ[0].ObjTB != "59.940" || *occ[0].Abstract != " Full text " || *occ[0].ObjectType != "RAW_TYPE" {
		t.Fatalf("opaque source values or presence changed: %+v", occ[0])
	}
	if occ[0].Media == nil || (*occ[0].Media)[0].Role != "objPath" || (*occ[0].Media)[0].TechDescription == nil || *(*occ[0].Media)[0].TechDescription != "" {
		t.Fatal("media role or explicit empty description was lost")
	}
	if occ[0].Metadata == nil || !strings.Contains((*occ[0].Metadata)[0], `<mosPayload><duration>unchanged</duration></mosPayload>`) {
		t.Fatal("opaque metadata changed")
	}
	cueID := occ[1].ID
	f.accept(t, sourceBody("first", strings.Replace(firstBody, `First\Detail\Tab`, `Changed\\`, 1)))
	got = f.snapshot(t)
	if !got.Complete || got.Stories[0].Occurrences[1].ID != cueID || !reflect.DeepEqual(*got.Stories[0].Occurrences[1].Fields, []string{"Changed", "", ""}) {
		t.Fatalf("unique cue payload edit lost continuity or empty fields: %+v", got)
	}
	f.accept(t, `<roReadyToAir><roID>rundown</roID><roAir>NOTREADY</roAir></roReadyToAir>`)
	if !f.snapshot(t).Active {
		t.Fatal("ready-to-air metadata deactivated the source")
	}
	f.accept(t, sourceRoster("second"))
	if f.snapshot(t).Complete {
		t.Fatal("new roster reused an old body freshness mark")
	}
	f.accept(t, sourceBody("second", ""))
	got = f.snapshot(t)
	if !got.Complete || len(got.Stories) != 1 || got.Stories[0].ID != "second" {
		t.Fatalf("authoritative roster removal failed: %+v", got)
	}
	if _, err := f.store.Stories().Get(context.Background(), "rundown/first"); err == nil {
		t.Fatal("omitted story survived full roster replacement")
	}
	f.accept(t, `<roDelete><roID>rundown</roID></roDelete>`)
	if got := f.snapshot(t); got.Active || !got.Complete || len(got.Stories) != 0 {
		t.Fatalf("deletion did not deactivate source: %+v", got)
	}
	f.accept(t, sourceRoster("first"))
	if got := f.snapshot(t); !got.Active || got.Complete {
		t.Fatal("reactivation reused stale completeness")
	}
}

func TestCommittedSourceIdentityUncertaintyIsScopedToOwnedSession(t *testing.T) {
	f := newSourceFixture(t, "")
	f.accept(t, sourceRoster("story"))
	f.accept(t, sourceBody("story", sourceItem("video")))
	before := f.snapshot(t)
	if !before.Complete {
		t.Fatal("source fixture is not complete")
	}
	for _, input := range []SourceInput{
		{Transport: "ws-client", Scope: "tcp:ro", NCSID: "other-newsroom", Session: "connection"},
		{Transport: "tcp", Scope: "tcp:ro", NCSID: "other-newsroom", Session: "unknown-connection"},
		{Transport: "tcp", Scope: "other-scope", NCSID: "other-newsroom", Session: "connection"},
	} {
		if err := f.source.Observe(context.Background(), input); err != nil {
			t.Fatal(err)
		}
		if got := f.snapshot(t); !got.Complete || got.Revision != before.Revision {
			t.Fatal("unowned input changed the selected source coverage")
		}
	}
	reply, _ := f.send(t, "other-rundown", `<mosItemReplace><roID>other-rundown</roID><storyID>story</storyID><item><itemID>video</itemID></item></mosItemReplace>`)
	if !bytes.Contains(reply, []byte("NACK")) {
		t.Fatal("another rundown's source mutation was accepted")
	}
	if got := f.snapshot(t); !got.Complete || got.Revision != before.Revision {
		t.Fatal("another rundown's unsupported mutation invalidated the selected source")
	}
	input := SourceInput{Transport: "tcp", Scope: "tcp:ro", NCSID: "other-newsroom", Session: "connection"}
	if err := f.source.Observe(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if got := f.snapshot(t); got.Complete || got.Revision <= before.Revision {
		t.Fatal("owned connection identity mismatch left complete source coverage")
	}
}

func TestCommittedSourceRepeatedCuesRemainIndependent(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		fields     [][]string
	}{
		{"identical-back-to-back", `<p>[CG A\Same\][CG A\Same\]</p>`, [][]string{{"Same", ""}, {"Same", ""}}},
		{"repeated-around-another-cue", `<p>[CG A\First]</p><p>[CG B\Middle]</p><p>[CG A\Last]</p>`, [][]string{{"First"}, {"Middle"}, {"Last"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSourceFixture(t, "")
			f.accept(t, sourceRoster("story"))
			operation := sourceBody("story", tc.body)
			reply, err := f.send(t, "cue-body", operation)
			if err != nil || !bytes.Contains(reply, []byte("<roStatus>OK</roStatus>")) {
				t.Fatalf("repeated editorial cues were rejected: %s, %v", reply, err)
			}
			got := f.snapshot(t)
			if !got.Complete || len(got.Stories) != 1 || len(got.Stories[0].Occurrences) != len(tc.fields) {
				t.Fatal("repeated editorial cues did not produce a complete ordered body")
			}
			ids := make(map[string]bool)
			for i, occurrence := range got.Stories[0].Occurrences {
				if occurrence.ID == "" || ids[occurrence.ID] || occurrence.Kind != "cue" || occurrence.CueType != "SERIAL_CG" || occurrence.Fields == nil || !reflect.DeepEqual(*occurrence.Fields, tc.fields[i]) {
					t.Fatalf("independent cue identity, content or order was lost: %+v", occurrence)
				}
				ids[occurrence.ID] = true
			}
			before, _ := f.store.Checkpoint()
			duplicate, err := f.send(t, "cue-body", operation)
			after, _ := f.store.Checkpoint()
			if err != nil || !bytes.Equal(reply, duplicate) || !reflect.DeepEqual(before, after) {
				t.Fatal("transport retry changed the original receipt or allocated more cues")
			}
			f.accept(t, operation)
			after, _ = f.store.Checkpoint()
			if after.Revision <= before.Revision || !bytes.Equal(before.State, after.State) || !reflect.DeepEqual(got.Stories, f.snapshot(t).Stories) {
				t.Fatal("unchanged authoritative body replaced its local cue allocations")
			}
		})
	}
}

func TestCommittedSourceCueReplacementIsBoundedByItems(t *testing.T) {
	for _, tc := range []struct {
		name, region string
		fields       []string
	}{
		{"edit", `<p>[CG A\Edited][CG B\Middle][CG A\Two]</p>`, []string{"Edited", "Middle", "Two"}},
		{"move", `<p>[CG B\Middle][CG A\Two][CG A\One]</p>`, []string{"Middle", "Two", "One"}},
		{"insert", `<p>[CG A\One][CG B\Middle][CG A\Two][CG A\Three]</p>`, []string{"One", "Middle", "Two", "Three"}},
		{"remove", `<p>[CG A\One][CG B\Middle]</p>`, []string{"One", "Middle"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSourceFixture(t, "")
			f.accept(t, sourceRoster("first", "second"))
			beforeRegion := `<p>[CG Head\Keep]</p>` + sourceItem("left")
			afterRegion := sourceItem("right") + `<p>[CG Tail\Stay]</p>`
			f.accept(t, sourceBody("first", beforeRegion+`<p>[CG A\One][CG B\Middle][CG A\Two]</p>`+afterRegion))
			f.accept(t, sourceBody("second", sourceItem("left")+`<p>[CG A\Other story]</p>`))
			before := f.snapshot(t)
			if !before.Complete {
				t.Fatal("initial repeated cues did not complete the source")
			}
			old := before.Stories[0].Occurrences
			f.accept(t, sourceBody("first", beforeRegion+tc.region+afterRegion))
			after := f.snapshot(t)
			if !after.Complete || len(after.Stories) != 2 || !reflect.DeepEqual(before.Stories[1], after.Stories[1]) {
				t.Fatal("regional replacement suspended the source or changed another story")
			}
			got := after.Stories[0].Occurrences
			if len(got) != len(tc.fields)+4 || !reflect.DeepEqual(old[:2], got[:2]) || !reflect.DeepEqual(old[len(old)-2:], got[len(got)-2:]) {
				t.Fatal("regional replacement changed explicit items or unaffected cue regions")
			}
			oldIDs := make(map[string]bool)
			for _, occurrence := range old {
				oldIDs[occurrence.ID] = true
			}
			for i, occurrence := range got[2 : len(got)-2] {
				if occurrence.ID == "" || oldIDs[occurrence.ID] || occurrence.Fields == nil || !reflect.DeepEqual(*occurrence.Fields, []string{tc.fields[i]}) {
					t.Fatal("uncertain region reused an old identity or lost the current ordered payload")
				}
				oldIDs[occurrence.ID] = true
			}
			items, err := f.store.Items().ListByStory(context.Background(), "rundown/first")
			if err != nil || len(items) != 2 || items[0].RawID != "left" || items[0].Order != 2 || items[1].RawID != "right" || items[1].Order != len(got)-1 {
				t.Fatal("normalized items lost their positions in the mixed source order")
			}
		})
	}
}

func TestCommittedSourceCueReplacementWithoutStableAnchors(t *testing.T) {
	for _, tc := range []struct{ name, old, next string }{
		{"unanchored-edit", `<p>[CG A\One][CG B\Middle][CG A\Two]</p>`, `<p>[CG A\Changed][CG B\Middle][CG A\Two]</p>`},
		{"unanchored-move", `<p>[CG A\One][CG B\Two]</p>`, `<p>[CG B\Two][CG A\One]</p>`},
		{"item-move", `<p>[CG Head\Keep]</p>` + sourceItem("left") + `<p>[CG A\One]</p>` + sourceItem("right") + `<p>[CG Tail\Stay]</p>`, `<p>[CG Head\Keep]</p>` + sourceItem("right") + `<p>[CG A\One]</p>` + sourceItem("left") + `<p>[CG Tail\Stay]</p>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSourceFixture(t, "")
			f.accept(t, sourceRoster("story"))
			f.accept(t, sourceBody("story", tc.old))
			before := f.snapshot(t)
			if !before.Complete {
				t.Fatal("initial source was incomplete")
			}
			oldIDs := make(map[string]bool)
			for _, occurrence := range before.Stories[0].Occurrences {
				if occurrence.Kind == "cue" {
					oldIDs[occurrence.ID] = true
				}
			}
			f.accept(t, sourceBody("story", tc.next))
			after := f.snapshot(t)
			if !after.Complete {
				t.Fatal("uncertain continuity suspended an authoritative replacement")
			}
			for _, occurrence := range after.Stories[0].Occurrences {
				if occurrence.Kind == "cue" && (occurrence.ID == "" || oldIDs[occurrence.ID]) {
					t.Fatal("unanchored replacement guessed historical cue continuity")
				}
			}
		})
	}
}

func TestCommittedSourceCueAllocationAvoidsExplicitItemIDs(t *testing.T) {
	f := newSourceFixture(t, "")
	f.accept(t, sourceRoster("story"))
	cues := `<p>[CG A\One][CG A\Two]</p>`
	f.accept(t, sourceBody("story", sourceItem("cue:1")+cues))
	before := f.snapshot(t)
	if !before.Complete || before.Stories[0].Occurrences[1].ID != "cue:2" || before.Stories[0].Occurrences[2].ID != "cue:3" {
		t.Fatal("first allocations collided with an explicit item ID")
	}
	// A new explicit item can use a formerly allocated cue ID or the next counter value.
	f.accept(t, sourceBody("story", sourceItem("cue:1")+sourceItem("cue:2")+sourceItem("cue:4")+cues))
	after := f.snapshot(t)
	if !after.Complete {
		t.Fatal("explicit item collision prevented an authoritative replacement")
	}
	for i, id := range []string{"cue:1", "cue:2", "cue:4", "cue:5", "cue:6"} {
		if after.Stories[0].Occurrences[i].ID != id {
			t.Fatal("replacement reset the cue counter, changed an explicit item or reused an occupied ID")
		}
	}
}

func TestCommittedSourceRetainsOversizeRawBodyWithoutTruncatedPublication(t *testing.T) {
	f := newSourceFixture(t, "")
	roster := sourceRoster("story")
	f.accept(t, roster)
	operation := sourceBody("story", `<p>Retain this script outside the neutral projection.</p>`+strings.Replace(sourceItem("video"), " Full text ", strings.Repeat("x", 2049), 1))
	f.accept(t, operation)
	if got := f.snapshot(t); got.Complete || len(got.Stories) != 0 {
		t.Fatal("oversized source was truncated into an authoritative publication")
	}
	before, _ := f.store.Checkpoint()
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.store, err = repository.OpenCommitted(f.dir, f.binding, false)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := f.store.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	state, err := readSourceState(cp.State)
	if err != nil {
		t.Fatal(err)
	}
	if state.RawRoster != roster || len(state.Stories) != 1 || state.Stories[0].Raw != operation || state.Problem != "source_projection_outside_limits" || cp.Revision != before.Revision {
		t.Fatal("durable OK did not retain the complete over-limit raw source and its publication problem")
	}
	if len(*state.Stories[0].Occurrences[0].Abstract) != 2049 {
		t.Fatal("over-limit source field was truncated in retained state")
	}
}

func TestCommittedSourceReceiptRetentionDoesNotStopAtLegacyCacheCapacity(t *testing.T) {
	f := newSourceFixture(t, "")
	f.accept(t, sourceRoster("story"))
	if err := f.store.Commit(context.Background(), func(_ repository.Repository, cp *repository.SourceCheckpoint) error {
		for i := range 4096 {
			cp.Receipts = append(cp.Receipts, repository.InputReceipt{Scope: "tcp:ro", NCSID: "newsroom", MessageID: fmt.Sprintf("retained:%d", i), Hash: strings.Repeat("0", 64), Response: []byte("retained original reply")})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.accept(t, sourceBody("story", ""))
	if !f.snapshot(t).Complete {
		t.Fatal("receipt cache capacity stopped ordinary source operation")
	}
	cp, _ := f.store.Checkpoint()
	if len(cp.Receipts) != 4098 {
		t.Fatal("old original-response receipts were evicted")
	}
	info, err := os.Stat(filepath.Join(f.dir, "source-checkpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("single-checkpoint receipt cost: %d receipts, %d bytes", len(cp.Receipts), info.Size())
}

type failingStoryItemWrite struct{ repository.ItemRepository }

func (r failingStoryItemWrite) Create(context.Context, *model.Item) (*model.Item, error) {
	return nil, errors.New("injected item storage failure")
}

func TestStoryRetentionPropagatesAnItemWriteFailure(t *testing.T) {
	orders, stories := repository.NewMemoryRunningOrderRepository(), repository.NewMemoryStoryRepository()
	svc := NewMOSService(orders, stories, failingStoryItemWrite{repository.NewMemoryItemRepository()}, repository.NewMemoryObjectRepository(), nil)
	roster, err := mosxml.ParseMessage(sourceRoster("story"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ProcessRunningOrderInfo(context.Background(), roster.(mosxml.RunningOrderInfo), "device"); err != nil {
		t.Fatal(err)
	}
	body, err := mosxml.ParseMessage(sourceBody("story", sourceItem("video")))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ProcessROStorySend(context.Background(), body.(mosxml.ROStorySend)); err == nil {
		t.Fatal("story retention concealed an item write failure")
	}
}

func TestSourceSnapshotUsesCodePointAndWholeByteBounds(t *testing.T) {
	label := strings.Repeat("😀", 512)
	snapshot := SourceSnapshot{Version: 1, SourceID: "source", RundownID: "rundown", Revision: 1, Active: true, Complete: true, Stories: []SourceStory{{ID: "story", Occurrences: []SourceOccurrence{{ID: "item", Kind: "mos_item", Label: &label}}}}}
	if _, err := marshalSource(snapshot); err != nil {
		t.Fatalf("512 Unicode code points were treated as bytes: %v", err)
	}
	label += "😀"
	if _, err := marshalSource(snapshot); err == nil {
		t.Fatal("513-code-point field accepted")
	}
	label = "within field limit"
	metadata := []string{strings.Repeat("x", 16384), strings.Repeat("x", 16384), strings.Repeat("x", 16384), strings.Repeat("x", 16384)}
	snapshot.Stories[0].Occurrences[0].Metadata = &metadata
	if _, err := marshalSource(snapshot); err == nil {
		t.Fatal("whole JSON byte cap ignored for individually valid fields")
	}
}

func TestCommittedSourceReplayAndRestartDoNotRefreshCoverage(t *testing.T) {
	f := newSourceFixture(t, "")
	firstReply, err := f.send(t, "sender-id", sourceRoster("story"))
	if err != nil {
		t.Fatal(err)
	}
	body := sourceBody("story", sourceItem("video")+`<p>[CG A\One][CG A\Two]</p>`)
	f.accept(t, body)
	f.accept(t, metadataReplacement(rundownMetadata))
	assertSourceMetadata(t, f, []string{rundownMetadata})
	accepted, _ := f.store.Checkpoint()
	acceptedStories := f.snapshot(t).Stories
	before := f.snapshot(t).Revision
	reply, err := f.send(t, "sender-id", sourceRoster("story"))
	if err != nil || !bytes.Equal(firstReply, reply) || f.snapshot(t).Revision != before {
		t.Fatal("duplicate was reapplied or original reply changed")
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store, err = repository.OpenCommitted(f.dir, f.binding, false)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := f.store.Checkpoint()
	if err != nil || !reflect.DeepEqual(accepted, reopened) {
		t.Fatal("restart changed the committed allocations, payload, revision or original receipts")
	}
	assertSourceMetadata(t, f, []string{rundownMetadata})
	f.source, err = NewCommittedSource(context.Background(), f.store, f.binding, "synthetic-token", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reply, err = f.send(t, "sender-id", sourceRoster("story"))
	if err != nil || !bytes.Equal(firstReply, reply) {
		t.Fatal("restart lost original input receipt")
	}
	if f.snapshot(t).Complete {
		t.Fatal("old input receipt re-established completeness after restart")
	}
	assertSourceMetadata(t, f, []string{rundownMetadata})
	conflict := strings.Replace(sourceRoster("story"), "Synthetic rundown", "Changed rundown", 1)
	reply, err = f.send(t, "sender-id", conflict)
	if err == nil || !bytes.Contains(reply, []byte("NACK")) {
		t.Fatal("changed duplicate was accepted")
	}
	order, _ := f.store.RunningOrders().Get(context.Background(), "rundown")
	if order.Slug != "Synthetic rundown" {
		t.Fatal("changed duplicate mutated retained state")
	}
	f.accept(t, sourceRoster("story"))
	f.accept(t, body)
	if got := f.snapshot(t); !got.Complete || got.Revision <= before || !reflect.DeepEqual(acceptedStories, got.Stories) {
		t.Fatal("fresh recovery lost the retained repeated-cue allocations or monotonic revision")
	}
	f.accept(t, sourceBody("story", sourceItem("video")+`<p>[CG A\Changed][CG A\Two]</p>`))
	cp, _ := f.store.Checkpoint()
	oldState, _ := readSourceState(accepted.State)
	newState, err := readSourceState(cp.State)
	if err != nil || newState.NextCue != oldState.NextCue+2 || !f.snapshot(t).Complete {
		t.Fatal("post-restart replacement reset the retained allocator or suspended the source")
	}
}

func TestCommittedSourceDurabilityFailureDoesNotAcknowledgeOrLeak(t *testing.T) {
	f := newSourceFixture(t, "")
	f.accept(t, sourceRoster("story"))
	f.accept(t, sourceBody("story", sourceItem("video")))
	backup := f.dir + "-retained"
	if err := os.Rename(f.dir, backup); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(f.dir); _ = os.Rename(backup, f.dir) })
	if err := os.WriteFile(f.dir, []byte("filesystem obstacle"), 0o600); err != nil {
		t.Fatal(err)
	}
	reply, err := f.send(t, "fail", sourceRoster("replacement"))
	if err == nil || bytes.Contains(reply, []byte("<roStatus>OK</roStatus>")) {
		t.Fatal("failed durability returned a successful retention ACK")
	}
	if _, err := f.store.Stories().Get(context.Background(), "rundown/story"); err != nil {
		t.Fatal("failed durable stage destroyed the last committed story")
	}
	if _, err := f.store.Stories().Get(context.Background(), "rundown/replacement"); err == nil {
		t.Fatal("failed durable stage leaked a new story")
	}
	if _, err := f.store.Checkpoint(); err == nil {
		t.Fatal("uncertain storage remained publishable")
	}
	if _, err := os.Stat(filepath.Join(backup, "source-checkpoint.json")); err != nil {
		t.Fatal("last committed file was not preserved")
	}
}

func TestCommittedSourcePublisherReplaysLostReplyAndFencesLateReceipt(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		bodies = append(bodies, append([]byte(nil), body...))
		count := len(bodies)
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer synthetic-token" || r.URL.Path != "/v1/openmos-snapshots" {
			t.Error("publisher changed its authenticated route")
		}
		if count == 1 {
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		if count == 3 {
			close(started)
			<-release
		}
		var snapshot SourceSnapshot
		_ = json.Unmarshal(body, &snapshot)
		_ = json.NewEncoder(w).Encode(map[string]any{"acceptedRevision": snapshot.Revision, "duplicate": count == 2, "destinationApplied": false})
	}))
	defer server.Close()
	f := newSourceFixture(t, server.URL+"/v1/openmos-snapshots")
	f.accept(t, sourceRoster("story"))
	body := sourceBody("story", sourceItem("video")+`<p>[CG A\Same][CG A\Same]</p>`)
	f.accept(t, body)
	f.accept(t, metadataReplacement(rundownMetadata))
	assertSourceMetadata(t, f, []string{rundownMetadata})
	if err := f.source.publishOnce(context.Background(), server.Client()); err == nil {
		t.Fatal("lost reply was reported as accepted")
	}
	if err := f.source.publishOnce(context.Background(), server.Client()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	same := bytes.Equal(bodies[0], bodies[1])
	mu.Unlock()
	if !same {
		t.Fatal("lost reply retried a different source body")
	}
	var replay struct{ Metadata []string }
	if err := json.Unmarshal(bodies[1], &replay); err != nil || !reflect.DeepEqual(replay.Metadata, []string{rundownMetadata}) {
		t.Fatal("pending HTTP replay lost rundown metadata")
	}
	// Exercise the first receipt of a still-pending revision, not only renewal of one already
	// accepted: a duplicate-receipt shortcut must not conceal a missing late-reply fence.
	f.accept(t, body)
	done := make(chan error, 1)
	go func() { done <- f.source.publishOnce(context.Background(), server.Client()) }()
	<-started
	f.accept(t, `<roDelete><roID>rundown</roID></roDelete>`)
	newest, _ := f.store.Checkpoint()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cp, _ := f.store.Checkpoint()
	if cp.AcceptedRevision == cp.Revision || !bytes.Equal(cp.Pending, newest.Pending) {
		t.Fatal("late receipt marked a newer inactive revision accepted")
	}
	if err := f.source.publishOnce(context.Background(), server.Client()); err != nil {
		t.Fatal(err)
	}
	cp, _ = f.store.Checkpoint()
	if cp.AcceptedRevision != cp.Revision {
		t.Fatal("latest inactive revision remained unaccepted")
	}
}

func TestCommittedSourcePublisherHaltsOnConflictAndExpiresCoverage(t *testing.T) {
	var posted SourceSnapshot
	conflict := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&posted)
		if conflict {
			w.WriteHeader(http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"acceptedRevision": posted.Revision, "duplicate": false, "destinationApplied": false})
	}))
	defer server.Close()
	f := newSourceFixture(t, server.URL+"/v1/openmos-snapshots")
	f.accept(t, sourceRoster("story"))
	f.accept(t, sourceBody("story", sourceItem("video")))
	f.source.timeout = time.Nanosecond
	if err := f.source.publishOnce(context.Background(), server.Client()); err != nil {
		t.Fatal(err)
	}
	if posted.Complete || len(posted.Stories) != 0 {
		t.Fatal("expired source renewed complete data")
	}
	before := f.snapshot(t).Revision
	conflict = true
	if err := f.source.publishOnce(context.Background(), server.Client()); err == nil {
		t.Fatal("receiver conflict was ignored")
	}
	if f.snapshot(t).Revision != before {
		t.Fatal("conflict silently bumped the source counter")
	}
	if err := f.source.publishOnce(context.Background(), server.Client()); err == nil {
		t.Fatal("halted source kept publishing")
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.store, err = repository.OpenCommitted(f.dir, f.binding, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewCommittedSource(context.Background(), f.store, f.binding, "synthetic-token", time.Minute); err == nil {
		t.Fatal("restart silently cleared a revision conflict")
	}
}
