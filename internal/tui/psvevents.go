package tui

// The events digest (#238 phase 4): the engine-wide "what happened while I
// was not looking" pane. Events are DERIVED, never stored and never parsed
// from log text: the poller diffs successive snapshots (run states, source
// health, leadership role) and reads the journal activity delta (commits).
// The stamp is the moment this view observed the change — client display,
// like every rendered log line; the engine still holds no clock.

import (
	"fmt"
	"sort"
	"time"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
)

// psEventSeverity classifies an event row's glyph.
type psEventSeverity int

const (
	psEvInfo psEventSeverity = iota // ·
	psEvCommit                      // ●
	psEvOK                          // ✔
	psEvFail                        // ✖
)

// glyph is the severity's one-cell marker and its SGR.
func (s psEventSeverity) glyph() (string, string) {
	switch s {
	case psEvCommit:
		return "●", ansiCyan
	case psEvOK:
		return "✔", ansiGreen
	case psEvFail:
		return "✖", ansiRed
	default:
		return "·", ansiDim
	}
}

// psEvent is one digest row.
type psEvent struct {
	Stamp    string // observed-at, HH:MM:SS, client display
	Severity psEventSeverity
	Text     string
	Pipeline string // for the filter; may be empty
}

// psEventsCap bounds the accumulated digest.
const psEventsCap = 500

// deriveEvents diffs one poll against the previous snapshot and the activity
// delta, returning the new digest rows oldest first. A nil prev yields
// nothing: the first poll is a baseline, not a burst of stale "news".
func deriveEvents(prev *Snapshot, next Snapshot, delta []api.JournalActivityGroup, stamp string) []psEvent {
	if prev == nil {
		return nil
	}
	var out []psEvent
	ev := func(sev psEventSeverity, pipeline, text string) {
		out = append(out, psEvent{Stamp: stamp, Severity: sev, Text: text, Pipeline: pipeline})
	}

	// Run state changes: appearance and transitions, terminal states loudest.
	prevRuns := map[string]api.PsRun{}
	for _, r := range prev.Ps.Runs {
		prevRuns[r.ID] = r
	}
	for _, r := range next.Ps.Runs {
		was, seen := prevRuns[r.ID]
		if seen && was.State == r.State {
			continue
		}
		id := r.Pipeline + "/" + r.ID
		switch r.State {
		case "running":
			ev(psEvInfo, r.Pipeline, id+" running")
		case "queued":
			if !seen {
				ev(psEvInfo, r.Pipeline, id+" queued")
			}
		case "succeeded":
			text := id + " succeeded"
			if r.Duration != "" {
				text += " · " + r.Duration
			}
			ev(psEvOK, r.Pipeline, text)
		case "dead_lettered":
			text := id + " dead-lettered"
			if r.ExitCode != nil {
				text += fmt.Sprintf(" · exit %d", *r.ExitCode)
			}
			ev(psEvFail, r.Pipeline, text)
		}
	}

	// Journal commits: one event per writing run in the delta, tables folded.
	type commit struct {
		pipeline string
		rows     int64
		tables   map[string]bool
	}
	byRun := map[int64]*commit{}
	var runOrder []int64
	for _, g := range delta {
		c := byRun[g.RunID]
		if c == nil {
			c = &commit{pipeline: g.Pipeline, tables: map[string]bool{}}
			byRun[g.RunID] = c
			runOrder = append(runOrder, g.RunID)
		}
		if g.Op == "delete" {
			c.rows -= g.Rows
		} else {
			c.rows += g.Rows
		}
		c.tables[g.Schema+"."+g.Table] = true
	}
	sort.Slice(runOrder, func(i, j int) bool { return runOrder[i] < runOrder[j] })
	for _, id := range runOrder {
		c := byRun[id]
		tables := make([]string, 0, len(c.tables))
		for t := range c.tables {
			tables = append(tables, t)
		}
		sort.Strings(tables)
		label := tables[0]
		if len(tables) > 1 {
			label = fmt.Sprintf("%s +%d more", tables[0], len(tables)-1)
		}
		who := c.pipeline
		if who == "" {
			who = fmt.Sprintf("run %d", id)
		}
		ev(psEvCommit, c.pipeline, fmt.Sprintf("%s committed %+d rows → %s", who, c.rows, label))
	}

	// Source health changes: a new failure streak, and recovery.
	prevSrc := map[string]api.SourceHealth{}
	for _, sh := range prev.Ps.Sources {
		prevSrc[sh.Pipeline] = sh
	}
	for _, sh := range next.Ps.Sources {
		was := prevSrc[sh.Pipeline]
		switch {
		case sh.ConsecutiveFails > 0 && sh.ConsecutiveFails != was.ConsecutiveFails:
			ev(psEvFail, sh.Pipeline, fmt.Sprintf("source %s failing ×%d · %s", sh.Pipeline, sh.ConsecutiveFails, sh.Error))
		case sh.ConsecutiveFails == 0 && was.ConsecutiveFails > 0:
			ev(psEvOK, sh.Pipeline, fmt.Sprintf("source %s recovered · %d", sh.Pipeline, sh.Status))
		}
	}

	// Leadership: role changes are engine news.
	if prev.Ps.Engine.Role != next.Ps.Engine.Role {
		ev(psEvInfo, "", "engine role "+prev.Ps.Engine.Role+" → "+next.Ps.Engine.Role)
	}

	return out
}

// foldEvents appends new digest rows, capped, newest last.
func foldEvents(acc, fresh []psEvent) []psEvent {
	acc = append(acc, fresh...)
	if len(acc) > psEventsCap {
		acc = acc[len(acc)-psEventsCap:]
	}
	return acc
}

// eventStamp renders the observed-at stamp for this poll.
func eventStamp(t time.Time) string { return t.Format("15:04:05") }
