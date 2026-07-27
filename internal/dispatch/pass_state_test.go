package dispatch_test

import (
	"context"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/dispatch"
)

// laneNamed finds one lane in a published view, failing when it is absent.
func laneNamed(t *testing.T, lanes []dispatch.LaneDispatch, name string) dispatch.LaneDispatch {
	t.Helper()
	for _, l := range lanes {
		if l.Lane == name {
			return l
		}
	}
	t.Fatalf("no lane %q in the published view %v", name, lanes)
	return dispatch.LaneDispatch{}
}

// TestLoopPublishesParkState proves the loop's observability publish tracks the
// decisions it already made: the lane it passed is marked passing, and once it
// parks on the watermark the published view says parked. The state is read by the
// ps readout's DISPATCH block, so a wrong mark there is an operator reading "idle"
// at a lane that is actually working.
func TestLoopPublishesParkState(t *testing.T) {
	t.Run("loop-publishes-park-state", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()

		events := dispatch.NewEvents()
		state := dispatch.NewState()
		hook, reports := passReports(t)
		loop := dispatch.NewLoop(
			newFakeWalk(dispatch.Lane{Name: "solo", Pipelines: []string{"a"}, Cares: []string{"a"}}),
			newFakeGate(), newFakeRunner(), nil,
			dispatch.WithOnPass(hook), dispatch.WithEvents(events), dispatch.WithState(state),
		)
		done := make(chan struct{})
		go func() { defer close(done); _ = loop.Run(ctx) }()
		defer func() { cancel(); <-done }()

		// The first pass runs unconditionally, so the view names the lane with its
		// members and its care set as the walk supplied them.
		nextReport(ctx, t, reports)
		lane := laneNamed(t, state.Lanes(), "solo")
		if len(lane.Members) != 1 || lane.Members[0] != "a" {
			t.Errorf("members = %v, want [a]", lane.Members)
		}
		if len(lane.Cares) != 1 || lane.Cares[0] != "a" {
			t.Errorf("cares = %v, want [a]", lane.Cares)
		}

		// Let the loop settle into its park: with no cause landing, the reconcile
		// that follows the pass marks the lane parked. Bumping and reading after the
		// next report would race the publish, so drive one more pass and then stop
		// the loop -- the final publish is the parked one.
		events.Bump()
		nextReport(ctx, t, reports)
		cancel()
		<-done
		if got := laneNamed(t, state.Lanes(), "solo"); !got.Parked {
			t.Errorf("lane = %+v, want parked once the watermark stood still", got)
		}
	})
}

// TestLoopRecordsGateVerdicts proves the pass records what the gate decided at each
// member's turn, so the readout explains a closed gate without running a gate query
// of its own. An ungated member records ungated; the ledger rides along verbatim.
func TestLoopRecordsGateVerdicts(t *testing.T) {
	t.Run("loop-records-gate-verdicts", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()

		state := dispatch.NewState()
		hook, reports := passReports(t)
		loop := dispatch.NewLoop(
			newFakeWalk(dispatch.Lane{Name: "etl", Pipelines: []string{"a", "b"}}),
			newFakeGate(), newFakeRunner(), nil,
			dispatch.WithOnPass(hook), dispatch.WithEvents(dispatch.NewEvents()), dispatch.WithState(state),
		)
		done := make(chan struct{})
		go func() { defer close(done); _ = loop.Run(ctx) }()
		nextReport(ctx, t, reports)
		cancel()
		<-done

		gates := state.Gates()
		if len(gates) != 2 {
			t.Fatalf("gates = %v, want one per walked member", gates)
		}
		for _, g := range gates {
			// The fake gate opens for everyone with no edges: an ungated member.
			if g.Decision != dispatch.GateUngated {
				t.Errorf("%s decision = %q, want %q", g.Pipeline, g.Decision, dispatch.GateUngated)
			}
			if len(g.Edges) != 0 {
				t.Errorf("%s edges = %v, want none", g.Pipeline, g.Edges)
			}
		}
	})
}
