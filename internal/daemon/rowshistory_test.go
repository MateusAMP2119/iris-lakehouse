package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/pg"
	"github.com/MateusAMP2119/iris-lakehouse/internal/store"
)

// fakeJournal is a journalRowsReader serving scripted deltas and recording the
// watermarks the sampler read from.
type fakeJournal struct {
	ceiling   int64
	deltas    [][]pg.ActivityGroup
	at        int
	since     []int64
	primeErr  error
	activeErr error
}

func (f *fakeJournal) JournalBounds(context.Context) (int64, int64, error) {
	if f.primeErr != nil {
		return 0, 0, f.primeErr
	}
	return 1, f.ceiling, nil
}

func (f *fakeJournal) Activity(_ context.Context, sinceID int64) ([]pg.ActivityGroup, int64, error) {
	if f.activeErr != nil {
		return nil, 0, f.activeErr
	}
	f.since = append(f.since, sinceID)
	if f.at >= len(f.deltas) {
		return nil, sinceID, nil
	}
	groups := f.deltas[f.at]
	f.at++
	watermark := sinceID
	for _, g := range groups {
		if g.MaxID > watermark {
			watermark = g.MaxID
		}
	}
	return groups, watermark, nil
}

// rowsTestRuns is the sampler's run snapshot: two pipelines on one lane, plus
// a lane-less pipeline that falls back to its own name.
func rowsTestRuns() []store.Run {
	return []store.Run{
		{ID: "14", Pipeline: "load_orders", Lane: "ingest"},
		{ID: "12", Pipeline: "extract", Lane: "ingest"},
		{ID: "2", Pipeline: "solo"},
	}
}

// TestRowsSamplerPrimes proves the priming read: a cold start adopts the
// journal's current high id and reports NO sample, so a restart never counts
// the whole journal as one bucket and seals that spike into history forever.
func TestRowsSamplerPrimes(t *testing.T) {
	t.Run("rows-sampler-primes", func(t *testing.T) {
		j := &fakeJournal{ceiling: 8110, deltas: [][]pg.ActivityGroup{
			{{RunID: 14, Rows: 1187, MaxID: 8300}},
		}}
		s := newRowsSampler(j, nil)

		rows, ok := s.sample(context.Background(), rowsTestRuns())
		if ok || rows != nil {
			t.Fatalf("the priming tick reported rows %v ok %v, want no sample at all", rows, ok)
		}
		if len(j.since) != 0 {
			t.Fatalf("the priming tick issued an activity read at %v, want bounds only", j.since)
		}

		rows, ok = s.sample(context.Background(), rowsTestRuns())
		if !ok {
			t.Fatal("the tick after priming must report a sample")
		}
		if len(j.since) != 1 || j.since[0] != 8110 {
			t.Fatalf("read since %v, want the primed ceiling 8110 -- not 0", j.since)
		}
		if rows["pipeline:load_orders"] != 1187 {
			t.Errorf("rows = %v, want only the delta above the primed watermark", rows)
		}
	})

	t.Run("a failed prime reports no sample and retries next tick", func(t *testing.T) {
		j := &fakeJournal{primeErr: errors.New("data database unreachable")}
		s := newRowsSampler(j, nil)
		if _, ok := s.sample(context.Background(), nil); ok {
			t.Fatal("a failed prime must report absence")
		}
		j.primeErr = nil
		j.ceiling = 42
		if _, ok := s.sample(context.Background(), nil); ok {
			t.Fatal("the retried prime is still a priming tick, not a sample")
		}
		if s.since != 42 {
			t.Errorf("watermark = %d, want the ceiling from the successful retry", s.since)
		}
	})

	t.Run("a nil reader is a permanent no-op", func(t *testing.T) {
		if rows, ok := newRowsSampler(nil, nil).sample(context.Background(), nil); ok || rows != nil {
			t.Errorf("nil sampler returned rows %v ok %v, want absence", rows, ok)
		}
	})
}

// TestRowsSamplerAttributes proves the delta lands on the right series: the
// writing run's pipeline and lane, with the engine total carrying every
// counted row including those whose run row is gone from meta.
func TestRowsSamplerAttributes(t *testing.T) {
	t.Run("rows-sampler-attributes", func(t *testing.T) {
		j := &fakeJournal{ceiling: 0, deltas: [][]pg.ActivityGroup{{
			{RunID: 14, Rows: 1000, Op: "insert", MaxID: 10},
			{RunID: 14, Rows: 187, Op: "update", MaxID: 20},
			{RunID: 12, Rows: 40, Op: "insert", MaxID: 30},
			{RunID: 2, Rows: 12, Op: "delete", MaxID: 40},
			{RunID: 99, Rows: 500, Op: "insert", MaxID: 50}, // run pruned from meta
		}}}
		s := newRowsSampler(j, nil)
		s.sample(context.Background(), rowsTestRuns()) // prime
		rows, ok := s.sample(context.Background(), rowsTestRuns())
		if !ok {
			t.Fatal("the sample must report a reading")
		}

		want := map[string]int64{
			// Both ops on run 14 fold together: captured rows, not net rows.
			"pipeline:load_orders": 1187,
			"pipeline:extract":     40,
			// A lane-less run falls back to its own pipeline name.
			"pipeline:solo": 12,
			"lane:solo":     12,
			"lane:ingest":   1227,
			// The engine total includes the unattributable 500.
			"engine": 1739,
		}
		for key, w := range want {
			if rows[key] != w {
				t.Errorf("rows[%s] = %d, want %d (all: %v)", key, rows[key], w, rows)
			}
		}
		if _, credited := rows["pipeline:"]; credited {
			t.Error("an unattributable group must not mint an empty-named pipeline series")
		}
	})

	t.Run("a successful read counting nothing is a real zero, not absence", func(t *testing.T) {
		j := &fakeJournal{deltas: [][]pg.ActivityGroup{{}}}
		s := newRowsSampler(j, nil)
		s.sample(context.Background(), rowsTestRuns()) // prime
		rows, ok := s.sample(context.Background(), rowsTestRuns())
		if !ok {
			t.Fatal("an empty delta is a reading: nothing was written")
		}
		if rows["engine"] != 0 {
			t.Errorf("engine = %d, want a real zero", rows["engine"])
		}
	})

	t.Run("a failed read reports absence, never a zero", func(t *testing.T) {
		j := &fakeJournal{activeErr: errors.New("query timeout")}
		s := newRowsSampler(j, nil)
		s.sample(context.Background(), rowsTestRuns()) // prime
		if rows, ok := s.sample(context.Background(), rowsTestRuns()); ok {
			t.Errorf("a failed read reported rows %v, want absence", rows)
		}
	})
}

// TestLoadSeriesRowsSum proves the coarse aggregation rule that separates rows
// from load: a coarse rows slot is the SUM of its bucket's ticks, while CPU
// and RSS keep the bucket MAXIMUM. A maximum here would understate a bucket by
// its whole tick count.
func TestLoadSeriesRowsSum(t *testing.T) {
	t.Run("load-series-rows-sum", func(t *testing.T) {
		s := newLoadSeries()
		for _, rows := range []int64{10, 0, 25, 5} {
			s.push(&api.PsLoad{CPUPercent: 4, RSSBytes: 1 << 20}, rows, true)
		}
		s.seal()
		if len(s.coarseRows) != 1 || s.coarseRows[0] != 40 {
			t.Fatalf("coarse rows = %v, want one slot summing to 40", s.coarseRows)
		}
		if len(s.coarseCPU) != 1 || s.coarseCPU[0] != 4 {
			t.Errorf("coarse CPU = %v, want the bucket maximum 4 (unchanged)", s.coarseCPU)
		}
		if s.bucketRows != 0 || s.rowsSampled {
			t.Errorf("seal left the partial bucket at %d sampled %v, want it reset", s.bucketRows, s.rowsSampled)
		}
	})

	t.Run("a bucket the journal never answered for seals absent", func(t *testing.T) {
		s := newLoadSeries()
		s.push(&api.PsLoad{}, 0, false)
		s.push(&api.PsLoad{}, 0, false)
		s.seal()
		if len(s.coarseRows) != 1 || s.coarseRows[0] != api.PsHistoryNoSample {
			t.Errorf("coarse rows = %v, want absence", s.coarseRows)
		}
	})

	t.Run("a bucket that counted zero seals as a real zero", func(t *testing.T) {
		s := newLoadSeries()
		s.push(&api.PsLoad{}, 0, true)
		s.seal()
		if len(s.coarseRows) != 1 || s.coarseRows[0] != 0 {
			t.Errorf("coarse rows = %v, want a real zero", s.coarseRows)
		}
	})

	t.Run("a series that wrote rows but burned no cpu is not dead", func(t *testing.T) {
		s := newLoadSeries()
		s.push(&api.PsLoad{}, 1200, true) // a run too short for the ps probe
		if s.dead() {
			t.Error("a series carrying captured rows must not be retired")
		}
	})
}
