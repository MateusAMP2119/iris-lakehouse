package tui

import (
	"strings"
	"testing"
)

// TestDetailPaneSurvivesAnUnpolledJournal proves the detail pane renders before
// the first journal-activity poll lands. Snapshot.Journal is nil until that poll
// succeeds, which is the state every view opens in and the state a view stays in
// whenever /journal/activity is failing -- so the pane's journal reads must all be
// nil-safe, not merely nil-safe once the aggregate arrives.
//
// This is a regression test for a real panic: the pipeline shape reached
// j.ByRun[...] directly, dereferencing the nil journal, so selecting a pipeline
// before the first poll crashed the view outright.
func TestDetailPaneSurvivesAnUnpolledJournal(t *testing.T) {
	t.Run("detail-pane-survives-an-unpolled-journal", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			shape func(*psModel)
		}{
			{"the pipeline shape", func(m *psModel) { m.selPipeline = "load_orders" }},
			{"the table shape", func(m *psModel) { m.selectTable("ingest", "demo.orders") }},
			{"a lane row", func(m *psModel) { m.selectLane("ingest"); m.selPipeline = "" }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s := psvFixture()
				s.Journal = nil // no activity poll has landed yet
				m := newPsModel(s, "")
				tc.shape(m)
				// A panic here is the failure; the render is the assertion.
				frame := strings.Join(renderPsFrame(m, 150, 40, false).plainLines(), "\n")
				if !strings.Contains(frame, "IRIS") {
					t.Fatalf("frame rendered without its chrome:\n%s", frame)
				}
			})
		}
	})
}

// TestJournalReadsAreNilSafe pins the journal accessors' own contract: every one
// of them answers a nil journal with absence rather than dereferencing it. The
// pane calls these on every frame, so a new accessor that skips the guard is a
// crash waiting for a slow first poll.
func TestJournalReadsAreNilSafe(t *testing.T) {
	t.Run("journal-reads-are-nil-safe", func(t *testing.T) {
		var j *psJournal
		if got := j.runWrote("14"); got != 0 {
			t.Errorf("runWrote = %d, want 0", got)
		}
		if lo, hi := j.runRange("14"); lo != 0 || hi != 0 {
			t.Errorf("runRange = %d,%d, want 0,0", lo, hi)
		}
		if _, ok := j.runWrite("14", "demo.orders"); ok {
			t.Error("runWrite reported a write from a nil journal")
		}
		if rows, id, open, promoted := j.tableTotals("demo.orders"); rows|id|open|promoted != 0 {
			t.Errorf("tableTotals = %d,%d,%d,%d, want zeros", rows, id, open, promoted)
		}
		if got := j.tableWriter("demo.orders"); got != "" {
			t.Errorf("tableWriter = %q, want empty", got)
		}
		if got := j.tableNames(); got != nil {
			t.Errorf("tableNames = %v, want nil", got)
		}
	})
}
