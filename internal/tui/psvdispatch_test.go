package tui

import (
	"strings"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
)

// paneText renders the fixture at full width and returns the detail pane's text,
// the columns right of the rail.
func paneText(t *testing.T, m *psModel) string {
	t.Helper()
	var pane []string
	for _, ln := range renderPsFrame(m, 150, 40, false).plainLines() {
		r := []rune(ln)
		if len(r) <= 40 {
			continue
		}
		pane = append(pane, string(r[40:]))
	}
	return strings.Join(pane, "\n")
}

// TestDispatchBlockNamesWhyAPipelineWaits proves the DISPATCH block answers the
// question it exists for, in the dispatcher's vocabulary: the lane's disposition,
// the gate's last verdict, the per-edge ledger behind a closed gate, the member's
// place in the serial walk, and the causes that would wake the lane. No row of it
// is a time — iris has no schedule, so the block must never imply one.
func TestDispatchBlockNamesWhyAPipelineWaits(t *testing.T) {
	t.Run("dispatch-block-names-why-a-pipeline-waits", func(t *testing.T) {
		t.Run("a parked lane names its closed gate and the upstream it awaits", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.selectLane("reporting")
			m.selPipeline = "monthly"
			got := paneText(t, m)
			for _, want := range []string{"DISPATCH", "parked", "closed", "load_orders", "up_to_date",
				"reporting", "wakes on"} {
				if !strings.Contains(got, want) {
					t.Errorf("pane missing %q:\n%s", want, got)
				}
			}
		})

		t.Run("a passing lane reports its position and pass count", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.selectLane("ingest")
			m.selPipeline = "load_orders"
			got := paneText(t, m)
			for _, want := range []string{"passing", "ingest · 3 of 3", "47"} {
				if !strings.Contains(got, want) {
					t.Errorf("pane missing %q:\n%s", want, got)
				}
			}
		})

		t.Run("an ungated pipeline says it runs every pass", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.selPipeline = "extract"
			if got := paneText(t, m); !strings.Contains(got, "runs each pass") {
				t.Errorf("an ungated pipeline must say so plainly:\n%s", got)
			}
		})

		// The block is leader-only state. A standby's readout carries no dispatch
		// block at all, and the pane must then show nothing rather than an idle claim.
		t.Run("no dispatch block sheds the whole section", func(t *testing.T) {
			s := psvFixture()
			s.Ps.Dispatch = nil
			m := newPsModel(s, "")
			m.selPipeline = "load_orders"
			got := paneText(t, m)
			if strings.Contains(got, "DISPATCH") {
				t.Errorf("a readout without dispatch state must render no block:\n%s", got)
			}
			// The blocks below it still render: shedding one never cascades.
			if !strings.Contains(got, "LOAD") {
				t.Errorf("the remaining blocks must survive the shed:\n%s", got)
			}
		})

		// A pipeline the leader is not walking (never registered, or removed since)
		// has no row, so the block sheds for it alone.
		t.Run("a pipeline the walk does not name sheds the block", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.selPipeline = "solo"
			if got := paneText(t, m); strings.Contains(got, "DISPATCH") {
				t.Errorf("an unwalked pipeline must render no block:\n%s", got)
			}
		})
	})
}

// TestDispatchBlockStyling proves the block's colour carries its meaning: a
// poisoned edge is the only alarm, a passing lane reads live, and a parked one
// recedes -- the frame's existing state-colour vocabulary, applied to dispatch.
func TestDispatchBlockStyling(t *testing.T) {
	t.Run("dispatch-block-styling", func(t *testing.T) {
		for _, tc := range []struct{ state, want string }{
			{api.DispatchPassing, ansiGreen},
			{api.DispatchParked, ansiDim},
			{api.DispatchEligible, ansiCyan},
		} {
			if got := dispatchStateSGR(tc.state); got != tc.want {
				t.Errorf("state %q sgr = %q, want %q", tc.state, got, tc.want)
			}
		}
		for _, tc := range []struct{ verdict, want string }{
			{"poisoned", ansiRed},
			{"open", ansiGreen},
			{"pending", ansiDim},
			{"up_to_date", ansiDim},
		} {
			if got := verdictSGR(tc.verdict); got != tc.want {
				t.Errorf("verdict %q sgr = %q, want %q", tc.verdict, got, tc.want)
			}
		}
	})
}

// TestWakeTextCountsWhatItCannotSpell proves the care set is spelled out when it
// fits and counted when it does not, and that an empty care set reads as the real
// answer it is -- a lane with no care set wakes on any labeled cause.
func TestWakeTextCountsWhatItCannotSpell(t *testing.T) {
	t.Run("wake-text-counts-what-it-cannot-spell", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			cares []string
			w     int
			want  string
		}{
			{"an empty care set wakes on anything", nil, 20, "any cause"},
			{"a short set is spelled out", []string{"a", "b"}, 20, "a · b"},
			{"a set too wide for the column is counted", []string{"extract", "load_orders", "monthly"}, 12, "3 pipelines"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := wakeText(tc.cares, tc.w); got != tc.want {
					t.Errorf("wakeText(%v, %d) = %q, want %q", tc.cares, tc.w, got, tc.want)
				}
			})
		}
	})
}
