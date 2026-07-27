package dispatch_test

import (
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/dispatch"
)

// TestStateLanes proves the live lane view's mechanics: a reconcile's
// publish replaces the previous one wholesale (a lane the walk dropped disappears
// rather than lingering stale), the readout is ordered and defensively copied, and
// Reset clears the whole view -- the leadership-term semantics the daemon drives.
func TestStateLanes(t *testing.T) {
	t.Run("dispatch-state-lanes", func(t *testing.T) {
		s := dispatch.NewState()

		// A leader that has not reconciled knows nothing: absence, not an idle claim.
		if got := s.Lanes(); len(got) != 0 {
			t.Fatalf("fresh state lanes = %v, want empty", got)
		}

		s.ObserveLanes([]dispatch.LaneDispatch{
			{Lane: "reporting", Members: []string{"monthly"}, Cares: []string{"monthly", "load_orders"}, Parked: true},
			{Lane: "ingest", Members: []string{"extract", "load_orders"}, Cares: []string{"extract", "load_orders"}, Passing: true},
		})
		lanes := s.Lanes()
		if len(lanes) != 2 {
			t.Fatalf("lanes = %v, want 2", lanes)
		}
		// Ordered by lane name, whatever order the reconcile published in.
		if lanes[0].Lane != "ingest" || lanes[1].Lane != "reporting" {
			t.Errorf("lane order = %q,%q, want ingest,reporting", lanes[0].Lane, lanes[1].Lane)
		}
		if !lanes[0].Passing || lanes[0].Parked {
			t.Errorf("ingest = %+v, want passing", lanes[0])
		}
		if !lanes[1].Parked || lanes[1].Passing {
			t.Errorf("reporting = %+v, want parked", lanes[1])
		}

		// The readout is a copy: mutating it never reaches the state.
		lanes[0].Members[0] = "mutated"
		if got := s.Lanes()[0].Members[0]; got != "extract" {
			t.Errorf("member after caller mutation = %q, want extract", got)
		}

		// A reconcile that no longer names a lane drops it: the view is the walk's,
		// never an accumulation of every lane ever seen.
		s.ObserveLanes([]dispatch.LaneDispatch{{Lane: "ingest", Members: []string{"extract"}}})
		if got := s.Lanes(); len(got) != 1 || got[0].Lane != "ingest" {
			t.Errorf("after re-publish lanes = %v, want ingest alone", got)
		}

		s.Reset()
		if got := s.Lanes(); len(got) != 0 {
			t.Errorf("lanes after reset = %v, want empty (a term never inherits a park view)", got)
		}
	})
}

// TestStateGates proves the per-pipeline gate record: each decision shape
// names itself for the readout, the newest turn wins, and a pipeline the walk drops
// loses its verdict (a removed pipeline's gate is not a fact about the engine).
func TestStateGates(t *testing.T) {
	t.Run("dispatch-state-gates", func(t *testing.T) {
		ledger := []dispatch.EdgeVerdict{{Upstream: "extract", Verdict: dispatch.VerdictOpen, LatestRunID: 12}}
		for _, tc := range []struct {
			name     string
			decision dispatch.Decision
			want     string
		}{
			{"ungated pipeline has no edges", dispatch.Decision{Run: true}, dispatch.GateUngated},
			{"an open gate ran", dispatch.Decision{Run: true, Ledger: ledger}, dispatch.GateOpen},
			{"a closed gate minted nothing", dispatch.Decision{Ledger: ledger}, dispatch.GateClosed},
			{"a poisoned gate outranks the rest", dispatch.Decision{Poisoned: true, Ledger: ledger}, dispatch.GatePoisoned},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s := dispatch.NewState()
				s.ObserveLanes([]dispatch.LaneDispatch{{Lane: "ingest", Members: []string{"load_orders"}}})
				s.RecordGate("load_orders", tc.decision)
				gates := s.Gates()
				if len(gates) != 1 {
					t.Fatalf("gates = %v, want 1", gates)
				}
				if gates[0].Decision != tc.want {
					t.Errorf("decision = %q, want %q", gates[0].Decision, tc.want)
				}
			})
		}

		// The newest turn wins: the readout explains the pipeline's CURRENT
		// disposition, not the first one it ever had.
		s := dispatch.NewState()
		s.ObserveLanes([]dispatch.LaneDispatch{{Lane: "ingest", Members: []string{"load_orders"}}})
		s.RecordGate("load_orders", dispatch.Decision{Ledger: ledger})
		s.RecordGate("load_orders", dispatch.Decision{Run: true, Ledger: ledger})
		if got := s.Gates()[0].Decision; got != dispatch.GateOpen {
			t.Errorf("decision after re-record = %q, want the newest (%q)", got, dispatch.GateOpen)
		}

		// A pipeline the walk no longer names loses its verdict at the next publish.
		s.ObserveLanes([]dispatch.LaneDispatch{{Lane: "ingest", Members: []string{"extract"}}})
		if got := s.Gates(); len(got) != 0 {
			t.Errorf("gates after the pipeline left the walk = %v, want empty", got)
		}
	})
}

// TestStateNil proves every method tolerates a nil state, so the loop and
// the readout compose without a state wired (the walk-only test wirings, and any
// node that never dispatches).
func TestStateNil(t *testing.T) {
	t.Run("dispatch-state-nil", func(t *testing.T) {
		var s *dispatch.State
		s.ObserveLanes([]dispatch.LaneDispatch{{Lane: "ingest"}})
		s.RecordGate("load_orders", dispatch.Decision{Run: true})
		s.Reset()
		if got := s.Lanes(); got != nil {
			t.Errorf("nil state lanes = %v, want nil", got)
		}
		if got := s.Gates(); got != nil {
			t.Errorf("nil state gates = %v, want nil", got)
		}
	})
}
