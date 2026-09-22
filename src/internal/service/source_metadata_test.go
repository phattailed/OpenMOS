package service

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"airshift/openmos/internal/repository"
	mosxml "airshift/openmos/internal/xml"
)

const rundownMetadata = `<mosExternalMetadata trace="synthetic"><mosScope>PLAYLIST</mosScope><mosSchema>urn:example:opaque</mosSchema><mosPayload><label>First &amp; second</label></mosPayload></mosExternalMetadata>`
const emptyRundownMetadata = `<mosExternalMetadata><mosScope>PLAYLIST</mosScope><mosSchema>urn:example:opaque</mosSchema><mosPayload></mosPayload></mosExternalMetadata>`

func metadataReplacement(blocks string) string {
	return `<roMetadataReplace><roID>rundown</roID>` + blocks + `</roMetadataReplace>`
}

func assertSourceMetadata(t *testing.T, f *sourceFixture, want []string) {
	t.Helper()
	cp, err := f.store.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]json.RawMessage{"state": cp.State, "pending": cp.Pending} {
		var got struct {
			Metadata *[]string `json:"metadata"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.Metadata == nil || !reflect.DeepEqual(*got.Metadata, want) {
			t.Errorf("%s did not retain the exact metadata array (including known empty)", name)
		}
	}
}

func TestCommittedSourceRundownMetadataReplacement(t *testing.T) {
	f := newSourceFixture(t, "")
	f.accept(t, sourceRoster("story"))
	f.accept(t, sourceBody("story", ""))
	before := f.snapshot(t)
	operation := metadataReplacement(rundownMetadata + emptyRundownMetadata)
	parsed, err := mosxml.ParseMessage(operation)
	if err != nil {
		t.Fatal(err)
	}
	blocks := parsed.(mosxml.ROMetadataReplace).MosExternalMetadata
	if len(blocks) != 2 || blocks[0].MosPayload.Raw != `<label>First &amp; second</label>` || blocks[1].MosPayload.Raw != "" {
		t.Fatal("parser did not retain populated and explicit-empty opaque payloads")
	}
	f.accept(t, operation)
	assertSourceMetadata(t, f, []string{rundownMetadata, emptyRundownMetadata})
	ro, err := f.store.RunningOrders().Get(context.Background(), "rundown")
	if err != nil {
		t.Fatal(err)
	}
	if len(ro.ExternalMetadata) != 2 || ro.ExternalMetadata[0].Payload != blocks[0].MosPayload.Raw || ro.ExternalMetadata[1].Payload != "" {
		t.Error("acknowledged replacement lost stored running-order metadata")
	}
	if after := f.snapshot(t); !after.Complete || !after.Active || after.Revision <= before.Revision || !reflect.DeepEqual(before.Stories, after.Stories) {
		t.Fatal("metadata-only replacement changed story coverage or contents")
	}
	f.accept(t, metadataReplacement(""))
	assertSourceMetadata(t, f, []string{})
	ro, err = f.store.RunningOrders().Get(context.Background(), "rundown")
	if err != nil || len(ro.ExternalMetadata) != 0 {
		t.Fatal("omitted replacement retained stale stored metadata")
	}
	f.accept(t, metadataReplacement(rundownMetadata))
	f.accept(t, `<roDelete><roID>rundown</roID></roDelete>`)
	assertSourceMetadata(t, f, []string{})
}

func TestCommittedSourceRundownMetadataRosterReplacement(t *testing.T) {
	for _, tag := range []string{"roCreate", "roReplace", "roList"} {
		t.Run(tag, func(t *testing.T) {
			f := newSourceFixture(t, "")
			for _, blocks := range []string{rundownMetadata, emptyRundownMetadata, ""} {
				f.accept(t, "<"+tag+"><roID>rundown</roID><roSlug>Synthetic rundown</roSlug>"+blocks+`<story><storyID>story</storyID></story></`+tag+">")
				f.accept(t, sourceBody("story", ""))
				want := []string{}
				if blocks != "" {
					want = append(want, blocks)
				}
				assertSourceMetadata(t, f, want)
				ro, err := f.store.RunningOrders().Get(context.Background(), "rundown")
				if err != nil || len(ro.ExternalMetadata) != len(want) || blocks == emptyRundownMetadata && ro.ExternalMetadata[0].Payload != "" {
					t.Fatal("authoritative roster retained stale running-order metadata")
				}
			}
		})
	}
}

func TestRunningOrderMetadataSharedFullRosterPaths(t *testing.T) {
	for _, tag := range []string{"roCreate", "roReplace", "roList"} {
		t.Run(tag, func(t *testing.T) {
			orders := repository.NewMemoryRunningOrderRepository()
			svc := NewMOSService(orders, repository.NewMemoryStoryRepository(), repository.NewMemoryItemRepository(), repository.NewMemoryObjectRepository(), nil)
			for _, blocks := range []string{rundownMetadata, emptyRundownMetadata, ""} {
				message, err := mosxml.ParseMessage("<" + tag + "><roID>rundown</roID><roSlug>Synthetic rundown</roSlug>" + blocks + "</" + tag + ">")
				if err != nil {
					t.Fatal(err)
				}
				switch m := message.(type) {
				case mosxml.RunningOrderInfo:
					err = svc.ProcessRunningOrderInfo(context.Background(), m, "device")
				case mosxml.ROReplace:
					err = svc.ReplaceRunningOrder(context.Background(), m)
				case mosxml.ROList:
					err = svc.ApplyROList(context.Background(), m, "device")
				default:
					t.Fatalf("unexpected full-roster type %T", message)
				}
				if err != nil {
					t.Fatal(err)
				}
				ro, err := orders.Get(context.Background(), "rundown")
				if err != nil {
					t.Fatal(err)
				}
				if blocks == "" {
					if len(ro.ExternalMetadata) != 0 {
						t.Fatal("full roster omission retained stale metadata")
					}
				} else if len(ro.ExternalMetadata) != 1 || blocks == emptyRundownMetadata && ro.ExternalMetadata[0].Payload != "" {
					t.Fatal("full roster dropped or retained stale metadata")
				}
			}
		})
	}
}

func TestCommittedSourceRundownMetadataLimitsRetainMOSContent(t *testing.T) {
	for _, tc := range []struct {
		name, blocks string
		count        int
	}{
		{"count", strings.Repeat(rundownMetadata, 33), 33},
		{"block-length", strings.Replace(rundownMetadata, "First &amp; second", strings.Repeat("x", 16385), 1), 1},
		{"total-utf8-bytes", strings.Repeat(strings.Replace(rundownMetadata, "First &amp; second", strings.Repeat("é", 2500), 1), 32), 32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSourceFixture(t, "")
			f.accept(t, sourceRoster("story"))
			f.accept(t, sourceBody("story", ""))
			f.accept(t, metadataReplacement(tc.blocks))
			ro, err := f.store.RunningOrders().Get(context.Background(), "rundown")
			if err != nil || len(ro.ExternalMetadata) != tc.count {
				t.Error("publication limits discarded acknowledged MOS metadata")
			}
			cp, err := f.store.Checkpoint()
			if err != nil {
				t.Fatal(err)
			}
			var state struct{ Metadata []string }
			if err := json.Unmarshal(cp.State, &state); err != nil || len(state.Metadata) != tc.count || strings.Join(state.Metadata, "") != tc.blocks {
				t.Error("publication limits truncated retained source metadata")
			}
			var pending map[string]json.RawMessage
			if err := json.Unmarshal(cp.Pending, &pending); err != nil {
				t.Fatal(err)
			}
			if f.snapshot(t).Complete || len(cp.Pending) > repository.MaxSourceSnapshotBytes || pending["metadata"] != nil {
				t.Error("unpublishable metadata must suspend coverage without an empty-array clearing claim")
			}
		})
	}
}
