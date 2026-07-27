package tui

// The #238 C1d frame: a catalog rail (filter box over a flush-left list,
// nothing folds), the statistics pane as the main surface, an engine-wide
// events pane, and a full-screen log view as the only raw-text surface.
// Raw log text never gets a resident pane — its three jobs split into the
// NOW line (live), the events digest (history), and the full-screen view
// (investigation).

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
)

// deadPipelines counts the pipelines whose newest run dead-lettered — the
// engine's terminal failure state, surfaced in the header and the rail.
func deadPipelines(s Snapshot) int {
	n := 0
	for _, l := range deriveLanes(s) {
		for _, p := range derivePipelines(s, l.name) {
			if p.latest == "dead_lettered" {
				n++
			}
		}
	}
	return n
}

// catalogEntry is one display row of the catalog rail.
type catalogEntry struct {
	kind     int // 0 lane, 2 pipeline, 4 table
	lane     psLaneRow
	dead     int // lane rows: member pipelines whose latest run dead-lettered
	pipeline psPipelineRow
	table    string // kind 4: "schema.table"
}

// catalogDot picks a pipeline row's state dot: run/queue activity, the
// dead-letter cross, or idle.
func catalogDot(p psPipelineRow) (string, string) {
	switch {
	case p.running > 0:
		return "●", ansiCyan
	case p.queued > 0:
		return "●", ansiYellow
	case p.latest == "dead_lettered":
		return "✖", ansiRed
	default:
		return "○", ansiDim
	}
}

// The rail's vertical blocks inside its one box.
const (
	// psRailFilterH is the filter block: the input row, its divider being that
	// row's own underline.
	psRailFilterH = 1
	// psRailFootFixedH is the lane summary's fixed part: the lane row and the
	// CPU and MEM strip rows.
	psRailFootFixedH = 3
	// psRailFootTables bounds the table rows the summary names; a lane with more
	// spends its last row saying how many it left out.
	psRailFootTables = 4
	// psRailFootMinList is the shortest list the summary will leave behind;
	// tighter rails keep every row for the tree.
	psRailFootMinList = 4
)

// railFootH is the lane summary's height for a lane owning n written tables.
func railFootH(n int) int {
	if n > psRailFootTables {
		n = psRailFootTables
	}
	return psRailFootFixedH + n
}

// renderCatalogPane paints the catalog rail in the statusline's chrome: side
// pipes, horizontal edges as SGR rules on the rows themselves, the title row
// carrying the gradient mark, then the filter row, the flush-left list — every
// lane always showing its pipelines (#238 C1d: the only concealment is scroll,
// and the filter) — and the selected lane's summary pinned to the bottom.
func renderCatalogPane(b *screenBuf, m *psModel, x, y, w, h int, colorless bool) {
	borderSGR, titleSGR, title := paneChrome(m.pane == psPaneLanes, colorless, "[CATALOG]")
	b.hairBox(x, y, w, h, borderSGR)
	m.addClick(psClick{x: x, y: y, w: w, h: h, kind: psClickPane, pane: psPaneLanes})

	// The title is a row of its own, wearing the wordmark's ramp; focus still
	// reads through the chrome colour the pipes and rules carry.
	paneTitle(b, x, y, titleSGR, title, colorless)
	defer b.ruleRow(x, y, w)          // the title row's edges, drawn last
	defer b.underlineRow(x, y+h-1, w) // the rail's bottom edge

	renderRailFilter(b, m, x, y+1, w)
	m.addClick(psClick{x: x + 1, y: y + 1, w: w - 2, kind: psClickPsCatFilter})
	b.underlineRow(x, y+1, w) // the filter's divider, as the row's own rule

	ly := y + 1 + psRailFilterH
	lh := h - 1 - psRailFilterH
	if footH := railFootH(len(m.laneTables()[m.selLane])); lh >= psRailFootMinList+footH {
		lh -= footH
		// The summary's divider, likewise the last list row's own rule —
		// deferred so the rows the list paints below still take it.
		defer b.underlineRow(x, ly+lh-1, w)
		renderRailFooter(b, m, x, ly+lh, w)
	}

	rows := m.treeRows()
	if len(rows) == 0 {
		if len(m.catFilter) > 0 {
			b.text(x+2, ly+1, ansiDim, clipCells("no rows match · esc clears the filter", w-4))
		} else {
			b.text(x+2, ly+1, ansiDim, clipCells("no lanes yet", w-4))
			b.text(x+2, ly+2, ansiDim, clipCells(":catalog to start", w-4))
		}
		return
	}

	// Lane headings and pipeline rows come from treeRows (the filtered nav
	// order); only the pipeline rows can hold the cursor.
	deadByLane := map[string]int{}
	for _, l := range deriveLanes(m.snap) {
		for _, p := range derivePipelines(m.snap, l.name) {
			if p.latest == "dead_lettered" {
				deadByLane[l.name]++
			}
		}
	}
	laneByName := map[string]psLaneRow{}
	for _, l := range deriveLanes(m.snap) {
		laneByName[l.name] = l
	}
	pipeByName := map[string]psPipelineRow{}
	for _, l := range deriveLanes(m.snap) {
		for _, p := range derivePipelines(m.snap, l.name) {
			pipeByName[l.name+"/"+p.name] = p
		}
	}

	var entries []catalogEntry
	cursor := -1
	for _, r := range rows {
		if r.pipeline == "" {
			entries = append(entries, catalogEntry{kind: 0, lane: laneByName[r.lane], dead: deadByLane[r.lane]})
			continue
		}
		if r.lane == m.selLane && r.pipeline == m.selPipeline {
			cursor = len(entries)
		}
		entries = append(entries, catalogEntry{kind: 2, lane: laneByName[r.lane], pipeline: pipeByName[r.lane+"/"+r.pipeline]})
	}

	sinceRun := map[string]uint64{}
	for _, r := range m.snap.Ps.Residents {
		sinceRun[r.Pipeline] = r.TurnsSinceRun
	}

	innerH := lh
	top := 0
	if cursor >= innerH {
		top = cursor - innerH + 1
	}
	for i := top; i < len(entries) && i-top < innerH; i++ {
		ry := ly + (i - top)
		e := entries[i]
		switch e.kind {
		case 0:
			// A lane heading names itself and its counts. It is not a cursor
			// stop; the lane the cursor sits in is marked instead.
			here := e.lane.name == m.selLane
			m.addClick(psClick{x: x + 1, y: ry, w: w - 2, kind: psClickLane, lane: e.lane.name})
			nameSGR := ""
			if here {
				nameSGR = ansiMagenta
			}
			b.text(x+2, ry, nameSGR, clipCells(e.lane.name, w-12))
			// Counts stay quiet; only the dead cross earns the alarm colour.
			badge := fmt.Sprintf("%dr·%dq", e.lane.running, e.lane.queued)
			if here {
				badge += fmt.Sprintf(" · %d pipelines", len(derivePipelines(m.snap, e.lane.name)))
			}
			badgeSGR := ansiDim
			if e.lane.running > 0 {
				badgeSGR = ansiCyan
			}
			cross := ""
			if e.dead > 0 {
				cross = fmt.Sprintf("%d✖", e.dead)
			}
			bx := x + w - 2 - len([]rune(badge))
			if cross != "" {
				bx -= len([]rune(cross)) + 1
				b.text(x+w-2-len([]rune(cross)), ry, ansiRed, cross)
			}
			b.text(bx, ry, badgeSGR, badge)
		case 2:
			m.addClick(psClick{x: x + 1, y: ry, w: w - 2, kind: psClickRailPipeline, lane: e.lane.name, name: e.pipeline.name})
			dot, dotSGR := catalogDot(e.pipeline)
			if m.markedPipes[e.pipeline.name] {
				dot, dotSGR = "●", ansiMagenta
			}
			b.text(x+2, ry, dotSGR, dot)
			m.addClick(psClick{x: x + 2, y: ry, w: 1, kind: psClickMarkPipeline, name: e.pipeline.name})
			nameSGR := ""
			if i == cursor {
				nameSGR = ansiBold
			}
			b.text(x+4, ry, nameSGR, clipCells(e.pipeline.name, w-12))
			badge, badgeSGR := "", ""
			switch {
			case e.pipeline.running > 0:
				badge, badgeSGR = "run", ansiCyan
			case e.pipeline.queued > 0:
				badge, badgeSGR = fmt.Sprintf("%dq", e.pipeline.queued), ansiYellow
			case e.pipeline.latest == "dead_lettered":
				badge, badgeSGR = "dead", ansiRed
			case sinceRun[e.pipeline.name] > 0:
				badge, badgeSGR = fmt.Sprintf("t+%d", sinceRun[e.pipeline.name]), ansiDim
			}
			if badge != "" {
				b.text(x+w-2-len([]rune(badge)), ry, badgeSGR, badge)
			}
		}
		// One background per row: the cursor's bright wash, else the quiet
		// lane wash over the block the cursor sits in.
		switch {
		case i == cursor:
			paintSelAccent(b, x+1, ry, w-2, colorless)
		case e.lane.name == m.selLane:
			paintLaneWash(b, x+1, ry, w-2, colorless)
		}
	}
}

// laneLoad is the lane's sampled load, nil when the snapshot carries none.
func (m *psModel) laneLoad(lane string) *api.PsLoad {
	for _, l := range deriveLanes(m.snap) {
		if l.name == lane {
			return l.load
		}
	}
	return nil
}

// renderRailFilter paints the rail's filter row: the dim placeholder, or the
// typed query (a trailing block cursor while the input holds typing focus),
// with the hidden-row count right-aligned.
func renderRailFilter(b *screenBuf, m *psModel, x, y, w int) {
	switch {
	case m.catInput:
		b.text(x+2, y, ansiYellow, "/")
		b.text(x+4, y, "", clipCells(string(m.catFilter)+"█", w-8))
	case len(m.catFilter) > 0:
		b.text(x+2, y, ansiYellow, "/")
		b.text(x+4, y, "", clipCells(string(m.catFilter), w-8))
	default:
		b.text(x+2, y, ansiDim, clipCells("/ type to filter", w-6))
	}
	if hidden := m.treeHidden(); hidden > 0 {
		right := fmt.Sprintf("%d hidden", hidden)
		if len([]rune(right))+8 < w {
			b.text(x+w-2-len([]rune(right)), y, ansiDim, right)
		}
	}
}

// renderRailFooter paints the rail's bottom summary for the lane the cursor
// sits in: the lane name with the stamp of the last commit this view observed,
// one row per written table with its newest delta, then the lane's CPU and MEM
// strips. Its divider is the underline of the row above (see
// renderCatalogPane). No clock math — the stamp is an observation, the deltas
// are watermark arithmetic.
func renderRailFooter(b *screenBuf, m *psModel, x, y, w int) {
	lane := m.selLane
	b.text(x+2, y, ansiDim, "LANE · ")
	b.text(x+9, y, ansiMagenta, clipCells(orDefault(lane, "none"), w-24))
	if stamp := m.laneLastCommit(lane); stamp != "" {
		label := "last " + stamp
		b.text(x+w-2-len([]rune(label)), y, ansiDim, "last ")
		b.text(x+w-2-len([]rune(stamp)), y, "", stamp)
	}

	ry := y + 1
	tables := m.laneTables()[lane]
	shown := tables
	if len(shown) > psRailFootTables {
		shown = shown[:psRailFootTables-1]
	}
	for _, name := range shown {
		m.addClick(psClick{x: x + 1, y: ry, w: w - 2, kind: psClickRailTable, lane: lane, name: name})
		nameSGR := ""
		if name == m.selTable {
			nameSGR = ansiMagenta
		}
		b.text(x+2, ry, nameSGR, clipCells(name, w-12))
		if d := latestRunDelta(m.snap, name); d != 0 {
			badge := fmt.Sprintf("%+d", d)
			b.text(x+w-2-len([]rune(badge)), ry, ansiCyan, badge)
		}
		ry++
	}
	if n := len(tables) - len(shown); n > 0 {
		b.text(x+2, ry, ansiDim, clipCells(fmt.Sprintf("+%d more tables", n), w-4))
		ry++
	}

	// The lane's load, the strip rows the tree no longer carries. The strips are
	// the day-deep ring (they span what the collector has recorded, growing to
	// its full depth and then rolling); the numbers stay live.
	key := "l:" + lane
	load := m.scopeLoad(m.laneLoad(lane))
	cpuVal, memVal := cpuText(load), memText(load)
	valW := loadValW(cpuVal, memVal)
	railStripRow(b, x, ry, w, valW, "CPU", func(n int) []float64 { return m.dayCPU(key, n) }, cpuVal)
	railStripRow(b, x, ry+1, w, valW, "MEM", func(n int) []float64 { return m.dayMem(key, n) }, memVal)
}

// railStripRow paints one labelled heat strip in the rail summary with its
// value right-aligned in a valW-wide column. The strip's geometry comes from
// valW, never from this row's own reading, so the CPU and MEM bars line up and
// neither one re-fits when its number changes width. samples is asked for the
// width actually painted, so the bar spans the whole ring instead of being
// truncated to its newest cells.
func railStripRow(b *screenBuf, x, y, w, valW int, label string, samples func(int) []float64, val string) {
	b.text(x+2, y, ansiDim, label)
	sx := x + 2 + len([]rune(label)) + 1
	valX := x + w - 2 - valW
	sw := valX - 1 - sx
	if sw > 0 {
		b.renderHeatStrip(sx, y, sw, samples(sw))
	}
	b.text(x+w-2-len([]rune(val)), y, "", val)
}

// laneLastCommit is the newest commit stamp this view observed for the lane's
// pipelines ("" when it has seen none). Seq orders the marks: HH:MM:SS wraps
// at midnight, the poll ordinal does not.
func (m *psModel) laneLastCommit(lane string) string {
	best := psCommitMark{}
	for _, p := range derivePipelines(m.snap, lane) {
		if mark, ok := m.snap.Commits[p.name]; ok && mark.Seq > best.Seq {
			best = mark
		}
	}
	return best.Stamp
}

// statsStripRow paints one labeled heat-strip row with its value right-aligned
// in a valW-wide column. Like railStripRow the strip's geometry is valW's, not
// this reading's, so the rows align and the bar holds still as the numbers move.
func statsStripRow(b *screenBuf, x, y, w, valW int, label string, samples func(int) []float64, val string) {
	b.text(x+2, y, ansiDim, label)
	stripX := x + 8
	stripW := x + w - 2 - valW - 2 - stripX
	if stripW < 8 {
		return
	}
	b.renderHeatStrip(stripX, y, stripW, samples(stripW))
	b.text(x+w-2-len([]rune(val)), y, "", val)
}

// psLoadValW floors the load readout column so the common width changes -- a
// CPU crossing 10%, a MEM crossing into MiB -- never move the bar beside it.
// "1023.9MiB" is the widest ordinary reading.
const psLoadValW = 9

// loadValW is the column two load readouts share: the wider of them, never
// below the floor.
func loadValW(a, b string) int {
	return max(psLoadValW, len([]rune(a)), len([]rune(b)))
}

// renderStatsPane paints the frame's main surface: the selected pipeline's
// statistics, or the selected lane's. Rows the engine cannot fill yet name
// the issue that fills them — placeholders are facts here, not apologies.
func renderStatsPane(b *screenBuf, m *psModel, x, y, w, h int, colorless bool) {
	switch {
	case m.selTable != "":
		renderTableStats(b, m, x, y, w, h, colorless)
	case m.selPipeline != "":
		renderPipelineStats(b, m, x, y, w, h, colorless)
	default:
		renderLaneStats(b, m, x, y, w, h, colorless)
	}
}

// latestRunDelta is the newest writing run's rows into one table — the
// catalog badge's Δ. Zero when the journal holds nothing for it.
func latestRunDelta(s Snapshot, table string) int64 {
	j := s.Journal
	if j == nil {
		return 0
	}
	var best psRunWrites
	for _, per := range j.ByRun {
		w, ok := per[table]
		if ok && w.MaxID > best.MaxID {
			best = w
		}
	}
	if best.Op == "delete" {
		return -best.Rows
	}
	return best.Rows
}

// renderTableStats is the detail pane's table shape: the two-column body
// scoped to one written table -- its identity and undo ledger on the left,
// the write rate over the runs that wrote it on the right. The provenance
// walk's on-frame doorway.
func renderTableStats(b *screenBuf, m *psModel, x, y, w, h int, colorless bool) {
	sc := tableSpecScope(m)
	borderSGR, titleSGR, mark := paneChrome(m.pane == psPaneStats, colorless, "[TABLE]")
	b.hairBox(x, y, w, h, borderSGR)
	m.addClick(psClick{x: x, y: y, w: w, h: h, kind: psClickPane, pane: psPaneStats})
	paneTitle(b, x, y, titleSGR, mark, colorless)
	paneSubject(b, x, y, w, m.selTable, detailDead(sc.runs))
	// Both edges are the caller's, deferred so every glyph painted below still
	// takes them -- and so even an early return leaves the card whole.
	defer b.ruleRow(x, y, w)
	defer b.underlineRow(x, y+h-1, w)
	if h < 6 {
		return
	}
	if m.snap.Journal == nil {
		b.text(x+2, y+1, ansiDim, clipCells("journal activity unavailable", w-4))
		return
	}
	renderDetailPane(b, m, sc, tableDetailRuns(m, sc), x, y, w, h, colorless)
	paneHint(b, x, y+h-1, w, "c cancel · :data provenance for the walk")
}

// pipelineTableRow is one table row of the pipeline statistics pane.
type pipelineTableRow struct {
	name      string
	op        string
	rows      int64
	delta     int64
	watermark int64
}

// pipelineTables lists the tables a pipeline's runs wrote, from the journal
// aggregate, descending watermark (the busiest current table first).
func pipelineTables(s Snapshot, pipeline string) []pipelineTableRow {
	j := s.Journal
	if j == nil {
		return nil
	}
	newest := ""
	for _, r := range s.Ps.Runs {
		if r.Pipeline == pipeline {
			newest = r.ID
			break
		}
	}
	var out []pipelineTableRow
	for _, k := range j.tableKeys() {
		t := j.Tables[k]
		name := t.Schema + "." + t.Table
		if t.Writer != pipeline {
			continue
		}
		var delta int64
		if newest != "" {
			if w, ok := j.ByRun[newest][name]; ok {
				delta = w.Rows
				if w.Op == "delete" {
					delta = -delta
				}
			}
		}
		out = append(out, pipelineTableRow{name: name, op: t.Op, rows: t.Rows, delta: delta, watermark: t.MaxID})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].watermark > out[b].watermark })
	return out
}

// signedRows renders deletes negative so the WROTE column reads as row flow.
func signedRows(w psRunWrites) int64 {
	if w.Op == "delete" {
		return -w.Rows
	}
	return w.Rows
}

// shortOp abbreviates a journal op for a column cell.
func shortOp(op string) string {
	switch op {
	case "insert":
		return "ins"
	case "update":
		return "upd"
	case "delete":
		return "del"
	}
	return op
}

// opsSplit renders one table's per-op captured-write counts.
func opsSplit(j *psJournal, name string) string {
	parts := []string{}
	for _, op := range []string{"insert", "update", "delete"} {
		var rows int64
		for _, k := range j.tableKeys() {
			t := j.Tables[k]
			if t.Schema+"."+t.Table == name && t.Op == op {
				rows += t.Rows
			}
		}
		parts = append(parts, fmt.Sprintf("%s %d", op, rows))
	}
	return strings.Join(parts, " · ")
}

// renderPipelineStats is the detail pane's pipeline shape: the two-column
// body scoped to the selected pipeline -- its output table, undo ledger and
// retention on the left, the write rate over its run history on the right.
func renderPipelineStats(b *screenBuf, m *psModel, x, y, w, h int, colorless bool) {
	sc := pipelineSpecScope(m)
	borderSGR, titleSGR, mark := paneChrome(m.pane == psPaneStats, colorless, "[PIPELINE]")
	b.hairBox(x, y, w, h, borderSGR)
	m.addClick(psClick{x: x, y: y, w: w, h: h, kind: psClickPane, pane: psPaneStats})
	paneTitle(b, x, y, titleSGR, mark, colorless)
	paneSubject(b, x, y, w, m.selPipeline, detailDead(sc.runs))
	defer b.ruleRow(x, y, w)
	defer b.underlineRow(x, y+h-1, w)
	if h < 6 {
		return
	}
	renderDetailPane(b, m, sc, pipelineDetailRuns(m, sc), x, y, w, h, colorless)
	paneHint(b, x, y+h-1, w, "c cancel")
}

// renderLaneStats is the statistics pane's lane shape: lane totals, lane load
// strips, and the member pipeline table (share-of-lane-time lands with #200).
func renderLaneStats(b *screenBuf, m *psModel, x, y, w, h int, colorless bool) {
	name := m.selLane
	borderSGR, titleSGR, mark := paneChrome(m.pane == psPaneStats, colorless, "[LANE]")
	b.hairBox(x, y, w, h, borderSGR)
	m.addClick(psClick{x: x, y: y, w: w, h: h, kind: psClickPane, pane: psPaneStats})
	paneTitle(b, x, y, titleSGR, mark, colorless)
	paneSubject(b, x, y, w, name, false)
	defer b.ruleRow(x, y, w)
	defer b.underlineRow(x, y+h-1, w)
	if h < 6 || name == "" {
		if name == "" {
			b.text(x+2, y+1, ansiDim, "no lane selected")
		}
		return
	}
	defer paneHint(b, x, y+h-1, w, "⏎ open · c cancel")

	var lane psLaneRow
	for _, l := range deriveLanes(m.snap) {
		if l.name == name {
			lane = l
		}
	}
	rows := derivePipelines(m.snap, name)
	counts := fmt.Sprintf("%d running · %d queued · %d pipelines", lane.running, lane.queued, len(rows))
	tail := "lane durations arrive with #200"
	leftW := w - 4
	if len([]rune(counts))+len([]rune(tail))+8 <= w {
		b.text(x+w-3-len([]rune(tail)), y+1, ansiDim, tail)
		leftW = w - 7 - len([]rune(tail))
	}
	b.text(x+2, y+1, "", clipCells(counts, leftW))

	key := "l:" + name
	load := m.scopeLoad(lane.load)
	memVal := memText(load) + " now"
	if ring := m.stripRing(key); ring != nil {
		if peak := ring.memPeak(); peak > 0 {
			memVal += " · " + memBytes(peak) + " peak"
		}
	}
	cpuVal := cpuText(load) + " now"
	valW := loadValW(cpuVal, memVal)
	statsStripRow(b, x, y+3, w, valW, "CPU", func(n int) []float64 { return m.stripCPU(key, n) }, cpuVal)
	statsStripRow(b, x, y+4, w, valW, "MEM", func(n int) []float64 { return m.stripMem(key, n) }, memVal)

	tblY := y + 6
	tblH := y + h - 1 - tblY
	if tblH < 2 {
		return
	}
	if len(rows) == 0 {
		b.text(x+3, tblY, ansiDim, clipCells("no pipelines in this lane · :catalog to install a pack", w-6))
		return
	}
	sub := newScreenBuf(w-4, tblH)
	renderTable(sub, 0, sub.h, pipelinesColumns(m, rows, w >= 90, m.markedPipes), selIndex(m.tblPipeline, m.pipelineKeys()), colorless)
	b.blit(sub, x+2, tblY)
	visible := tblH - 1
	sel := selIndex(m.tblPipeline, m.pipelineKeys())
	top := 0
	if sel >= visible {
		top = sel - visible + 1
	}
	keys := m.pipelineKeys()
	for r := top; r < len(keys) && r-top < visible; r++ {
		ry := tblY + 1 + (r - top)
		m.addClick(psClick{x: x + 1, y: ry, w: w - 2, kind: psClickTableRow, name: keys[r]})
		m.addClick(psClick{x: x + 2, y: ry, w: 1, kind: psClickMarkPipeline, name: keys[r]})
	}
}

// runsColumns builds the statistics pane's run history columns. ELAPSED is
// the engine's rendered span (#238 phase 2); WROTE and the journal range are
// the run's captured writes from the journal aggregate (phase 3).
func runsColumns(m *psModel, runs []api.PsRun) []psColumn {
	j := m.snap.Journal
	n := len(runs)
	return []psColumn{
		psCol("RUN", n, func(i int) string { return runs[i].ID }),
		psCol("WROTE", n, func(i int) string {
			if rows := j.runWrote(runs[i].ID); rows > 0 {
				return fmt.Sprintf("%+d", rows)
			}
			return "-"
		}),
		psColStyled("STATE", n, func(i int) (string, string) {
			s := runs[i].State
			if s == "dead_lettered" {
				return "✖ dead", ansiRed
			}
			return s, psStateSGR(s)
		}),
		psCol("ELAPSED", n, func(i int) string { return orDash(runSpan(runs[i])) }),
		psCol("EXIT", n, func(i int) string { return exitCodeCell(runs[i].ExitCode) }),
		psCol("CPU", n, func(i int) string { return cpuText(runs[i].Load) }),
		psCol("MEM", n, func(i int) string { return memText(runs[i].Load) }),
	}
}

// runSpan is a run's one rendered span: elapsed while running, duration once
// terminal, empty when the engine observed neither.
func runSpan(r api.PsRun) string {
	if r.State == "running" {
		return r.Elapsed
	}
	return r.Duration
}

// orDash renders an absent engine string as the dash cell.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// pipeTimes indexes the payload's per-pipeline duration aggregates by name.
func pipeTimes(s Snapshot) map[string]api.PsPipelineTime {
	out := make(map[string]api.PsPipelineTime, len(s.Ps.PipelineTimes))
	for _, t := range s.Ps.PipelineTimes {
		out[t.Pipeline] = t
	}
	return out
}

// pipeElapsed is the pipeline's newest running run's rendered age, "" when
// nothing runs.
func pipeElapsed(s Snapshot, pipeline string) string {
	for _, r := range s.Ps.Runs {
		if r.Pipeline == pipeline && r.State == "running" {
			return r.Elapsed
		}
	}
	return ""
}

// timeStripGlyphs maps the engine's quantized 1..8 duration levels onto bar
// glyphs, newest at the right edge, fitted to width.
func timeStripGlyphs(levels []int, w int) string {
	if len(levels) > w {
		levels = levels[len(levels)-w:]
	}
	ramp := []rune("▁▂▃▄▅▆▇█")
	out := make([]rune, 0, len(levels))
	for _, l := range levels {
		if l < 1 {
			l = 1
		}
		if l > 8 {
			l = 8
		}
		out = append(out, ramp[l-1])
	}
	return string(out)
}
