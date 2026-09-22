package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Immutable content is flushed before the tiny atomic checkpoint pointer. Existing v1
// directories are never migrated implicitly. The v2 source binding selects this representation.
type checkpointContent struct {
	dir   string
	known map[string]bool
}

func newCheckpointContent(path string) *checkpointContent {
	return &checkpointContent{dir: filepath.Join(filepath.Dir(path), "content"), known: map[string]bool{}}
}

func (c *checkpointContent) save(path string, raw []byte) error {
	if err := os.MkdirAll(c.dir, 0700); err != nil {
		return err
	}
	used := map[string]bool{}
	var pack func(json.RawMessage, bool) (json.RawMessage, error)
	store := func(body []byte, opaque bool) (json.RawMessage, error) {
		id := checkpointDigest(body)
		used[id] = true
		if !c.known[id] {
			filename := filepath.Join(c.dir, id)
			existing, err := os.ReadFile(filename)
			if err == nil {
				if !bytes.Equal(existing, body) {
					return nil, errors.New("source content integrity mismatch")
				}
			} else if os.IsNotExist(err) {
				if err := replaceCheckpoint(filename, body); err != nil {
					return nil, err
				}
			} else {
				return nil, err
			}
			c.known[id] = true
		}
		reference := map[string]any{"$content": id}
		if opaque {
			reference["raw"] = true
		}
		return json.Marshal(reference)
	}
	pack = func(body json.RawMessage, external bool) (json.RawMessage, error) {
		if external {
			return store(body, true)
		}
		trimmed := bytes.TrimSpace(body)
		if len(trimmed) == 0 {
			return nil, errors.New("empty source content")
		}
		if trimmed[0] == '{' {
			var object map[string]json.RawMessage
			if err := json.Unmarshal(body, &object); err != nil {
				return nil, err
			}
			if _, reserved := object["$content"]; reserved {
				return store(body, true)
			}
			for key, value := range object {
				if key == "stories" || key == "items" || key == "runningOrders" || key == "receipts" {
					var rows []json.RawMessage
					if err := json.Unmarshal(value, &rows); err != nil {
						return nil, err
					}
					if rows != nil {
						for i, row := range rows {
							packed, err := pack(row, true)
							if err != nil {
								return nil, err
							}
							rows[i] = packed
						}
						value, _ = json.Marshal(rows)
					}
				} else {
					var err error
					value, err = pack(value, false)
					if err != nil {
						return nil, err
					}
				}
				object[key] = value
			}
			body, _ = json.Marshal(object)
		}
		if external || len(body) > 4096 {
			return store(body, false)
		}
		return body, nil
	}
	packed, err := pack(raw, false)
	if err == nil {
		packed, err = store(packed, false)
	}
	if err != nil {
		return err
	}
	// One directory flush covers every newly created immutable object before the root moves.
	dir, err := os.Open(c.dir)
	if err != nil {
		return err
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = replaceCheckpoint(path, packed); err != nil {
		return err
	}
	// Only unreachable objects are disposable. A failed or uncertain root write skips collection.
	for id := range c.known {
		if !used[id] {
			if os.Remove(filepath.Join(c.dir, id)) == nil {
				delete(c.known, id)
			}
		}
	}
	return nil
}
func (c *checkpointContent) read(raw []byte) ([]byte, error) {
	seen := map[string]bool{}
	var unpack func(json.RawMessage) (json.RawMessage, error)
	unpack = func(body json.RawMessage) (json.RawMessage, error) {
		trimmed := bytes.TrimSpace(body)
		if len(trimmed) == 0 {
			return nil, errors.New("empty source content")
		}
		if trimmed[0] == '{' {
			var object map[string]json.RawMessage
			if err := json.Unmarshal(body, &object); err != nil {
				return nil, err
			}
			if ref, ok := object["$content"]; ok && (len(object) == 1 || len(object) == 2 && string(object["raw"]) == "true") {
				var id string
				if json.Unmarshal(ref, &id) != nil || len(id) != 64 {
					return nil, errors.New("invalid source content reference")
				}
				for _, ch := range id {
					if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
						return nil, errors.New("invalid source content reference")
					}
				}
				if seen[id] {
					return nil, errors.New("cyclic source content reference")
				}
				seen[id] = true
				raw, err := os.ReadFile(filepath.Join(c.dir, id))
				if err != nil {
					return nil, err
				}
				if checkpointDigest(raw) != id {
					return nil, errors.New("source content integrity mismatch")
				}
				c.known[id] = true
				if string(object["raw"]) == "true" {
					delete(seen, id)
					return raw, nil
				}
				result, err := unpack(raw)
				delete(seen, id)
				return result, err
			}
			for key, value := range object {
				value, err := unpack(value)
				if err != nil {
					return nil, err
				}
				object[key] = value
			}
			return json.Marshal(object)
		}
		if trimmed[0] == '[' {
			var rows []json.RawMessage
			if err := json.Unmarshal(body, &rows); err != nil {
				return nil, err
			}
			for i, row := range rows {
				value, err := unpack(row)
				if err != nil {
					return nil, err
				}
				rows[i] = value
			}
			return json.Marshal(rows)
		}
		return body, nil
	}
	return unpack(raw)
}

func CanonicalSourceJSON(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// UpgradeSourceSync is an explicit offline operation. It acquires the existing ownership
// lock, preserves the exact legacy checkpoint, counters and replay bytes, and never starts
// a publisher. Ordinary source startup still invalidates freshness before any delivery.
func UpgradeSourceSync(dir string, binding SourceBinding) error {
	if !strings.HasSuffix(binding.Destination, "/v2/source-sync") {
		return errors.New("upgrade requires the v2 source destination")
	}
	old := binding
	old.Destination = strings.TrimSuffix(binding.Destination, "/v2/source-sync") + "/v1/openmos-snapshots"
	upgrade := func(raw json.RawMessage) (json.RawMessage, error) {
		if len(raw) == 0 {
			return raw, nil
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
		doc["version"] = json.RawMessage(`2`)
		return json.Marshal(doc)
	}
	if binding.RundownID == "" {
		old.Destination = strings.TrimSuffix(binding.Destination, "/v2/source-sync") + "/v1/openmos-catalogue"
		c, err := OpenCatalogue(dir, old, false)
		if err != nil {
			current, currentErr := OpenCatalogue(dir, binding, false)
			if currentErr == nil {
				return current.Close()
			}
			return err
		}
		defer c.Close()
		if err := backupSourceCheckpoint(c.path); err != nil {
			return err
		}
		state := c.state
		state.Pending, err = upgrade(state.Pending)
		if err != nil {
			return err
		}
		c.binding = binding
		c.content = newCheckpointContent(c.path)
		if err := c.validate(state); err != nil {
			return err
		}
		return c.save(state)
	}
	d, err := OpenCommitted(dir, old, false)
	if err != nil {
		current, currentErr := OpenCommitted(dir, binding, false)
		if currentErr == nil {
			return current.Close()
		}
		return err
	}
	defer d.Close()
	if err := backupSourceCheckpoint(d.path); err != nil {
		return err
	}
	state := *d.checkpoint
	state.Pending, err = upgrade(state.Pending)
	if err != nil {
		return err
	}
	snap, err := d.takeSnapshot(context.Background())
	if err != nil {
		return err
	}
	d.binding = binding
	d.content = newCheckpointContent(d.path)
	if err := validateCheckpoint(state, binding); err != nil {
		return err
	}
	return d.saveCheckpoint(snap, state)
}
func backupSourceCheckpoint(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	backup := path + ".pre-v2"
	f, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		existing, err := os.ReadFile(backup)
		if err == nil && !bytes.Equal(existing, raw) {
			return errors.New("pre-v2 recovery checkpoint differs")
		}
		return err
	}
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
