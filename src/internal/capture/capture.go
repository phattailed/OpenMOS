// Package capture records raw MOS frames to disk for interop work.
//
// Every fixture in this repository was written by hand. That is a real weakness:
// hand-written frames use identifiers like RO-41, while a live NCS sends
// composites such as
//
//	NCS-HOST;P_NEWS\W;C45B2CF1-D7C9-4E3D-AEF9-C60DAEC93538
//
// and the difference has already caught us out once. Capturing genuine traffic
// lets real frames replace invented ones.
//
// Capture is off unless a directory is configured. It writes message payloads --
// which for roStorySend includes the full body of news stories -- so it must be a
// deliberate act, never a default, and the destination should be treated as
// containing editorial content.
package capture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Direction records which way a frame was travelling.
type Direction string

const (
	// Inbound is a frame received from a peer.
	Inbound Direction = "in"
	// Outbound is a frame sent to a peer.
	Outbound Direction = "out"
)

// defaultMaxFrames bounds how many frames are written before capture stops, so an
// enabled capture cannot fill a disk during a long run.
const defaultMaxFrames = 2000

// Recorder writes frames to a directory. A nil *Recorder is valid and does
// nothing, so call sites need no conditionals.
type Recorder struct {
	dir       string
	maxFrames int

	// KeepAlive records keepAlive frames when set. Off by default; see skipKeepAlive.
	Liveness bool

	mu       sync.Mutex
	seq      int
	manifest *os.File
	stopped  bool
	skipped  int
}

// Entry is one line of the manifest, describing a captured frame.
type Entry struct {
	Seq       int    `json:"seq"`
	Time      string `json:"time"`
	Transport string `json:"transport"`
	Direction string `json:"direction"`
	Peer      string `json:"peer"`
	// WireBytes is the size as it appeared on the wire, before any decoding. For
	// UCS-2BE this is roughly twice the UTF-8 length, which is what makes the
	// encoding visible in the record.
	WireBytes int    `json:"wireBytes"`
	Encoding  string `json:"encoding"`
	File      string `json:"file"`
}

// New opens a Recorder writing into dir, creating it if needed.
//
// An empty dir returns (nil, nil): capture is simply off, and a nil Recorder is
// safe to use.
func New(dir string) (*Recorder, error) {
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create capture directory %s: %w", dir, err)
	}

	manifest, err := os.OpenFile(filepath.Join(dir, "manifest.jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, fmt.Errorf("failed to open capture manifest: %w", err)
	}

	// Continue the sequence from what is already on disk rather than restarting at zero.
	//
	// Two things go wrong otherwise, and both were observed on the standing appliance. Frame files are
	// named by sequence, so a restart reuses names and SILENTLY OVERWRITES earlier frames -- destroying
	// exactly the evidence capture exists to collect. And the cap then bounds a single run rather than
	// the directory, so a process restarted through a day of testing grew it past 2000 (measured at
	// 2074) while the README claimed capture "is bounded at 2000 frames so an enabled run cannot fill
	// a disk".
	seq := highestSequence(dir)

	return &Recorder{dir: dir, seq: seq, maxFrames: defaultMaxFrames, manifest: manifest}, nil
}

// Dir reports where frames are being written. Empty when capture is off.
func (r *Recorder) Dir() string {
	if r == nil {
		return ""
	}
	return r.dir
}

// Record writes one frame. utf8XML is the decoded message; wireBytes and encoding
// describe how it appeared on the wire, so a fixture carries evidence of its own
// framing rather than just its content.
//
// Failures are reported but never propagated to message handling: losing a capture
// must not disturb the exchange being captured.
func (r *Recorder) Record(transport string, direction Direction, peer string, utf8XML []byte, wireBytes int, encoding string) error {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.stopped {
		return nil
	}

	if !r.Liveness && isLiveness(utf8XML) {
		r.skipped++
		return nil
	}
	if r.seq >= r.maxFrames {
		r.stopped = true
		return fmt.Errorf("capture stopped after %d frames", r.maxFrames)
	}
	r.seq++

	name := fmt.Sprintf("%04d-%s-%s.xml", r.seq, transport, direction)
	if err := os.WriteFile(filepath.Join(r.dir, name), utf8XML, 0o640); err != nil {
		return fmt.Errorf("failed to write capture %s: %w", name, err)
	}

	line, err := json.Marshal(Entry{
		Seq:       r.seq,
		Time:      time.Now().UTC().Format(time.RFC3339Nano),
		Transport: transport,
		Direction: string(direction),
		Peer:      peer,
		WireBytes: wireBytes,
		Encoding:  encoding,
		File:      name,
	})
	if err != nil {
		return fmt.Errorf("failed to encode manifest entry: %w", err)
	}
	if _, err := r.manifest.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("failed to append to capture manifest: %w", err)
	}
	return nil
}

// Count reports how many frames have been recorded.
func (r *Recorder) Count() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seq
}

// Close flushes and closes the manifest.
func (r *Recorder) Close() error {
	if r == nil || r.manifest == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.manifest.Close()
}

// isLiveness reports whether a frame is Profile 0 liveness traffic, which carries no state.
//
// keepAlive frames are excluded from capture by default, and the reason is arithmetic. A standing
// passive appliance sends one every thirty seconds: about 2,880 a day against a 2,000-frame cap.
// So capture would stop roughly overnight, and any real running-order delivery after that would
// leave no evidence at all -- the exact failure that made a live passive delivery look like a
// non-event in doc/interop §38. The least informative message we handle would consume the entire
// budget.
//
// Excluding it is not a loss. keepAlive carries no protocol information beyond its own arrival:
// MOS 4.0 §4.1.1 gives it no messageID because it is unsequenced, it requires no reply, and its
// only purpose is holding a connection open through firewalls. Its arrival is already visible in
// the service log. Nothing in a capture directory is learned from the 2,879th one.
//
// Raising the cap instead would only move the cliff. Set Recorder.Liveness when deliberately
// debugging Profile 0 keep-alive behaviour and a short run is expected.
//
// The check is a substring test rather than a parse because Record deliberately runs BEFORE
// parsing -- a frame that fails to parse is the most valuable one to keep, so capture must not
// depend on parsing succeeding. A MOS envelope carries exactly one operation, so the presence of
// the element is sufficient to identify it.
func isLiveness(utf8XML []byte) bool {
	return bytes.Contains(utf8XML, []byte("<keepAlive")) ||
		bytes.Contains(utf8XML, []byte("<heartbeat"))
}

// Skipped reports how many frames were excluded by the liveness filter, so a quiet capture
// directory can be distinguished from a broken recorder.
func (r *Recorder) Skipped() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.skipped
}

// highestSequence returns the largest frame number already present in dir.
//
// Read from filenames rather than from the manifest, because the manifest is append-only and could have
// been truncated or rotated independently, whereas the files are what a new name would collide with.
// A directory that cannot be read yields zero, which risks overwriting but is preferable to refusing to
// capture at all -- and the caller has already logged that the directory is in use.
func highestSequence(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	highest := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Frames are named "<seq>-<transport>-<direction>.xml"; anything else, such as the manifest,
		// is skipped.
		dash := strings.IndexByte(name, '-')
		if dash <= 0 {
			continue
		}
		n, convErr := strconv.Atoi(name[:dash])
		if convErr != nil {
			continue
		}
		if n > highest {
			highest = n
		}
	}
	return highest
}
