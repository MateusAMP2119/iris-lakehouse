package daemon

// This file is the daemon's journal-activity plane: the
// api.JournalActivityHandler behind GET /journal/activity (#238 phase 3). It
// runs the pg aggregate for the caller's watermark delta and names each
// group's writing pipeline from the meta run rows — the journal knows only
// run ids; a run row pruned from meta leaves the pipeline empty rather than
// guessed. It is a read, served on any role that holds a data pool.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/pg"
	"github.com/MateusAMP2119/iris-lakehouse/internal/store"
)

// activityReader is the pg seam: the data client satisfies it.
type activityReader interface {
	Activity(ctx context.Context, sinceID int64) ([]pg.ActivityGroup, int64, error)
}

// journalPlane implements api.JournalActivityHandler.
type journalPlane struct {
	data   activityReader
	runs   store.Reader
	logger *slog.Logger
}

// compile-time proof.
var _ api.JournalActivityHandler = (*journalPlane)(nil)

// NewJournalActivityPlane wires the journal activity handler. A nil logger
// discards.
func NewJournalActivityPlane(data activityReader, runs store.Reader, logger *slog.Logger) api.JournalActivityHandler {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &journalPlane{data: data, runs: runs, logger: logger}
}

// JournalActivity aggregates the delta and maps run ids to pipelines.
func (p *journalPlane) JournalActivity(ctx context.Context, sinceID int64) (api.JournalActivity, error) {
	groups, watermark, err := p.data.Activity(ctx, sinceID)
	if err != nil {
		p.logger.Error("journal activity read failed", "since_id", sinceID, "err", err)
		return api.JournalActivity{}, fmt.Errorf("daemon: journal activity since %d: %w", sinceID, err)
	}

	out := api.JournalActivity{Watermark: watermark}
	if len(groups) == 0 {
		return out, nil
	}

	pipes, err := p.runPipelines(ctx)
	if err != nil {
		p.logger.Error("journal activity run mapping failed", "err", err)
		return api.JournalActivity{}, fmt.Errorf("daemon: journal activity run mapping: %w", err)
	}

	out.Groups = make([]api.JournalActivityGroup, 0, len(groups))
	for _, g := range groups {
		out.Groups = append(out.Groups, api.JournalActivityGroup{
			RunID:        g.RunID,
			Pipeline:     pipes[g.RunID],
			Schema:       g.Schema,
			Table:        g.Table,
			Op:           g.Op,
			Rows:         g.Rows,
			MinID:        g.MinID,
			MaxID:        g.MaxID,
			UndoOpen:     g.UndoOpen,
			UndoPromoted: g.UndoPromoted,
		})
	}
	return out, nil
}

// runPipelines maps meta run ids to their pipelines for group naming.
func (p *journalPlane) runPipelines(ctx context.Context) (map[int64]string, error) {
	runs, err := p.runs.Runs(ctx, store.RunFilter{})
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(runs))
	for _, r := range runs {
		id, err := strconv.ParseInt(r.ID, 10, 64)
		if err != nil {
			continue // a non-numeric id can never appear in the journal's bigint run_id
		}
		out[id] = r.Pipeline
	}
	return out, nil
}
