package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/MateusAMP2119/iris-lakehouse/internal/dispatch"
)

// This file is the framed run-log capture (the declared logs block, E-logs):
// a runCapture wraps the per-run log file and, per the pipeline's declared
// recording contract, frames every line it writes -- stderr log lines tagged
// L|, protocol frames >| (engine to pipeline) and <| (pipeline to engine), and
// stamp metadata #| -- so one chronological file carries the whole run and
// stays machine-separable. A pipeline declaring neither split nor stamp gets
// the legacy behavior byte-for-byte: raw stderr passthrough, no tags.

// frameRecorder receives the turn protocol's frame traffic for capture. The
// turn driver calls it beside every sent and received line; a nil recorder (or
// one without transcript capture declared) drops the traffic.
type frameRecorder interface {
	// EngineFrame records one engine-to-pipeline frame line (go/row/run).
	EngineFrame(line string)
	// PipelineFrame records one pipeline-to-engine frame line (row/done/error).
	PipelineFrame(line string)
}

// captureLineCap bounds one buffered partial stderr line; a longer unterminated
// line is flushed in captureLineCap-sized L| segments rather than growing
// without bound.
const captureLineCap = 64 << 10

// captureStamp is the shape of a #| stamp line: the open stamp carries the run
// identity and start, the close stamp the end and outcome.
type captureStamp struct {
	IrisLog  int    `json:"iris_log,omitempty"`
	Run      string `json:"run,omitempty"`
	Pipeline string `json:"pipeline,omitempty"`
	Started  string `json:"started,omitempty"`
	Ended    string `json:"ended,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
}

// runCapture is one run's log sink under the declared recording contract. It
// satisfies dispatch.WriteCloser for the stderr stream (Write frames or passes
// through per the contract) and frameRecorder for the protocol transcript.
// Writes and frame records arrive from different goroutines (the pipe pump and
// the turn driver), so every mutation holds the mutex.
type runCapture struct {
	mu      sync.Mutex
	f       io.WriteCloser
	framed  bool   // any line framing (split or stamp declared)
	split   bool   // transcript capture declared
	stamp   bool   // stamp metadata declared
	partial []byte // buffered unterminated stderr bytes (framed mode only)
	outcome string // recorded before Close; rides the close stamp
	now     func() time.Time
}

// newRunCapture wraps the open per-run log file per the declared contract. Any
// framing (split or stamp) opens the capture with the identity header -- the
// #| first line that marks the file framed for every consumer -- while the
// declared stamp additionally writes the close stamp with the run's outcome.
func newRunCapture(f io.WriteCloser, runID, pipeline string, split, stamp bool) *runCapture {
	c := &runCapture{f: f, framed: split || stamp, split: split, stamp: stamp, now: time.Now}
	if c.framed {
		c.mu.Lock()
		c.writeStamp(captureStamp{IrisLog: 1, Run: runID, Pipeline: pipeline, Started: c.now().UTC().Format(time.RFC3339)})
		c.mu.Unlock()
	}
	return c
}

// Write receives the pipeline's stderr stream. Unframed it passes through
// verbatim (the legacy capture); framed it splits the stream into lines and
// tags each L|, carrying an unterminated tail until its newline (or the cap)
// arrives. It never fails the caller: capture is best-effort by contract.
func (c *runCapture) Write(p []byte) (int, error) {
	if c == nil || c.f == nil {
		return len(p), nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.framed {
		_, _ = c.f.Write(p)
		return len(p), nil
	}
	c.partial = append(c.partial, p...)
	for {
		i := bytes.IndexByte(c.partial, '\n')
		if i < 0 {
			if len(c.partial) >= captureLineCap {
				c.writeTagged(dispatch.LogLineLog, c.partial)
				c.partial = c.partial[:0]
			}
			return len(p), nil
		}
		c.writeTagged(dispatch.LogLineLog, c.partial[:i])
		c.partial = append(c.partial[:0], c.partial[i+1:]...)
	}
}

// EngineFrame records one engine-to-pipeline protocol frame when transcript
// capture is declared.
func (c *runCapture) EngineFrame(line string) { c.frameLine(dispatch.LogLineEngineFrame, line) }

// PipelineFrame records one pipeline-to-engine protocol frame when transcript
// capture is declared.
func (c *runCapture) PipelineFrame(line string) { c.frameLine(dispatch.LogLinePipelineFrame, line) }

// frameLine writes one tagged protocol frame line under the transcript
// contract; without split declared the traffic is dropped.
func (c *runCapture) frameLine(tag, line string) {
	if c == nil || c.f == nil || !c.split {
		return
	}
	c.mu.Lock()
	c.writeTagged(tag, []byte(line))
	c.mu.Unlock()
}

// SetOutcome records the run's ending for the close stamp (succeeded,
// dead_lettered, ...); it is a no-op without stamp declared.
func (c *runCapture) SetOutcome(outcome string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.outcome = outcome
	c.mu.Unlock()
}

// Close flushes any buffered partial stderr line, writes the close stamp when
// declared, and closes the underlying file.
func (c *runCapture) Close() error {
	if c == nil || c.f == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.partial) > 0 {
		c.writeTagged(dispatch.LogLineLog, c.partial)
		c.partial = nil
	}
	if c.stamp {
		c.writeStamp(captureStamp{Ended: c.now().UTC().Format(time.RFC3339), Outcome: c.outcome})
	}
	return c.f.Close()
}

// writeTagged writes one tagged line (tag + payload + newline) under the held
// mutex, best-effort.
func (c *runCapture) writeTagged(tag string, payload []byte) {
	buf := make([]byte, 0, len(tag)+len(payload)+1)
	buf = append(buf, tag...)
	buf = append(buf, payload...)
	buf = append(buf, '\n')
	_, _ = c.f.Write(buf)
}

// writeStamp writes one #| stamp line under the held mutex, best-effort.
func (c *runCapture) writeStamp(s captureStamp) {
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	c.writeTagged(dispatch.LogLineMeta, b)
}

// turnTranscriptCap bounds a loop turn's buffered frame transcript; past it the
// head is dropped (marked) so the retained transcript carries the turn's end.
const turnTranscriptCap = 256 << 10

// turnTranscript buffers a loop turn's protocol traffic in memory until the
// turn records, mirroring turnLogBuffer for frames: a quiet turn's transcript
// is dropped with the turn. It satisfies frameRecorder.
type turnTranscript struct {
	mu        sync.Mutex
	lines     []taggedLine
	size      int
	truncated bool
}

// taggedLine is one buffered transcript line and its frame tag.
type taggedLine struct {
	tag  string
	line string
}

// EngineFrame buffers one engine-to-pipeline frame line.
func (t *turnTranscript) EngineFrame(line string) { t.add(dispatch.LogLineEngineFrame, line) }

// PipelineFrame buffers one pipeline-to-engine frame line.
func (t *turnTranscript) PipelineFrame(line string) { t.add(dispatch.LogLinePipelineFrame, line) }

// add appends one line, dropping from the head past the cap.
func (t *turnTranscript) add(tag, line string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, taggedLine{tag: tag, line: line})
	t.size += len(line)
	for t.size > turnTranscriptCap && len(t.lines) > 0 {
		t.size -= len(t.lines[0].line)
		t.lines = t.lines[1:]
		t.truncated = true
	}
}

// flushTo writes the buffered transcript through the capture's frame recorder,
// noting a dropped head. A nil capture (or one without split) drops it.
func (t *turnTranscript) flushTo(c *runCapture) {
	if t == nil || c == nil || !c.split {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.truncated {
		c.frameLine(dispatch.LogLineEngineFrame, `{"iris":"transcript head truncated"}`)
	}
	for _, l := range t.lines {
		c.frameLine(l.tag, l.line)
	}
}
