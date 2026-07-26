package tui

import "testing"

// TestSplitPane pins the detail pane's column arithmetic at every tier the
// frame actually reaches, plus the boundaries either side of the split. The
// widths come from the real geometry: a 150-column terminal leaves the pane
// an interior of 105, a 100-column one 66, an 80-column one 46, and a
// 60-column one 54 (the rail sheds below 70, handing its width back).
func TestSplitPane(t *testing.T) {
	t.Run("split-pane", func(t *testing.T) {
		const ix = 3
		tests := []struct {
			name       string
			iw         int
			wantSplit  bool
			wantLW     int
			wantRW     int
			wantRuleAt int
		}{
			{name: "150x40 interior", iw: 105, wantSplit: true, wantLW: 30, wantRW: 72, wantRuleAt: ix + 31},
			{name: "wide interior clamps the spec column at its ceiling", iw: 95, wantSplit: true, wantLW: 27, wantRW: 65, wantRuleAt: ix + 28},
			{name: "100x30 interior clamps at the floor", iw: 66, wantSplit: true, wantLW: 22, wantRW: 41, wantRuleAt: ix + 23},
			{name: "narrowest split", iw: 62, wantSplit: true, wantLW: 22, wantRW: 37, wantRuleAt: ix + 23},
			{name: "one column short of a split stacks", iw: 61, wantSplit: false, wantLW: 61, wantRW: 61, wantRuleAt: -1},
			{name: "60x20 interior stacks", iw: 54, wantSplit: false, wantLW: 54, wantRW: 54, wantRuleAt: -1},
			{name: "80x24 interior stacks", iw: 46, wantSplit: false, wantLW: 46, wantRW: 46, wantRuleAt: -1},
			{name: "an interior narrower than the spec floor still stacks", iw: 20, wantSplit: false, wantLW: 20, wantRW: 20, wantRuleAt: -1},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := splitPane(ix, tt.iw)
				if got.split != tt.wantSplit {
					t.Fatalf("split = %v, want %v (%+v)", got.split, tt.wantSplit, got)
				}
				if got.lw != tt.wantLW || got.rw != tt.wantRW || got.ruleX != tt.wantRuleAt {
					t.Errorf("geometry = lw %d rw %d rule %d, want lw %d rw %d rule %d",
						got.lw, got.rw, got.ruleX, tt.wantLW, tt.wantRW, tt.wantRuleAt)
				}
				if !got.split {
					return
				}
				// The two columns plus the rule and its spaces must account for
				// every interior cell, and never overrun it.
				if got.lw+3+got.rw != tt.iw {
					t.Errorf("columns span %d cells, want the whole interior %d", got.lw+3+got.rw, tt.iw)
				}
				if got.rx != got.ruleX+2 || got.lx != ix {
					t.Errorf("column origins = lx %d rule %d rx %d, want lx %d and rx two past the rule", got.lx, got.ruleX, got.rx, ix)
				}
			})
		}
	})
}

// TestRunsTier proves the run table sheds whole columns at its width
// boundaries -- a clipped header reads as a bug, a missing column as a narrow
// terminal.
func TestRunsTier(t *testing.T) {
	t.Run("runs-tier", func(t *testing.T) {
		tests := []struct {
			w    int
			want int
			cols int
		}{
			{w: 72, want: 2, cols: 7},
			{w: psRunsFullW, want: 2, cols: 7},
			{w: psRunsFullW - 1, want: 1, cols: 6},
			{w: psRunsCauseW, want: 1, cols: 6},
			{w: psRunsCauseW - 1, want: 0, cols: 5},
			{w: psRunsCoreW, want: 0, cols: 5},
		}
		rows := []detailRun{{id: "14", state: "running"}}
		for _, tt := range tests {
			if got := runsTier(tt.w); got != tt.want {
				t.Errorf("runsTier(%d) = %d, want %d", tt.w, got, tt.want)
			}
			if got := len(runWriteColumns(rows, runsTier(tt.w))); got != tt.cols {
				t.Errorf("width %d builds %d columns, want %d", tt.w, got, tt.cols)
			}
		}
	})
}

// TestDetailRunsAbsence proves the run table renders what the engine has not
// said as absence, never as a fabricated zero: a run that wrote nothing shows
// a dash in WROTE and JOURNAL, while TRIGGER carries the recorded cause.
func TestDetailRunsAbsence(t *testing.T) {
	t.Run("detail-runs-absence", func(t *testing.T) {
		m := newPsModel(psvFixture(), "")
		m.selectTree(psTreeRow{lane: "ingest", pipeline: "load_orders"})
		m.showAll = true
		rows := pipelineDetailRuns(m, pipelineSpecScope(m))
		if len(rows) == 0 {
			t.Fatal("load_orders must have recorded runs in the fixture")
		}
		cols := runWriteColumns(rows, 2)
		headers := make([]string, len(cols))
		for i, c := range cols {
			headers[i] = c.header
		}
		want := []string{"RUN", "WROTE", "OP", "STATE", "ELAPSED", "JOURNAL", "TRIGGER"}
		for i, h := range want {
			if headers[i] != h {
				t.Fatalf("columns = %v, want %v", headers, want)
			}
		}
		trigger := cols[6]
		wantCause := map[string]string{"14": "loop", "9": "loop", "6": "replay"}
		for i, r := range rows {
			if got, want := trigger.cells[i], wantCause[r.id]; got != want {
				t.Errorf("TRIGGER for run %s = %q, want %q", r.id, got, want)
			}
		}
		// Run 6 dead-lettered without writing: its journal range is unknown.
		dead := -1
		for i, r := range rows {
			if r.id == "6" {
				dead = i
			}
		}
		if dead < 0 {
			t.Fatal("the fixture's dead-lettered run 6 is missing")
		}
		if got := cols[1].cells[dead]; got != "-" {
			t.Errorf("WROTE for a run that wrote nothing = %q, want absence", got)
		}
		if got := cols[5].cells[dead]; got != "-" {
			t.Errorf("JOURNAL for a run that wrote nothing = %q, want absence", got)
		}
	})
}
