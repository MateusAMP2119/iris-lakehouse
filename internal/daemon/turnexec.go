package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/MateusAMP2119/iris-lakehouse/internal/declare"
	"github.com/MateusAMP2119/iris-lakehouse/internal/dispatch"
	"github.com/MateusAMP2119/iris-lakehouse/internal/exec"
	"github.com/MateusAMP2119/iris-lakehouse/internal/pg"
	"github.com/MateusAMP2119/iris-lakehouse/internal/store"
)

// This file is the daemon-side turn driver (#206): the one place a resident
// session's pipes meet the pure turn model. It resolves a pipeline's declared
// reads and writes from the meta grants ledger, feeds the declared-read delta as
// input frames, collects the output frames against the declared writes, and
// classifies the turn's ending -- done, pipeline-declared error, protocol
// violation, process death, or shutdown. What each ending writes (mint at
// commit for a loop turn, terminal transition for a pre-minted run) is the
// caller's; this file owns only the drive.

// turnData is the data-database seam a turn drives: the declared-read delta feed
// and the atomic turn commit. *pg.Client is the production implementation; nil
// composes a shape test with no data database (empty feeds, and a producing turn
// faults rather than writing nowhere).
type turnData interface {
	// ReadTurnFeed reads the pipeline's input delta past its consumed position.
	ReadTurnFeed(ctx context.Context, pipeline string, reads []pg.TurnRead) (pg.TurnFeed, error)
	// CommitTurn commits a turn's rows, stamps, and position atomically.
	CommitTurn(ctx context.Context, tc pg.TurnCommit) (pg.TurnStamps, error)
}

// declaredAccess is a pipeline's declared access resolved for the turn protocol:
// the reads the feed covers, the writes the collector enforces, and the plugins
// the engine's verb dispatch serves.
type declaredAccess struct {
	reads   []pg.TurnRead
	writes  dispatch.WriteSet
	plugins map[string]declare.PluginRequirement
}

// accessFromDeclaration resolves a pipeline's declared access straight from its
// parsed declaration -- the same file the checksum is taken over, so the access
// a turn enforces is exactly the declaration the run records. Reading the source
// (never the grants ledger) keeps the resolution free of any apply-ordering
// race: a registered pipeline always has its declaration on disk.
func accessFromDeclaration(decl *declare.Pipeline) declaredAccess {
	acc := declaredAccess{writes: dispatch.WriteSet{}}
	for _, r := range decl.Reads {
		schema, table, ok := strings.Cut(r.Table, ".")
		if !ok {
			continue // validated at apply; a malformed entry grants nothing
		}
		acc.reads = append(acc.reads, pg.TurnRead{Schema: schema, Table: table, Fields: append([]string(nil), r.Fields...)})
	}
	for _, w := range decl.Writes {
		if acc.writes[w.Table] == nil {
			acc.writes[w.Table] = map[string]bool{}
		}
		for _, f := range w.Fields {
			acc.writes[w.Table][f] = true
		}
	}
	acc.plugins = decl.Plugins
	return acc
}

// accessCache caches each pipeline's resolved declared access keyed by its
// declaration checksum, so a turn costs no re-parse until the declaration's
// bytes change.
type accessCache struct {
	mu sync.Mutex
	m  map[string]cachedAccess
}

// cachedAccess is one cached resolution and the checksum it was resolved under.
type cachedAccess struct {
	checksum string
	access   declaredAccess
}

// newAccessCache builds an empty declared-access cache.
func newAccessCache() *accessCache {
	return &accessCache{m: map[string]cachedAccess{}}
}

// resolve returns the pipeline's declared access for the declaration raw bytes
// (whose checksum keys the cache), parsing on a miss. A declaration that parses
// as something other than a pipeline resolves to no declared access.
func (c *accessCache) resolve(pipeline, checksum string, raw []byte) (declaredAccess, error) {
	c.mu.Lock()
	got, ok := c.m[pipeline]
	c.mu.Unlock()
	if ok && got.checksum == checksum {
		return got.access, nil
	}
	decl, err := declare.ParseDeclaration(raw)
	if err != nil {
		return declaredAccess{}, fmt.Errorf("turn access for %q: %w", pipeline, err)
	}
	acc := declaredAccess{writes: dispatch.WriteSet{}}
	if decl.Kind == declare.KindPipeline && decl.Pipeline != nil {
		acc = accessFromDeclaration(decl.Pipeline)
	}
	c.mu.Lock()
	c.m[pipeline] = cachedAccess{checksum: checksum, access: acc}
	c.mu.Unlock()
	return acc, nil
}

// turnLogCap bounds a turn's buffered stderr; past it the head is dropped so the
// recorded log carries the tail (the useful end of a failing turn's output).
const turnLogCap = 256 << 10

// turnLogBuffer buffers one turn's stderr in memory until the turn records (a
// quiet turn records nothing, so its log is dropped with it). It is bounded and
// concurrency-safe (the switch sink writes from the pipe pump's goroutine).
type turnLogBuffer struct {
	mu        sync.Mutex
	buf       []byte
	truncated bool
}

// Write appends p, keeping only the tail past turnLogCap.
func (b *turnLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - turnLogCap; over > 0 {
		b.buf = append(b.buf[:0], b.buf[over:]...)
		b.truncated = true
	}
	return len(p), nil
}

// flushTo writes the buffered log into sink (nil discards), noting a dropped head.
func (b *turnLogBuffer) flushTo(sink dispatch.WriteCloser) {
	if sink == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.truncated {
		_, _ = sink.Write([]byte("[iris: turn log head truncated]\n"))
	}
	_, _ = sink.Write(b.buf)
}

// turnKind classifies how a turn ended.
type turnKind int

// The turn endings the driver distinguishes.
const (
	// turnDone is a clean done terminal; the turn's rows are the outcome.
	turnDone turnKind = iota
	// turnErrored is a pipeline-declared error terminal.
	turnErrored
	// turnViolated is a protocol violation (the offending line rides the error).
	turnViolated
	// turnDied is a process death before the terminal frame.
	turnDied
	// turnShutdown is a context cancellation (leadership term over).
	turnShutdown
)

// turnResult is one driven turn's ending: its kind, the kind's payload, and the
// plugin calls the turn made (recorded whatever the ending -- an external effect
// happened, so it must land in the ledger).
type turnResult struct {
	kind      turnKind
	rows      []dispatch.TurnRow
	end       dispatch.TurnEnd
	violation error
	status    exec.ExitStatus
	calls     []store.PluginCallRecord
}

// verbCaller is the engine-side plugin verb dispatch a turn drives its call
// frames through. The production implementation execs the resolved tool binary
// per call (pluginVerbCaller); nil means the pipeline declared no plugins and
// every call is answered with an err frame.
type verbCaller interface {
	// Call invokes one verb and returns its JSON result; an error becomes the
	// err reply (the engine always replies, never silence).
	Call(ctx context.Context, call dispatch.TurnCall) (json.RawMessage, error)
}

// serveCall answers one collected call frame: it invokes the verb through
// caller, sends the ok or err reply through send (the driver's recorded frame
// writer, so replies land in the frame transcript too), and returns the audit
// record. Every path replies -- a nil caller, a failed verb, and an unframeable
// result all produce an err frame, never silence.
func serveCall(ctx context.Context, send func(line string) bool, caller verbCaller, call dispatch.TurnCall) store.PluginCallRecord {
	rec := store.PluginCallRecord{
		CallID:     call.ID,
		Verb:       call.Verb,
		ArgsDigest: hexDigest(call.Args),
		Status:     store.PluginCallErr,
	}
	if caller == nil {
		rec.Error = "pipeline declares no plugins"
		_ = send(dispatch.EncodeResErrFrame(call.ID, rec.Error))
		return rec
	}
	result, err := caller.Call(ctx, call)
	if err != nil {
		rec.Error = err.Error()
		_ = send(dispatch.EncodeResErrFrame(call.ID, rec.Error))
		return rec
	}
	frame, err := dispatch.EncodeResOKFrame(call.ID, result)
	if err != nil {
		rec.Error = err.Error()
		_ = send(dispatch.EncodeResErrFrame(call.ID, "plugin result could not be framed"))
		return rec
	}
	rec.Status = store.PluginCallOK
	rec.ResponseDigest = hexDigest(result)
	_ = send(frame)
	return rec
}

// hexDigest returns the hex sha256 of b, the digest form plugin_calls pins.
// Absent args and an empty result digest as the empty input, deterministically.
func hexDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// driveTurn runs one turn over a live session: it writes the go/row/run frames,
// feeds every stdout line to the turn collector, serves any plugin calls through
// caller (replying before the next line is fed, so one call is outstanding at a
// time), and classifies the ending. A
// send failure is not an ending of its own -- the process is gone, and its exit
// reports through the session's exited channel. On process exit the scanner's
// already-delivered lines are drained first, so a one-shot pipeline that answers
// its frames and exits cleanly still ends in done, not death.
func driveTurn(ctx context.Context, ses *residentSession, turn int64, feed []pg.FeedRow, writes dispatch.WriteSet, rec frameRecorder, caller verbCaller) turnResult {
	col := dispatch.NewTurnCollector(turn, writes)
	var calls []store.PluginCallRecord

	sendRecorded := func(line string) bool {
		if rec != nil {
			rec.EngineFrame(line)
		}
		return ses.send(line) == nil
	}

	alive := sendRecorded(dispatch.EncodeGoFrame(turn))
	for _, r := range feed {
		if !alive {
			break
		}
		line, err := dispatch.EncodeRowFrame(r.Table, r.Row)
		if err != nil {
			continue // a feed row that cannot frame is skipped, never fatal
		}
		alive = sendRecorded(line)
	}
	if alive {
		_ = sendRecorded(dispatch.EncodeRunFrame())
	}

	feedLine := func(line string) (turnResult, bool) {
		if rec != nil {
			rec.PipelineFrame(line)
		}
		end, terminal, err := col.Feed(line)
		if err != nil {
			return turnResult{kind: turnViolated, violation: err, rows: col.Rows(), calls: calls}, true
		}
		if call, ok := col.TakeCall(); ok {
			calls = append(calls, serveCall(ctx, sendRecorded, caller, call))
			return turnResult{}, false
		}
		if !terminal {
			return turnResult{}, false
		}
		if end.Errored {
			return turnResult{kind: turnErrored, end: end, rows: col.Rows(), calls: calls}, true
		}
		return turnResult{kind: turnDone, rows: col.Rows(), calls: calls}, true
	}

	for {
		select {
		case line := <-ses.scanner.lines:
			if res, ended := feedLine(line); ended {
				return res
			}
		case <-ses.exited:
			// Drain frames the process wrote before exiting: a one-shot answer
			// (rows, done, exit) is a completed turn, not a death.
			for {
				select {
				case line := <-ses.scanner.lines:
					if res, ended := feedLine(line); ended {
						return res
					}
				default:
					return turnResult{kind: turnDied, rows: col.Rows(), status: ses.status, calls: calls}
				}
			}
		case <-ctx.Done():
			return turnResult{kind: turnShutdown, calls: calls}
		}
	}
}

// turnWrites converts collected output rows to the data commit's write shape.
func turnWrites(rows []dispatch.TurnRow) []pg.TurnWrite {
	out := make([]pg.TurnWrite, 0, len(rows))
	for _, r := range rows {
		schema, table, _ := strings.Cut(r.Table, ".")
		out = append(out, pg.TurnWrite{Schema: schema, Table: table, Row: r.Row})
	}
	return out
}

// errorTurnDetail renders a pipeline-declared error terminal as the dead
// letter's human detail: the reason, with the opaque detail appended verbatim
// when the pipeline sent a non-empty one.
func errorTurnDetail(end dispatch.TurnEnd) string {
	detail := end.Reason
	if s := string(end.Detail); len(end.Detail) > 0 && s != "null" && s != "{}" {
		detail += " " + s
	}
	return detail
}

// deathTurnDetail renders a process death as the dead letter's human detail: the
// exit disposition plus the retained stderr tail.
func deathTurnDetail(status exec.ExitStatus, stderrTail string) string {
	detail := exitDetail(status)
	if tail := strings.TrimSpace(stderrTail); tail != "" {
		detail += "; stderr tail: " + tail
	}
	return detail
}
