package repository

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

// CatalogueCheckpoint has its own counter and receipt stream. It never changes a rundown's
// checkpoint, binding or allocator, including the primary source's existing version 1 file.
type CatalogueCheckpoint struct {
	Revision         uint64          `json:"revision"`
	Pending          json.RawMessage `json:"pending,omitempty"`
	AcceptedRevision uint64          `json:"acceptedRevision"`
	Receipts         []InputReceipt  `json:"receipts"`
	Raw              string          `json:"raw,omitempty"`
	Halted           bool            `json:"halted"`
}

type catalogueFile struct {
	Version int                 `json:"version"`
	Binding SourceBinding       `json:"binding"`
	State   CatalogueCheckpoint `json:"state"`
	Digest  string              `json:"digest"`
}

type Catalogue struct {
	mu            sync.Mutex
	path          string
	lock          *os.File
	binding       SourceBinding
	state         CatalogueCheckpoint
	degraded      bool
	content       *checkpointContent
	members       map[string]*Durable
	memberIDs     []string
	ownedMembers  []*Durable
	memberLimit   int
	retainedCount int
}

// MaxSourceMembers bounds retained stores, including inactive members. History is never evicted
// to make room. The discovery walk uses the same ceiling before granting catalogue authority.
const MaxSourceMembers = 512

type sourceMembersFile struct {
	Version  int           `json:"version"`
	Binding  SourceBinding `json:"binding"`
	Rundowns []string      `json:"rundowns"`
	Digest   string        `json:"digest"`
}

func (c *Catalogue) Binding() SourceBinding { return c.binding }

func (c *Catalogue) MemberBinding(id string) SourceBinding {
	binding := c.binding
	binding.RundownID = id
	if !strings.HasSuffix(binding.Destination, "/v2/source-sync") {
		binding.Destination = strings.TrimSuffix(binding.Destination, "/v1/openmos-catalogue") + "/v1/openmos-snapshots"
	}
	return binding
}

func (c *Catalogue) memberRoot() string { return filepath.Join(filepath.Dir(c.path), "rundowns") }

func (c *Catalogue) memberPath(id string) string {
	return filepath.Join(c.memberRoot(), checkpointDigest([]byte(id)))
}

func (c *Catalogue) membersBinding() SourceBinding {
	binding := c.binding
	// Membership survives an explicit publication upgrade. The catalogue and every member's
	// checkpoint still fence the exact destination; inventory never authorizes changing it.
	binding.Destination = ""
	return binding
}

// OpenMembers reopens the complete enrollment inventory, including members no longer active.
// A missing checkpoint is an error, never permission to initialize its counters again. Supplied
// stores may already own these exact paths after a reversible explicit-configuration cutover.
func (c *Catalogue) OpenMembers(configured map[string]*Durable) (map[string]*Durable, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.degraded || c.members != nil {
		return nil, errors.New("catalogue members are unavailable or already opened")
	}
	for id, store := range configured {
		if store == nil || store.binding != c.MemberBinding(id) {
			return nil, errors.New("configured member does not match the catalogue binding")
		}
	}
	file := sourceMembersFile{Version: 1, Binding: c.membersBinding(), Rundowns: []string{}}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(c.path), "source-members.json"))
	if os.IsNotExist(err) {
		if _, err := os.Lstat(c.memberRoot()); !os.IsNotExist(err) {
			return nil, errors.New("member inventory is missing beside retained enrollment state")
		}
		if err := c.saveMembers(file.Rundowns); err != nil {
			return nil, err
		}
	} else {
		if err != nil {
			return nil, err
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err := d.Decode(&file); err != nil {
			return nil, err
		}
		digest := file.Digest
		file.Digest = ""
		canonical, err := json.Marshal(file)
		if err != nil || d.Decode(new(any)) != io.EOF || file.Version != 1 || file.Binding != c.membersBinding() || file.Rundowns == nil || len(file.Rundowns) > MaxSourceMembers || digest != checkpointDigest(canonical) {
			return nil, errors.New("member inventory binding, integrity or capacity is invalid")
		}
	}
	limit := MaxSourceMembers
	if !strings.HasSuffix(c.binding.Destination, "/v2/source-sync") {
		limit = 100
	}
	retainedCount := len(configured)
	for _, id := range file.Rundowns {
		if configured[id] == nil {
			retainedCount++
		}
	}
	if retainedCount > limit {
		return nil, errors.New("retained member inventory exceeds the selected source capacity")
	}
	members := make(map[string]*Durable)
	var owned []*Durable
	opened := false
	defer func() {
		if !opened {
			for _, store := range owned {
				_ = store.Close()
			}
		}
	}()
	for _, id := range file.Rundowns {
		if id == "" || !utf8.ValidString(id) || utf8.RuneCountInString(id) > 512 || members[id] != nil {
			return nil, errors.New("member inventory contains an invalid or repeated identity")
		}
		if err := c.checkMemberPath(id); err != nil {
			return nil, err
		}
		if store := configured[id]; store != nil {
			actual, err := filepath.Abs(filepath.Dir(store.path))
			want, pathErr := filepath.Abs(c.memberPath(id))
			if err != nil || pathErr != nil || actual != want {
				return nil, errors.New("configured member would replace an enrolled member's original store")
			}
			members[id] = store
			continue
		}
		store, err := OpenCommitted(c.memberPath(id), c.MemberBinding(id), false)
		if err != nil {
			return nil, fmt.Errorf("reopen enrolled source: %w", err)
		}
		members[id] = store
		owned = append(owned, store)
	}
	c.members, c.memberIDs, c.ownedMembers = members, file.Rundowns, owned
	c.memberLimit, c.retainedCount = limit, retainedCount
	opened = true
	return members, nil
}

// Only internal digest keys reach the filesystem. Reject symlinked member paths rather than
// following them outside the selected root or aliasing an existing checkpoint.
func (c *Catalogue) checkMemberPath(id string) error {
	for _, path := range []string{c.memberRoot(), c.memberPath(id)} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("member path is missing or is not a directory: %s", path)
		}
	}
	return nil
}

// EnrollMember installs an ordinary committed checkpoint before recording its identity. The
// fixed staging directory can be resumed after interruption, but can never receive MOS input.
// Existing stores are opened with their exact binding and are never initialized or replaced.
func (c *Catalogue) EnrollMember(id string) (*Durable, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.degraded || c.members == nil || id == "" || !utf8.ValidString(id) || utf8.RuneCountInString(id) > 512 {
		return nil, errors.New("member enrollment is unavailable or identity is invalid")
	}
	if store := c.members[id]; store != nil {
		return store, nil
	}
	if c.retainedCount >= c.memberLimit {
		return nil, errors.New("retained member capacity exhausted; eviction is unsupported")
	}
	root := c.memberRoot()
	if err := os.Mkdir(root, 0700); err == nil {
		if err := syncSourceDirectory(filepath.Dir(root)); err != nil {
			c.degraded = true
			return nil, err
		}
	} else if !os.IsExist(err) {
		return nil, err
	}
	if info, err := os.Lstat(root); err != nil || !info.IsDir() {
		return nil, errors.New("member root must be a directory, not a symlink")
	}
	path := c.memberPath(id)
	_, err := os.Lstat(path)
	var store *Durable
	if os.IsNotExist(err) {
		stage := filepath.Join(root, ".enrolling-"+filepath.Base(path))
		if err := os.Mkdir(stage, 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
		if info, err := os.Lstat(stage); err != nil || !info.IsDir() {
			return nil, errors.New("member staging path must be a directory")
		}
		_, stateErr := os.Lstat(filepath.Join(stage, checkpointFilename))
		store, err = OpenCommitted(stage, c.MemberBinding(id), os.IsNotExist(stateErr))
		if err == nil {
			if store.checkpoint.Revision != 0 {
				err = errors.New("enrollment staging contains previously accepted state")
			} else {
				err = os.Rename(stage, path)
			}
			if err == nil {
				store.path = filepath.Join(path, checkpointFilename)
				if store.content != nil {
					store.content.dir = filepath.Join(path, "content")
				}
				if err = syncSourceDirectory(root); err != nil {
					c.degraded = true
				}
			}
		}
	} else if err == nil {
		if err = c.checkMemberPath(id); err == nil {
			store, err = OpenCommitted(path, c.MemberBinding(id), false)
			if err == nil && store.checkpoint.Revision != 0 {
				err = errors.New("unregistered member contains previously accepted state")
			}
		}
	}
	if err == nil {
		ids := append(append([]string(nil), c.memberIDs...), id)
		err = c.saveMembers(ids)
		if err == nil {
			c.memberIDs = ids
			c.retainedCount++
			c.members[id] = store
			c.ownedMembers = append(c.ownedMembers, store)
			return store, nil
		}
		c.degraded = true // A failed inventory sync has an uncertain durable result.
	}
	if store != nil {
		_ = store.Close()
	}
	return nil, err
}

func (c *Catalogue) saveMembers(ids []string) error {
	file := sourceMembersFile{Version: 1, Binding: c.membersBinding(), Rundowns: ids}
	raw, err := json.Marshal(file)
	if err != nil {
		return err
	}
	file.Digest = checkpointDigest(raw)
	raw, err = json.Marshal(file)
	if err != nil {
		return err
	}
	return replaceCheckpoint(filepath.Join(filepath.Dir(c.path), "source-members.json"), raw)
}

func syncSourceDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	err = dir.Sync()
	return errors.Join(err, dir.Close())
}

// OpenCatalogue requires a separate, explicitly provisioned directory. The binding has no
// rundown: this stream describes the configured source set, independently of any one show.
func OpenCatalogue(dir string, binding SourceBinding, initialize bool) (*Catalogue, error) {
	if dir == "" || binding.RundownID != "" || binding.SourceID == "" || binding.NCSID == "" || binding.MosID == "" {
		return nil, errors.New("catalogue requires a directory and a source-level binding")
	}
	if initialize {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	for _, name := range []string{checkpointFilename, "runningorders.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			return nil, errors.New("catalogue directory must be separate from rundown and native state")
		}
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".catalogue.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	opened := false
	defer func() {
		if !opened {
			_ = lock.Close()
		}
	}()
	if err := lockCheckpoint(lock); err != nil {
		return nil, fmt.Errorf("catalogue state already owned or cannot be locked: %w", err)
	}
	c := &Catalogue{path: filepath.Join(dir, "source-catalogue.json"), lock: lock, binding: binding}
	if strings.HasSuffix(binding.Destination, "/v2/source-sync") {
		c.content = newCheckpointContent(c.path)
	}
	raw, err := os.ReadFile(c.path)
	if os.IsNotExist(err) && initialize {
		if err := c.save(c.state); err != nil {
			return nil, err
		}
		opened = true
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read catalogue state (initialize only a new catalogue): %w", err)
	}
	if initialize {
		return nil, errors.New("catalogue already initialized; reset is unsupported")
	}
	if c.content != nil {
		raw, err = c.content.read(raw)
		if err != nil {
			return nil, err
		}
	}
	var file catalogueFile
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&file); err != nil {
		return nil, err
	}
	if d.Decode(new(any)) != io.EOF || file.Version != 1 || file.Binding != binding {
		return nil, errors.New("catalogue version, binding or trailing data is invalid")
	}
	digest := file.Digest
	file.Digest = ""
	canonical, err := json.Marshal(file)
	if err != nil || digest != checkpointDigest(canonical) {
		return nil, errors.New("catalogue integrity check failed")
	}
	if err := c.validate(file.State); err != nil {
		return nil, err
	}
	c.state = file.State
	opened = true
	return c, nil
}

func (c *Catalogue) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.degraded = true
	if c.lock == nil {
		return nil
	}
	var err error
	for _, member := range c.ownedMembers {
		err = errors.Join(err, member.Close())
	}
	err = errors.Join(err, c.lock.Close())
	c.lock = nil
	return err
}

func (c *Catalogue) Checkpoint() (CatalogueCheckpoint, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.degraded {
		return CatalogueCheckpoint{}, errors.New("catalogue storage is unavailable")
	}
	return cloneCatalogue(c.state)
}

func (c *Catalogue) Commit(apply func(*CatalogueCheckpoint) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.degraded {
		return errors.New("catalogue storage is unavailable")
	}
	next, err := cloneCatalogue(c.state)
	if err != nil {
		return err
	}
	if err := apply(&next); err != nil {
		return err
	}
	if next.Revision < c.state.Revision || next.Revision > c.state.Revision+1 {
		return errors.New("catalogue revision must stay unchanged or advance once")
	}
	if err := c.validate(next); err != nil {
		return err
	}
	if err := c.save(next); err != nil {
		c.degraded = true
		return err
	}
	c.state = next
	return nil
}

func cloneCatalogue(in CatalogueCheckpoint) (out CatalogueCheckpoint, err error) {
	raw, err := json.Marshal(in)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}

func (c *Catalogue) validate(state CatalogueCheckpoint) error {
	if state.Revision > MaxSourceRevision || state.AcceptedRevision > state.Revision || len(state.Raw) > 6<<20 {
		return errors.New("invalid catalogue counter or raw content bound")
	}
	if state.Revision == 0 {
		if len(state.Pending) != 0 || len(state.Receipts) != 0 || state.Raw != "" || state.Halted {
			return errors.New("invalid unstarted catalogue")
		}
		return nil
	}
	var header struct {
		Version  int    `json:"version"`
		SourceID string `json:"sourceId"`
		Revision uint64 `json:"revision"`
	}
	version := 1
	if strings.HasSuffix(c.binding.Destination, "/v2/source-sync") {
		version = 2
	}
	if (version == 1 && len(state.Pending) > 64<<10) || json.Unmarshal(state.Pending, &header) != nil || header.Version != version || header.SourceID != c.binding.SourceID || header.Revision != state.Revision {
		return errors.New("catalogue payload does not match its binding and revision")
	}
	seen := make(map[[3]string]bool)
	for _, r := range state.Receipts {
		key := [3]string{r.Scope, r.NCSID, r.MessageID}
		hash, err := hex.DecodeString(r.Hash)
		if seen[key] || r.Scope == "" || !utf8.ValidString(r.Scope) || utf8.RuneCountInString(r.Scope) > 512 || r.NCSID != c.binding.NCSID || r.MessageID == "" || !utf8.ValidString(r.MessageID) || utf8.RuneCountInString(r.MessageID) > 4<<20 || err != nil || len(hash) != sha256.Size || len(r.Response) != 0 {
			return errors.New("invalid catalogue input receipt")
		}
		seen[key] = true
	}
	return nil
}

func (c *Catalogue) save(state CatalogueCheckpoint) error {
	if c.content != nil {
		var err error
		state.Pending, err = CanonicalSourceJSON(state.Pending)
		if err != nil {
			return err
		}
	}
	file := catalogueFile{Version: 1, Binding: c.binding, State: state}
	raw, err := json.Marshal(file)
	if err != nil {
		return err
	}
	file.Digest = checkpointDigest(raw)
	raw, err = json.Marshal(file)
	if err != nil {
		return err
	}
	if c.content != nil {
		return c.content.save(c.path, raw)
	}
	return replaceCheckpoint(c.path, raw)
}
