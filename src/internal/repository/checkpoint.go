package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

const MaxSourceRevision uint64 = 9007199254740991

const checkpointFilename = "source-checkpoint.json"

// SourceBinding fences a checkpoint to one configured producer and recipient. Credentials are
// deliberately absent: changing a credential must not change the source's revision stream.
type SourceBinding struct {
	SourceID    string `json:"sourceId"`
	RundownID   string `json:"rundownId"`
	MosID       string `json:"mosId"`
	NCSID       string `json:"ncsId"`
	Transport   string `json:"transport"`
	Destination string `json:"destination"`
}

type InputReceipt struct {
	Scope     string `json:"scope"`
	NCSID     string `json:"ncsId"`
	MessageID string `json:"messageId"`
	Hash      string `json:"hash"`
	Response  []byte `json:"response"`
}

// SourceCheckpoint is committed with repository state, never in a separate acknowledgement log.
// State belongs to the source service; Pending is the exact HTTP body to send or renew.
type SourceCheckpoint struct {
	Revision         uint64          `json:"revision"`
	State            json.RawMessage `json:"state,omitempty"`
	Pending          json.RawMessage `json:"pending,omitempty"`
	AcceptedRevision uint64          `json:"acceptedRevision"`
	Receipts         []InputReceipt  `json:"receipts"`
}

type checkpointFile struct {
	Version  int              `json:"version"`
	Binding  SourceBinding    `json:"binding"`
	Snapshot snapshot         `json:"snapshot"`
	Source   SourceCheckpoint `json:"source"`
	Digest   string           `json:"digest"`
}

// OpenCommitted never upgrades, discards or resets existing state. initialize is only for an
// explicit one-shot provisioning command; normal startup must pass false.
func OpenCommitted(dir string, binding SourceBinding, initialize bool) (*Durable, error) {
	if dir == "" {
		return nil, errors.New("committed source requires a state directory")
	}
	if initialize {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".source.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open source ownership lock: %w", err)
	}
	opened := false
	defer func() {
		if !opened {
			_ = lock.Close()
		}
	}()
	if err := lockCheckpoint(lock); err != nil {
		return nil, fmt.Errorf("source state already owned or cannot be locked: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "runningorders.json")); err == nil {
		return nil, errors.New("legacy running-order state present; automatic migration is unsupported")
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect legacy state: %w", err)
	}
	d := newMemoryDurable()
	d.lock = lock
	d.path = filepath.Join(dir, checkpointFilename)
	d.binding = binding
	d.committed = true
	d.checkpoint = &SourceCheckpoint{}
	raw, err := os.ReadFile(d.path)
	if os.IsNotExist(err) && initialize {
		if err := d.saveCheckpoint(snapshot{}, *d.checkpoint); err != nil {
			return nil, err
		}
		opened = true
		return d, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read committed source state (initialize only a new source): %w", err)
	}
	if initialize {
		return nil, errors.New("source state already initialized; reset is unsupported")
	}
	var file checkpointFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("decode committed source: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("committed source has trailing data")
	}
	if file.Version != 1 || file.Binding != binding {
		return nil, errors.New("committed source version or binding mismatch; reset is unsupported")
	}
	digest := file.Digest
	file.Digest = ""
	canonical, err := json.Marshal(file)
	if err != nil || digest != checkpointDigest(canonical) {
		return nil, errors.New("committed source integrity mismatch")
	}
	if err := validateCheckpoint(file.Source, binding); err != nil {
		return nil, err
	}
	if err := d.restoreSnapshot(file.Snapshot); err != nil {
		return nil, err
	}
	d.checkpoint = &file.Source
	opened = true
	return d, nil
}

func (d *Durable) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lock == nil {
		return nil
	}
	err := d.lock.Close()
	d.lock = nil
	d.degraded = true
	return err
}

func newMemoryDurable() *Durable {
	return &Durable{
		runningOrders: NewMemoryRunningOrderRepository(), stories: NewMemoryStoryRepository(),
		items: NewMemoryItemRepository(), objects: NewMemoryObjectRepository(),
	}
}

// Commit stages one message in detached repositories. A callback error publishes nothing.
// ponytail: one commit lock for one source; partition only when multiple sources are supported.
func (d *Durable) Commit(ctx context.Context, apply func(Repository, *SourceCheckpoint) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.checkpoint == nil || d.degraded {
		return errors.New("committed source storage is unavailable")
	}
	snap, err := d.takeSnapshot(ctx)
	if err != nil {
		return err
	}
	staged := newMemoryDurable()
	if err := staged.restoreSnapshot(snap); err != nil {
		return err
	}
	state, err := cloneCheckpoint(*d.checkpoint)
	if err != nil {
		return err
	}
	if err := apply(staged, &state); err != nil {
		return err
	}
	if state.Revision < d.checkpoint.Revision || state.Revision > d.checkpoint.Revision+1 {
		return errors.New("source revision must stay unchanged or advance once")
	}
	if err := validateCheckpoint(state, d.binding); err != nil {
		return err
	}
	snap, err = staged.takeSnapshot(ctx)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.saveCheckpoint(snap, state); err != nil {
		// After an uncertain rename/sync no further state may be advertised until restart.
		d.degraded = true
		return err
	}
	d.runningOrders, d.stories, d.items = staged.runningOrders, staged.stories, staged.items
	d.checkpoint = &state
	return nil
}

func (d *Durable) Checkpoint() (SourceCheckpoint, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.checkpoint == nil || d.degraded {
		return SourceCheckpoint{}, errors.New("committed source storage is unavailable")
	}
	return cloneCheckpoint(*d.checkpoint)
}

func cloneCheckpoint(in SourceCheckpoint) (out SourceCheckpoint, err error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}

func validateCheckpoint(state SourceCheckpoint, binding SourceBinding) error {
	if state.Revision > MaxSourceRevision || state.AcceptedRevision > state.Revision {
		return errors.New("invalid committed source counter or receipt bound")
	}
	if state.Revision == 0 {
		if len(state.Pending) != 0 || len(state.State) != 0 || len(state.Receipts) != 0 {
			return errors.New("invalid unstarted source checkpoint")
		}
		return nil
	}
	var header struct {
		Version   int    `json:"version"`
		SourceID  string `json:"sourceId"`
		RundownID string `json:"rundownId"`
		Revision  uint64 `json:"revision"`
	}
	if !json.Valid(state.State) || len(state.Pending) > 64<<10 || json.Unmarshal(state.Pending, &header) != nil || header.Version != 1 || header.SourceID != binding.SourceID || header.RundownID != binding.RundownID || header.Revision != state.Revision {
		return errors.New("committed source payload does not match its binding and revision")
	}
	// Retain receipts without eviction, but never accept a malformed or conflicting replay key.
	// Silent roList receipts belong to our request sequence; ACK-bearing receipts belong to the
	// peer's independent request sequence. This direction is already present in version 1 data.
	// The per-message bounds exceed a decoded transport frame; they are not a lifetime capacity.
	seen := make(map[[4]string]bool, len(state.Receipts))
	for _, receipt := range state.Receipts {
		key := [4]string{receipt.Scope, receipt.NCSID, receipt.MessageID, "request"}
		if len(receipt.Response) == 0 {
			key[3] = "response"
		}
		hash, err := hex.DecodeString(receipt.Hash)
		if seen[key] || receipt.Scope == "" || !utf8.ValidString(receipt.Scope) || utf8.RuneCountInString(receipt.Scope) > 512 || receipt.NCSID != binding.NCSID || receipt.MessageID == "" || !utf8.ValidString(receipt.MessageID) || utf8.RuneCountInString(receipt.MessageID) > 4<<20 || err != nil || len(hash) != sha256.Size || len(receipt.Response) > 8<<20 || !utf8.Valid(receipt.Response) {
			return errors.New("invalid committed source input receipt")
		}
		seen[key] = true
	}
	return nil
}

func (d *Durable) saveCheckpoint(snap snapshot, state SourceCheckpoint) error {
	file := checkpointFile{Version: 1, Binding: d.binding, Snapshot: snap, Source: state}
	raw, err := json.Marshal(file)
	if err != nil {
		return err
	}
	file.Digest = checkpointDigest(raw)
	raw, err = json.Marshal(file)
	if err != nil {
		return err
	}
	return replaceCheckpoint(d.path, raw)
}

func checkpointDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func replaceCheckpoint(path string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".source-checkpoint-*")
	if err != nil {
		return fmt.Errorf("create checkpoint: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return fmt.Errorf("write/sync checkpoint: %w", err)
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("replace checkpoint: %w", err)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	err = dir.Sync()
	closeErr = dir.Close()
	if err != nil {
		return fmt.Errorf("sync checkpoint directory: %w", err)
	}
	return closeErr
}
