package tui

import "testing"

// TestSplitDetail pins the two panes' arithmetic at every tier the frame
// reaches, plus the boundaries either side of the split. The widths are the
// real geometry: 150 columns leave the detail pane 109, 100 leave 70, 80 leave
// 50, 60 leave 58 (the rail sheds below 70, handing its width back).
func TestSplitDetail(t *testing.T) {
	t.Run("split-detail", func(t *testing.T) {
		const px = 3
		tests := []struct {
			name      string
			w         int
			wantSplit bool
			wantLW    int
			wantRW    int
		}{
			{name: "150x40 detail pane", w: 109, wantSplit: true, wantLW: 31, wantRW: 76},
			{name: "a wide pane clamps the spec pane at its ceiling", w: 140, wantSplit: true, wantLW: 34, wantRW: 104},
			{name: "100x30 clamps at the floor", w: 70, wantSplit: true, wantLW: 26, wantRW: 42},
			{name: "narrowest split", w: 69, wantSplit: true, wantLW: 26, wantRW: 41},
			{name: "one column short of a split keeps one pane", w: 68, wantSplit: false, wantLW: 68, wantRW: 68},
			{name: "60x20 keeps one pane", w: 58, wantSplit: false, wantLW: 58, wantRW: 58},
			{name: "80x24 keeps one pane", w: 50, wantSplit: false, wantLW: 50, wantRW: 50},
			{name: "narrower than the spec floor still keeps one pane", w: 20, wantSplit: false, wantLW: 20, wantRW: 20},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := splitDetail(px, tt.w)
				if got.split != tt.wantSplit {
					t.Fatalf("split = %v, want %v (%+v)", got.split, tt.wantSplit, got)
				}
				if got.lw != tt.wantLW || got.rw != tt.wantRW {
					t.Errorf("geometry = lw %d rw %d, want lw %d rw %d", got.lw, got.rw, tt.wantLW, tt.wantRW)
				}
				if !got.split {
					return
				}
				// The panes plus the gap must account for every cell, no more.
				if got.lw+psPaneGap+got.rw != tt.w {
					t.Errorf("panes span %d cells, want the whole width %d", got.lw+psPaneGap+got.rw, tt.w)
				}
				if got.rx != px+got.lw+psPaneGap || got.lx != px {
					t.Errorf("pane origins = lx %d rx %d, want lx %d and rx one gap past it", got.lx, got.rx, px)
				}
				// Both panes must still afford their content once chrome is paid.
				if got.lw-psCardPad < psSpecMinW || got.rw-psCardPad < psRunsCoreW {
					t.Errorf("panes cannot hold their content: lw %d rw %d", got.lw, got.rw)
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
