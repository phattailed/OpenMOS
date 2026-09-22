package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestContentCheckpointIncremental(t *testing.T) {
	dir := t.TempDir()
	binding := SourceBinding{SourceID: "source", RundownID: "rundown", MosID: "device", NCSID: "newsroom", Transport: "tcp", Destination: "http://127.0.0.1:1234/v2/source-sync"}
	store, err := OpenCommitted(dir, binding, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stories := make([]map[string]any, 500)
	for i := range stories {
		stories[i] = map[string]any{"id": i, "text": strings.Repeat("synthetic ", 100), "params": map[string]string{"$content": strings.Repeat("a", 64)}}
	}
	var pending []byte
	commit := func(revision uint64) {
		t.Helper()
		pending, _ = json.Marshal(map[string]any{"version": 2, "sourceId": "source", "rundownId": "rundown", "revision": revision, "stories": stories})
		if err := store.Commit(context.Background(), func(_ Repository, cp *SourceCheckpoint) error {
			cp.Revision = revision
			cp.State = json.RawMessage(`{}`)
			cp.Pending = pending
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	commit(1)
	head, err := os.ReadFile(filepath.Join(dir, checkpointFilename))
	if err != nil {
		t.Fatal(err)
	}
	if len(head) > 1024 {
		t.Fatalf("checkpoint still rewrites complete source: %d bytes", len(head))
	}
	before := map[string]os.FileInfo{}
	entries, _ := os.ReadDir(filepath.Join(dir, "content"))
	for _, entry := range entries {
		info, _ := entry.Info()
		before[entry.Name()] = info
	}
	stories[0]["text"] = "changed"
	commit(2)
	written := 0
	entries, _ = os.ReadDir(filepath.Join(dir, "content"))
	for _, entry := range entries {
		info, _ := entry.Info()
		if old := before[entry.Name()]; old == nil || !old.ModTime().Equal(info.ModTime()) {
			written += int(info.Size())
		}
	}
	if written > 100000 {
		t.Fatalf("one story edit rewrote %d bytes", written)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenCommitted(dir, binding, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	cp, err := reopened.Checkpoint()
	if err != nil || string(cp.Pending) != string(pending) {
		t.Fatal("source content did not survive restart", err)
	}
}

func TestExplicitSourceSyncUpgradePreservesReceipts(t *testing.T) {
	for _, catalogue := range []bool{false, true} {
		t.Run(fmt.Sprint(catalogue), func(t *testing.T) {
			dir := t.TempDir()
			binding := SourceBinding{SourceID: "source", RundownID: "rundown", MosID: "device", NCSID: "newsroom", Transport: "tcp", Destination: "http://127.0.0.1:1234/v1/openmos-snapshots"}
			name := checkpointFilename
			receipt := InputReceipt{Scope: "tcp:ro", NCSID: "newsroom", MessageID: "1", Hash: strings.Repeat("a", 64), Response: []byte("<roAck>synthetic</roAck>")}
			if catalogue {
				receipt.Response = nil
				binding.RundownID = ""
				binding.Destination = "http://127.0.0.1:1234/v1/openmos-catalogue"
				name = "source-catalogue.json"
				store, err := OpenCatalogue(dir, binding, true)
				if err != nil {
					t.Fatal(err)
				}
				err = store.Commit(func(cp *CatalogueCheckpoint) error {
					cp.Revision = 1
					cp.AcceptedRevision = 1
					cp.Pending = json.RawMessage(`{"version":1,"sourceId":"source","revision":1,"complete":true,"rundowns":[]}`)
					cp.Receipts = []InputReceipt{receipt}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				store.Close()
			} else {
				store, err := OpenCommitted(dir, binding, true)
				if err != nil {
					t.Fatal(err)
				}
				err = store.Commit(context.Background(), func(_ Repository, cp *SourceCheckpoint) error {
					cp.Revision = 1
					cp.AcceptedRevision = 1
					cp.State = json.RawMessage(`{"active":true}`)
					cp.Pending = json.RawMessage(`{"version":1,"sourceId":"source","rundownId":"rundown","revision":1,"active":true,"complete":true,"stories":[]}`)
					cp.Receipts = []InputReceipt{receipt}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				store.Close()
			}
			original, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			binding.Destination = "http://127.0.0.1:1234/v2/source-sync"
			if err := UpgradeSourceSync(dir, binding); err != nil {
				t.Fatal(err)
			}
			if err := UpgradeSourceSync(dir, binding); err != nil {
				t.Fatal("upgrade retry was not idempotent", err)
			}
			backup, err := os.ReadFile(filepath.Join(dir, name+".pre-v2"))
			if err != nil || !bytes.Equal(original, backup) {
				t.Fatal("original checkpoint not preserved", err)
			}
			var receipts []InputReceipt
			var revision uint64
			if catalogue {
				store, err := OpenCatalogue(dir, binding, false)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				cp, _ := store.Checkpoint()
				receipts = cp.Receipts
				revision = cp.Revision
			} else {
				store, err := OpenCommitted(dir, binding, false)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				cp, _ := store.Checkpoint()
				receipts = cp.Receipts
				revision = cp.Revision
			}
			if revision != 1 || !reflect.DeepEqual(receipts, []InputReceipt{receipt}) {
				t.Fatal("upgrade changed counter or original replay receipt")
			}
		})
	}
}

func TestContentCheckpointInterruptedWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file-size fault injection")
	}
	raw := []byte(`{"stories":[{"id":"one","text":"` + strings.Repeat("synthetic", 16384) + `"}]}`)
	if dir := os.Getenv("SOURCE_CONTENT_WRITE_FAILURE"); dir != "" {
		if err := newCheckpointContent(filepath.Join(dir, checkpointFilename)).save(filepath.Join(dir, checkpointFilename), raw); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	root := filepath.Join(dir, checkpointFilename)
	original := []byte(`{"stories":[{"id":"one","text":"original"}]}`)
	if err := newCheckpointContent(root).save(root, original); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(root)
	// A separate process hits a real short write without changing this test process's limits.
	cmd := exec.Command("/bin/sh", "-c", `ulimit -f 1; exec "$@"`, "content-failure", os.Args[0], "-test.run=^TestContentCheckpointInterruptedWrite$")
	cmd.Env = append(os.Environ(), "SOURCE_CONTENT_WRITE_FAILURE="+dir)
	if err := cmd.Run(); err == nil {
		t.Fatal("write fault did not fire")
	}
	after, _ := os.ReadFile(root)
	if !bytes.Equal(before, after) {
		t.Fatal("failed content write advanced root")
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "content"))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		} // An interrupted private temp file is not published content.
		body, err := os.ReadFile(filepath.Join(dir, "content", entry.Name()))
		if err != nil || checkpointDigest(body) != entry.Name() {
			t.Fatal("interrupted write published corrupt content", entry.Name(), err)
		}
	}
	restarted := newCheckpointContent(root)
	if _, err := restarted.read(after); err != nil {
		t.Fatal("previous checkpoint lost", err)
	}
	if err := restarted.save(root, raw); err != nil {
		t.Fatal("retry after restart failed", err)
	}
	after, _ = os.ReadFile(root)
	restored, err := newCheckpointContent(root).read(after)
	if err != nil || !bytes.Equal(restored, raw) {
		t.Fatal("retried checkpoint unavailable", err)
	}
}
