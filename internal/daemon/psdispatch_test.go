package daemon

import (
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/dispatch"
)

// TestDispatchReadoutAbsentWithoutALeader proves the block is omitted rather than
// emitted empty when this node dispatches nothing: an unwired state (any non-leader
// composition) and a leader that has not reconciled both read as absence. A standby
// answering with an empty lane list would read as "the engine has no lanes", which
// is a claim it is in no position to make.
func TestDispatchReadoutAbsentWithoutALeader(t *testing.T) {
	t.Run("dispatch-readout-absent-without-a-leader", func(t *testing.T) {
		if got := dispatchReadout(nil, nil); got != nil {
			t.Errorf("unwired state readout = %+v, want nil", got)
		}
		if got := dispatchReadout(dispatch.NewState(), nil); got != nil {
			t.Errorf("pre-reconcile readout = %+v, want nil", got)
		}
	})
}

// TestDispatchReadoutComposesLanesAndGates proves the readout carries what the pane
// needs: each lane's disposition and pass count, and one row per walked member
// carrying its position in the serial walk and its last gate verdict. A member that
// has not reached a turn still gets a row -- the pane must place a pipeline in its
// lane before the first pass gates it.
func TestDispatchReadoutComposesLanesAndGates(t *testing.T) {
	t.Run("dispatch-readout-composes-lanes-and-gates", func(t *testing.T) {
		state := dispatch.NewState()
		state.ObserveLanes([]dispatch.LaneDispatch{
			{Lane: "ingest", Members: []string{"extract", "load_orders"}, Cares: []string{"extract", "load_orders"}, Passing: true},
			{Lane: "reporting", Members: []string{"monthly"}, Cares: []string{"monthly", "load_orders"}, Parked: true},
		})
		// Only two of the three members have reached a turn.
		state.RecordGate("extract", dispatch.Decision{Run: true})
		state.RecordGate("monthly", dispatch.Decision{Ledger: []dispatch.EdgeVerdict{
			{Upstream: "load_orders", Verdict: dispatch.VerdictUpToDate, LatestRunID: 9},
		}})
		passes := dispatch.NewPassCounter()
		hook := passes.Hook()
		hook(dispatch.PassReport{Lane: "ingest"})
		hook(dispatch.PassReport{Lane: "ingest"})

		got := dispatchReadout(state, passes)
		if got == nil {
			t.Fatal("readout = nil, want a block")
		}

		if len(got.Lanes) != 2 {
			t.Fatalf("lanes = %+v, want 2", got.Lanes)
		}
		if got.Lanes[0].State != api.DispatchPassing || got.Lanes[0].Passes != 2 {
			t.Errorf("ingest = %+v, want passing with 2 passes", got.Lanes[0])
		}
		if got.Lanes[1].State != api.DispatchParked {
			t.Errorf("reporting = %+v, want parked", got.Lanes[1])
		}

		// One row per walked member, ordered by pipeline.
		rows := map[string]api.PsDispatchPipeline{}
		for _, p := range got.Pipelines {
			rows[p.Pipeline] = p
		}
		if len(rows) != 3 {
			t.Fatalf("pipeline rows = %+v, want one per walked member", got.Pipelines)
		}
		if r := rows["load_orders"]; r.Pos != 2 || r.Members != 2 || r.Gate != "" {
			t.Errorf("load_orders = %+v, want member 2 of 2 with no gate yet", r)
		}
		if r := rows["extract"]; r.Gate != api.DispatchGateUngated || r.Pos != 1 {
			t.Errorf("extract = %+v, want an ungated member 1", r)
		}
		r := rows["monthly"]
		if r.Gate != api.DispatchGateClosed || r.Lane != "reporting" {
			t.Errorf("monthly = %+v, want a closed gate on reporting", r)
		}
		if len(r.Edges) != 1 || r.Edges[0].Upstream != "load_orders" || r.Edges[0].Verdict != "up_to_date" {
			t.Fatalf("monthly edges = %+v, want the up_to_date load_orders edge", r.Edges)
		}
		// Run ids ride the wire as strings, like every other id in the readout.
		if r.Edges[0].LatestRunID != "9" {
			t.Errorf("edge latest run id = %q, want \"9\"", r.Edges[0].LatestRunID)
		}
	})
}

// TestDispatchReadoutOmitsUnrunEdgeID proves an upstream that has never run carries
// no id rather than a fabricated zero -- the readout's absence-over-fabrication rule.
func TestDispatchReadoutOmitsUnrunEdgeID(t *testing.T) {
	t.Run("dispatch-readout-omits-unrun-edge-id", func(t *testing.T) {
		state := dispatch.NewState()
		state.ObserveLanes([]dispatch.LaneDispatch{{Lane: "etl", Members: []string{"b"}}})
		state.RecordGate("b", dispatch.Decision{Ledger: []dispatch.EdgeVerdict{
			{Upstream: "a", Verdict: dispatch.VerdictPending},
		}})
		got := dispatchReadout(state, nil)
		if got == nil || len(got.Pipelines) != 1 {
			t.Fatalf("readout = %+v, want one pipeline row", got)
		}
		if id := got.Pipelines[0].Edges[0].LatestRunID; id != "" {
			t.Errorf("latest run id = %q, want empty for an upstream that never ran", id)
		}
	})
}
