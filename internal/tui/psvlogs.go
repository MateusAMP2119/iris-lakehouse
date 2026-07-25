package tui

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// This file renders the logs pane's naturalized capture tail readable: raw
// protocol frames ([engine]/[pipeline] JSON, #206) become compact one-liners
// and consecutive row frames of one origin and table fold into a count line.
// Anything unparseable passes through verbatim, so nothing captured is hidden;
// the exact wire bytes stay reachable via `iris run logs --frames`.

// captureFrame is the superset of one naturalized line's JSON payload: the
// turn-protocol frame fields plus the #| stamp fields ([iris] lines).
type captureFrame struct {
	Event  string          `json:"event"`
	Turn   *int64          `json:"turn"`
	Table  string          `json:"table"`
	Row    json.RawMessage `json:"row"`
	Reason string          `json:"reason"`
	Call   *int64          `json:"call"`
	Verb   string          `json:"verb"`
	OK     *bool           `json:"ok"`
	Error  string          `json:"error"`

	Run      string `json:"run"`
	Pipeline string `json:"pipeline"`
	Started  string `json:"started"`
	Ended    string `json:"ended"`
	Outcome  string `json:"outcome"`
}

// humanizeCapture rewrites the naturalized log tail for display. Bare log
// lines ride unchanged; marked frame lines whose payload parses render
// compact, keeping their origin prefix so logLineStyle still colors them.
func humanizeCapture(lines []string) []string {
	out := make([]string, 0, len(lines))
	var g *rowGroup
	flush := func() {
		if g != nil {
			out = append(out, g.line())
			g = nil
		}
	}
	for _, line := range lines {
		origin, payload, marked := splitOrigin(line)
		if !marked {
			flush()
			out = append(out, line)
			continue
		}
		var f captureFrame
		if err := json.Unmarshal([]byte(payload), &f); err != nil {
			flush()
			out = append(out, line)
			continue
		}
		if f.Event == "row" && f.Table != "" {
			if g == nil || g.origin != origin || g.table != f.Table {
				flush()
				g = &rowGroup{origin: origin, table: f.Table}
			}
			g.add(f.Row)
			continue
		}
		flush()
		if h, ok := f.human(); ok {
			out = append(out, origin+" "+h)
		} else {
			out = append(out, line)
		}
	}
	flush()
	return out
}

// splitOrigin splits one line into its frame-origin prefix and JSON payload,
// reporting whether the line is a marked frame at all.
func splitOrigin(line string) (origin, payload string, marked bool) {
	for _, o := range []string{"[engine] ", "[pipeline] ", "[iris] "} {
		if strings.HasPrefix(line, o) {
			return strings.TrimSuffix(o, " "), line[len(o):], true
		}
	}
	return "", "", false
}

// human renders one non-row frame compact, reporting whether the frame's
// shape is known; an unknown shape falls back to the raw line.
func (f captureFrame) human() (string, bool) {
	switch f.Event {
	case "go":
		if f.Turn != nil {
			return "go · turn " + strconv.FormatInt(*f.Turn, 10), true
		}
		return "go", true
	case "run":
		return "run", true
	case "done":
		if f.Turn != nil {
			return "done · turn " + strconv.FormatInt(*f.Turn, 10), true
		}
		return "done", true
	case "error":
		s := "error"
		if f.Turn != nil {
			s += " · turn " + strconv.FormatInt(*f.Turn, 10)
		}
		if f.Reason != "" {
			s += " · " + f.Reason
		}
		return s, true
	case "call":
		s := "call"
		if f.Call != nil {
			s += " " + strconv.FormatInt(*f.Call, 10)
		}
		if f.Verb != "" {
			s += " · " + f.Verb
		}
		return s, true
	case "res":
		s := "res"
		if f.Call != nil {
			s += " call " + strconv.FormatInt(*f.Call, 10)
		}
		switch {
		case f.OK != nil && *f.OK:
			s += " · ok"
		case f.Error != "":
			s += " · error: " + f.Error
		}
		return s, true
	case "":
		switch {
		case f.Run != "":
			s := "run " + f.Run
			if f.Pipeline != "" {
				s += " · " + f.Pipeline
			}
			if f.Started != "" {
				s += " · started " + clock(f.Started)
			}
			return s, true
		case f.Ended != "":
			s := "ended " + clock(f.Ended)
			if f.Outcome != "" {
				s += " · " + f.Outcome
			}
			return s, true
		}
	}
	return "", false
}

// rowGroup accumulates one burst of consecutive row frames sharing an origin
// and table, tracking the rows' occurred_at range when the rows carry one.
type rowGroup struct {
	origin, table   string
	count           int
	firstAt, lastAt string
}

// add counts one row into the group.
func (g *rowGroup) add(row json.RawMessage) {
	g.count++
	var r struct {
		OccurredAt string `json:"occurred_at"`
	}
	if json.Unmarshal(row, &r) == nil && r.OccurredAt != "" {
		if g.firstAt == "" {
			g.firstAt = r.OccurredAt
		}
		g.lastAt = r.OccurredAt
	}
}

// line renders the group's fold line.
func (g *rowGroup) line() string {
	s := g.origin + " " + strconv.Itoa(g.count) + " row"
	if g.count != 1 {
		s += "s"
	}
	s += " → " + g.table
	switch {
	case g.firstAt != "" && g.count > 1:
		s += " (" + clock(g.firstAt) + " … " + clock(g.lastAt) + ")"
	case g.firstAt != "":
		s += " (" + clock(g.firstAt) + ")"
	}
	return s
}

// clock renders an RFC3339 stamp as its clock time; a stamp that does not
// parse rides verbatim.
func clock(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.Format("15:04:05")
}
