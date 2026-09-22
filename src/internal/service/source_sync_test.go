package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"airshift/openmos/internal/repository"
)

func TestLargeSourceV2RetainsCompleteProjection(t *testing.T) {
	f := newSourceFixture(t, "http://127.0.0.1:1234/v2/source-sync")
	ids := make([]string, 500)
	for i := range ids {
		ids[i] = fmt.Sprintf("story-%d", i)
	}
	f.accept(t, sourceRoster(ids...))
	for _, id := range ids {
		f.accept(t, sourceBody(id, sourceItem("video")+`<p>[CG L3 MAIN\`+strings.Repeat("Synthetic ", 30)+`\Detail]</p>`))
	}
	got := f.snapshot(t)
	if got.Version != 2 || !got.Complete || len(got.Stories) != 500 {
		t.Fatalf("large source incomplete: version=%d complete=%v stories=%d", got.Version, got.Complete, len(got.Stories))
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	first, err := newSourceTransfer(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := repository.OpenCommitted(f.dir, f.binding, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	cp, err := reopened.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	after, err := newSourceTransfer(cp.Pending)
	if err != nil || first.binding["manifest"] != after.binding["manifest"] {
		t.Fatal("restart changed immutable transfer identity", err)
	}
	t.Logf("500-story MOS source retained at %d bytes", len(raw))
}

func TestSourceTransferResumesAndOnlySendsChanges(t *testing.T) {
	objects := map[string]bool{}
	parts := map[string][]byte{}
	requests, uploads, heartbeats := 0, 0, 0
	largest := 0
	expired := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		requests++
		largest = max(largest, len(raw))
		var body map[string]json.RawMessage
		if json.Unmarshal(raw, &body) != nil {
			t.Error("invalid request")
		}
		var revision uint64
		_ = json.Unmarshal(body["revision"], &revision)
		reply := map[string]any{"sourceId": "source", "rundownId": "rundown", "revision": revision}
		switch path.Base(r.URL.Path) {
		case "start":
			reply["published"] = false
		case "missing":
			var ids []string
			_ = json.Unmarshal(body["hashes"], &ids)
			missing := []string{}
			for _, id := range ids {
				if !objects[id] {
					missing = append(missing, id)
				}
			}
			reply["missing"] = missing
		case "parts":
			var id, data string
			var index, count int
			_ = json.Unmarshal(body["hash"], &id)
			_ = json.Unmarshal(body["data"], &data)
			_ = json.Unmarshal(body["index"], &index)
			_ = json.Unmarshal(body["parts"], &count)
			decoded, _ := base64.StdEncoding.DecodeString(data)
			if index == 0 {
				parts[id] = nil
			}
			parts[id] = append(parts[id], decoded...)
			reply["hash"] = id
			reply["stored"] = index == count-1
			if index == count-1 && count > 1 && !expired {
				expired = true
				delete(parts, id)
				reply["stored"] = false
				break
			}
			if index == count-1 {
				if makeSourceObject(parts[id]).Hash != id {
					t.Error("content hash mismatch")
				}
				objects[id] = true
				uploads++
			}
		case "commit", "heartbeat":
			reply["acceptedRevision"] = revision
			reply["duplicate"] = true
			reply["destinationApplied"] = false
			if path.Base(r.URL.Path) == "heartbeat" {
				heartbeats++
			}
		default:
			t.Error("unexpected request")
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(reply)
	}))
	defer server.Close()
	stories := make([]map[string]any, 500)
	for i := range stories {
		stories[i] = map[string]any{"id": fmt.Sprint(i), "occurrences": []any{}, "slug": "Synthetic"}
	}
	large := make([]map[string]any, 6000)
	for i := range large {
		large[i] = map[string]any{"id": fmt.Sprint(i), "kind": "cue", "cueType": "UNKNOWN", "fields": []string{strings.Repeat("Synthetic ", 20)}}
	}
	stories[0]["occurrences"] = large
	makeTransfer := func(rev int) *sourceTransfer {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"version": 2, "sourceId": "source", "rundownId": "rundown", "revision": rev, "active": true, "complete": true, "stories": stories})
		transfer, err := newSourceTransfer(raw)
		if err != nil {
			t.Fatal(err)
		}
		return transfer
	}
	send := func(transfer *sourceTransfer) {
		t.Helper()
		for n := 0; n < 2000; n++ {
			complete, err := transfer.step(context.Background(), server.Client(), server.URL+"/v2/source-sync", "token")
			if err != nil {
				t.Fatal(err)
			}
			if complete {
				return
			}
		}
		t.Fatal("transfer did not finish")
	}
	transfer := makeTransfer(1)
	expectedObjects := len(transfer.objects)
	for range 100 {
		if _, err := transfer.step(context.Background(), server.Client(), server.URL+"/v2/source-sync", "token"); err != nil {
			t.Fatal(err)
		}
	}
	transfer = makeTransfer(1)
	send(transfer)
	if uploads != expectedObjects {
		t.Fatalf("resumption resent complete objects: %d", uploads)
	}
	before := uploads
	stories[10]["slug"] = "Changed"
	send(makeTransfer(2))
	if uploads-before != 2 {
		t.Fatalf("one edit sent %d objects", uploads-before)
	}
	before = uploads
	stories[0], stories[1] = stories[1], stories[0]
	transfer = makeTransfer(3)
	send(transfer)
	if uploads-before != 1 {
		t.Fatal("reorder resent stories")
	}
	before = requests
	send(transfer)
	if requests-before != 1 || heartbeats != 1 {
		t.Fatal("unchanged source did not use one heartbeat")
	}
	if !expired {
		t.Fatal("partial upload expiry was not exercised")
	}
	if largest > 65536 {
		t.Fatalf("unbounded transfer request: %d", largest)
	}
}

// Optional paired qualification against the actual consumer's loopback test gateway.
// Normal repository checks use the deterministic receiver above and need no sibling checkout.
func TestSourceSyncAgainstReceiver(t *testing.T) {
	destination := os.Getenv("SOURCE_SYNC_TEST_URL")
	if destination == "" {
		t.Skip("paired loopback receiver not requested")
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	stories := make([]SourceStory, 500)
	for i := range stories {
		stories[i] = SourceStory{ID: fmt.Sprint(i), Occurrences: []SourceOccurrence{}}
		for j := 0; j < 5; j++ {
			fields := []string{strings.Repeat("Synthetic ", 20)}
			stories[i].Occurrences = append(stories[i].Occurrences, SourceOccurrence{ID: fmt.Sprint(j), Kind: "cue", CueType: "UNKNOWN", Fields: &fields})
		}
	}
	// One large story spans several object chunks, each itself split into bounded requests.
	for j := 5; j < 1500; j++ {
		fields := []string{strings.Repeat("Synthetic ", 50)}
		stories[0].Occurrences = append(stories[0].Occurrences, SourceOccurrence{ID: fmt.Sprint(j), Kind: "cue", CueType: "UNKNOWN", Fields: &fields})
	}
	snapshot := SourceSnapshot{Version: 2, SourceID: "source", RundownID: "rundown", Revision: 1, Active: true, Complete: true, Stories: stories}
	publish := func(restart bool) {
		t.Helper()
		raw, err := marshalSource(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		transfer, err := newSourceTransfer(raw)
		if err != nil {
			t.Fatal(err)
		}
		for n := 0; n < 2000; n++ {
			if restart && n == 25 {
				transfer, err = newSourceTransfer(raw)
				if err != nil {
					t.Fatal(err)
				}
			}
			complete, err := transfer.step(context.Background(), client, destination, "synthetic-token")
			if err != nil {
				t.Fatal(err)
			}
			if complete {
				return
			}
		}
		t.Fatal("paired source did not finish")
	}
	publish(true)
	snapshot.Revision++
	label := "Changed"
	snapshot.Stories[10].Slug = &label
	publish(false)
	req, _ := http.NewRequest(http.MethodGet, strings.TrimSuffix(destination, "/v2/source-sync")+"/v1/preparation", nil)
	req.Header.Set("Authorization", "Bearer synthetic-token")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var status struct {
		Revision int  `json:"sourceRevision"`
		Complete bool `json:"complete"`
	}
	if json.NewDecoder(response.Body).Decode(&status) != nil || status.Revision != 2 || !status.Complete {
		t.Fatal("paired publication not complete")
	}
}

func TestSourceSetBoundsConcurrencyAndGivesEveryShowATurn(t *testing.T) {
	var mu sync.Mutex
	active, peak := 0, 0
	completed := map[string]bool{}
	smallDone := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		active++
		peak = max(peak, active)
		mu.Unlock()
		defer func() { mu.Lock(); active--; mu.Unlock() }()
		time.Sleep(3 * time.Millisecond)
		var b map[string]any
		if json.NewDecoder(r.Body).Decode(&b) != nil {
			return
		}
		response := map[string]any{"sourceId": b["sourceId"], "rundownId": b["rundownId"], "revision": b["revision"]}
		switch path.Base(r.URL.Path) {
		case "start":
			response["published"] = false
		case "missing":
			response["missing"] = b["hashes"]
		case "parts":
			response["hash"] = b["hash"]
			response["stored"] = true
		case "commit", "heartbeat":
			response["acceptedRevision"] = b["revision"]
			response["duplicate"] = true
			response["destinationApplied"] = false
			if id, ok := b["rundownId"].(string); ok {
				mu.Lock()
				completed[id] = true
				if len(completed) == 96 && !completed["show-0"] && !completed["show-1"] && !completed["show-2"] && !completed["show-3"] {
					once.Do(func() { close(smallDone) })
				}
				mu.Unlock()
			}
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	binding := repository.SourceBinding{SourceID: "source", MosID: "device", NCSID: "newsroom", Transport: "tcp", Destination: server.URL + "/v2/source-sync"}
	stores := make([]SourceRundownStore, 100)
	for i := range stores {
		binding.RundownID = fmt.Sprintf("show-%d", i)
		store, err := repository.OpenCommitted(t.TempDir(), binding, true)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		stores[i] = SourceRundownStore{Store: store, Binding: binding}
	}
	catalogue, err := repository.OpenCatalogue(t.TempDir(), SourceCatalogueBinding(binding), true)
	if err != nil {
		t.Fatal(err)
	}
	defer catalogue.Close()
	set, err := NewCommittedSourceSet(context.Background(), stores, catalogue, "token", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	state := sourceState{Active: true, RosterFresh: true, RawRoster: "<roCreate/>", Stories: []heldSourceStory{}}
	for i := 0; i < 500; i++ {
		state.Stories = append(state.Stories, heldSourceStory{ID: fmt.Sprint(i), Fresh: true, Raw: "<roStorySend/>", Occurrences: []SourceOccurrence{}})
	}
	for i := 0; i < 4; i++ {
		if err := stores[i].Store.Commit(context.Background(), func(_ repository.Repository, cp *repository.SourceCheckpoint) error {
			return set.members[i].revise(cp, state)
		}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { set.RunPublisher(ctx); close(done) }()
	select {
	case <-smallDone:
	case <-time.After(10 * time.Second):
		cancel()
		<-done
		t.Fatal("small shows stalled behind the large show")
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if peak > 4 {
		t.Fatalf("in-flight requests grew with catalog size: %d", peak)
	}
}
