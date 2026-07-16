package cli

import (
	"sort"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
)

// This file is the state of the `iris ps` live view: the polled snapshot, the
// navigation model over its four screens (lanes, one lane's pipelines, one
// pipeline's runs, one run's detail), and the pure derivations that turn the
// /ps payload plus the pipeline listing into the tables each screen renders.
// Everything here is plain data and pure functions -- no terminal, no HTTP --
// so the whole state machine is unit-testable with fixture payloads.

// psScreen names one of the live view's four screens.
type psScreen int

// The live view's screens, in descent order.
const (
	psScreenLanes psScreen = iota
	psScreenPipelines
	psScreenRuns
	psScreenRun
)

// psSnapshot is one poll's worth of view data: the /ps payload (always the
// ?all=true history -- the level-3 toggle filters client-side), the pipeline
// listing (?all=1, idle pipelines included, each row carrying its lane), and
// the focused run's log tail when a run is open. logsRun names the run the
// tail belongs to, so a snapshot buffered before a focus switch never paints
// the previous run's lines under the new run's header.
type psSnapshot struct {
	ps        api.PsPayload
	pipelines []api.PipelineListItem
	logs      []string
	logsRun   string
}

// psModel is the live view's whole state: which screen is open, the breadcrumb
// selection that led there, the per-screen selection cursors, and the level-4
// toggles. update() is the only mutator the event loop drives.
type psModel struct {
	screen   psScreen
	lane     string // breadcrumb, level 2+
	pipeline string // breadcrumb, level 3+
	runID    string // breadcrumb, level 4

	// Selection cursors, keyed by row identity (lane name, pipeline name, run
	// id) so a re-poll that reorders rows keeps the selection on the same
	// entity; a vanished key clamps to the first row.
	selLane     string
	selPipeline string
	selRun      string

	showAll       bool   // level 3: 'a' toggled the whole history in
	follow        bool   // level 4: log tail follows new output
	scroll        int    // level 4: lines scrolled back from the tail when not following
	confirmCancel bool   // level 4: y/N cancel confirm armed
	note          string // transient action outcome, cleared on the next key
	warn          string // standing soft-fetch warning, cleared by the next good poll

	search *psSearch // non-nil while the search overlay is open

	snap   psSnapshot
	target string // footer right slot: "remote <host>" or "local <socket>"
	quit   bool
}

// focus is the run id the poller should tail logs for: the open run on the
// detail screen, nothing anywhere else. Derived, never stored -- every exit
// from the detail screen drops the focus by construction.
func (m *psModel) focus() string {
	if m.screen == psScreenRun {
		return m.runID
	}
	return ""
}

// newPsModel builds the home-screen model over the first snapshot.
func newPsModel(first psSnapshot, target string) *psModel {
	m := &psModel{screen: psScreenLanes, follow: true, snap: first, target: target}
	m.clampSelections()
	return m
}

// psLaneRow is one row of the lanes screen (level 1).
type psLaneRow struct {
	name            string
	pipelines       int
	queued, running int
	load            *api.PsLoad // summed over the lane's running runs; nil renders dashes
}

// psPipelineRow is one row of a lane's pipeline screen (level 2).
type psPipelineRow struct {
	name            string
	latest          string // newest run's state, "-" when the pipeline never ran
	queued, running int
	load            *api.PsLoad
}

// laneOf resolves the lane a listing row belongs to: its composer lane, or --
// mirroring dispatch.BuildWalk -- the pipeline itself as its own anonymous
// lane when no composer row names it.
func laneOf(p api.PipelineListItem) string {
	if p.Lane != "" {
		return p.Lane
	}
	return p.Name
}

// runLaneOf resolves the lane a run row belongs to, falling back to the run's
// own pipeline as its anonymous lane when the run carries none.
func runLaneOf(r api.PsRun) string {
	if r.Lane != "" {
		return r.Lane
	}
	return r.Pipeline
}

// sumLoad accumulates a run's sampled load into a lane or pipeline total,
// keeping nil -- dashes, never fabricated zeros -- until a real sample lands.
func sumLoad(total *api.PsLoad, l *api.PsLoad) *api.PsLoad {
	if l == nil {
		return total
	}
	if total == nil {
		total = &api.PsLoad{}
	}
	total.CPUPercent += l.CPUPercent
	total.RSSBytes += l.RSSBytes
	return total
}

// deriveLanes composes the level-1 table: one row per lane, the union of the
// listing's lanes and the run rows' lanes (a run whose pipeline was since
// unregistered still shows), sorted by name.
func deriveLanes(s psSnapshot) []psLaneRow {
	byName := map[string]*psLaneRow{}
	row := func(name string) *psLaneRow {
		r := byName[name]
		if r == nil {
			r = &psLaneRow{name: name}
			byName[name] = r
		}
		return r
	}
	for _, p := range s.pipelines {
		row(laneOf(p)).pipelines++
	}
	for _, run := range s.ps.Runs {
		r := row(runLaneOf(run))
		switch run.State {
		case "queued":
			r.queued++
		case "running":
			r.running++
			r.load = sumLoad(r.load, run.Load)
		}
	}
	out := make([]psLaneRow, 0, len(byName))
	for _, r := range byName {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// derivePipelines composes one lane's level-2 table: one row per member
// pipeline, idle members included, each with its newest run's state and its
// running load. Runs of pipelines missing from the listing (unregistered
// since) still contribute rows so the counts add up.
func derivePipelines(s psSnapshot, lane string) []psPipelineRow {
	byName := map[string]*psPipelineRow{}
	order := []string{}
	row := func(name string) *psPipelineRow {
		r := byName[name]
		if r == nil {
			r = &psPipelineRow{name: name, latest: "-"}
			byName[name] = r
			order = append(order, name)
		}
		return r
	}
	for _, p := range s.pipelines {
		if laneOf(p) == lane {
			row(p.Name)
		}
	}
	// Runs are newest first: the first run seen per pipeline is its latest.
	for _, run := range s.ps.Runs {
		if runLaneOf(run) != lane {
			continue
		}
		r := row(run.Pipeline)
		if r.latest == "-" {
			r.latest = run.State
		}
		switch run.State {
		case "queued":
			r.queued++
		case "running":
			r.running++
			r.load = sumLoad(r.load, run.Load)
		}
	}
	sort.Strings(order)
	out := make([]psPipelineRow, 0, len(order))
	for _, name := range order {
		out = append(out, *byName[name])
	}
	return out
}

// deriveRuns filters one pipeline's level-3 table from the history snapshot:
// newest first as the wire orders them, queued and running only until the 'a'
// toggle widens to the whole history.
func deriveRuns(s psSnapshot, pipeline string, all bool) []api.PsRun {
	var out []api.PsRun
	for _, run := range s.ps.Runs {
		if run.Pipeline != pipeline {
			continue
		}
		if !all && run.State != "queued" && run.State != "running" {
			continue
		}
		out = append(out, run)
	}
	return out
}

// findRun resolves a run id in the snapshot, for the level-4 fact line.
func findRun(s psSnapshot, id string) (api.PsRun, bool) {
	for _, run := range s.ps.Runs {
		if run.ID == id {
			return run, true
		}
	}
	return api.PsRun{}, false
}

// absorb replaces the snapshot after a poll, drops a log tail that belongs to
// a previously focused run, and re-clamps the selections so vanished rows
// never leave a dangling cursor.
func (m *psModel) absorb(s psSnapshot) {
	if s.logsRun != m.runID || m.screen != psScreenRun {
		s.logs, s.logsRun = nil, ""
	}
	m.snap = s
	m.clampSelections()
	if m.search != nil {
		m.search.rematch(m.snap)
	}
}

// rows/keys helpers: the current screen's row identities, in display order.
func (m *psModel) laneKeys() []string {
	lanes := deriveLanes(m.snap)
	keys := make([]string, len(lanes))
	for i, l := range lanes {
		keys[i] = l.name
	}
	return keys
}

func (m *psModel) pipelineKeys() []string {
	rows := derivePipelines(m.snap, m.lane)
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = r.name
	}
	return keys
}

func (m *psModel) runKeys() []string {
	runs := deriveRuns(m.snap, m.pipeline, m.showAll)
	keys := make([]string, len(runs))
	for i, r := range runs {
		keys[i] = r.ID
	}
	return keys
}

// clampSelections snaps the open screen's cursor to a live row (the first,
// when the selected identity vanished or nothing was selected yet). Only the
// visible cursor needs clamping -- descend() re-clamps the next screen's --
// so a poll never derives the two off-screen tables just to tidy them.
func (m *psModel) clampSelections() {
	switch m.screen {
	case psScreenLanes:
		m.selLane = clampKey(m.selLane, m.laneKeys())
	case psScreenPipelines:
		m.selPipeline = clampKey(m.selPipeline, m.pipelineKeys())
	case psScreenRuns:
		m.selRun = clampKey(m.selRun, m.runKeys())
	}
}

// clampKey keeps sel when it still names a row, else the first row, else "".
func clampKey(sel string, keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	for _, k := range keys {
		if k == sel {
			return sel
		}
	}
	return keys[0]
}

// moveSel moves a cursor delta rows within keys, clamped to the ends.
func moveSel(sel string, keys []string, delta int) string {
	if len(keys) == 0 {
		return ""
	}
	at := 0
	for i, k := range keys {
		if k == sel {
			at = i
			break
		}
	}
	at += delta
	if at < 0 {
		at = 0
	}
	if at >= len(keys) {
		at = len(keys) - 1
	}
	return keys[at]
}

// update advances the model by one keypress and returns the run the loop
// should ask the poller to cancel ("" almost always). The loop reads its two
// other signals off the model after the call: m.quit to exit, and m.focus()
// to re-point the poller's log tail when it changed.
func (m *psModel) update(k psKey) (cancelRun string) {
	m.note = ""

	// The search overlay owns the keyboard while open (Esc lives only here).
	if m.search != nil {
		m.updateSearch(k)
		return ""
	}

	// An armed cancel confirm consumes the next key: y confirms, all else disarms.
	if m.confirmCancel {
		m.confirmCancel = false
		if k.kind == psKeyRune && (k.r == 'y' || k.r == 'Y') {
			return m.runID
		}
		if k.kind == psKeyCtrlC {
			m.quit = true
		}
		return ""
	}

	switch k.kind {
	case psKeyCtrlC:
		m.quit = true
	case psKeyRune:
		m.updateRune(k.r)
	case psKeyUp:
		m.move(-1)
	case psKeyDown:
		m.move(1)
	case psKeyEnter, psKeyRight:
		m.descend()
	case psKeyLeft:
		m.ascend()
	}
	return ""
}

// updateRune routes a printable keypress outside the overlay.
func (m *psModel) updateRune(r rune) {
	switch r {
	case 'q', 'Q':
		m.quit = true
	case 'j':
		m.move(1)
	case 'k':
		m.move(-1)
	case '/':
		m.openSearch()
	case 'a':
		if m.screen == psScreenRuns {
			m.showAll = !m.showAll
			m.selRun = clampKey(m.selRun, m.runKeys())
		}
	case 'f':
		if m.screen == psScreenRun {
			m.follow = !m.follow
			m.scroll = 0
		}
	case 'c':
		if m.screen == psScreenRun {
			if run, ok := findRun(m.snap, m.runID); ok && run.State == "running" {
				m.confirmCancel = true
			}
		}
	}
}

// move shifts the current screen's selection; on a non-following run detail it
// scrolls the log tail instead.
func (m *psModel) move(delta int) {
	switch m.screen {
	case psScreenLanes:
		m.selLane = moveSel(m.selLane, m.laneKeys(), delta)
	case psScreenPipelines:
		m.selPipeline = moveSel(m.selPipeline, m.pipelineKeys(), delta)
	case psScreenRuns:
		m.selRun = moveSel(m.selRun, m.runKeys(), delta)
	case psScreenRun:
		if !m.follow {
			m.scroll -= delta // up (-1) scrolls further back from the tail
			m.clampScroll(len(m.snap.logs))
		}
	}
}

// clampScroll keeps the scrollback offset within the held tail: at the far
// end the first line stays on screen (never a blank pane past the top).
func (m *psModel) clampScroll(lines int) {
	if m.scroll < 0 {
		m.scroll = 0
	}
	if top := lines - 1; top >= 0 && m.scroll > top {
		m.scroll = top
	}
}

// descend opens the selected row's screen (Enter / right arrow).
func (m *psModel) descend() {
	switch m.screen {
	case psScreenLanes:
		if m.selLane == "" {
			return
		}
		m.lane = m.selLane
		m.screen = psScreenPipelines
		m.selPipeline = clampKey(m.selPipeline, m.pipelineKeys())
	case psScreenPipelines:
		if m.selPipeline == "" {
			return
		}
		m.pipeline = m.selPipeline
		m.screen = psScreenRuns
		m.selRun = clampKey(m.selRun, m.runKeys())
	case psScreenRuns:
		if m.selRun == "" {
			return
		}
		m.openRun(m.selRun)
	}
}

// openRun jumps to the level-4 detail of one run; the loop reads the changed
// focus() and points the poller's log tail at it.
func (m *psModel) openRun(id string) {
	m.runID = id
	m.screen = psScreenRun
	m.follow = true
	m.scroll = 0
	m.confirmCancel = false
	m.snap.logs, m.snap.logsRun = nil, "" // a previous run's tail never flashes
}

// ascend backs out one level (left arrow).
func (m *psModel) ascend() {
	switch m.screen {
	case psScreenPipelines:
		m.screen = psScreenLanes
	case psScreenRuns:
		m.screen = psScreenPipelines
	case psScreenRun:
		m.screen = psScreenRuns
		m.confirmCancel = false
	}
}

// breadcrumb renders the title bar's left slot for the current screen.
func (m *psModel) breadcrumb() string {
	switch m.screen {
	case psScreenPipelines:
		return "iris ps · " + m.lane
	case psScreenRuns:
		return "iris ps · " + m.lane + " · " + m.pipeline
	case psScreenRun:
		return "iris ps · " + m.lane + " · " + m.pipeline + " · run " + m.runID
	default:
		return "iris ps"
	}
}
