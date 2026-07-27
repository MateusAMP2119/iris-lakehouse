package dispatch

import "sync"

// This file is the leader's live dispatch state: what the lane loop is doing right
// now, held in process memory so the ps readout can answer "why is this pipeline not
// running?" without a single extra query.
//
// It is deliberately not a meta table. Every fact here is a property of the current
// leader's runtime -- which lanes are parked on the watermark, which member the walk
// is on, what each gate last said -- so a daemon restart clears it by construction (a
// fresh process constructs a fresh state) and the daemon resets it when it wins a
// term, exactly like the pass counter beside it (passcounter.go). A standby holds an
// empty state and reports none: it dispatches nothing, so it knows nothing.
//
// Clock-free throughout. A lane is parked or passing, never "due in 30s"; a gate is
// open or closed, never "retrying at". The whole point of the block this feeds is
// that iris has no schedule -- it has causes -- and the readout must say so in the
// engine's own vocabulary rather than inventing a timetable the dispatcher does not
// have.
//
// The gate ledger is recorded where the loop already evaluates it (pass.go's member
// walk), so the readout costs no gate query of its own: the pane reads what the last
// pass decided, which is exactly the decision that left the pipeline where it is.

// LaneDispatch is one lane's live loop state as of the last reconcile.
type LaneDispatch struct {
	// Lane is the lane's name.
	Lane string
	// Members are the lane's pipelines in composer order -- the serial walk order.
	Members []string
	// Cares are the pipeline names whose causes wake this lane: its members plus
	// their upstreams. Empty means the lane wakes on every labeled cause.
	Cares []string
	// Passing reports that a pass over this lane is in flight.
	Passing bool
	// Parked reports that the lane is waiting on the watermark: its last pass
	// started at the current care-set sequence, so no cause it consumes has landed
	// since. A lane is either Passing or Parked or eligible (neither).
	Parked bool
}

// PipelineGate is one pipeline's gate resolution at its last turn in a pass: what
// the gate decided and the per-edge ledger behind it. A pipeline with no depends_on
// edges resolves ungated with an empty ledger.
type PipelineGate struct {
	// Pipeline is the gated pipeline.
	Pipeline string
	// Decision is the resolution: "open" (ran), "closed" (minted nothing),
	// "poisoned" (an awaited upstream dead-lettered), or "ungated" (no edges).
	Decision string
	// Edges is the per-edge verdict list in edge order, empty when ungated.
	Edges []EdgeVerdict
}

// Gate decision strings. They name what the pass did with the pipeline, not what a
// caller should do about it.
const (
	GateOpen     = "open"
	GateClosed   = "closed"
	GatePoisoned = "poisoned"
	GateUngated  = "ungated"
)

// State is the leader's live lane-loop state: per-lane park/pass status and
// per-pipeline gate resolutions. It is safe for concurrent use -- the loop's
// reconcile goroutine publishes lanes while per-lane pass goroutines record gates and
// the ps read path snapshots both. The zero value is not usable; construct one with
// NewState.
type State struct {
	mu    sync.Mutex
	epoch uint64 // term boundary: Reset bumps it, staling hooks minted before
	lanes map[string]LaneDispatch
	gates map[string]PipelineGate
}

// NewState returns an empty state: a leader that has not reconciled yet
// knows nothing about its lanes.
func NewState() *State {
	return &State{lanes: map[string]LaneDispatch{}, gates: map[string]PipelineGate{}}
}

// ObserveLanes replaces the per-lane view wholesale with the reconcile's own, so a
// lane the walk no longer names disappears rather than lingering stale. Gates are
// left alone: a member's last gate resolution outlives the reconcile that dropped
// its lane only until the next ForgetPipelines, which the caller drives from the
// walk.
func (s *State) ObserveLanes(lanes []LaneDispatch) {
	if s == nil {
		return
	}
	next := make(map[string]LaneDispatch, len(lanes))
	for _, l := range lanes {
		next[l.Lane] = LaneDispatch{
			Lane:    l.Lane,
			Members: append([]string(nil), l.Members...),
			Cares:   append([]string(nil), l.Cares...),
			Passing: l.Passing,
			Parked:  l.Parked,
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lanes = next
	// Drop gate entries for pipelines no live lane walks: a removed pipeline's
	// last verdict is not a fact about the engine any more.
	live := map[string]bool{}
	for _, l := range lanes {
		for _, m := range l.Members {
			live[m] = true
		}
	}
	for name := range s.gates {
		if !live[name] {
			delete(s.gates, name)
		}
	}
}

// RecordGate stores one pipeline's gate resolution from its turn in a pass. Later
// turns overwrite earlier ones: the readout answers with the newest decision, which
// is the one that explains the pipeline's current disposition.
func (s *State) RecordGate(pipeline string, d Decision) {
	if s == nil || pipeline == "" {
		return
	}
	entry := PipelineGate{Pipeline: pipeline, Decision: gateDecision(d), Edges: append([]EdgeVerdict(nil), d.Ledger...)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gates[pipeline] = entry
}

// gateDecision names a gate decision for the readout.
func gateDecision(d Decision) string {
	switch {
	case d.Poisoned:
		return GatePoisoned
	case len(d.Ledger) == 0:
		return GateUngated
	case d.Run:
		return GateOpen
	default:
		return GateClosed
	}
}

// Lanes returns the per-lane state, ordered by lane name. The returned slices are
// copies: mutating them never reaches the state.
func (s *State) Lanes() []LaneDispatch {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LaneDispatch, 0, len(s.lanes))
	for _, l := range s.lanes {
		out = append(out, LaneDispatch{
			Lane:    l.Lane,
			Members: append([]string(nil), l.Members...),
			Cares:   append([]string(nil), l.Cares...),
			Passing: l.Passing,
			Parked:  l.Parked,
		})
	}
	sortByName(out, func(l LaneDispatch) string { return l.Lane })
	return out
}

// Gates returns the per-pipeline gate resolutions, ordered by pipeline name.
func (s *State) Gates() []PipelineGate {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PipelineGate, 0, len(s.gates))
	for _, g := range s.gates {
		out = append(out, PipelineGate{Pipeline: g.Pipeline, Decision: g.Decision, Edges: append([]EdgeVerdict(nil), g.Edges...)})
	}
	sortByName(out, func(g PipelineGate) string { return g.Pipeline })
	return out
}

// Reset clears every lane and gate and bumps the epoch: state never carries across
// leadership terms, exactly like the pass counts (passcounter.go). A daemon restart
// needs no call -- a new process constructs fresh.
func (s *State) Reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch++
	s.lanes = map[string]LaneDispatch{}
	s.gates = map[string]PipelineGate{}
}

// sortByName orders a slice by a string key, insertion-sorted: these slices are
// lane- and pipeline-sized, so the simple form beats pulling in a comparator.
func sortByName[T any](s []T, key func(T) string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && key(s[j]) < key(s[j-1]); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
