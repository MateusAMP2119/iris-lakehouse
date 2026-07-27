package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/buildinfo"
	"github.com/MateusAMP2119/iris-lakehouse/internal/config"
	"github.com/MateusAMP2119/iris-lakehouse/internal/dispatch"
	"github.com/MateusAMP2119/iris-lakehouse/internal/store"
)

// This file is the daemon's process-status plane: the api.PsHandler behind GET
// /ps (and therefore behind `iris ps` -- one route, one payload). It composes
// the run snapshot (one plain-MVCC read over the reader pool), the live
// leadership role, and the load collector's newest sample (loadhistory.go):
// the engine load is the daemon's descendant tree plus the managed
// postmaster's (Postgres daemonizes into its own session, so parentage -- not
// the process group -- finds its backends), and each running run's load is its
// recorded process group. Under ?history=1 the payload also carries the
// collector's recorded history. It is a read, served on any role, so a remote
// `iris ps` against a standby answers with the standby's own facts.
//
// Load is best-effort: a collector that could not probe its host holds no
// sample and the payload carries null load -- absence over fabrication. The
// load is at most one collector tick stale (the plane reads the collector, it
// never probes in-request), and a run younger than the newest sample carries
// null load until the next tick attributes it. Uptime keeps the display-only
// wall-clock doctrine: rendered to a string here, so no computable time value
// ever reaches the wire.

// RunSnapshotReader is the run read the ps plane draws from: one plain-MVCC
// snapshot of the run records. store.Reader satisfies it.
type RunSnapshotReader interface {
	// Runs returns the runs matching filter, in ordering-identity order.
	Runs(ctx context.Context, filter store.RunFilter) ([]store.Run, error)
}

// psPlane is the api.PsHandler over the run reader, the role state, and the
// load collector (which owns the host probing and the managed-postmaster
// summing the plane's readout reports).
type psPlane struct {
	retain   int64
	role     api.RoleReporter
	runs     RunSnapshotReader
	loads    *loadHistory
	counters *turnCounters  // resident turn tallies (#206); nil renders none
	runLogs  *RunLogWriter  // local capture files for the per-run log metadata; nil renders none
	sources  *sourceWatcher // declared-source health; nil renders none
	// dispatch and passes are the leader's live lane-loop state and per-lane pass
	// counts. Both are leader-held runtime memory: on a standby they are empty (it
	// dispatches nothing), so the readout omits the dispatch block entirely.
	dispatch *dispatch.State
	passes   *dispatch.PassCounter
	logger   *slog.Logger
	pid      int
	started  time.Time
}

// compile-time proof the plane satisfies the mux's ps seam.
var _ api.PsHandler = (*psPlane)(nil)

// NewPsPlane builds the ps handler the daemon wires into the api mux: role is
// the daemon's live leadership role (nil reads unknown), runs the meta-backed
// (or fake) run read seam, loads the running load collector the readout's load
// fields and history come from (nil reads as never sampled: null loads, no
// history). The plane records its own pid at construction and counts uptime
// from it: the plane is built at daemon start, so its age is the daemon's. A
// nil logger discards output.
func NewPsPlane(role api.RoleReporter, runs RunSnapshotReader, loads *loadHistory, counters *turnCounters, runLogs *RunLogWriter, sources *sourceWatcher, dispatchState *dispatch.State, passes *dispatch.PassCounter, retain int64, logger *slog.Logger) api.PsHandler {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &psPlane{
		role:     role,
		retain:   retain,
		runs:     runs,
		loads:    loads,
		counters: counters,
		sources:  sources,
		runLogs:  runLogs,
		dispatch: dispatchState,
		passes:   passes,
		logger:   logger,
		pid:      os.Getpid(),
		started:  time.Now(),
	}
}

// ManagedPostmasterPID returns a locator for the managed Postgres postmaster's
// live pid: it reads <pg data dir>/postmaster.pid (its first line is the pid,
// per Postgres's own contract) on each call, so a restarted instance reports
// its current pid. It answers 0 -- no managed load to sum -- when the file is
// absent (external mode, stopped instance) or unreadable.
func ManagedPostmasterPID(s config.Settings) func() int {
	pidPath := filepath.Join(ManagedPGDir(s), "data", "postmaster.pid")
	return func() int {
		raw, err := os.ReadFile(pidPath) //nolint:gosec // G304: pidPath is the engine-owned managed-Postgres data dir's postmaster.pid, never user or network input.
		if err != nil {
			return 0
		}
		line, _, _ := strings.Cut(string(raw), "\n")
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			return 0
		}
		return pid
	}
}

// Ps composes the current process-status payload: the engine block over the
// live role and the collector's newest load sample, and the run rows newest
// first -- queued and running only, or the whole history under all. history
// attaches the collector's recorded series to the payload.
func (p *psPlane) Ps(ctx context.Context, all, history bool) (api.PsPayload, error) {
	runs, err := p.runs.Runs(ctx, store.RunFilter{})
	if err != nil {
		p.logger.Error("ps run snapshot failed", "err", err)
		return api.PsPayload{}, fmt.Errorf("daemon: ps run snapshot: %w", err)
	}

	engine := api.PsEngine{
		Version: buildinfo.Version,
		Role:    string(api.RoleUnknown),
		PID:     p.pid,
		Uptime:  renderUptime(time.Since(p.started)),
	}
	if p.role != nil {
		role := p.role.Role()
		engine.Role = string(role)
		if role != api.RoleLeader {
			engine.Leader = p.role.LeaderHint()
		}
	}

	// The load is the collector's newest sample -- best-effort by the probe's
	// contract, so a host it could not probe reads null, never a fabricated
	// zero. The collector's values are read-only by contract, safe to marshal.
	tick, engineLoad, groupLoad := p.loads.latest()
	engine.Load = engineLoad

	rows := make([]api.PsRun, 0, len(runs))
	// The reader returns ascending ordering identity; the readout is newest first.
	for i := len(runs) - 1; i >= 0; i-- {
		run := runs[i]
		switch run.State {
		case store.RunQueued:
			engine.QueuedRuns++
		case store.RunRunning:
			engine.RunningRuns++
		}
		if !all && run.State != store.RunQueued && run.State != store.RunRunning {
			continue
		}
		row := api.PsRun{
			ID:       run.ID,
			Pipeline: run.Pipeline,
			Lane:     run.Lane,
			State:    string(run.State),
			Cause:    string(run.Cause),
		}
		// Observed run times (#238 phase 2): rendered here, second- (or
		// millisecond-) truncated, so the wire never carries a computable
		// timestamp — the renderUptime stance, per run.
		if run.State == store.RunRunning && run.ElapsedMillis != nil {
			row.Elapsed = renderRunSpan(*run.ElapsedMillis)
		}
		if (run.State == store.RunSucceeded || run.State == store.RunDeadLettered) && run.DurationMillis != nil {
			row.Duration = renderRunSpan(*run.DurationMillis)
		}
		// The reader coalesces a missing exit code to zero; only a terminal run
		// carries a real one on the wire.
		if run.ExitCode != nil && (run.State == store.RunSucceeded || run.State == store.RunDeadLettered) {
			code := *run.ExitCode
			row.ExitCode = &code
		}
		if run.State == store.RunRunning && run.Handle != 0 {
			row.Load = groupLoad[run.Handle]
		}
		row.Log = runLogMeta(p.runLogs, run.ID)
		rows = append(rows, row)
	}
	payload := api.PsPayload{Engine: engine, Runs: rows, Residents: p.counters.snapshot(), SampleTick: tick}
	payload.PipelineTimes = pipelineTimes(runs)
	payload.Retention = pipelineRetention(runs, p.retain)
	if p.sources != nil {
		payload.Sources = p.sources.health()
	}
	if history {
		payload.History = p.loads.snapshot()
	}
	payload.Dispatch = dispatchReadout(p.dispatch, p.passes)
	return payload, nil
}

// dispatchReadout composes the live dispatch block from the leader's lane-loop
// state and pass counts. It returns nil -- the block absent from the wire -- when no
// state is wired or the loop has not reconciled, which is exactly the standby case:
// a node that dispatches nothing reports nothing rather than an empty claim.
func dispatchReadout(state *dispatch.State, passes *dispatch.PassCounter) *api.PsDispatch {
	lanes := state.Lanes()
	if len(lanes) == 0 {
		return nil
	}
	counts := map[string]int64{}
	if passes != nil {
		counts = passes.Counts()
	}
	out := &api.PsDispatch{Lanes: make([]api.PsDispatchLane, 0, len(lanes))}
	// position indexes each pipeline's place in its lane's serial walk, so the
	// per-pipeline rows can answer "member 2 of 3" without re-reading the walk.
	type slot struct {
		lane     string
		pos, len int
	}
	position := map[string]slot{}
	for _, l := range lanes {
		out.Lanes = append(out.Lanes, api.PsDispatchLane{
			Lane:    l.Lane,
			Members: l.Members,
			Cares:   l.Cares,
			State:   laneState(l),
			Passes:  counts[l.Lane],
		})
		for i, m := range l.Members {
			position[m] = slot{lane: l.Lane, pos: i + 1, len: len(l.Members)}
		}
	}

	// Every walked member gets a row, whether or not it has reached a turn yet:
	// the pane must be able to place a pipeline in its lane before the first pass
	// gates it. Gate fields stay empty until a turn resolves one.
	gates := map[string]dispatch.PipelineGate{}
	for _, g := range state.Gates() {
		gates[g.Pipeline] = g
	}
	for _, l := range lanes {
		for _, m := range l.Members {
			slot := position[m]
			row := api.PsDispatchPipeline{Pipeline: m, Lane: slot.lane, Pos: slot.pos, Members: slot.len}
			if g, ok := gates[m]; ok {
				row.Gate = g.Decision
				for _, e := range g.Edges {
					edge := api.PsDispatchEdge{Upstream: e.Upstream, Verdict: e.Verdict.String()}
					if e.LatestRunID != 0 {
						edge.LatestRunID = strconv.FormatInt(e.LatestRunID, 10)
					}
					row.Edges = append(row.Edges, edge)
				}
			}
			out.Pipelines = append(out.Pipelines, row)
		}
	}
	sort.Slice(out.Pipelines, func(a, b int) bool { return out.Pipelines[a].Pipeline < out.Pipelines[b].Pipeline })
	return out
}

// laneState names a lane's disposition for the wire. Passing wins over parked: a
// lane whose pass is in flight is working, whatever the watermark says.
func laneState(l dispatch.LaneDispatch) string {
	switch {
	case l.Passing:
		return api.DispatchPassing
	case l.Parked:
		return api.DispatchParked
	default:
		return api.DispatchEligible
	}
}

// sumTrees sums the host sample over the engine's process trees: every process
// whose parentage reaches one of the roots (the daemon, the managed
// postmaster), roots included. A zero root is skipped (no managed instance).
// It returns nil -- null on the wire -- when no sampled process matched, so a
// dead root never reads as a zero-load engine.
func sumTrees(samples []procSample, roots ...int) *api.PsLoad {
	inTree := map[int]bool{}
	for _, r := range roots {
		if r != 0 {
			inTree[r] = true
		}
	}
	// Children reparent below their root, never above it, so one pass per depth
	// level converges; the loop caps at the sample size for safety.
	for grew := true; grew; {
		grew = false
		for _, s := range samples {
			if !inTree[s.PID] && inTree[s.PPID] {
				inTree[s.PID] = true
				grew = true
			}
		}
	}
	var load *api.PsLoad
	for _, s := range samples {
		if !inTree[s.PID] {
			continue
		}
		if load == nil {
			load = &api.PsLoad{}
		}
		load.CPUPercent += s.CPUPercent
		load.RSSBytes += s.RSSBytes
	}
	return load
}

// renderUptime renders the daemon's age as the display-only uptime string (the
// one wall-clock readout, display only). Rendering happens here,
// second-truncated, so the wire never carries a computable duration or
// timestamp.
func renderUptime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return d.Truncate(time.Second).String()
}

// renderRunSpan renders an observed run span for display: second-truncated
// once it reaches a second, milliseconds below (a 40ms run's duration is its
// only honest cost signal — #238). Display only, like renderUptime.
func renderRunSpan(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	d := time.Duration(ms) * time.Millisecond
	if d < time.Second {
		return d.Truncate(time.Millisecond).String()
	}
	return d.Truncate(time.Second).String()
}

// pipelineRetention folds the run snapshot into the retention readout: the
// configured keep count and each pipeline's surviving run-id range. Pure over
// the snapshot, so it is table-testable without a database. Ids, never
// timestamps: prune is count-based, so the floor is the oldest id meta still
// holds, not a moment.
func pipelineRetention(runs []store.Run, retain int64) *api.PsRetention {
	if len(runs) == 0 {
		return nil
	}
	type span struct {
		oldest, newest string
		count          int
	}
	byPipe := map[string]*span{}
	var order []string
	for _, r := range runs { // ascending id, the reader's order
		s := byPipe[r.Pipeline]
		if s == nil {
			s = &span{oldest: r.ID}
			byPipe[r.Pipeline] = s
			order = append(order, r.Pipeline)
		}
		s.newest = r.ID
		s.count++
	}
	sort.Strings(order)
	out := &api.PsRetention{Retain: retain}
	for _, name := range order {
		s := byPipe[name]
		out.Pipelines = append(out.Pipelines, api.PsPipelineRetention{
			Pipeline: name, Runs: s.count, OldestRunID: s.oldest, NewestRunID: s.newest,
		})
	}
	return out
}

// pipelineTimes aggregates observed durations per pipeline (#238 phase 2):
// last / avg / p50 / max as rendered strings plus the newest-last quantized
// per-run levels the TIME strip draws. Aggregation happens engine-side so the
// wire ships no numeric durations a client could feed back as scheduling
// input; the levels are 1..8 against the pipeline's own maximum.
func pipelineTimes(runs []store.Run) []api.PsPipelineTime {
	type acc struct {
		durs []int64 // ascending run order (reader order)
	}
	byPipe := map[string]*acc{}
	var order []string
	for _, run := range runs {
		if run.DurationMillis == nil {
			continue
		}
		a := byPipe[run.Pipeline]
		if a == nil {
			a = &acc{}
			byPipe[run.Pipeline] = a
			order = append(order, run.Pipeline)
		}
		a.durs = append(a.durs, *run.DurationMillis)
	}
	sort.Strings(order)
	out := make([]api.PsPipelineTime, 0, len(order))
	for _, name := range order {
		durs := byPipe[name].durs
		maxMs, sum := int64(0), int64(0)
		for _, d := range durs {
			sum += d
			if d > maxMs {
				maxMs = d
			}
		}
		sorted := append([]int64(nil), durs...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		levels := make([]int, len(durs))
		for i, d := range durs {
			levels[i] = quantizeLevel(d, maxMs)
		}
		out = append(out, api.PsPipelineTime{
			Pipeline: name,
			Runs:     len(durs),
			Last:     renderRunSpan(durs[len(durs)-1]),
			Avg:      renderRunSpan(sum / int64(len(durs))),
			P50:      renderRunSpan(sorted[len(sorted)/2]),
			Max:      renderRunSpan(maxMs),
			Levels:   levels,
		})
	}
	return out
}

// quantizeLevel maps one duration onto the 1..8 strip ramp against the
// pipeline's maximum (1 floor so every recorded run stays visible).
func quantizeLevel(ms, maxMs int64) int {
	if maxMs <= 0 {
		return 1
	}
	l := int((ms*8 + maxMs - 1) / maxMs)
	if l < 1 {
		l = 1
	}
	if l > 8 {
		l = 8
	}
	return l
}
