package bridge

import (
	"fmt"
	"os"
	"path/filepath"

	"airshift/openmos/pkg/logger"
)

// CSVWriter writes the rundown to a file on every snapshot change, for sites that
// keep their existing vMix file Data Source rather than switching to HTTP polling.
// It exists to make migration a no-op: point the file at the path vMix already
// reads and the manual copy/paste step is gone.
type CSVWriter struct {
	path   string
	fields []string
}

// NewCSVWriter builds a writer for the given path and field list.
func NewCSVWriter(path string, fields []string) *CSVWriter {
	return &CSVWriter{path: path, fields: fields}
}

// Write renders the snapshot to CSV and replaces the file atomically.
//
// vMix reads this file on a timer, so a half-written file would be read as a
// corrupt Data Source. Writing to a temp file in the same directory and renaming
// over the target means a reader ever sees only the old file or the whole new
// one, never a partial write. Same-directory temp keeps the rename on one volume,
// where it is atomic.
func (cw *CSVWriter) Write(snap Snapshot) error {
	data, err := RenderCSV(snap, cw.fields)
	if err != nil {
		return fmt.Errorf("render csv: %w", err)
	}

	dir := filepath.Dir(cw.path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create csv directory %s: %w", dir, err)
		}
	}

	tmp, err := os.CreateTemp(dir, ".rundown-*.csv.tmp")
	if err != nil {
		return fmt.Errorf("create temp csv: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename.
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp csv: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp csv: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp csv: %w", err)
	}

	if err := os.Rename(tmpName, cw.path); err != nil {
		return fmt.Errorf("replace csv %s: %w", cw.path, err)
	}
	return nil
}

// OnSnapshot is the events-callback shape, logging failures rather than
// propagating them: a CSV write failure must not stall the bridge or the MOS
// core, and the next change will try again.
func (cw *CSVWriter) OnSnapshot(snap Snapshot) {
	if err := cw.Write(snap); err != nil {
		logger.Errorf("bridge csv: %v", err)
		return
	}
	logger.Infof("bridge csv: wrote %d rows to %s", len(snap.Rows), cw.path)
}
