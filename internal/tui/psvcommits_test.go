package tui

import (
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
)

// TestDeriveCommits proves the rail footer's commit marks: a delta stamps the
// pipelines it names, an unattributed or empty group credits nobody, a
// pipeline the delta omits keeps the mark it had, and Seq -- not the wrapping
// HH:MM:SS stamp -- is what orders two marks.
func TestDeriveCommits(t *testing.T) {
	t.Run("derive-commits", func(t *testing.T) {
		group := func(pipeline string, rows int64) api.JournalActivityGroup {
			return api.JournalActivityGroup{Pipeline: pipeline, Rows: rows, Schema: "demo", Table: "orders"}
		}

		tests := []struct {
			name  string
			acc   map[string]psCommitMark
			delta []api.JournalActivityGroup
			stamp string
			seq   int64
			want  map[string]psCommitMark
		}{
			{
				name:  "a nil accumulator takes the first delta",
				delta: []api.JournalActivityGroup{group("load_orders", 1187)},
				stamp: "14:31:07", seq: 3,
				want: map[string]psCommitMark{"load_orders": {Stamp: "14:31:07", Seq: 3}},
			},
			{
				name:  "one delta stamps every pipeline it names",
				acc:   map[string]psCommitMark{},
				delta: []api.JournalActivityGroup{group("load_orders", 1187), group("solo", 12)},
				stamp: "14:32:19", seq: 4,
				want: map[string]psCommitMark{
					"load_orders": {Stamp: "14:32:19", Seq: 4},
					"solo":        {Stamp: "14:32:19", Seq: 4},
				},
			},
			{
				name:  "a pipeline the delta omits keeps its mark",
				acc:   map[string]psCommitMark{"solo": {Stamp: "14:29:41", Seq: 1}},
				delta: []api.JournalActivityGroup{group("load_orders", 1187)},
				stamp: "14:31:07", seq: 3,
				want: map[string]psCommitMark{
					"solo":        {Stamp: "14:29:41", Seq: 1},
					"load_orders": {Stamp: "14:31:07", Seq: 3},
				},
			},
			{
				name:  "an unattributed group credits nobody",
				acc:   map[string]psCommitMark{},
				delta: []api.JournalActivityGroup{group("", 1187)},
				stamp: "14:31:07", seq: 3,
				want: map[string]psCommitMark{},
			},
			{
				name:  "a rowless group is not a commit",
				acc:   map[string]psCommitMark{},
				delta: []api.JournalActivityGroup{group("load_orders", 0)},
				stamp: "14:31:07", seq: 3,
				want: map[string]psCommitMark{},
			},
			{
				name:  "a stale replay never rewinds a newer mark",
				acc:   map[string]psCommitMark{"load_orders": {Stamp: "00:01:12", Seq: 9}},
				delta: []api.JournalActivityGroup{group("load_orders", 1187)},
				stamp: "23:59:58", seq: 8, // the wall clock wrapped; the ordinal did not
				want: map[string]psCommitMark{"load_orders": {Stamp: "00:01:12", Seq: 9}},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := deriveCommits(tt.acc, tt.delta, tt.stamp, tt.seq)
				if len(got) != len(tt.want) {
					t.Fatalf("marks = %+v, want %+v", got, tt.want)
				}
				for name, want := range tt.want {
					if got[name] != want {
						t.Errorf("mark[%s] = %+v, want %+v", name, got[name], want)
					}
				}
			})
		}
	})
}

// TestLaneLastCommit proves the rail footer reads the newest mark across the
// lane's pipelines, ignoring marks belonging to other lanes.
func TestLaneLastCommit(t *testing.T) {
	t.Run("lane-last-commit", func(t *testing.T) {
		m := newPsModel(psvFixture(), "")
		m.snap.Commits = map[string]psCommitMark{
			"extract":     {Stamp: "14:20:00", Seq: 1},
			"load_orders": {Stamp: "14:31:07", Seq: 3},
			"solo":        {Stamp: "14:40:00", Seq: 9}, // a different lane
		}
		if got := m.laneLastCommit("ingest"); got != "14:31:07" {
			t.Errorf("ingest last commit = %q, want the newest in-lane mark 14:31:07", got)
		}
		if got := m.laneLastCommit("reporting"); got != "" {
			t.Errorf("a lane with no observed write = %q, want absence", got)
		}
	})
}
