package api

// GET /journal/activity — the #238 phase 3 aggregate over public.data_journal:
// which tables are being written, by which runs, grouped by (run, schema,
// table, op). Polled with since_id so each poll reads only the delta; the
// journal holds no clock, so activity is an id-watermark delta between polls,
// never a time window. It is a read, served on any role.

import (
	"context"
	"errors"
	"net/http"
	"strconv"
)

// JournalActivityHandler answers the journal activity aggregate.
type JournalActivityHandler interface {
	// JournalActivity aggregates journal entries with id > sinceID.
	JournalActivity(ctx context.Context, sinceID int64) (JournalActivity, error)
}

// JournalActivity is the aggregate readout: the journal's id watermark and
// the delta's write groups. A poll with nothing new answers the caller's own
// watermark and no groups.
type JournalActivity struct {
	// Watermark is max(id) over the whole journal after this delta — the
	// caller's next since_id. Identity, never a clock.
	Watermark int64 `json:"watermark"`
	// Groups are the delta's write aggregates, ascending max entry id.
	Groups []JournalActivityGroup `json:"groups,omitempty"`
}

// JournalActivityGroup is one (run, schema, table, op) write aggregate.
type JournalActivityGroup struct {
	// RunID is the writing run's meta id.
	RunID int64 `json:"run_id"`
	// Pipeline is the writing run's pipeline, "" when the run row is gone.
	Pipeline string `json:"pipeline,omitempty"`
	// Schema and Table name the written table.
	Schema string `json:"schema"`
	Table  string `json:"table"`
	// Op is the captured write kind: insert, update, or delete.
	Op string `json:"op"`
	// Rows counts the delta's captured writes in this group.
	Rows int64 `json:"rows"`
	// MinID and MaxID are the group's journal id range within the delta.
	MinID int64 `json:"min_id"`
	MaxID int64 `json:"max_id"`
	// UndoOpen and UndoPromoted split the group by undo state.
	UndoOpen     int64 `json:"undo_open"`
	UndoPromoted int64 `json:"undo_promoted"`
}

// WithJournalActivity wires the journal activity handler.
func WithJournalActivity(h JournalActivityHandler) MuxOption {
	return func(m *mux) {
		if h != nil {
			m.jactivity = h
		}
	}
}

// ErrJournalActivityUnavailable is returned by the default (unwired) handler.
var ErrJournalActivityUnavailable = errors.New("api: journal activity not available")

// noJournalActivity is the default before wiring.
type noJournalActivity struct{}

func (noJournalActivity) JournalActivity(context.Context, int64) (JournalActivity, error) {
	return JournalActivity{}, ErrJournalActivityUnavailable
}

// serveJournalActivity handles GET /journal/activity[?since_id=N].
func (m *mux) serveJournalActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET "+r.URL.Path+" only")
		return
	}
	q := r.URL.Query()
	for k := range q {
		if k != "since_id" {
			WriteError(w, http.StatusBadRequest, "bad_param", "unknown query parameter: "+k)
			return
		}
	}
	sinceID := int64(0)
	if raw := q.Get("since_id"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			WriteError(w, http.StatusBadRequest, "bad_param", "since_id must be a non-negative integer")
			return
		}
		sinceID = n
	}
	activity, err := m.jactivity.JournalActivity(r.Context(), sinceID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	WriteData(w, http.StatusOK, activity)
}
