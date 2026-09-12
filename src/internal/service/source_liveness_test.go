package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestCommittedSourceHealthDoesNotReviveOwnershipOrFreshness(t *testing.T) {
	for _, state := range []string{"current", "unvalidated", "superseded", "expired-before-sweep", "disconnected", "uncertain"} {
		t.Run(state, func(t *testing.T) {
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var snapshot SourceSnapshot
				_ = json.NewDecoder(r.Body).Decode(&snapshot)
				_ = json.NewEncoder(w).Encode(map[string]any{"acceptedRevision": snapshot.Revision, "duplicate": false, "destinationApplied": false})
			}))
			defer receiver.Close()
			f := newSourceFixture(t, receiver.URL+"/v1/openmos-snapshots")
			f.accept(t, sourceRoster("story"))
			f.accept(t, sourceBody("story", sourceItem("video")))
			f.source.sessions["connection"] = time.Now().Add(-f.source.timeout / 2)
			session := "connection"
			switch state {
			case "unvalidated":
				session = "unknown"
			case "superseded":
				if err := f.source.Observe(context.Background(), SourceInput{Transport: "tcp", Scope: "tcp:ro", NCSID: "newsroom", Session: "replacement"}); err != nil {
					t.Fatal(err)
				}
			case "expired-before-sweep":
				f.source.sessions[session] = time.Now().Add(-2 * f.source.timeout)
			case "disconnected":
				f.source.Disconnected(session)
			case "uncertain":
				f.source.Uncertain(session)
			}
			before, _ := f.store.Checkpoint()
			seen, existed := f.source.sessions[session]
			owner := f.source.owners["tcp:ro"]
			f.source.RefreshSession(session)
			after, _ := f.store.Checkpoint()
			if !reflect.DeepEqual(before, after) || f.source.owners["tcp:ro"] != owner {
				t.Fatal("transport health changed source content, revision, receipts or ownership")
			}
			refreshed, exists := f.source.sessions[session]
			if state == "current" || state == "uncertain" {
				if !exists || !refreshed.After(seen) {
					t.Fatal("current owner did not retain transport health")
				}
			} else if exists != existed || !refreshed.Equal(seen) {
				t.Fatal("late transport health revived an unknown, superseded, expired or disconnected owner")
			}
			if state == "expired-before-sweep" {
				if err := f.source.publishOnce(context.Background(), receiver.Client()); err != nil {
					t.Fatal(err)
				}
				if f.snapshot(t).Complete {
					t.Fatal("late health erased an expired interval before the publisher sweep")
				}
				f.source.RefreshSession(session)
				if _, exists := f.source.sessions[session]; exists {
					t.Fatal("late health registered the swept session again")
				}
			}
		})
	}
}
