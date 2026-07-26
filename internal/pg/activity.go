package pg

// The journal activity aggregate (#238 phase 3): one GROUP BY over
// public.data_journal above a caller-held id watermark. The journal holds no
// clock — id is the monotonic identity — so activity is always an id-delta
// between polls, never a time window.

import (
	"context"
	"errors"
	"fmt"
)

// ActivityGroup is one (run, schema, table, op) aggregate of the delta.
type ActivityGroup struct {
	RunID        int64
	Schema       string
	Table        string
	Op           string
	Rows         int64
	MinID        int64
	MaxID        int64
	UndoOpen     int64
	UndoPromoted int64
}

// activitySQL aggregates the delta above the watermark, ascending group
// ceiling so the caller folds groups in journal order.
const activitySQL = `
SELECT run_id, "schema", "table", op, count(*), min(id), max(id),
       count(*) FILTER (WHERE undo = 'open'),
       count(*) FILTER (WHERE undo = 'promoted')
FROM public.data_journal
WHERE id > $1
GROUP BY run_id, "schema", "table", op
ORDER BY max(id)`

// Activity aggregates the journal entries with id > sinceID and returns the
// groups plus the journal's current watermark (sinceID when nothing new).
func (c *Client) Activity(ctx context.Context, sinceID int64) ([]ActivityGroup, int64, error) {
	if c.pool == nil {
		return nil, 0, errors.New("pg: closed")
	}
	rows, err := c.pool.Query(ctx, activitySQL, sinceID)
	if err != nil {
		return nil, 0, fmt.Errorf("pg: query journal activity since %d: %w", sinceID, err)
	}
	defer rows.Close()

	var out []ActivityGroup
	watermark := sinceID
	for rows.Next() {
		var g ActivityGroup
		if err := rows.Scan(&g.RunID, &g.Schema, &g.Table, &g.Op, &g.Rows, &g.MinID, &g.MaxID, &g.UndoOpen, &g.UndoPromoted); err != nil {
			return nil, 0, fmt.Errorf("pg: scan journal activity: %w", err)
		}
		if g.MaxID > watermark {
			watermark = g.MaxID
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("pg: iterate journal activity: %w", err)
	}
	return out, watermark, nil
}
