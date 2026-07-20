package daemon

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/config"
	"github.com/MateusAMP2119/iris-lakehouse/internal/dispatch"
)

// This file holds the per-run log lifecycle helpers (per-run logs unrotated
// (bounded output), run-id-keyed under .iris/logs/ (runs.log_ref), deleted when run
// row pruned). A run's log is created unrotated under .iris/logs/ keyed by run id
// (run-<id>.log), its path is the value recorded in runs.log_ref, and it is deleted
// when the run row is pruned. Run rows and pruning proper are E05's; this builds
// the mechanics both drive: the dispatcher creates the log and records Ref in
// runs.log_ref when a run starts, and the pruner calls DeleteOnPrune (a
// RunLogPruneFunc) for each run row it removes.

// RunLogPruneFunc is the delete-on-prune callback the run-record pruner (E05)
// invokes for each run it removes, so a run's per-run log dies with its row without
// the pruner reaching into the daemon's log layout. RunLogWriter.DeleteOnPrune
// satisfies it.
type RunLogPruneFunc func(runID string) error

// RunLogWriter manages per-run log files under the engine home's logs directory,
// keyed by run id. Per-run logs are unrotated (a run's output is bounded); their
// path is what runs.log_ref records and what DeleteOnPrune removes on prune.
type RunLogWriter struct {
	settings config.Settings
}

// NewRunLogWriter builds a RunLogWriter rooted at the settings' engine-home
// tree.
func NewRunLogWriter(s config.Settings) *RunLogWriter {
	return &RunLogWriter{settings: s}
}

// Ref returns the per-run log reference for runID: the run-id-keyed path under
// .iris/logs/ recorded in runs.log_ref. It is RunLogPath, the naming convention
// the whole engine shares, so distinct runs never share a log.
func (w *RunLogWriter) Ref(runID string) string {
	return RunLogPath(w.settings, runID)
}

// Create ensures the logs directory and creates (truncating) the unrotated per-run
// log for runID, returning the open file for the dispatcher to stream run output
// into and the reference to record in runs.log_ref. The caller closes the file.
func (w *RunLogWriter) Create(runID string) (*os.File, string, error) {
	if err := EnsureLogsDir(w.settings); err != nil {
		return nil, "", err
	}
	path := w.Ref(runID)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, logFilePerm) //nolint:gosec // G304: path is the engine-owned run log under the engine home, keyed by an engine-assigned run id, not user or network input.
	if err != nil {
		return nil, "", fmt.Errorf("daemon: create run log %s: %w", path, err)
	}
	return f, path, nil
}

// DeleteOnPrune removes the per-run log for runID. It is the callback the run-record
// pruner invokes when it prunes the run row (a per-run log does not outlive its
// run). An already-absent log is not an error, so a prune is idempotent.
func (w *RunLogWriter) DeleteOnPrune(runID string) error {
	path := w.Ref(runID)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("daemon: delete pruned run log %s: %w", path, err)
	}
	return nil
}

// Open satisfies dispatch.RunLog: it creates the per-run log and returns it as
// the run's output sink plus the runs.log_ref value the dispatcher records.
func (w *RunLogWriter) Open(runID string) (dispatch.WriteCloser, string, error) {
	f, ref, err := w.Create(runID)
	if err != nil {
		return nil, "", err
	}
	return f, ref, nil
}

// compile-time proof the production log writer satisfies the dispatch seam both
// run paths stream output through.
var _ dispatch.RunLog = (*RunLogWriter)(nil)

// openRunLog opens the per-run output capture through logs under the
// pipeline's declared recording contract, best-effort: capture never blocks
// dispatch, so a nil writer (shape tests) or a failed open runs the pipeline
// uncaptured -- (nil, "") with the failure warned -- and runs.log_ref stays
// NULL for that run. A contract declaring split or stamp yields a line-framed
// capture (its #| identity header marks the file framed); the default contract
// is the legacy raw stderr capture, ref and bytes unchanged.
func openRunLog(logs *RunLogWriter, runID, pipeline string, split, stamp bool, logger *slog.Logger) (*runCapture, string) {
	if logs == nil {
		return nil, ""
	}
	f, ref, err := logs.Create(runID)
	if err != nil {
		if logger != nil {
			logger.Warn("run log: open failed; run output is not captured", "run", runID, "err", err)
		}
		return nil, ""
	}
	return newRunCapture(f, runID, pipeline, split, stamp), ref
}

// closeRunLog closes a run's output capture once the run is reaped; a nil
// capture (an uncaptured run) is a no-op.
func closeRunLog(sink *runCapture) {
	if sink != nil {
		_ = sink.Close()
	}
}

// runLogMetaTailCap bounds the tail read runLogMeta scans for the capture's
// last line; a longer final line reports its retained tail.
const runLogMetaTailCap = 4096

// runLogMeta stats the run's local capture file and reads its last line for
// the ps readout's per-run log metadata. It answers nil -- no metadata rides
// the row -- when capture is off, the file is absent (the run executed on
// another host or was pruned), or the file cannot be read; ps never fabricates.
func runLogMeta(logs *RunLogWriter, runID string) *api.PsRunLog {
	if logs == nil {
		return nil
	}
	path := logs.Ref(runID)
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	meta := &api.PsRunLog{Ref: path, SizeBytes: info.Size()}
	f, err := os.Open(path) //nolint:gosec // G304: the path is the engine-owned run log under the engine home, keyed by an engine-assigned run id.
	if err != nil {
		return meta
	}
	defer func() { _ = f.Close() }()

	head := make([]byte, 2)
	if n, _ := io.ReadFull(f, head); n == 2 {
		meta.Framed = dispatch.FramedCapture(string(head))
	}
	if last, ok := lastCaptureLine(f, info.Size()); ok {
		if meta.Framed {
			if rendered, keep := renderCaptureLine(last, ""); keep {
				meta.LastLine = rendered
			}
		} else {
			meta.LastLine = last
		}
	}
	return meta
}

// lastCaptureLine reads the file's last non-empty line from a bounded tail.
func lastCaptureLine(f *os.File, size int64) (string, bool) {
	off := size - runLogMetaTailCap
	if off < 0 {
		off = 0
	}
	buf := make([]byte, size-off)
	if _, err := f.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
		return "", false
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i] != "" {
			return lines[i], true
		}
	}
	return "", false
}
