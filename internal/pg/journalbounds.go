package pg

// The journal's id extent. Identity only: the journal holds no clock, so its
// bounds are the oldest entry retention and compaction still keep and the
// current high-water id -- never a time range. data_journal is range
// partitioned by id, so both are index probes rather than a scan.

import (
	"context"
	"errors"
	"fmt"
)

// journalBoundsSQL reads the journal's id extent. An empty journal reads as
// (0, 0), so a caller can tell it apart from a journal starting at id 1.
const journalBoundsSQL = `SELECT coalesce(min(id), 0), coalesce(max(id), 0) FROM public.data_journal`

// JournalBounds returns the journal's oldest kept id and its current
// watermark. It serves two callers: the rows collector's priming read (a cold
// start must not count the whole journal as one bucket) and the retention
// readout's floor.
func (c *Client) JournalBounds(ctx context.Context) (floor, ceiling int64, err error) {
	if c.pool == nil {
		return 0, 0, errors.New("pg: closed")
	}
	if err := c.pool.QueryRow(ctx, journalBoundsSQL).Scan(&floor, &ceiling); err != nil {
		return 0, 0, fmt.Errorf("pg: read journal bounds: %w", err)
	}
	return floor, ceiling, nil
}
