package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/pg"
	"github.com/MateusAMP2119/iris-lakehouse/internal/store"
)

// This file is the daemon's load collector: the continuous sampler behind the
// ps readout's load fields and its recorded history. Every tick it takes one
// host-load probe (loadprobe.go) and one plain-MVCC run snapshot, attributes
// the sampled process groups to the engine and to each running run's lane and
// pipeline, and pushes one slot into every entity's in-memory history ring.
// The rings live in daemon memory only -- no table, no file -- so the history
// survives any number of `iris ps` clients coming and going and dies with the
// daemon; that is deliberate (memory-first, persistence can follow if daemon
// restarts prove it worth a schema).
//
// Retention is tiered: a fine ring holds one slot per tick (minutes of recent
// detail), and a coarse ring holds one slot per aggregation bucket carrying
// the bucket's MAXIMUM fine sample (a day of history where a short spike stays
// visible instead of averaging away). Every live series is pushed in lockstep
// each tick, so series of different ages align from their ends. Sampling is
// best-effort by the probe's own contract: a failed probe records an absent
// slot, never a fabricated zero.

const (
	// loadSampleInterval is the collector's tick: one probe and one run
	// snapshot per interval. Coarser than the live view's 1s poll on purpose --
	// the collector runs for the daemon's whole life, attached clients or not.
	loadSampleInterval = 2 * time.Second
	// loadFineRingCap bounds the fine ring: at the 2s tick this holds 10
	// minutes of full-resolution history.
	loadFineRingCap = 300
	// loadCoarseBucketTicks is the aggregation bucket in ticks: 30 ticks at 2s
	// seal one 60-second bucket.
	loadCoarseBucketTicks = 30
	// loadCoarseRingCap bounds the coarse ring: at 60s buckets this holds a full
	// day of history. It is the readout's ceiling, not its window -- a strip
	// spans whatever has accumulated, growing up to this depth and then rolling.
	loadCoarseRingCap = 1440
	// loadPersistRetention bounds the persisted history: buckets older than
	// this are pruned. Wider than the ring so the table stays SQL-queryable
	// past what the readout renders.
	loadPersistRetention = 7 * 24 * time.Hour
	// loadPersistPruneSeals spaces the retention prune: once per this many
	// seals (about hourly at 60s buckets) rather than every seal.
	loadPersistPruneSeals = 60
	// loadSeedWindow is how far back the seeding read replays at start: the
	// coarse ring's own depth, no more.
	loadSeedWindow = loadSampleInterval * loadCoarseBucketTicks * loadCoarseRingCap
)

// loadBucketSeconds is one coarse bucket's span in seconds, the step the
// seeding read sizes downtime gaps with.
const loadBucketSeconds = int64(loadSampleInterval/time.Second) * loadCoarseBucketTicks

// loadPersister is the collector's persistence seam: the data-database client
// the sealed coarse buckets write through, read back from at start, and prune
// on the retention window. *pg.Client satisfies it; nil keeps the collector
// memory-only (tests, or a data database whose ensure failed).
type loadPersister interface {
	// WriteLoadBuckets persists one seal's buckets for node.
	WriteLoadBuckets(ctx context.Context, node string, buckets []pg.LoadBucket) error
	// ReadLoadHistory reads node's buckets sealed at or after since, in bucket order.
	ReadLoadHistory(ctx context.Context, node string, since int64) ([]pg.LoadBucket, error)
	// PruneLoadHistory deletes every node's buckets sealed before the cutoff.
	PruneLoadHistory(ctx context.Context, before int64) error
}

// compile-time proof the data client satisfies the collector's persistence seam.
var _ loadPersister = (*pg.Client)(nil)

// loadSeries is one entity's recorded history: the fine ring, the coarse ring,
// and the running partial bucket (the per-bucket maxima accumulated since the
// last seal). Slots with no sample carry api.PsHistoryNoSample CPU and zero
// RSS.
type loadSeries struct {
	cpu       []float64
	rss       []int64
	coarseCPU []float64
	coarseRSS []int64
	bucketCPU float64
	bucketRSS int64
	// rows is the captured-row history: journal rows counted in each tick's id
	// delta. Its coarse slots carry the bucket's SUM, not its maximum -- rows
	// are additive, and a maximum would understate an hour by the bucket's
	// tick count. Absence is api.PsHistoryNoSample; a successful read counting
	// nothing is a real zero.
	rows        []int64
	coarseRows  []int64
	bucketRows  int64
	rowsSampled bool
}

// newLoadSeries builds an empty series with an all-absent partial bucket.
func newLoadSeries() *loadSeries {
	return &loadSeries{bucketCPU: api.PsHistoryNoSample}
}

// tickSample is one tick's whole attributed reading: the host probe's
// verdict and process attribution, plus the journal's row delta. It is a
// struct so the collector can grow series families without record() growing
// another parameter.
type tickSample struct {
	probed bool                   // the host answered
	rowsOK bool                   // the journal answered
	engine *api.PsLoad            // the engine tree's summed load
	groups map[int]*api.PsLoad    // per-process-group load
	entity map[string]*api.PsLoad // per lane and pipeline load
	rows   map[string]int64       // per-series captured rows this tick
}

// push appends one tick's slot (nil for no sample) to the fine ring and folds
// it into the partial bucket's maxima.
func (s *loadSeries) push(l *api.PsLoad, rows int64, rowsOK bool) {
	cpu, rss := float64(api.PsHistoryNoSample), int64(0)
	if l != nil {
		cpu, rss = l.CPUPercent, l.RSSBytes
	}
	s.cpu = append(s.cpu, cpu)
	s.rss = append(s.rss, rss)
	slot := int64(api.PsHistoryNoSample)
	if rowsOK {
		slot = rows
	}
	s.rows = append(s.rows, slot)
	if len(s.cpu) > loadFineRingCap {
		s.cpu = s.cpu[len(s.cpu)-loadFineRingCap:]
		s.rss = s.rss[len(s.rss)-loadFineRingCap:]
	}
	if len(s.rows) > loadFineRingCap {
		s.rows = s.rows[len(s.rows)-loadFineRingCap:]
	}
	if l != nil {
		if s.bucketCPU == api.PsHistoryNoSample || cpu > s.bucketCPU {
			s.bucketCPU = cpu
		}
		if rss > s.bucketRSS {
			s.bucketRSS = rss
		}
	}
	// Rows accumulate: the bucket is the sum of its ticks, so one seal is the
	// rows captured over the bucket's whole span.
	if rowsOK {
		s.bucketRows += rows
		s.rowsSampled = true
	}
}

// seal closes the partial bucket into the coarse ring and starts a fresh one.
// A bucket that saw no sample seals as an absent slot.
func (s *loadSeries) seal() {
	s.coarseCPU = append(s.coarseCPU, s.bucketCPU)
	s.coarseRSS = append(s.coarseRSS, s.bucketRSS)
	rows := int64(api.PsHistoryNoSample)
	if s.rowsSampled {
		rows = s.bucketRows
	}
	s.coarseRows = append(s.coarseRows, rows)
	if len(s.coarseCPU) > loadCoarseRingCap {
		s.coarseCPU = s.coarseCPU[len(s.coarseCPU)-loadCoarseRingCap:]
		s.coarseRSS = s.coarseRSS[len(s.coarseRSS)-loadCoarseRingCap:]
	}
	if len(s.coarseRows) > loadCoarseRingCap {
		s.coarseRows = s.coarseRows[len(s.coarseRows)-loadCoarseRingCap:]
	}
	s.bucketCPU, s.bucketRSS = api.PsHistoryNoSample, 0
	s.bucketRows, s.rowsSampled = 0, false
}

// dead reports whether the series holds nothing worth keeping: no positive
// sample anywhere across the fine ring, the coarse ring, and the partial
// bucket. Absent and zero both count as nothing -- an idle entity records real
// zeros, so absence alone would never retire a series again. A dead series is
// an entity idle past the whole retention window; keeping it would grow the map
// forever.
func (s *loadSeries) dead() bool {
	for i, c := range s.cpu {
		if c > 0 || s.rss[i] > 0 {
			return false
		}
	}
	for i, c := range s.coarseCPU {
		if c > 0 || s.coarseRSS[i] > 0 {
			return false
		}
	}
	// Rows are scanned on their own rather than beside the load slots: push
	// and seal keep the rings in lockstep, but dead() must not assume it of a
	// series assembled any other way.
	for _, r := range s.rows {
		if r > 0 {
			return false
		}
	}
	for _, r := range s.coarseRows {
		if r > 0 {
			return false
		}
	}
	return s.bucketCPU <= 0 && s.bucketRSS == 0 && s.bucketRows <= 0
}

// loadHistory is the collector: the probe and run-snapshot seams it samples
// through, and the mutex-guarded state the ps plane reads (the latest sample
// for the live load fields, the series map for ?history=1). The production
// collector runs as one goroutine for the daemon's life; tests drive sample()
// directly.
type loadHistory struct {
	probe     loadProber
	rows      *rowsSampler
	runs      RunSnapshotReader
	managedPG func() int
	persist   loadPersister
	node      string
	logger    *slog.Logger
	pid       int

	mu          sync.Mutex
	tick        uint64
	bucketTicks int
	seals       int
	engineLoad  *api.PsLoad
	groupLoad   map[int]*api.PsLoad
	series      map[string]*loadSeries
}

// newLoadHistory builds the collector over the run reader, the managed
// postmaster locator (nil for none), and the persistence seam (nil for
// memory-only), probing through the production ps(1) probe. The node identity
// is the sampling host's name -- ps(1) load is a per-host truth, so the
// persisted rows carry it. A nil logger discards output.
func newLoadHistory(runs RunSnapshotReader, managedPG func() int, persist loadPersister, rows *rowsSampler, logger *slog.Logger) *loadHistory {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if managedPG == nil {
		managedPG = func() int { return 0 }
	}
	node, err := os.Hostname()
	if err != nil || node == "" {
		node = "unknown"
	}
	return &loadHistory{
		probe:     psProbe{},
		rows:      rows,
		runs:      runs,
		managedPG: managedPG,
		persist:   persist,
		node:      node,
		logger:    logger,
		pid:       os.Getpid(),
		series:    map[string]*loadSeries{},
	}
}

// run is the collector's goroutine: the persisted-history seed, an immediate
// first sample (the first readout should not wait a full interval), then one
// per tick until ctx ends.
func (h *loadHistory) run(ctx context.Context) {
	h.seed(ctx)
	h.sample(ctx)
	t := time.NewTicker(loadSampleInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.sample(ctx)
		}
	}
}

// seed replays the persisted coarse buckets into the coarse rings, so a
// restarted daemon opens with its recorded history instead of a blank window
// (the whole point of persisting: engine restarts -- updates, crashes,
// reboots -- stop truncating the readout). Buckets arrive in seal order;
// missing steps between them, and the downtime gap between the newest bucket
// and now, fill as absent slots so the time axis stays honest. Best-effort by
// contract: a failed read logs at debug and the collector starts blank.
func (h *loadHistory) seed(ctx context.Context) {
	if h.persist == nil {
		return
	}
	now := time.Now().Unix()
	rows, err := h.persist.ReadLoadHistory(ctx, h.node, now-int64(loadSeedWindow/time.Second))
	if err != nil {
		h.logger.Debug("load collector history seed failed", "err", err)
		return
	}
	bySeries := map[string][]pg.LoadBucket{}
	for _, b := range rows {
		bySeries[b.Series] = append(bySeries[b.Series], b)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, buckets := range bySeries {
		s := newLoadSeries()
		prev := int64(0)
		absent := func(n int64) {
			for ; n > 0 && int64(len(s.coarseCPU)) < loadCoarseRingCap; n-- {
				s.coarseCPU = append(s.coarseCPU, api.PsHistoryNoSample)
				s.coarseRSS = append(s.coarseRSS, 0)
				s.coarseRows = append(s.coarseRows, api.PsHistoryNoSample)
			}
		}
		for _, b := range buckets {
			if prev != 0 {
				absent((b.Bucket-prev)/loadBucketSeconds - 1)
			}
			cpu, rss := float64(api.PsHistoryNoSample), int64(0)
			if b.Sampled {
				cpu, rss = b.CPUMax, b.RSSMax
			}
			rows := int64(api.PsHistoryNoSample)
			if b.RowsSampled {
				rows = b.RowsSum
			}
			s.coarseCPU = append(s.coarseCPU, cpu)
			s.coarseRSS = append(s.coarseRSS, rss)
			s.coarseRows = append(s.coarseRows, rows)
			prev = b.Bucket
		}
		if prev != 0 {
			absent((now - prev) / loadBucketSeconds) // the downtime gap reaches the present
		}
		if len(s.coarseCPU) > loadCoarseRingCap {
			s.coarseCPU = s.coarseCPU[len(s.coarseCPU)-loadCoarseRingCap:]
			s.coarseRSS = s.coarseRSS[len(s.coarseRSS)-loadCoarseRingCap:]
			s.coarseRows = s.coarseRows[len(s.coarseRows)-loadCoarseRingCap:]
		}
		h.series[key] = s
	}
}

// sample takes one tick: probe the host, snapshot the runs, attribute the
// process groups (engine tree; running runs' groups summed under their lane
// and pipeline), and push one slot into every live series. Both reads are
// best-effort: a failed probe records the tick with absent slots everywhere, a
// failed run snapshot records the engine but no lane or pipeline attribution.
func (h *loadHistory) sample(ctx context.Context) {
	var engine *api.PsLoad
	var snapshot []store.Run
	groups := map[int]*api.PsLoad{}
	entity := map[string]*api.PsLoad{}
	samples, err := h.probe.Sample(ctx)
	probed := err == nil
	if err != nil {
		h.logger.Debug("load collector host probe failed", "err", err)
	} else {
		for _, s := range samples {
			l := groups[s.PGID]
			if l == nil {
				l = &api.PsLoad{}
				groups[s.PGID] = l
			}
			l.CPUPercent += s.CPUPercent
			l.RSSBytes += s.RSSBytes
		}
		engine = sumTrees(samples, h.pid, h.managedPG())
		if runs, rerr := h.runs.Runs(ctx, store.RunFilter{}); rerr != nil {
			h.logger.Debug("load collector run snapshot failed", "err", rerr)
		} else {
			snapshot = runs
			accumulate := func(key string, l *api.PsLoad) {
				e := entity[key]
				if e == nil {
					e = &api.PsLoad{}
					entity[key] = e
				}
				e.CPUPercent += l.CPUPercent
				e.RSSBytes += l.RSSBytes
			}
			for _, run := range runs {
				if run.State != store.RunRunning || run.Handle == 0 {
					continue
				}
				l := groups[run.Handle]
				if l == nil {
					continue
				}
				lane := run.Lane
				if lane == "" {
					lane = run.Pipeline
				}
				accumulate("lane:"+lane, l)
				accumulate("pipeline:"+run.Pipeline, l)
			}
		}
	}

	// The journal read reuses the run snapshot the probe block already took --
	// one meta read per tick, not two.
	rows, rowsOK := h.rows.sample(ctx, snapshot)

	sealed, prune := h.record(tickSample{
		probed: probed, rowsOK: rowsOK,
		engine: engine, groups: groups, entity: entity, rows: rows,
	})

	// Persistence rides after the lock: one best-effort write per seal, and
	// the retention prune on its own sparser cadence. A failed write loses at
	// most one bucket row; the memory rings are untouched either way.
	if h.persist == nil || len(sealed) == 0 {
		return
	}
	if err := h.persist.WriteLoadBuckets(ctx, h.node, sealed); err != nil {
		h.logger.Debug("load collector history write failed", "err", err)
	}
	if prune {
		if err := h.persist.PruneLoadHistory(ctx, time.Now().Add(-loadPersistRetention).Unix()); err != nil {
			h.logger.Debug("load collector history prune failed", "err", err)
		}
	}
}

// record takes one tick's attributed sample under the lock: the tick advances,
// every live series takes exactly one slot (lockstep, so all series end at
// this tick and align from their ends), and a full bucket seals. probed says
// the host answered, which is what separates an idle lane (a real zero: nothing
// was running, so nothing burned) from an unknowable one (an absent slot). It
// returns the sealed buckets for persistence (nil between seals) and whether
// this seal is a prune tick.
func (h *loadHistory) record(ts tickSample) (sealed []pg.LoadBucket, prune bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tick++
	h.engineLoad = ts.engine
	h.groupLoad = ts.groups
	ensure := func(key string) {
		if h.series[key] == nil {
			h.series[key] = newLoadSeries()
		}
	}
	ensure("engine")
	for key := range ts.entity {
		ensure(key)
	}
	// A pipeline whose runs are too short to catch the ps probe still writes
	// rows, so the journal's keys mint series of their own.
	for key := range ts.rows {
		ensure(key)
	}
	idle := (*api.PsLoad)(nil)
	if ts.probed {
		idle = &api.PsLoad{}
	}
	for key, s := range h.series {
		rows := ts.rows[key] // a key the delta did not name captured no rows
		if key == "engine" {
			s.push(ts.engine, rows, ts.rowsOK)
			continue
		}
		if l := ts.entity[key]; l != nil {
			s.push(l, rows, ts.rowsOK)
		} else {
			s.push(idle, rows, ts.rowsOK)
		}
	}
	h.bucketTicks++
	if h.bucketTicks >= loadCoarseBucketTicks {
		h.bucketTicks = 0
		at := time.Now().Unix()
		for key, s := range h.series {
			// The partial's maxima are collected before seal() resets them.
			sealed = append(sealed, pg.LoadBucket{
				Series:      key,
				Bucket:      at,
				CPUMax:      max(s.bucketCPU, 0),
				RSSMax:      s.bucketRSS,
				Sampled:     s.bucketCPU != api.PsHistoryNoSample,
				RowsSum:     s.bucketRows,
				RowsSampled: s.rowsSampled,
			})
			s.seal()
			if key != "engine" && s.dead() {
				delete(h.series, key)
			}
		}
		h.seals++
		prune = h.seals%loadPersistPruneSeals == 0
	}
	return sealed, prune
}

// latest returns the newest sample: the tick counter, the engine load, and the
// per-process-group sums. The returned values are replaced wholesale each tick
// and never mutated after, so callers may hold and marshal them without
// copying -- read-only by contract. A nil collector reads as never sampled.
func (h *loadHistory) latest() (tick uint64, engine *api.PsLoad, groups map[int]*api.PsLoad) {
	if h == nil {
		return 0, nil, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tick, h.engineLoad, h.groupLoad
}

// snapshot renders the recorded history as the wire document: a deep copy of
// every series, the running partial bucket appended as the coarse ring's
// newest slot so the coarse grid reaches the present. Series order is
// unspecified (a map walk); clients key on Series.Key. A nil collector or one
// that never ticked reads as nil -- absent on the wire.
func (h *loadHistory) snapshot() *api.PsHistory {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tick == 0 {
		return nil
	}
	doc := &api.PsHistory{
		FineIntervalSeconds:   int(loadSampleInterval / time.Second),
		CoarseIntervalSeconds: int(loadSampleInterval/time.Second) * loadCoarseBucketTicks,
	}
	for key, s := range h.series {
		series := api.PsSeries{
			Key:        key,
			CPU:        append([]float64(nil), s.cpu...),
			RSS:        append([]int64(nil), s.rss...),
			Rows:       append([]int64(nil), s.rows...),
			CoarseCPU:  append([]float64(nil), s.coarseCPU...),
			CoarseRSS:  append([]int64(nil), s.coarseRSS...),
			CoarseRows: append([]int64(nil), s.coarseRows...),
		}
		if h.bucketTicks > 0 {
			series.CoarseCPU = append(series.CoarseCPU, s.bucketCPU)
			series.CoarseRSS = append(series.CoarseRSS, s.bucketRSS)
			partial := int64(api.PsHistoryNoSample)
			if s.rowsSampled {
				partial = s.bucketRows
			}
			series.CoarseRows = append(series.CoarseRows, partial)
		}
		doc.Series = append(doc.Series, series)
	}
	return doc
}
