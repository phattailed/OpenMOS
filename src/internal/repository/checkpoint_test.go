package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"airshift/openmos/internal/model"
)

func TestMemoryRepositoriesDetachNestedValues(t *testing.T) {
	ctx := context.Background()
	stories := NewMemoryStoryRepository()
	story := &model.Story{
		ID: "story", RunningOrderID: "rundown", Metadata: map[string]string{"key": "original"},
		Cues: []model.StoryCue{{Raw: "original", Fields: []string{"original"}, Params: map[string]string{"key": "original"}}},
	}
	created, err := stories.Create(ctx, story)
	if err != nil {
		t.Fatal(err)
	}
	story.Metadata["key"] = "input changed"
	created.Cues[0].Fields[0] = "result changed"
	read, _ := stories.Get(ctx, story.ID)
	read.Cues[0].Params["key"] = "read changed"
	listed, _ := stories.ListByRunningOrder(ctx, "rundown")
	listed[0].Cues[0].Raw = "list changed"
	held, _ := stories.Get(ctx, story.ID)
	if held.Metadata["key"] != "original" || held.Cues[0].Raw != "original" || held.Cues[0].Fields[0] != "original" || held.Cues[0].Params["key"] != "original" {
		t.Errorf("story aliases mutable caller values: %#v", held)
	}

	items := NewMemoryItemRepository()
	item, err := items.Create(ctx, &model.Item{ID: "item", StoryID: "story", Media: &model.MediaPaths{Essence: []model.MediaPath{{URL: "original"}}}})
	if err != nil {
		t.Fatal(err)
	}
	item.Media.Essence[0].URL = "changed"
	heldItem, _ := items.Get(ctx, "item")
	if heldItem.Media.Essence[0].URL != "original" {
		t.Error("item media aliases the create result")
	}

	orders := NewMemoryRunningOrderRepository()
	at := time.Unix(100, 0).UTC()
	order, err := orders.Create(ctx, &model.RunningOrder{ID: "rundown", AirTime: &at})
	if err != nil {
		t.Fatal(err)
	}
	*order.AirTime = time.Unix(200, 0).UTC()
	heldOrder, _ := orders.Get(ctx, "rundown")
	if heldOrder.AirTime.Unix() != 100 {
		t.Error("running order time aliases the create result")
	}
}

func TestCommittedSourceHasOneWriter(t *testing.T) {
	dir := t.TempDir()
	binding := SourceBinding{SourceID: "source", RundownID: "rundown"}
	first, err := OpenCommitted(dir, binding, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCommitted(dir, binding, false); err == nil {
		t.Fatal("a second runtime can overwrite the committed revision stream")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := OpenCommitted(dir, binding, false)
	if err != nil {
		t.Fatalf("closed owner prevented normal restart: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
}

func TestCommittedSourceRefusesLegacyMissingAndMismatchedState(t *testing.T) {
	binding := SourceBinding{SourceID: "source", RundownID: "rundown"}
	dir := t.TempDir()
	if _, err := OpenCommitted(dir, binding, false); err == nil {
		t.Fatal("missing source state silently initialized")
	}
	legacy := []byte(`{"runningOrders":[]}`)
	if err := os.WriteFile(filepath.Join(dir, "runningorders.json"), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCommitted(dir, binding, true); err == nil {
		t.Fatal("legacy state silently migrated")
	}
	got, _ := os.ReadFile(filepath.Join(dir, "runningorders.json"))
	if string(got) != string(legacy) {
		t.Fatal("legacy data changed")
	}
	dir = t.TempDir()
	d, err := OpenCommitted(dir, binding, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	binding.RundownID = "different"
	if _, err := OpenCommitted(dir, binding, false); err == nil {
		t.Fatal("configuration mismatch silently reset source")
	}
	if OpenDurable(dir).OpenError() == nil {
		t.Fatal("legacy mode silently opened a committed source directory")
	}
	binding.RundownID = "rundown"
	path := filepath.Join(dir, checkpointFilename)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := strings.Replace(string(raw), `"digest":"`, `"digest":"0`, 1)
	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCommitted(dir, binding, false); err == nil {
		t.Fatal("source integrity failure silently loaded or reset")
	}
	held, _ := os.ReadFile(path)
	if string(held) != corrupt {
		t.Fatal("failed source open replaced the retained file")
	}
}

func TestCommittedMessageIsAtomicAndSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	binding := SourceBinding{SourceID: "source", RundownID: "rundown", MosID: "device", NCSID: "newsroom", Destination: "http://127.0.0.1:1234/v1/openmos-snapshots"}
	d, err := OpenCommitted(dir, binding, true)
	if err != nil {
		t.Fatal(err)
	}
	apply := func(repos Repository, commit *SourceCheckpoint) error {
		if _, err := repos.RunningOrders().Create(ctx, &model.RunningOrder{ID: "rundown", Slug: "Synthetic rundown"}); err != nil {
			return err
		}
		commit.Revision = 1
		commit.State = []byte(`{"roster":true}`)
		commit.Pending = []byte(`{"version":1,"sourceId":"source","rundownId":"rundown","revision":1,"active":true,"complete":false,"stories":[]}`)
		commit.Receipts = []InputReceipt{{Scope: "tcp:ro", NCSID: "newsroom", MessageID: "7", Hash: checkpointDigest([]byte("operation")), Response: []byte("original reply")}}
		return nil
	}
	if err := d.Commit(ctx, func(repos Repository, commit *SourceCheckpoint) error {
		if err := apply(repos, commit); err != nil {
			return err
		}
		return errors.New("application failed")
	}); err == nil {
		t.Fatal("failed application was accepted")
	}
	orders, _ := d.RunningOrders().List(ctx)
	if len(orders) != 0 {
		t.Fatal("failed application leaked staged state")
	}
	if err := d.Commit(ctx, apply); err != nil {
		t.Fatal(err)
	}
	for _, corrupt := range []func(*SourceCheckpoint){
		func(cp *SourceCheckpoint) { cp.Receipts[0].Hash = "invalid" },
		func(cp *SourceCheckpoint) { cp.Receipts[0].MessageID = "" },
		func(cp *SourceCheckpoint) { cp.Receipts[0].NCSID = "other-peer" },
		func(cp *SourceCheckpoint) { cp.Receipts = append(cp.Receipts, cp.Receipts[0]) },
	} {
		if err := d.Commit(ctx, func(_ Repository, cp *SourceCheckpoint) error { corrupt(cp); return nil }); err == nil {
			t.Fatal("invalid retained receipt was committed")
		}
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenCommitted(dir, binding, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	order, err := reopened.RunningOrders().Get(ctx, "rundown")
	if err != nil || order.Slug != "Synthetic rundown" {
		t.Fatalf("committed state missing after reopen: %v", err)
	}
	commit, err := reopened.Checkpoint()
	if err != nil || commit.Revision != 1 || string(commit.State) != `{"roster":true}` || len(commit.Receipts) != 1 || string(commit.Receipts[0].Response) != "original reply" {
		t.Fatalf("source state and original receipt did not reopen together: %#v, %v", commit, err)
	}
	if err := reopened.RunningOrders().Update(ctx, &model.RunningOrder{ID: "rundown", Slug: "uncommitted"}); err == nil {
		t.Fatal("committed repository allowed a write outside its message transaction")
	}
}
