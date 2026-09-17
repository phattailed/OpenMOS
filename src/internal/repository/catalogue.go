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
	mu       sync.Mutex
	path     string
	lock     *os.File
	binding  SourceBinding
	state    CatalogueCheckpoint
	degraded bool
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
	err := c.lock.Close()
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
	if len(state.Pending) > 64<<10 || json.Unmarshal(state.Pending, &header) != nil || header.Version != 1 || header.SourceID != c.binding.SourceID || header.Revision != state.Revision {
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
	return replaceCheckpoint(c.path, raw)
}
