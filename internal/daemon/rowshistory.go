package daemon

// This file is the collector's journal half: the captured-row sampler behind
// the ps readout's rows-over-time bar. Every tick it reads the journal's
// id-delta aggregate -- the same pg.Client.Activity the /journal/activity
// route serves -- and attributes the counted rows to the writing run's
// pipeline and lane, so rows ride the same fine/coarse rings CPU and RSS
// already ride.
//
// The doctrine boundary, stated plainly: the journal holds no clock and gains
// none here. No timestamp column, no time predicate -- the read is the same
// pure `WHERE id > $1` delta, and the sampler holds its own watermark in
// daemon memory. The only clock is the collector's own ticker and the unix
// seal stamp the load buckets already persist, exactly the precedent
// loadhistory.go set. Nothing the engine decides reads any of it.
//
// One asymmetry with load matters: a probe that fails records absence, but a
// journal read that succeeds and returns nothing is a REAL ZERO -- nothing was
// written that tick. Only a failed or unprimed read records absence.

import (
	"context"
	"log/slog"
	"time"

	"github.com/MateusAMP2119/iris-lakehouse/internal/pg"
	"github.com/MateusAMP2119/iris-lakehouse/internal/store"
)

// rowsSampleTimeout bounds one journal read so a slow data database degrades
// the rows series to an absent slot instead of stalling the load tick.
const rowsSampleTimeout = 1500 * time.Millisecond

// journalRowsReader is the rows sampler's data-database seam: the id-delta
// aggregate and the journal's id extent. *pg.Client satisfies it.
type journalRowsReader interface {
	// Activity aggregates the journal entries above sinceID and returns the
	// groups plus the new watermark.
	Activity(ctx context.Context, sinceID int64) ([]pg.ActivityGroup, int64, error)
	// JournalBounds returns the journal's oldest kept id and current watermark.
	JournalBounds(ctx context.Context) (floor, ceiling int64, err error)
}

// compile-time proof the data client satisfies the sampler's seam.
var _ journalRowsReader = (*pg.Client)(nil)

// rowsSampler reads one id-delta per tick and attributes its counted rows to
// the collector's series keys. It holds its own watermark: the journal has no
// clock, so a tick is an id delta, never a time window.
type rowsSampler struct {
	journal journalRowsReader
	logger  *slog.Logger

	primed bool
	since  int64
}

// newRowsSampler builds the journal sampler over the data client. A nil reader
// makes it a permanent no-op (a daemon with no data database), the same
// nil-seam discipline the load persister keeps.
func newRowsSampler(journal journalRowsReader, logger *slog.Logger) *rowsSampler {
	if journal == nil {
		return nil
	}
	return &rowsSampler{journal: journal, logger: logger}
}

// sample returns one tick's captured-row delta keyed by collector series key,
// and false when the journal could not be read (an absent slot, never a
// fabricated zero).
//
// The first call primes the watermark to the journal's current high id and
// reports no sample. Without it a cold start would count the entire journal as
// one tick -- a false multi-million spike at every daemon restart, sealed into
// the coarse ring and persisted forever.
func (s *rowsSampler) sample(ctx context.Context, runs []store.Run) (map[string]int64, bool) {
	if s == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, rowsSampleTimeout)
	defer cancel()

	if !s.primed {
		_, ceiling, err := s.journal.JournalBounds(ctx)
		if err != nil {
			s.debug("rows collector journal prime failed", err)
			return nil, false
		}
		s.since, s.primed = ceiling, true
		return nil, false
	}

	groups, watermark, err := s.journal.Activity(ctx, s.since)
	if err != nil {
		s.debug("rows collector journal read failed", err)
		return nil, false
	}
	s.since = watermark

	// A run row pruned from meta leaves its journal rows unattributable. They
	// still belong in the engine total, so the engine series is a superset of
	// the per-pipeline ones and never the other way round.
	pipelineOf := make(map[int64]store.Run, len(runs))
	for _, run := range runs {
		if id, ok := runIDNumber(run.ID); ok {
			pipelineOf[id] = run
		}
	}
	out := map[string]int64{"engine": 0}
	for _, g := range groups {
		// Captured rows, not net rows: an update and a delete both moved data
		// through the pipeline, and throughput is what this series measures.
		out["engine"] += g.Rows
		run, ok := pipelineOf[g.RunID]
		if !ok {
			continue
		}
		lane := run.Lane
		if lane == "" {
			lane = run.Pipeline
		}
		out["lane:"+lane] += g.Rows
		out["pipeline:"+run.Pipeline] += g.Rows
	}
	return out, true
}

// debug logs a best-effort sampler failure; a nil logger discards it.
func (s *rowsSampler) debug(msg string, err error) {
	if s.logger != nil {
		s.logger.Debug(msg, "err", err)
	}
}

// runIDNumber parses a meta run id into the numeric identity the journal
// records, reporting false for an id that is not one.
func runIDNumber(id string) (int64, bool) {
	var n int64
	if id == "" {
		return 0, false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int64(r-'0')
	}
	return n, true
}
