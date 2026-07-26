package tui

import (
	"strings"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
)

// TestDeriveEvents proves the digest diff: run transitions, journal commits,
// source health changes, and role changes each land as one row; the first
// poll is a baseline; unchanged state yields nothing.
func TestDeriveEvents(t *testing.T) {
	t.Run("derive-events", func(t *testing.T) {
		exit3 := 3
		base := psvFixture()

		t.Run("nil previous snapshot is a baseline, not news", func(t *testing.T) {
			if got := deriveEvents(nil, base, nil, "10:00:00"); got != nil {
				t.Fatalf("baseline events = %v, want none", got)
			}
		})

		t.Run("unchanged state derives nothing", func(t *testing.T) {
			if got := deriveEvents(&base, base, nil, "10:00:00"); len(got) != 0 {
				t.Fatalf("no-change events = %v, want none", got)
			}
		})

		t.Run("transitions, commits, and failures each land one row", func(t *testing.T) {
			next := psvFixture()
			// 14 finishes; a fresh run 17 dead-letters; extract's 12 starts.
			next.Ps.Runs = []api.PsRun{
				{ID: "17", Pipeline: "hello_iris", Lane: "ingest", State: "dead_lettered", ExitCode: &exit3},
				{ID: "14", Pipeline: "load_orders", Lane: "ingest", State: "succeeded", Duration: "2m20s"},
				{ID: "12", Pipeline: "extract", Lane: "ingest", State: "running", Elapsed: "1s"},
			}
			next.Ps.Engine.Role = "standby"
			next.Ps.Sources = []api.SourceHealth{{Pipeline: "quake_feed", URL: "u", ConsecutiveFails: 2, Error: "boom"}}
			delta := []api.JournalActivityGroup{
				{RunID: 14, Pipeline: "load_orders", Schema: "demo", Table: "orders", Op: "insert", Rows: 96, MinID: 8111, MaxID: 8206},
			}
			got := deriveEvents(&base, next, delta, "10:00:01")
			want := map[string]psEventSeverity{
				"extract/12 running":                           psEvInfo,
				"load_orders/14 succeeded · 2m20s":             psEvOK,
				"hello_iris/17 dead-lettered · exit 3":         psEvFail,
				"load_orders committed +96 rows → demo.orders": psEvCommit,
				"source quake_feed failing ×2 · boom":          psEvFail,
				"engine role leader → standby":                 psEvInfo,
			}
			if len(got) != len(want) {
				t.Fatalf("derived %d events %v, want %d", len(got), got, len(want))
			}
			for _, e := range got {
				sev, ok := want[e.Text]
				if !ok {
					t.Errorf("unexpected event %q", e.Text)
					continue
				}
				if e.Severity != sev {
					t.Errorf("event %q severity = %d, want %d", e.Text, e.Severity, sev)
				}
				if e.Stamp != "10:00:01" {
					t.Errorf("event %q stamp = %q", e.Text, e.Stamp)
				}
			}
		})

		t.Run("multi-table commit folds with a +N more label", func(t *testing.T) {
			delta := []api.JournalActivityGroup{
				{RunID: 14, Pipeline: "load_orders", Schema: "demo", Table: "orders", Op: "insert", Rows: 10},
				{RunID: 14, Pipeline: "load_orders", Schema: "demo", Table: "items", Op: "insert", Rows: 5},
				{RunID: 14, Pipeline: "load_orders", Schema: "demo", Table: "audit", Op: "delete", Rows: 2},
			}
			got := deriveEvents(&base, base, delta, "10:00:02")
			if len(got) != 1 {
				t.Fatalf("commit events = %v, want one folded row", got)
			}
			if !strings.Contains(got[0].Text, "+13 rows") || !strings.Contains(got[0].Text, "+2 more") {
				t.Errorf("folded commit = %q, want net rows and a +2 more label", got[0].Text)
			}
		})
	})
}
