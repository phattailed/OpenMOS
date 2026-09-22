package service

import (
	"airshift/openmos/internal/repository"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const sourcePartBytes = 32768

var errSourceContinue = errors.New("source transfer has more bounded work")

type sourceObject struct {
	Hash string
	Body []byte
}
type sourceTransfer struct {
	binding            map[string]any
	retainedState      []byte
	objects            []sourceObject
	started, published bool
	scan, part         int
	queue              []sourceObject
}

// The same protocol carries the catalogue (no story objects) and ordered rundown revisions.
// Hashes cover the exact UTF-8 bytes, never a language-specific canonical JSON encoding.
func newSourceTransfer(raw []byte) (*sourceTransfer, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	var sourceID, rundownID string
	var revision uint64
	if json.Unmarshal(document["sourceId"], &sourceID) != nil || json.Unmarshal(document["revision"], &revision) != nil {
		return nil, errors.New("invalid committed source header")
	}
	t := &sourceTransfer{binding: map[string]any{"sourceId": sourceID, "rundownId": nil, "revision": revision}}
	if id, ok := document["rundownId"]; ok {
		if err := json.Unmarshal(id, &rundownID); err != nil {
			return nil, err
		}
		t.binding["rundownId"] = rundownID
		var stories []json.RawMessage
		if err := json.Unmarshal(document["stories"], &stories); err != nil {
			return nil, err
		}
		refs := make([]map[string]string, 0, len(stories))
		for _, rawStory := range stories {
			var story struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(rawStory, &story); err != nil {
				return nil, err
			}
			canonical, err := repository.CanonicalSourceJSON(rawStory)
			if err != nil {
				return nil, err
			}
			if len(canonical) > 128<<10 {
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(canonical, &fields); err != nil {
					return nil, err
				}
				var occurrences []json.RawMessage
				if err := json.Unmarshal(fields["occurrences"], &occurrences); err != nil {
					return nil, err
				}
				chunks, err := t.split(occurrences)
				if err != nil {
					return nil, err
				}
				delete(fields, "occurrences")
				fields["occurrenceParts"], _ = json.Marshal(chunks)
				canonical, _ = json.Marshal(fields)
			}
			object := makeSourceObject(canonical)
			t.objects = append(t.objects, object)
			refs = append(refs, map[string]string{"id": story.ID, "hash": object.Hash})
		}
		document["stories"], _ = json.Marshal(refs)
		if len(document["stories"]) > 128<<10 {
			var rows []json.RawMessage
			_ = json.Unmarshal(document["stories"], &rows)
			chunks, err := t.split(rows)
			if err != nil {
				return nil, err
			}
			delete(document, "stories")
			document["storyParts"], _ = json.Marshal(chunks)
		}
	}
	if len(document["rundowns"]) > 128<<10 {
		var rows []json.RawMessage
		if err := json.Unmarshal(document["rundowns"], &rows); err != nil {
			return nil, err
		}
		chunks, err := t.split(rows)
		if err != nil {
			return nil, err
		}
		delete(document, "rundowns")
		document["rundownParts"], _ = json.Marshal(chunks)
	}
	manifest, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	object := makeSourceObject(manifest)
	t.binding["manifest"] = object.Hash
	t.objects = append([]sourceObject{object}, t.objects...)
	seen := map[string]bool{}
	unique := t.objects[:0]
	for _, object := range t.objects {
		if !seen[object.Hash] {
			seen[object.Hash] = true
			unique = append(unique, object)
		}
	}
	t.objects = unique
	return t, nil
}
func makeSourceObject(raw []byte) sourceObject {
	sum := sha256.Sum256(raw)
	return sourceObject{hex.EncodeToString(sum[:]), raw}
}

func (t *sourceTransfer) request(ctx context.Context, client *http.Client, destination, token, method, path string, extra map[string]any, out any) error {
	body := make(map[string]any, len(t.binding)+len(extra))
	for k, v := range t.binding {
		body[k] = v
	}
	for k, v := range extra {
		body[k] = v
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if len(raw) > 65536 {
		return errors.New("source request exceeds negotiated bound")
	}
	req, err := http.NewRequestWithContext(ctx, method, destination+"/"+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("source sync receiver unavailable")
	}
	defer resp.Body.Close()
	received, err := io.ReadAll(io.LimitReader(resp.Body, 32769))
	if err != nil || len(received) > 32768 {
		return errors.New("source sync receipt exceeds bound")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		var problem struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(received, &problem)
		if resp.StatusCode == http.StatusConflict && problem.Code == "source_revision_conflict" {
			return errSourceHalted
		}
		if problem.Code == "source_parts_missing" {
			t.started = false
			t.scan = 0
			t.queue = nil
			t.part = 0
		}
		return fmt.Errorf("source sync receiver returned HTTP %d", resp.StatusCode)
	}
	if out != nil {
		if err := json.Unmarshal(received, out); err != nil {
			return errors.New("invalid source sync receipt")
		}
	}
	return nil
}

// One call performs at most one bounded HTTP request, so a large show cannot monopolize delivery.
func (t *sourceTransfer) step(ctx context.Context, client *http.Client, destination, token string) (bool, error) {
	if !t.started {
		var response struct {
			SourceID  string  `json:"sourceId"`
			RundownID *string `json:"rundownId"`
			Revision  uint64  `json:"revision"`
			Published bool    `json:"published"`
		}
		if err := t.request(ctx, client, destination, token, http.MethodPost, "start", nil, &response); err != nil {
			return false, err
		}
		if response.SourceID != t.binding["sourceId"] || response.Revision != t.binding["revision"] || !t.matchesRundown(response.RundownID) {
			return false, errors.New("source start identity mismatch")
		}
		t.started = true
		t.published = response.Published
		return false, nil
	}
	if !t.published && len(t.queue) > 0 {
		object := t.queue[0]
		offset := t.part * sourcePartBytes
		end := min(offset+sourcePartBytes, len(object.Body))
		var receipt struct {
			Hash   string `json:"hash"`
			Stored bool   `json:"stored"`
		}
		err := t.request(ctx, client, destination, token, http.MethodPut, "parts", map[string]any{"hash": object.Hash, "index": t.part, "parts": (len(object.Body) + sourcePartBytes - 1) / sourcePartBytes, "bytes": len(object.Body), "data": base64.StdEncoding.EncodeToString(object.Body[offset:end])}, &receipt)
		if err != nil {
			return false, err
		}
		if receipt.Hash != object.Hash {
			return false, errors.New("source part identity mismatch")
		}
		if end == len(object.Body) && !receipt.Stored {
			// An earlier part may have expired during an outage. Retrying only the last
			// part cannot finish the object; replay its idempotent bounded parts.
			t.part = 0
			return false, nil
		}
		t.part++
		if receipt.Stored {
			t.part = 0
			t.queue = t.queue[1:]
		}
		return false, nil
	}
	if !t.published && t.scan < len(t.objects) {
		end := min(t.scan+256, len(t.objects))
		hashes := make([]string, 0, end-t.scan)
		known := map[string]sourceObject{}
		for _, object := range t.objects[t.scan:end] {
			hashes = append(hashes, object.Hash)
			known[object.Hash] = object
		}
		var response struct {
			Missing []string `json:"missing"`
		}
		if err := t.request(ctx, client, destination, token, http.MethodPost, "missing", map[string]any{"hashes": hashes}, &response); err != nil {
			return false, err
		}
		if response.Missing == nil {
			return false, errors.New("invalid missing objects receipt")
		}
		for _, hash := range response.Missing {
			object, ok := known[hash]
			if !ok {
				return false, errors.New("unexpected missing object")
			}
			t.queue = append(t.queue, object)
			delete(known, hash)
		}
		t.scan = end
		return false, nil
	}
	path := "commit"
	if t.published {
		path = "heartbeat"
	}
	var receipt struct {
		SourceID  string  `json:"sourceId"`
		RundownID *string `json:"rundownId"`
		Revision  uint64  `json:"acceptedRevision"`
		Duplicate *bool   `json:"duplicate"`
		Applied   *bool   `json:"destinationApplied"`
	}
	if err := t.request(ctx, client, destination, token, http.MethodPost, path, nil, &receipt); err != nil {
		return false, err
	}
	if receipt.SourceID != t.binding["sourceId"] || receipt.Revision != t.binding["revision"] || !t.matchesRundown(receipt.RundownID) || receipt.Duplicate == nil || receipt.Applied == nil || *receipt.Applied {
		return false, errors.New("source receipt binding mismatch")
	}
	t.published = true
	return true, nil
}
func (t *sourceTransfer) matchesRundown(id *string) bool {
	return id == nil && t.binding["rundownId"] == nil || id != nil && *id == t.binding["rundownId"]
}

// Split ordered arrays as well as individual objects. Neither one large story nor the
// manifest inherits the HTTP request limit; repeated chunks retain their content identity.
func (t *sourceTransfer) split(rows []json.RawMessage) ([]string, error) {
	hashes := []string{}
	for len(rows) > 0 {
		n, bytes := 0, 2
		for n < len(rows) && (n == 0 || bytes+len(rows[n])+1 <= 128<<10) {
			bytes += len(rows[n]) + 1
			n++
		}
		raw, err := json.Marshal(rows[:n])
		if err != nil {
			return nil, err
		}
		object := makeSourceObject(raw)
		t.objects = append(t.objects, object)
		hashes = append(hashes, object.Hash)
		rows = rows[n:]
	}
	return hashes, nil
}
