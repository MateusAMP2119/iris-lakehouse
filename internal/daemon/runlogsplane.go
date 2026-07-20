package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/dispatch"
)

// This file is the daemon's run-logs plane: the api.RunLogsHandler behind GET
// /runs/{id}/logs and `iris run logs`. A run's captured output lives in the
// run-id-keyed file under this node's engine-home logs directory (the sink the
// run paths stream into and runs.log_ref records), so the plane serves the
// local file -- a read, served on any role. A run that executed on another host
// keeps its log on that host; this node then answers honestly that it holds no
// captured output for the run.
//
// A framed capture (declared logs block; #| identity header first) is rendered
// per the requested view: naturalized by default (tags stripped, frames and
// stamps marked), filtered to one stream, or streamed verbatim under
// format=tagged. A legacy raw capture is byte-for-byte and refuses filters
// honestly (there is nothing to filter by).

// runLogsPlane implements api.RunLogsHandler over the per-run log writer's
// naming convention.
type runLogsPlane struct {
	logs *RunLogWriter
}

// compile-time proof the plane satisfies the mux's run-logs seam.
var _ api.RunLogsHandler = runLogsPlane{}

// NewRunLogsPlane wires the run-logs handler over the per-run log writer.
func NewRunLogsPlane(logs *RunLogWriter) api.RunLogsHandler {
	return runLogsPlane{logs: logs}
}

// Logs opens the run's captured output for streaming under the requested view.
func (p runLogsPlane) Logs(_ context.Context, id string, opts api.LogsOptions) (io.ReadCloser, error) {
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return nil, fmt.Errorf("run logs: %q is not a run id", id)
	}
	f, err := os.Open(p.logs.Ref(id)) //nolint:gosec // G304: the path is the engine-owned run log under the engine home, keyed by a validated numeric run id.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("run logs: no captured output for run %s on this node (the run may predate output capture, its log was pruned with the run, or it executed on another host)", id)
		}
		return nil, fmt.Errorf("run logs: open captured output for run %s: %w", id, err)
	}

	br := bufio.NewReader(f)
	head, _ := br.Peek(2)
	if !dispatch.FramedCapture(string(head)) {
		// Legacy raw capture: filters have no framing to act on, so a requested
		// view is refused rather than silently served unfiltered.
		if opts.Stream != "" || opts.Format != "" {
			_ = f.Close()
			return nil, fmt.Errorf("run logs: run %s was captured without framing (no declared logs block); stream and format views need a framed capture", id)
		}
		return readCloser{Reader: br, Closer: f}, nil
	}
	if opts.Format == "tagged" {
		return readCloser{Reader: br, Closer: f}, nil
	}
	return readCloser{Reader: &logViewReader{src: br, stream: opts.Stream}, Closer: f}, nil
}

// readCloser joins a buffered reader with the file it wraps for closing.
type readCloser struct {
	io.Reader
	io.Closer
}

// logViewReader renders a framed capture line by line into the requested view:
// the whole capture naturalized, or one stream of it. It reads lazily, so a
// large capture streams without loading whole.
type logViewReader struct {
	src     *bufio.Reader
	stream  string // "", "log", or "frames"
	pending []byte
	done    bool
}

// Read serves the next rendered bytes, pulling source lines as needed.
func (v *logViewReader) Read(p []byte) (int, error) {
	for len(v.pending) == 0 && !v.done {
		line, err := v.src.ReadString('\n')
		if line != "" {
			if rendered, ok := renderCaptureLine(strings.TrimSuffix(line, "\n"), v.stream); ok {
				v.pending = append(v.pending, rendered...)
				v.pending = append(v.pending, '\n')
			}
		}
		if err != nil {
			v.done = true
		}
	}
	if len(v.pending) == 0 {
		return 0, io.EOF
	}
	n := copy(p, v.pending)
	v.pending = v.pending[n:]
	return n, nil
}

// renderCaptureLine renders one framed-capture line for the requested view,
// reporting whether the line belongs in it. The naturalized default keeps
// everything: log lines bare, frames and stamps marked by origin. The log view
// keeps only bare log lines; the frames view only marked protocol traffic.
func renderCaptureLine(line, stream string) (string, bool) {
	tag, payload := "", line
	if len(line) >= 2 {
		tag, payload = line[:2], line[2:]
	}
	switch tag {
	case dispatch.LogLineLog:
		if stream == "frames" {
			return "", false
		}
		return payload, true
	case dispatch.LogLineEngineFrame:
		if stream == "log" {
			return "", false
		}
		return "[engine] " + payload, true
	case dispatch.LogLinePipelineFrame:
		if stream == "log" {
			return "", false
		}
		return "[pipeline] " + payload, true
	case dispatch.LogLineMeta:
		if stream != "" {
			return "", false
		}
		return "[iris] " + payload, true
	default:
		// A line without a known tag inside a framed capture should not happen;
		// it is served bare rather than dropped, so nothing captured is hidden.
		return line, stream != "frames"
	}
}
