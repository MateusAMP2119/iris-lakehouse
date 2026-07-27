package tui

// The view's journal-activity state (#238 phase 3): the poller reads
// GET /journal/activity with its held watermark, and folds each delta into
// the accumulated per-table and per-run write aggregates the catalog, the
// statistics pane, and the TABLE view render. The first poll (since_id 0)
// folds the whole journal, so totals are real table history, not view-age
// counters. Everything here is id-watermark arithmetic — no clock.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
)

// psTableWrites is one (schema, table, op) accumulated write aggregate.
type psTableWrites struct {
	Schema       string
	Table        string
	Op           string
	Rows         int64 // accumulated captured writes
	Delta        int64 // rows added by the newest non-empty delta
	MaxID        int64 // the group's journal watermark
	UndoOpen     int64
	UndoPromoted int64
	Writer       string // the latest writing pipeline
}

// psRunWrites is one run's accumulated writes into one table.
type psRunWrites struct {
	Rows         int64
	MinID, MaxID int64
	Op           string
}

// psJournal is the accumulated journal-activity state riding the snapshot.
type psJournal struct {
	Watermark int64
	// Tables is keyed "schema.table|op"; TableKeys orders it for display
	// (ascending name, then op).
	Tables map[string]psTableWrites
	// ByRun is keyed run id, then "schema.table".
	ByRun map[string]map[string]psRunWrites
}

// fetchJournalActivity reads the write-activity aggregate above sinceID.
func (c *Client) fetchJournalActivity(ctx context.Context, sinceID int64) (api.JournalActivity, error) {
	resp, err := c.get(ctx, fmt.Sprintf("/journal/activity?since_id=%d", sinceID))
	if err != nil {
		return api.JournalActivity{}, err
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		return api.JournalActivity{}, fmt.Errorf("daemon returned status %d from /journal/activity", resp.StatusCode)
	}
	var env struct {
		Data api.JournalActivity `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return api.JournalActivity{}, fmt.Errorf("decode /journal/activity response: %w", err)
	}
	return env.Data, nil
}

// foldJournal folds one activity delta into the accumulated state. A nil prev
// starts fresh; the returned state is prev mutated (the poller owns it).
func foldJournal(prev *psJournal, act api.JournalActivity) *psJournal {
	j := prev
	if j == nil {
		j = &psJournal{
			Tables: map[string]psTableWrites{},
			ByRun:  map[string]map[string]psRunWrites{},
		}
	}
	j.Watermark = act.Watermark

	for _, g := range act.Groups {
		name := g.Schema + "." + g.Table
		key := name + "|" + g.Op
		t := j.Tables[key]
		t.Schema, t.Table, t.Op = g.Schema, g.Table, g.Op
		t.Rows += g.Rows
		t.Delta = g.Rows
		if g.MaxID > t.MaxID {
			t.MaxID = g.MaxID
			if g.Pipeline != "" {
				t.Writer = g.Pipeline
			}
		}
		t.UndoOpen += g.UndoOpen
		t.UndoPromoted += g.UndoPromoted
		j.Tables[key] = t

		runID := fmt.Sprintf("%d", g.RunID)
		per := j.ByRun[runID]
		if per == nil {
			per = map[string]psRunWrites{}
			j.ByRun[runID] = per
		}
		w := per[name]
		w.Rows += g.Rows
		if w.MinID == 0 || g.MinID < w.MinID {
			w.MinID = g.MinID
		}
		if g.MaxID > w.MaxID {
			w.MaxID = g.MaxID
		}
		w.Op = g.Op
		per[name] = w
	}

	// Undo states move (open -> promoted/wiped) without new rows; the
	// accumulated split is approximate between full refolds. Honest enough
	// for the pane; the provenance walk stays the exact record.
	return j
}

// tableKeys orders the accumulated table aggregates for display.
func (j *psJournal) tableKeys() []string {
	if j == nil {
		return nil
	}
	keys := make([]string, 0, len(j.Tables))
	for k := range j.Tables {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// tableNames lists the distinct "schema.table" names, ordered.
func (j *psJournal) tableNames() []string {
	if j == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, k := range j.tableKeys() {
		t := j.Tables[k]
		name := t.Schema + "." + t.Table
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// tableWriter is the table's latest writing pipeline ("" unknown).
func (j *psJournal) tableWriter(name string) string {
	if j == nil {
		return ""
	}
	writer, maxID := "", int64(0)
	for _, k := range j.tableKeys() {
		t := j.Tables[k]
		if t.Schema+"."+t.Table == name && t.MaxID >= maxID && t.Writer != "" {
			writer, maxID = t.Writer, t.MaxID
		}
	}
	return writer
}

// tableTotals sums a table's aggregates across ops: rows, watermark, undo split.
func (j *psJournal) tableTotals(name string) (rows, maxID, undoOpen, undoPromoted int64) {
	if j == nil {
		return 0, 0, 0, 0
	}
	for _, k := range j.tableKeys() {
		t := j.Tables[k]
		if t.Schema+"."+t.Table != name {
			continue
		}
		switch t.Op {
		case "delete":
			rows -= t.Rows
		default:
			rows += t.Rows
		}
		if t.MaxID > maxID {
			maxID = t.MaxID
		}
		undoOpen += t.UndoOpen
		undoPromoted += t.UndoPromoted
	}
	return rows, maxID, undoOpen, undoPromoted
}

// runWrote sums one run's captured writes across tables.
func (j *psJournal) runWrote(runID string) int64 {
	if j == nil {
		return 0
	}
	var total int64
	for _, w := range j.ByRun[runID] {
		total += w.Rows
	}
	return total
}

// runWrite is one run's captured writes into one table (false when none, or
// when no activity aggregate has landed yet).
func (j *psJournal) runWrite(runID, table string) (psRunWrites, bool) {
	if j == nil {
		return psRunWrites{}, false
	}
	w, ok := j.ByRun[runID][table]
	return w, ok
}

// runRange is one run's overall journal id range (0,0 when it wrote nothing).
func (j *psJournal) runRange(runID string) (minID, maxID int64) {
	if j == nil {
		return 0, 0
	}
	for _, w := range j.ByRun[runID] {
		if minID == 0 || w.MinID < minID {
			minID = w.MinID
		}
		if w.MaxID > maxID {
			maxID = w.MaxID
		}
	}
	return minID, maxID
}
