package repository

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestCatalogueReceiptOnlyStoreRequiresDurableEnrollment(t *testing.T) {
	dir := t.TempDir()
	binding := SourceBinding{SourceID: "source", MosID: "device", NCSID: "newsroom", Transport: "tcp", Destination: "http://127.0.0.1:1234/v1/openmos-catalogue"}
	catalogue, err := OpenCatalogue(dir, binding, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalogue.OpenMembers(nil); err != nil {
		t.Fatal(err)
	}
	store, err := catalogue.RetainMember("unknown")
	if err != nil || !catalogue.MemberUnenrolled("unknown") {
		t.Fatalf("receipt-only storage granted enrollment: %v", err)
	}
	checkpoint, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalogue.Close(); err != nil {
		t.Fatal(err)
	}
	catalogue, err = OpenCatalogue(dir, binding, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalogue.OpenMembers(nil); err != nil || !catalogue.MemberUnenrolled("unknown") {
		t.Fatalf("restart lost the unenrolled marker: %v", err)
	}
	path := filepath.Join(dir, "source-members.json")
	if err := os.Rename(path, path+".held-by-test"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := catalogue.EnrollMember("unknown"); err == nil || !catalogue.MemberUnenrolled("unknown") {
		t.Fatal("failed inventory replacement admitted an unenrolled member")
	}
	if err := catalogue.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".held-by-test", path); err != nil {
		t.Fatal(err)
	}
	catalogue, err = OpenCatalogue(dir, binding, false)
	if err != nil {
		t.Fatal(err)
	}
	defer catalogue.Close()
	if _, err := catalogue.OpenMembers(nil); err != nil || !catalogue.MemberUnenrolled("unknown") {
		t.Fatalf("failed enrollment did not survive reopening as unenrolled: %v", err)
	}
	catalogue.memberLimit = 1
	if _, err := catalogue.RetainMember("another"); err == nil {
		t.Fatal("receipt-only store was not counted against retained capacity")
	}
	store, err = catalogue.EnrollMember("unknown")
	if err != nil || catalogue.MemberUnenrolled("unknown") {
		t.Fatalf("durable enrollment did not reuse the receipt-only store at capacity: %v", err)
	}
	if got, err := os.ReadFile(store.path); err != nil || !bytes.Equal(got, checkpoint) {
		t.Fatal("admission rewrote the retained checkpoint")
	}
}

func TestCatalogueMemberEnrollmentRecoversStagingAndRejectsPathAliases(t *testing.T) {
	for _, endpoint := range []string{"/v1/openmos-catalogue", "/v2/source-sync"} {
		t.Run(endpoint, func(t *testing.T) {
			dir := t.TempDir()
			binding := SourceBinding{SourceID: "source", MosID: "device", NCSID: "newsroom", Transport: "tcp", Destination: "http://127.0.0.1:1234" + endpoint}
			catalogue, err := OpenCatalogue(dir, binding, true)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = catalogue.Close() })
			if _, err := catalogue.OpenMembers(nil); err != nil {
				t.Fatal(err)
			}
			id := "../opaque/folder\\show"
			stage := filepath.Join(catalogue.memberRoot(), ".enrolling-"+checkpointDigest([]byte(id)))
			store, err := OpenCommitted(stage, catalogue.MemberBinding(id), true)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(stage, checkpointFilename))
			store, err = catalogue.EnrollMember(id)
			if err != nil {
				t.Fatalf("interrupted initial checkpoint could not finish enrollment: %v", err)
			}
			if store.binding.RundownID != id || filepath.Dir(store.path) != catalogue.memberPath(id) || filepath.Base(filepath.Dir(store.path)) != checkpointDigest([]byte(id)) {
				t.Fatal("opaque ID escaped its root or changed identity")
			}
			after, _ := os.ReadFile(store.path)
			if !bytes.Equal(before, after) {
				t.Fatal("resuming enrollment rewrote the initial checkpoint")
			}
			// An occupied digest key must not alias a different binding, even with no receipts.
			collisionPath := catalogue.memberPath("collision")
			other, err := OpenCommitted(collisionPath, catalogue.MemberBinding("different-identity"), true)
			if err != nil {
				t.Fatal(err)
			}
			_ = other.Close()
			occupied, _ := os.ReadFile(filepath.Join(collisionPath, checkpointFilename))
			if _, err := catalogue.EnrollMember("collision"); err == nil {
				t.Fatal("enrollment aliased another identity's checkpoint")
			}
			if got, _ := os.ReadFile(filepath.Join(collisionPath, checkpointFilename)); !bytes.Equal(occupied, got) {
				t.Fatal("failed collision check overwrote the existing store")
			}
			outside := t.TempDir()
			if err := os.Symlink(outside, catalogue.memberPath("symlink")); err != nil {
				t.Fatal(err)
			}
			if _, err := catalogue.EnrollMember("symlink"); err == nil {
				t.Fatal("enrollment followed a symlink outside its root")
			}
			if entries, _ := os.ReadDir(outside); len(entries) != 0 {
				t.Fatal("failed path check wrote outside the selected root")
			}
			if err := catalogue.Close(); err != nil {
				t.Fatal(err)
			}
			catalogue, err = OpenCatalogue(dir, binding, false)
			if err != nil {
				t.Fatal(err)
			}
			members, err := catalogue.OpenMembers(nil)
			if err != nil || len(members) != 1 || members[id] == nil {
				t.Fatalf("restart lost the durable inventory or adopted unrelated directories: %v", err)
			}
			if cp, err := members[id].Checkpoint(); err != nil || cp.Revision != 0 {
				t.Fatal("reopening the repository changed its source revision")
			}
			if err := catalogue.Close(); err != nil {
				t.Fatal(err)
			}
			// A downgrade-style explicit binding reopens the same current file, then a newer
			// runtime can use it again without double ownership or substituting a new path.
			explicit, err := OpenCommitted(catalogue.memberPath(id), catalogue.MemberBinding(id), false)
			if err != nil {
				t.Fatal(err)
			}
			defer explicit.Close()
			catalogue, err = OpenCatalogue(dir, binding, false)
			if err != nil {
				t.Fatal(err)
			}
			members, err = catalogue.OpenMembers(map[string]*Durable{id: explicit})
			if err != nil || members[id] != explicit {
				t.Fatalf("current explicit binding was not reused: %v", err)
			}
			_ = catalogue.Close()
			if _, err := explicit.Checkpoint(); err != nil {
				t.Fatal("catalogue closed a store owned by its explicit caller")
			}
		})
	}
}

func TestCatalogueMemberInventoryNeverResetsMissingHistory(t *testing.T) {
	for _, missing := range []string{"inventory", "checkpoint", "directory"} {
		t.Run(missing, func(t *testing.T) {
			dir := t.TempDir()
			binding := SourceBinding{SourceID: "source", MosID: "device", NCSID: "newsroom", Transport: "tcp", Destination: "http://127.0.0.1:1234/v1/openmos-catalogue"}
			c, err := OpenCatalogue(dir, binding, true)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.OpenMembers(nil); err != nil {
				t.Fatal(err)
			}
			if _, err := c.EnrollMember("retained"); err != nil {
				t.Fatal(err)
			}
			_ = c.Close()
			switch missing {
			case "inventory":
				err = os.Remove(filepath.Join(dir, "source-members.json"))
			case "checkpoint":
				err = os.Remove(filepath.Join(c.memberPath("retained"), checkpointFilename))
			case "directory":
				err = os.Rename(c.memberPath("retained"), filepath.Join(dir, "preserved-member"))
			}
			if err != nil {
				t.Fatal(err)
			}
			c, err = OpenCatalogue(dir, binding, false)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err := c.OpenMembers(nil); err == nil {
				t.Fatal("missing retained history was silently reset")
			}
		})
	}
	// The inventory is independent of active catalogue membership, and its integrity is checked.
	dir := t.TempDir()
	binding := SourceBinding{SourceID: "source", MosID: "device", NCSID: "newsroom", Transport: "tcp", Destination: "http://127.0.0.1:1234/v1/openmos-catalogue"}
	c, err := OpenCatalogue(dir, binding, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.OpenMembers(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EnrollMember("retained"); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "source-members.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(raw), "retained", "rewritten", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	c, err = OpenCatalogue(dir, binding, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.OpenMembers(nil); err == nil {
		t.Fatal("tampered inventory was accepted")
	}
}

func TestCatalogueEnrollmentSurvivesExplicitPublicationUpgrade(t *testing.T) {
	dir := t.TempDir()
	binding := SourceBinding{SourceID: "source", MosID: "device", NCSID: "newsroom", Transport: "tcp", Destination: "http://127.0.0.1:1234/v1/openmos-catalogue"}
	catalogue, err := OpenCatalogue(dir, binding, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalogue.OpenMembers(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := catalogue.EnrollMember("opaque/retained"); err != nil {
		t.Fatal(err)
	}
	memberPath := catalogue.memberPath("opaque/retained")
	if err := catalogue.Close(); err != nil {
		t.Fatal(err)
	}
	inventory := filepath.Join(dir, "source-members.json")
	before, err := os.ReadFile(inventory)
	if err != nil {
		t.Fatal(err)
	}
	binding.Destination = "http://127.0.0.1:1234/v2/source-sync"
	memberBinding := binding
	memberBinding.RundownID = "opaque/retained"
	if err := UpgradeSourceSync(memberPath, memberBinding); err != nil {
		t.Fatal(err)
	}
	if err := UpgradeSourceSync(dir, binding); err != nil {
		t.Fatal(err)
	}
	catalogue, err = OpenCatalogue(dir, binding, false)
	if err != nil {
		t.Fatal(err)
	}
	defer catalogue.Close()
	members, err := catalogue.OpenMembers(nil)
	if err != nil || len(members) != 1 || members[memberBinding.RundownID] == nil {
		t.Fatalf("existing explicit upgrade lost the enrollment inventory: %v", err)
	}
	if after, err := os.ReadFile(inventory); err != nil || !bytes.Equal(before, after) {
		t.Fatal("publication upgrade rewrote or replaced membership history")
	}
}
