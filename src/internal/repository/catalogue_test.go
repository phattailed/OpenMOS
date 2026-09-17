package repository

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogueProvisioningPersistenceAndBinding(t *testing.T) {
	dir := t.TempDir()
	binding := SourceBinding{SourceID: "source", MosID: "device", NCSID: "newsroom", Transport: "tcp", Destination: "http://127.0.0.1:1234/v1/openmos-catalogue"}
	if _, err := OpenCatalogue(dir, binding, false); err == nil {
		t.Fatal("normal startup initialized an absent catalogue")
	}
	store, err := OpenCatalogue(dir, binding, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCatalogue(dir, binding, false); err == nil {
		t.Fatal("a second process acquired catalogue ownership")
	}
	if err := store.Commit(func(cp *CatalogueCheckpoint) error {
		cp.Revision = 1
		cp.Pending = json.RawMessage(`{"version":1,"sourceId":"source","revision":1,"complete":true,"rundowns":[]}`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "source-catalogue.json"))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCatalogue(dir, binding, true); err == nil {
		t.Fatal("initialization reset an existing catalogue")
	}
	rundownBinding := binding
	rundownBinding.RundownID = "rundown"
	if _, err := OpenCommitted(dir, rundownBinding, true); err == nil {
		t.Fatal("rundown initialization mixed state into an existing catalogue directory")
	}
	other := binding
	other.SourceID = "different-source"
	if _, err := OpenCatalogue(dir, other, false); err == nil {
		t.Fatal("catalogue binding changed without a new explicitly provisioned store")
	}
	store, err = OpenCatalogue(dir, binding, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.Checkpoint()
	if err != nil || got.Revision != 1 || got.AcceptedRevision != 0 {
		t.Fatalf("catalogue counter did not survive reopening: %+v %v", got, err)
	}
	got.Pending[0] = 'x'
	if retained, _ := store.Checkpoint(); retained.Pending[0] != '{' {
		t.Fatal("a checkpoint read mutated retained state")
	}
	if err := store.Commit(func(cp *CatalogueCheckpoint) error { cp.Revision = 3; return nil }); err == nil {
		t.Fatal("catalogue counter skipped a revision")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "source-catalogue.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("failed opens or a failed transaction changed catalogue bytes")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Replace(before, []byte(`"complete":true`), []byte(`"complete":false`), 1)
	path := filepath.Join(dir, "source-catalogue.json")
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCatalogue(dir, binding, false); err == nil {
		t.Fatal("corrupt catalogue bypassed its integrity check")
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, corrupt) {
		t.Fatal("failed integrity check reset the corrupt catalogue")
	}
}
