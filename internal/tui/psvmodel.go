package tui

import (
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/quotes"
)

// This file is the state of the `iris ps` dashboard: the polled snapshot, the
// pane-focus model over the four panes (lanes rail, table, detail, logs), the
// lane tree's expand/collapse state, the client-side load history rings behind
// every heat strip, and the pure derivations that turn the /ps payload plus
// the pipeline listing into rows. Everything here is plain data and pure
// functions -- no terminal, no HTTP -- so the whole state machine is
// unit-testable with fixture payloads.

// psPane names one of the dashboard's focusable panes. The detail box is
// display-only (nothing to operate), so tab skips it.
type psPane int

// The focusable panes, in tab order.
const (
	psPaneLanes psPane = iota
	psPaneStats
	psPaneEvents
)

// psRingCap bounds every fine load-history ring, comfortably past the widest
// strip any layout renders. The daemon's fine ring is deeper; a re-seed trims
// to this.
const psRingCap = 240

// psNoSample marks a tick with no load sample in a ring (idle lane, absent
// payload load); strips render it as an empty cell, never a fabricated zero.
// It is the same sentinel the wire history uses (api.PsHistoryNoSample), so
// re-seeded slots need no translation.
const psNoSample = api.PsHistoryNoSample

// psRing is one entity's sampled load history, newest last. cpu holds
// percentages (psNoSample for a sampleless tick), mem resident bytes.
type psRing struct {
	cpu []float64
	mem []int64
}

// push appends one tick's sample, evicting past the cap.
func (r *psRing) push(cpu float64, mem int64) {
	r.cpu = append(r.cpu, cpu)
	r.mem = append(r.mem, mem)
	if len(r.cpu) > psRingCap {
		r.cpu = r.cpu[len(r.cpu)-psRingCap:]
		r.mem = r.mem[len(r.mem)-psRingCap:]
	}
}

// memPeak is the largest resident sample in the ring (0 when none).
func (r *psRing) memPeak() int64 {
	var peak int64
	for _, m := range r.mem {
		if m > peak {
			peak = m
		}
	}
	return peak
}

// Snapshot is one poll's worth of view data: the /ps payload (always the
// ?all=true history), the pipeline listing (?all=1, idle pipelines included,
// each row carrying its lane), and the tailed run's log lines. logsRun names
// the run the tail belongs to, so a snapshot buffered before a retarget never
// paints the previous run's lines under the new run's title.
type Snapshot struct {
	Ps        api.PsPayload
	Pipelines []api.PipelineListItem
	Logs      []string
	LogsRun   string
	// Journal is the poller-accumulated write-activity state (#238 phase 3);
	// nil until the first successful activity poll (renders as absence).
	Journal *psJournal
	// Events is the poller-derived engine-wide digest (#238 phase 4): state
	// changes observed between polls, newest last. Never raw log text.
	Events []psEvent
	// staleAge marks a snapshot revived from the last-known-state cache (the
	// engine was unreachable at open): how old the cached state is. Zero on a
	// live snapshot. The view opens it under the unreachable banner.
	StaleAge time.Duration
}

// psModel is the dashboard's whole state. update() is the only mutator the
// event loop drives.
type psModel struct {
	pane psPane

	// The catalog rail's tree cursor (nothing folds — every lane always shows
	// its pipelines, per #238 C1d). A cursor with selPipeline == "" sits on a
	// lane row; otherwise on that pipeline's row.
	selLane     string
	selPipeline string
	selTable    string

	// Per-pane filter queries (#238 C1d): '/' on the focused pane opens its
	// filter input; the query narrows that pane's rows until cleared. The
	// *Input flags hold the input row's typing focus.
	catFilter []rune
	catInput  bool
	evtFilter []rune
	evtInput  bool

	// logsOpen is the full-screen log view over the pinned/derived run: the
	// frame's only raw-text surface, opened with ⏎ on a run row.
	logsOpen bool

	// The table pane's cursor, keyed by row identity so a re-poll that
	// reorders rows keeps the cursor on the same entity. tblPipeline cursors
	// the pipelines table (lane row selected), tblRun the runs table
	// (pipeline row selected).
	tblPipeline string
	tblRun      string

	// pinnedRun is an explicit log target picked in the runs table; "" lets
	// the target follow the selection automatically.
	pinnedRun string

	showAll       bool // runs table: 'a' toggled the whole history in
	follow        bool // logs pane: tail follows new output
	scroll        int  // logs pane: lines scrolled back when not following
	confirmCancel bool // logs pane: y/N cancel confirm armed
	confirmBulk   bool // y/N bulk-cancel confirm armed over the marked pipelines

	// markedPipes is the space-marked pipeline set a bulk cancel acts on;
	// marks outlive selection moves and are pruned when their pipeline leaves
	// the snapshot.
	markedPipes map[string]bool
	histView    bool   // strips: 'h' toggled the coarse hours-deep history in
	note        string // transient action outcome, cleared on the next key
	warn        string // standing soft-fetch warning, cleared by the next good poll
	frozen      bool   // live polls paused so terminal select/copy is stable

	search  *psSearch  // non-nil while the search overlay is open
	command *psCommand // non-nil while the ':' command prompt is open (#218)

	catalog    *psCatalog    // non-nil while the catalog overlay is open (#219)
	idleCat    *psCatalog    // the idle card's inline searchable catalog; nil once work registers
	catalogReq *psCatalogReq // catalog action parked for the loop, consumed via takeCatalogReq
	catalogSeq int           // monotonic request correlation counter (stale outcomes drop)

	// rings holds every heat strip's fine history: key "" is the engine,
	// "l:<name>" a lane, "p:<name>" a pipeline. Seeded from the daemon's
	// recorded history and grown one slot per collector tick (the payload's
	// sample_tick names the tick, so a poll that races the collector never
	// double-counts). coarse holds the same keys' coarse (per-bucket-maximum)
	// history, hours deep, refreshed only on a history re-seed -- exactly its
	// own cadence. lastTick is the newest absorbed collector tick.
	rings    map[string]*psRing
	coarse   map[string]*psRing
	lastTick uint64

	spin   int          // spinner phase, advanced by the event loop while catalog work is in flight
	quote  quotes.Quote // the idle card's ceremony quote, picked once per view
	clicks []psClick    // clickable regions of the last rendered frame

	snap   Snapshot
	target string // watched engine id for the disk cache ("remote <host>" / "local <socket>")
	quit   bool
}

// newPsModel builds the dashboard model over the first snapshot: lanes pane
// focused, the first lane selected and unfolded.
func newPsModel(first Snapshot, target string) *psModel {
	m := &psModel{
		pane:   psPaneLanes,
		follow: true,
		rings:  map[string]*psRing{},
		coarse: map[string]*psRing{},
		snap:   first,
		target: target,
	}
	m.absorbRings()
	m.clampTree()
	m.clampTable()
	if first.StaleAge > 0 {
		m.warn = psUnreachableWarn + " · cached " + first.StaleAge.Truncate(time.Second).String() + " ago"
	}
	m.quote = quotes.Farewell[rand.IntN(len(quotes.Farewell))] //nolint:gosec // G404: cosmetic quote pick.
	if psIsEmptyWorkspace(m) {
		m.openIdleCatalog()
	}
	return m
}

// focus is the run id the poller should tail logs for -- the logs pane's
// current target. Derived, never stored.
func (m *psModel) focus() string { return m.logsTarget() }

// logsTarget resolves the logs pane's run: the pinned run while it still
// exists, else the newest running run under the tree selection, else the
// newest run under it, else "".
func (m *psModel) logsTarget() string {
	if m.pinnedRun != "" {
		if _, ok := findRun(m.snap, m.pinnedRun); ok {
			return m.pinnedRun
		}
	}
	inScope := func(r api.PsRun) bool {
		if m.selPipeline != "" {
			return r.Pipeline == m.selPipeline
		}
		return runLaneOf(r) == m.selLane
	}
	first := ""
	for _, r := range m.snap.Ps.Runs { // newest first as the wire orders them
		if !inScope(r) {
			continue
		}
		if r.State == "running" {
			return r.ID
		}
		if first == "" {
			first = r.ID
		}
	}
	return first
}

// detailPipeline resolves which pipeline the detail box charts: the selected
// pipeline row, or -- on a lane row -- the pipelines table's cursor.
func (m *psModel) detailPipeline() string {
	if m.selPipeline != "" {
		return m.selPipeline
	}
	return m.tblPipeline
}

// psLaneRow is one lane row of the rail and its metrics line.
type psLaneRow struct {
	name            string
	pipelines       int
	queued, running int
	load            *api.PsLoad // summed over the lane's running runs; nil renders dashes
}

// psPipelineRow is one row of the pipelines table.
type psPipelineRow struct {
	name            string
	latest          string // newest run's state, "-" when the pipeline never ran
	queued, running int
	load            *api.PsLoad
}

// psTreeRow is one visible row of the catalog: a lane row (pipeline and
// table empty), a written-table row (table set, "schema.table"), or a member
// pipeline row.
type psTreeRow struct {
	lane     string
	pipeline string
	table    string
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

// deriveLanes composes the rail's lane rows: one per lane, the union of the
// listing's lanes and the run rows' lanes (a run whose pipeline was since
// unregistered still shows), sorted by name.
func deriveLanes(s Snapshot) []psLaneRow {
	byName := map[string]*psLaneRow{}
	row := func(name string) *psLaneRow {
		r := byName[name]
		if r == nil {
			r = &psLaneRow{name: name}
			byName[name] = r
		}
		return r
	}
	for _, p := range s.Pipelines {
		row(laneOf(p)).pipelines++
	}
	for _, run := range s.Ps.Runs {
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

// derivePipelines composes one lane's pipeline rows: one per member pipeline,
// idle members included, each with its newest run's state and its running
// load. Runs of pipelines missing from the listing (unregistered since) still
// contribute rows so the counts add up.
func derivePipelines(s Snapshot, lane string) []psPipelineRow {
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
	for _, p := range s.Pipelines {
		if laneOf(p) == lane {
			row(p.Name)
		}
	}
	// Runs are newest first: the first run seen per pipeline is its latest.
	for _, run := range s.Ps.Runs {
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

// deriveRuns filters one pipeline's runs from the history snapshot: newest
// first as the wire orders them, queued and running only until the 'a' toggle
// widens to the whole history.
func deriveRuns(s Snapshot, pipeline string, all bool) []api.PsRun {
	var out []api.PsRun
	for _, run := range s.Ps.Runs {
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

// findRun resolves a run id in the snapshot, for the logs title and cancel.
func findRun(s Snapshot, id string) (api.PsRun, bool) {
	for _, run := range s.Ps.Runs {
		if run.ID == id {
			return run, true
		}
	}
	return api.PsRun{}, false
}

// treeRows enumerates the catalog's navigable rows in display order: every
// lane and every member pipeline — nothing folds (#238 C1d). The catalog
// filter narrows rows: a lane hit keeps its whole block, a pipeline hit keeps
// its lane header for context plus the matching pipelines.
func (m *psModel) treeRows() []psTreeRow {
	q := strings.ToLower(strings.TrimSpace(string(m.catFilter)))
	tablesByLane := m.laneTables()
	var out []psTreeRow
	for _, l := range deriveLanes(m.snap) {
		pipes := derivePipelines(m.snap, l.name)
		laneHit := q == "" || strings.Contains(strings.ToLower(l.name), q)
		var keptT []string
		for _, name := range tablesByLane[l.name] {
			if laneHit || strings.Contains(strings.ToLower(name), q) {
				keptT = append(keptT, name)
			}
		}
		var kept []psPipelineRow
		for _, p := range pipes {
			if laneHit || strings.Contains(strings.ToLower(p.name), q) {
				kept = append(kept, p)
			}
		}
		if !laneHit && len(kept) == 0 && len(keptT) == 0 {
			continue
		}
		out = append(out, psTreeRow{lane: l.name})
		for _, name := range keptT {
			out = append(out, psTreeRow{lane: l.name, table: name})
		}
		for _, p := range kept {
			out = append(out, psTreeRow{lane: l.name, pipeline: p.name})
		}
	}
	return out
}

// laneTables groups the journal's written tables under the lane of their
// latest writing pipeline (#238 C1d: tables are lane scoped in the rail; the
// statistics pane answers which pipeline wrote what). A table whose writer is
// unknown or unregistered shows under no lane.
func (m *psModel) laneTables() map[string][]string {
	j := m.snap.Journal
	if j == nil {
		return nil
	}
	laneOfPipe := map[string]string{}
	for _, l := range deriveLanes(m.snap) {
		for _, p := range derivePipelines(m.snap, l.name) {
			laneOfPipe[p.name] = l.name
		}
	}
	out := map[string][]string{}
	for _, name := range j.tableNames() {
		if lane, ok := laneOfPipe[j.tableWriter(name)]; ok {
			out[lane] = append(out[lane], name)
		}
	}
	return out
}

// filteredEvents narrows the digest by the events filter: a case-insensitive
// substring match over the row text and its pipeline.
func (m *psModel) filteredEvents() []psEvent {
	q := strings.ToLower(strings.TrimSpace(string(m.evtFilter)))
	if q == "" {
		return m.snap.Events
	}
	var out []psEvent
	for _, e := range m.snap.Events {
		if strings.Contains(strings.ToLower(e.Text), q) || strings.Contains(strings.ToLower(e.Pipeline), q) {
			out = append(out, e)
		}
	}
	return out
}

// treeHidden counts the catalog rows the active filter is hiding.
func (m *psModel) treeHidden() int {
	if len(m.catFilter) == 0 {
		return 0
	}
	total := 0
	for _, l := range deriveLanes(m.snap) {
		total += 1 + len(derivePipelines(m.snap, l.name))
	}
	return total - len(m.treeRows())
}

// absorb replaces the snapshot after a poll: grows the load rings, drops a
// log tail that belongs to a run other than the current target, and re-clamps
// the cursors so vanished rows never leave them dangling.
func (m *psModel) absorb(s Snapshot) {
	if s.LogsRun != "" && s.LogsRun != m.logsTargetIn(s) {
		s.Logs, s.LogsRun = nil, ""
	}
	m.snap = s
	m.absorbRings()
	m.clampTree()
	m.clampTable()
	if m.search != nil {
		m.search.rematch(m.snap)
	}
	// The inline idle catalog lives exactly as long as the empty workspace.
	if psIsEmptyWorkspace(m) {
		if m.idleCat == nil {
			m.openIdleCatalog()
		}
	} else if m.idleCat != nil {
		m.idleCat = nil
	}
	// Pipeline marks live only as long as their pipeline stays in the snapshot.
	if len(m.markedPipes) > 0 {
		alive := map[string]bool{}
		for _, p := range s.Pipelines {
			alive[p.Name] = true
		}
		for _, r := range s.Ps.Runs {
			alive[r.Pipeline] = true
		}
		for name := range m.markedPipes {
			if !alive[name] {
				delete(m.markedPipes, name)
			}
		}
	}
}

// logsTargetIn resolves the logs target against a candidate snapshot, so
// absorb can judge an arriving tail before committing the snapshot.
func (m *psModel) logsTargetIn(s Snapshot) string {
	held := m.snap
	m.snap = s
	defer func() { m.snap = held }()
	return m.logsTarget()
}

// absorbRings advances the load rings for one absorbed snapshot. A snapshot
// carrying the daemon's recorded history re-seeds every ring from it (the
// view-open backfill, and the periodic refresh that keeps the coarse rings
// current). Otherwise the payload's sample_tick decides: unchanged means the
// collector has not sampled since the last poll, so nothing is pushed (the 1s
// poll outpaces the collector's tick on purpose); an advanced tick pushes one
// slot, with any missed ticks filled absent first so the time axis stays
// honest. A tick-less payload (a daemon without a collector) falls back to one
// push per absorb, the pre-history behavior.
func (m *psModel) absorbRings() {
	if h := m.snap.Ps.History; h != nil {
		m.reseedRings(h)
		m.lastTick = m.snap.Ps.SampleTick
		return
	}
	if tick := m.snap.Ps.SampleTick; tick != 0 {
		if tick < m.lastTick {
			// The collector's counter went backwards: a restarted (or different)
			// daemon answers now -- the reconnect case. Reset the gate so its
			// samples land instead of being skipped until the counter catches up.
			m.lastTick = 0
		}
		if tick <= m.lastTick {
			return
		}
		if m.lastTick != 0 {
			gaps := tick - m.lastTick - 1
			if gaps > psRingCap {
				gaps = psRingCap // deeper gaps fall off the ring anyway
			}
			for range gaps {
				m.pushRingsAbsent()
			}
		}
		m.lastTick = tick
	}
	m.pushRings()
}

// reseedRings replaces every ring with the daemon's recorded history: the fine
// series trimmed to the client cap, the coarse series whole. The wire keys
// ("engine", "lane:<name>", "pipeline:<name>") map onto the ring keys ("",
// "l:<name>", "p:<name>"); an unrecognized key is skipped, never guessed.
func (m *psModel) reseedRings(h *api.PsHistory) {
	m.rings = map[string]*psRing{}
	m.coarse = map[string]*psRing{}
	for _, s := range h.Series {
		key, ok := ringKeyFor(s.Key)
		if !ok {
			continue
		}
		fine := &psRing{cpu: append([]float64(nil), s.CPU...), mem: append([]int64(nil), s.RSS...)}
		if len(fine.cpu) > psRingCap {
			fine.cpu = fine.cpu[len(fine.cpu)-psRingCap:]
			fine.mem = fine.mem[len(fine.mem)-psRingCap:]
		}
		m.rings[key] = fine
		m.coarse[key] = &psRing{cpu: append([]float64(nil), s.CoarseCPU...), mem: append([]int64(nil), s.CoarseRSS...)}
	}
}

// ringKeyFor maps a wire history series key onto the model's ring key.
func ringKeyFor(wire string) (string, bool) {
	switch {
	case wire == "engine":
		return "", true
	case strings.HasPrefix(wire, "lane:"):
		return "l:" + strings.TrimPrefix(wire, "lane:"), true
	case strings.HasPrefix(wire, "pipeline:"):
		return "p:" + strings.TrimPrefix(wire, "pipeline:"), true
	}
	return "", false
}

// pushRingsAbsent pushes one absent slot into every existing ring: a collector
// tick the poller missed carries no knowable sample.
func (m *psModel) pushRingsAbsent() {
	for _, r := range m.rings {
		r.push(psNoSample, 0)
	}
}

// pushRings pushes one tick of load history for the engine, every lane, and
// every pipeline seen in the snapshot. Entities without a sample this tick
// push psNoSample so their strips show absence, not zero.
func (m *psModel) pushRings() {
	ring := func(key string) *psRing {
		r := m.rings[key]
		if r == nil {
			r = &psRing{}
			m.rings[key] = r
		}
		return r
	}
	if l := m.snap.Ps.Engine.Load; l != nil {
		ring("").push(l.CPUPercent, l.RSSBytes)
	} else {
		ring("").push(psNoSample, 0)
	}
	for _, lane := range deriveLanes(m.snap) {
		r := ring("l:" + lane.name)
		if lane.load != nil {
			r.push(lane.load.CPUPercent, lane.load.RSSBytes)
		} else {
			r.push(psNoSample, 0)
		}
	}
	perPipe := map[string]*api.PsLoad{}
	seen := map[string]bool{}
	for _, run := range m.snap.Ps.Runs {
		seen[run.Pipeline] = true
		if run.State == "running" {
			perPipe[run.Pipeline] = sumLoad(perPipe[run.Pipeline], run.Load)
		}
	}
	for _, p := range m.snap.Pipelines {
		seen[p.Name] = true
	}
	for name := range seen {
		r := ring("p:" + name)
		if l := perPipe[name]; l != nil {
			r.push(l.CPUPercent, l.RSSBytes)
		} else {
			r.push(psNoSample, 0)
		}
	}
}

// clampTree snaps the catalog cursor to a visible row: the selected pipeline
// when it survives the snapshot and the filter, else its lane header, else
// the first visible row.
func (m *psModel) clampTree() {
	rows := m.treeRows()
	if len(rows) == 0 {
		m.selLane, m.selPipeline, m.selTable = "", "", ""
		return
	}
	for _, r := range rows {
		if r.lane == m.selLane && r.pipeline == m.selPipeline && r.table == m.selTable {
			return
		}
	}
	for _, r := range rows {
		if r.lane == m.selLane && r.pipeline == "" && r.table == "" {
			m.selPipeline, m.selTable = "", ""
			return
		}
	}
	m.selLane, m.selPipeline, m.selTable = rows[0].lane, rows[0].pipeline, rows[0].table
}

// clampTable snaps the table cursor to a live row for the current context.
func (m *psModel) clampTable() {
	if m.selPipeline != "" {
		m.tblRun = clampKey(m.tblRun, m.runKeys())
		return
	}
	m.tblPipeline = clampKey(m.tblPipeline, m.pipelineKeys())
}

// pipelineKeys lists the pipelines table's row identities in display order.
func (m *psModel) pipelineKeys() []string {
	rows := derivePipelines(m.snap, m.selLane)
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = r.name
	}
	return keys
}

// runKeys lists the runs table's row identities in display order.
func (m *psModel) runKeys() []string {
	runs := deriveRuns(m.snap, m.selPipeline, m.showAll)
	keys := make([]string, len(runs))
	for i, r := range runs {
		keys[i] = r.ID
	}
	return keys
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

// update advances the model by one keypress and returns the runs the loop
// should ask the poller to cancel (nil almost always; several only on a
// confirmed bulk cancel). The loop reads its two other signals off the model
// after the call: m.quit to exit, and m.focus() to re-point the poller's log
// tail when it changed.
func (m *psModel) update(k psKey) (cancelRuns []string) {
	m.note = ""

	// The search overlay owns the keyboard while open (Esc lives only here).
	if m.search != nil {
		m.updateSearch(k)
		return nil
	}

	// The ':' command prompt owns the keyboard while open (#218).
	if m.command != nil {
		m.updateCommand(k)
		return nil
	}

	// The catalog overlay owns the keyboard while open (#219).
	if m.catalog != nil {
		m.updateCatalog(k)
		return nil
	}

	// The idle card's inline catalog gets first pick at keys on the empty
	// workspace; unclaimed keys fall through to the normal bindings.
	if m.idleCat != nil && psIsEmptyWorkspace(m) && m.updateIdleCatalog(k) {
		return nil
	}

	// An open pane filter input owns printable keys: type to narrow, ⏎ keeps
	// the query and returns focus to the rows, Esc clears it. Arrows fall
	// through so the narrowed list stays navigable mid-type.
	if m.catInput || m.evtInput {
		if m.updateFilterInput(k) {
			return nil
		}
	}

	// The full-screen log view (#238 C1d: the frame's only raw-text surface)
	// intercepts its keys; unclaimed ones fall through.
	if m.logsOpen {
		if m.updateLogsView(k) {
			return nil
		}
	}

	// An armed cancel confirm consumes the next key: y confirms, all else disarms.
	if m.confirmCancel {
		m.confirmCancel = false
		if k.kind == psKeyRune && (k.r == 'y' || k.r == 'Y') {
			return []string{m.logsTarget()}
		}
		if k.kind == psKeyCtrlC {
			m.quit = true
		}
		return nil
	}

	// An armed bulk confirm likewise: y cancels every marked pipeline's
	// running runs and drops the marks, all else disarms and keeps them.
	if m.confirmBulk {
		m.confirmBulk = false
		if k.kind == psKeyRune && (k.r == 'y' || k.r == 'Y') {
			runs := m.bulkCancelRuns()
			m.markedPipes = nil
			return runs
		}
		if k.kind == psKeyCtrlC {
			m.quit = true
		}
		return nil
	}

	switch k.kind {
	case psKeyCtrlC:
		m.quit = true
	case psKeyTab:
		m.cyclePane()
	case psKeyRune:
		m.updateRune(k.r)
	case psKeyUp:
		m.move(-1)
	case psKeyDown:
		m.move(1)
	case psKeyEnter, psKeyRight:
		m.enter()
	case psKeyLeft:
		m.back()
	}
	return nil
}

// updateFilterInput routes a key while a pane filter input holds typing
// focus. Returns true when the key was consumed.
func (m *psModel) updateFilterInput(k psKey) bool {
	q := &m.catFilter
	if m.evtInput {
		q = &m.evtFilter
	}
	switch k.kind {
	case psKeyRune:
		*q = append(*q, k.r)
		if m.catInput {
			m.clampTree()
		}
		return true
	case psKeyBackspace:
		if len(*q) > 0 {
			*q = (*q)[:len(*q)-1]
			if m.catInput {
				m.clampTree()
			}
		}
		return true
	case psKeyEnter:
		m.catInput, m.evtInput = false, false
		return true
	case psKeyEsc:
		wasCat := m.catInput
		*q = nil
		m.catInput, m.evtInput = false, false
		if wasCat {
			m.clampTree()
		}
		return true
	}
	return false
}

// updateLogsView routes a key while the full-screen log view is open.
// Returns true when the key was consumed.
func (m *psModel) updateLogsView(k psKey) bool {
	switch k.kind {
	case psKeyEsc, psKeyLeft:
		m.closeLogsView()
		return true
	case psKeyUp:
		if !m.follow {
			m.scroll++
			m.clampScroll(len(m.snap.Logs))
		}
		return true
	case psKeyDown:
		if !m.follow {
			m.scroll--
			m.clampScroll(len(m.snap.Logs))
		}
		return true
	case psKeyRune:
		switch k.r {
		case 'q':
			m.closeLogsView()
			return true
		case 'k':
			if !m.follow {
				m.scroll++
				m.clampScroll(len(m.snap.Logs))
			}
			return true
		case 'j':
			if !m.follow {
				m.scroll--
				m.clampScroll(len(m.snap.Logs))
			}
			return true
		case 'f':
			m.follow = !m.follow
			m.scroll = 0
			return true
		case 'c':
			if run, ok := findRun(m.snap, m.logsTarget()); ok && run.State == "running" {
				m.confirmCancel = true
			}
			return true
		}
	}
	return false
}

// closeLogsView leaves the full-screen log view, unpinning its run.
func (m *psModel) closeLogsView() {
	m.logsOpen = false
	m.pinnedRun = ""
	m.follow = true
	m.scroll = 0
}

// bulkCancelRuns lists every running run belonging to a marked pipeline.
func (m *psModel) bulkCancelRuns() []string {
	var out []string
	for _, r := range m.snap.Ps.Runs {
		if r.State == "running" && m.markedPipes[r.Pipeline] {
			out = append(out, r.ID)
		}
	}
	return out
}

// toggleMarkPipeline flips the bulk mark on the pipeline row under the cursor:
// the rail's pipeline row, or the pipelines table's cursor.
func (m *psModel) toggleMarkPipeline() {
	name := ""
	switch m.pane {
	case psPaneLanes:
		name = m.selPipeline
	case psPaneStats:
		if m.selPipeline == "" {
			name = m.tblPipeline
		}
	}
	if name == "" {
		return
	}
	m.togglePipeMark(name)
}

// togglePipeMark flips the bulk mark on one pipeline by name (the ○/● circle's
// click target).
func (m *psModel) togglePipeMark(name string) {
	if m.markedPipes == nil {
		m.markedPipes = map[string]bool{}
	}
	if m.markedPipes[name] {
		delete(m.markedPipes, name)
	} else {
		m.markedPipes[name] = true
	}
	if len(m.markedPipes) == 0 {
		m.markedPipes = nil
	}
}

// cyclePane advances the pane focus: catalog, statistics, events, around.
func (m *psModel) cyclePane() {
	switch m.pane {
	case psPaneLanes:
		m.pane = psPaneStats
	case psPaneStats:
		m.pane = psPaneEvents
	default:
		m.pane = psPaneLanes
	}
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
		// '/' filters the focused pane (#238 C1d); the global telescope
		// search lives at :search.
		switch m.pane {
		case psPaneLanes:
			m.catInput = true
		case psPaneEvents:
			m.evtInput = true
		}
	case ':':
		m.openCommand()
	case '?':
		m.openCommandHelp()
	case 'a':
		if m.pane == psPaneStats && m.selPipeline != "" {
			m.showAll = !m.showAll
			m.tblRun = clampKey(m.tblRun, m.runKeys())
		}
	case 'h':
		m.histView = !m.histView
	case 'p', 'P':
		// Freeze the live display so the terminal can select and copy text
		// without the next poll wiping the highlight.
		m.frozen = !m.frozen
		if m.frozen {
			m.note = "frozen · select text to copy · p resumes"
		} else {
			m.note = "live"
		}
	case ' ':
		m.toggleMarkPipeline()
	case 'c':
		// Quiet engine: c is the one-key jump into the catalog (the empty
		// card's primary action). With work registered it stays cancel-in-logs,
		// or the bulk confirm when pipelines are marked.
		if psIsEmptyWorkspace(m) {
			m.openCatalog()
			return
		}
		if len(m.markedPipes) > 0 {
			if len(m.bulkCancelRuns()) == 0 {
				m.note = "no running runs in the marked pipelines"
				return
			}
			m.confirmBulk = true
			return
		}
		// Cancel-the-watched-run stays reachable from the statistics pane's
		// run cursor (and inside the full-screen log view, handled there).
		if m.pane == psPaneStats && m.selPipeline != "" {
			if run, ok := findRun(m.snap, m.logsTarget()); ok && run.State == "running" {
				m.confirmCancel = true
			}
		}
	}
}

// move shifts the focused pane's cursor. The events pane holds no cursor
// until its route lands (#238 phase 4).
func (m *psModel) move(delta int) {
	switch m.pane {
	case psPaneLanes:
		m.moveTree(delta)
	case psPaneStats:
		if m.selPipeline != "" {
			m.tblRun = moveSel(m.tblRun, m.runKeys(), delta)
		} else {
			m.tblPipeline = moveSel(m.tblPipeline, m.pipelineKeys(), delta)
		}
	}
}

// moveTree walks the rail cursor over the visible tree rows.
func (m *psModel) moveTree(delta int) {
	rows := m.treeRows()
	if len(rows) == 0 {
		return
	}
	at := 0
	for i, r := range rows {
		if r.lane == m.selLane && r.pipeline == m.selPipeline && r.table == m.selTable {
			at = i
			break
		}
	}
	at += delta
	if at < 0 {
		at = 0
	}
	if at >= len(rows) {
		at = len(rows) - 1
	}
	m.selectTree(rows[at])
}

// selectTree lands the rail cursor on a row, resetting the per-selection
// state that follows it: table cursors, the pinned run, the runs toggle.
func (m *psModel) selectTree(row psTreeRow) {
	if row.lane == m.selLane && row.pipeline == m.selPipeline && row.table == m.selTable {
		return
	}
	m.selLane, m.selPipeline, m.selTable = row.lane, row.pipeline, row.table
	m.pinnedRun = ""
	m.showAll = false
	m.scroll = 0
	m.follow = true
	m.clampTable()
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

// enter acts on the focused pane's selection (Enter / right arrow): a catalog
// pipeline row hands focus to its statistics pane, a run row in the
// statistics pane opens the full-screen log view (#238 C1d — nothing folds,
// so a lane row needs no Enter action beyond being selected).
func (m *psModel) enter() {
	switch m.pane {
	case psPaneLanes:
		if m.selPipeline == "" && m.selTable == "" {
			return
		}
		m.pane = psPaneStats
		m.clampTable()
	case psPaneStats:
		if m.selPipeline == "" {
			if m.tblPipeline == "" {
				return
			}
			// Drill: the catalog selection descends to the pipeline row.
			m.selectTree(psTreeRow{lane: m.selLane, pipeline: m.tblPipeline})
			return
		}
		if m.tblRun != "" {
			m.pinnedRun = m.tblRun
			m.logsOpen = true
			m.follow = true
			m.scroll = 0
		}
	}
}

// back retreats the focused pane's selection (left arrow): a pipeline row
// climbs to its lane row, a pipeline-scoped statistics pane returns to the
// catalog.
func (m *psModel) back() {
	switch m.pane {
	case psPaneLanes:
		if m.selPipeline != "" || m.selTable != "" {
			m.selectTree(psTreeRow{lane: m.selLane})
		}
	case psPaneStats:
		if m.selPipeline != "" || m.selTable != "" {
			m.selectTree(psTreeRow{lane: m.selLane})
			return
		}
		m.pane = psPaneLanes
	}
}
