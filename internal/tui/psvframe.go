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

const (
	// psBannerMinHeight is the frame height at which the three-row brand
	// banner earns its rows.
	psBannerMinHeight = 34
	// psFilterBoxH is a pane filter input box: borders around one input row.
	psFilterBoxH = 3
	// psEventsBoxH is the events list box height: borders + nine event rows.
	psEventsBoxH = 11
	// psEventsMinPaneH is the right-column height below which the events
	// pane sheds whole, leaving the statistics pane the full column.
	psEventsMinPaneH = 26
)

// renderFilterBox paints one idle-screen-style pane filter: a bordered input
// box carrying the pane title, with the dim placeholder, or the typed query
// (a trailing block cursor while the input holds typing focus).
func renderFilterBox(b *screenBuf, x, y, w int, title, placeholder string, focused, typing bool, query []rune, right string, colorless bool) {
	borderSGR, titleSGR, title := paneChrome(focused, colorless, title)
	b.box(x, y, w, psFilterBoxH, borderSGR, titleSGR, title)
	switch {
	case typing:
		b.text(x+2, y+1, ansiYellow, "/")
		b.text(x+4, y+1, "", clipCells(string(query)+"█", w-8))
	case len(query) > 0:
		b.text(x+2, y+1, ansiYellow, "/")
		b.text(x+4, y+1, "", clipCells(string(query), w-8))
	default:
		b.text(x+2, y+1, ansiDim, clipCells(placeholder, w-6))
	}
	if right != "" && len([]rune(right))+8 < w {
		b.text(x+w-2-len([]rune(right)), y+1, ansiDim, right)
	}
}

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
	kind     int // 0 lane, 1 metrics, 2 pipeline, 3 blank, 4 table
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

// renderCatalogPane paints the catalog rail: the filter box, then the
// flush-left list — every lane always showing its pipelines (#238 C1d: the
// only concealment is scroll, and the filter).
func renderCatalogPane(b *screenBuf, m *psModel, x, y, w, h int, colorless bool) {
	focused := m.pane == psPaneLanes
	right := ""
	if hidden := m.treeHidden(); hidden > 0 {
		right = fmt.Sprintf("%d hidden", hidden)
	}
	renderFilterBox(b, x, y, w, "CATALOG", "/ type to filter the catalog",
		focused, m.catInput, m.catFilter, right, colorless)
	m.addClick(psClick{x: x, y: y, w: w, h: psFilterBoxH, kind: psClickPsCatFilter})

	ly := y + psFilterBoxH
	lh := h - psFilterBoxH
	b.box(x, ly, w, lh, ansiBorder, "", "")
	m.addClick(psClick{x: x, y: ly, w: w, h: lh, kind: psClickPane, pane: psPaneLanes})

	rows := m.treeRows()
	if len(rows) == 0 {
		if len(m.catFilter) > 0 {
			b.text(x+2, ly+2, ansiDim, clipCells("no rows match · esc clears the filter", w-4))
		} else {
			b.text(x+2, ly+2, ansiDim, clipCells("no lanes yet", w-4))
			b.text(x+2, ly+3, ansiDim, clipCells(":catalog to start", w-4))
		}
		return
	}

	// Lane and pipeline rows come from treeRows (the filtered nav order);
	// metrics and blank rows are display-only interleavings.
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
	cursor := 0
	for _, r := range rows {
		switch {
		case r.pipeline == "" && r.table == "":
			if len(entries) > 0 {
				entries = append(entries, catalogEntry{kind: 3})
			}
			if r.lane == m.selLane && m.selPipeline == "" && m.selTable == "" {
				cursor = len(entries)
			}
			entries = append(entries, catalogEntry{kind: 0, lane: laneByName[r.lane], dead: deadByLane[r.lane]})
			entries = append(entries, catalogEntry{kind: 1, lane: laneByName[r.lane]})
		case r.table != "":
			if r.lane == m.selLane && r.table == m.selTable {
				cursor = len(entries)
			}
			entries = append(entries, catalogEntry{kind: 4, lane: laneByName[r.lane], table: r.table})
		default:
			if r.lane == m.selLane && r.pipeline == m.selPipeline && m.selTable == "" {
				cursor = len(entries)
			}
			entries = append(entries, catalogEntry{kind: 2, lane: laneByName[r.lane], pipeline: pipeByName[r.lane+"/"+r.pipeline]})
		}
	}

	sinceRun := map[string]uint64{}
	for _, r := range m.snap.Ps.Residents {
		sinceRun[r.Pipeline] = r.TurnsSinceRun
	}

	innerH := lh - 2
	top := 0
	if cursor >= innerH {
		top = cursor - innerH + 1
	}
	for i := top; i < len(entries) && i-top < innerH; i++ {
		ry := ly + 1 + (i - top)
		e := entries[i]
		switch e.kind {
		case 0:
			m.addClick(psClick{x: x + 1, y: ry, w: w - 2, kind: psClickLane, lane: e.lane.name})
			b.text(x+2, ry, "", e.lane.name)
			badge := fmt.Sprintf("%dr·%dq", e.lane.running, e.lane.queued)
			badgeSGR := ansiDim
			if e.lane.running > 0 {
				badgeSGR = ansiCyan
			}
			if e.dead > 0 {
				badge += fmt.Sprintf("·%d✖", e.dead)
				badgeSGR = ansiRed
			}
			b.text(x+w-2-len([]rune(badge)), ry, badgeSGR, badge)
		case 1:
			cpu, mem := cpuText(e.lane.load), memText(e.lane.load)
			b.text(x+2, ry, ansiDim, cpu+" "+mem)
			sx := x + 2 + len([]rune(cpu)) + 1 + len([]rune(mem)) + 1
			sw := x + w - 2 - sx
			b.renderHeatStrip(sx, ry, sw, m.stripCPU("l:"+e.lane.name, sw))
		case 4:
			m.addClick(psClick{x: x + 1, y: ry, w: w - 2, kind: psClickRailTable, lane: e.lane.name, name: e.table})
			b.text(x+2, ry, "", clipCells(e.table, w-12))
			if d := latestRunDelta(m.snap, e.table); d != 0 {
				badge := fmt.Sprintf("%+d", d)
				b.text(x+w-2-len([]rune(badge)), ry, ansiCyan, badge)
			}
		case 2:
			m.addClick(psClick{x: x + 1, y: ry, w: w - 2, kind: psClickRailPipeline, lane: e.lane.name, name: e.pipeline.name})
			dot, dotSGR := catalogDot(e.pipeline)
			if m.markedPipes[e.pipeline.name] {
				dot, dotSGR = "●", ansiMagenta
			}
			b.text(x+2, ry, dotSGR, dot)
			m.addClick(psClick{x: x + 2, y: ry, w: 1, kind: psClickMarkPipeline, name: e.pipeline.name})
			b.text(x+4, ry, "", clipCells(e.pipeline.name, w-12))
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
		if i == cursor && (e.kind == 0 || e.kind == 2 || e.kind == 4) {
			paintSelAccent(b, x+1, ry, w-2, colorless)
		}
	}
}

// bottomHint splices a right-aligned dim hint into a box's bottom border row.
func bottomHint(b *screenBuf, x, y, w int, hint string) {
	if hint == "" || len([]rune(hint))+6 > w {
		return
	}
	b.text(x+w-3-len([]rune(hint)), y, ansiDim, " "+hint+" ")
}

// statsStripRow paints one labeled heat-strip row with a right-aligned value.
func statsStripRow(b *screenBuf, x, y, w int, label string, samples []float64, val string) {
	b.text(x+2, y, ansiDim, label)
	stripX := x + 8
	stripW := x + w - 3 - len([]rune(val)) - 2 - stripX
	if stripW < 8 {
		return
	}
	b.renderHeatStrip(stripX, y, stripW, samples)
	b.text(x+w-3-len([]rune(val)), y, "", val)
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

// renderTableStats is the statistics pane's table shape (#238 C1d): writer,
// watermark, write rate, the ops split, and the runs that wrote it — the
// provenance walk's on-frame doorway.
func renderTableStats(b *screenBuf, m *psModel, x, y, w, h int, colorless bool) {
	name := m.selTable
	j := m.snap.Journal
	borderSGR, titleSGR, title := paneChrome(m.pane == psPaneStats, colorless, "TABLE · "+name)
	b.box(x, y, w, h, borderSGR, titleSGR, title)
	m.addClick(psClick{x: x, y: y, w: w, h: h, kind: psClickPane, pane: psPaneStats})
	if h < 6 {
		return
	}
	bottomHint(b, x, y+h-1, w, "⏎ run → full-screen logs · :data provenance for the walk")
	if j == nil {
		b.text(x+3, y+2, ansiDim, clipCells("journal activity unavailable", w-6))
		return
	}

	rows, watermark, undoOpen, undoPromoted := j.tableTotals(name)
	idLine := fmt.Sprintf("%s · %d rows captured · writer %s · lane %s", name, rows, orDash(j.tableWriter(name)), m.selLane)
	wm := fmt.Sprintf("watermark %d", watermark)
	leftW := w - 4
	if len([]rune(idLine))+len([]rune(wm))+8 <= w {
		b.text(x+w-3-len([]rune(wm)), y+1, ansiDim, wm)
		leftW = w - 7 - len([]rune(wm))
	}
	b.text(x+2, y+1, "", clipCells(idLine, leftW))

	// WRITE RATE: the per-poll delta history, percent-of-peak like MEM strips.
	rate := j.Rate[name]
	peak := 0.0
	var latest float64
	for _, v := range rate {
		if v > peak {
			peak = v
		}
	}
	if len(rate) > 0 {
		latest = rate[len(rate)-1]
	}
	scaled := make([]float64, len(rate))
	for i, v := range rate {
		if peak > 0 {
			scaled[i] = v / peak * 100
		}
	}
	rateVal := fmt.Sprintf("%d rows this poll · %d peak", int64(latest), int64(peak))
	statsStripRow(b, x, y+3, w, "RATE", scaled, rateVal)

	ops := opsSplit(j, name)
	b.text(x+2, y+4, ansiDim, "OPS")
	undo := fmt.Sprintf("undo open %d / promoted %d", undoOpen, undoPromoted)
	b.text(x+8, y+4, "", clipCells(ops, w-14-len([]rune(undo))))
	b.text(x+w-3-len([]rune(undo)), y+4, ansiDim, undo)

	// The runs that wrote it, newest first: id, delta, op, span, state, range.
	tblY := y + 6
	tblH := y + h - 1 - tblY
	if tblH < 2 {
		return
	}
	type wrote struct {
		id string
		w  psRunWrites
	}
	var writers []wrote
	for id, per := range j.ByRun {
		if ww, ok := per[name]; ok {
			writers = append(writers, wrote{id: id, w: ww})
		}
	}
	sort.Slice(writers, func(a, b int) bool { return writers[a].w.MaxID > writers[b].w.MaxID })
	n := len(writers)
	cols := []psColumn{
		psCol("RUN", n, func(i int) string { return writers[i].id }),
		psCol("WROTE", n, func(i int) string { return fmt.Sprintf("%+d", signedRows(writers[i].w)) }),
		psCol("OP", n, func(i int) string { return shortOp(writers[i].w.Op) }),
		psColStyled("STATE", n, func(i int) (string, string) {
			if run, ok := findRun(m.snap, writers[i].id); ok {
				if run.State == "dead_lettered" {
					return "✖ dead", ansiRed
				}
				return run.State, psStateSGR(run.State)
			}
			return "-", ansiDim
		}),
		psCol("ELAPSED", n, func(i int) string {
			if run, ok := findRun(m.snap, writers[i].id); ok {
				return orDash(runSpan(run))
			}
			return "-"
		}),
		psCol("JOURNAL RANGE", n, func(i int) string {
			return fmt.Sprintf("%d → %d", writers[i].w.MinID, writers[i].w.MaxID)
		}),
	}
	sel := -1
	for i, ww := range writers {
		if ww.id == m.tblRun {
			sel = i
		}
	}
	sub := newScreenBuf(w-4, tblH)
	renderTable(sub, 0, sub.h, cols, sel, colorless)
	b.blit(sub, x+2, tblY)
	visible := tblH - 1
	top := 0
	if sel >= visible {
		top = sel - visible + 1
	}
	for r := top; r < n && r-top < visible; r++ {
		m.addClick(psClick{x: x + 1, y: tblY + 1 + (r - top), w: w - 2, kind: psClickTableRow, name: writers[r].id})
	}
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

// renderPipelineStats is the statistics pane's pipeline shape: run identity,
// load strips, the TIME and TABLE placeholders, the run history, and the NOW
// line — the frame's one live raw-text row.
func renderPipelineStats(b *screenBuf, m *psModel, x, y, w, h int, colorless bool) {
	name := m.selPipeline
	title := name + " · lane " + m.selLane
	runs := deriveRuns(m.snap, name, true)
	if len(runs) > 0 && runs[0].State == "dead_lettered" {
		title = "✖ " + title
	}
	borderSGR, titleSGR, title := paneChrome(m.pane == psPaneStats, colorless, title)
	b.box(x, y, w, h, borderSGR, titleSGR, title)
	m.addClick(psClick{x: x, y: y, w: w, h: h, kind: psClickPane, pane: psPaneStats})
	if h < 6 {
		return
	}
	bottomHint(b, x, y+h-1, w, "⏎ run → full-screen logs")

	// Run identity: the newest run, its state, and the recorded count.
	idLine := "no runs recorded"
	if len(runs) > 0 {
		r := runs[0]
		idLine = "run " + r.ID + " · " + r.State
		if r.State == "running" && r.Elapsed != "" {
			idLine += " " + r.Elapsed
		}
		if r.State != "running" && r.State != "queued" {
			idLine = "last " + idLine
			if r.ExitCode != nil {
				idLine += fmt.Sprintf(" · exit %d", *r.ExitCode)
			}
			if r.Duration != "" {
				idLine += " · " + r.Duration
			}
		}
	}
	count := fmt.Sprintf("%d runs recorded", len(runs))
	leftW := w - 4
	if len([]rune(idLine))+len([]rune(count))+8 <= w {
		b.text(x+w-3-len([]rune(count)), y+1, ansiDim, count)
		leftW = w - 7 - len([]rune(count))
	}
	b.text(x+2, y+1, "", clipCells(idLine, leftW))

	// Load strips: CPU, MEM, and the TIME row that waits on issue #200.
	key := "p:" + name
	cpuNow, memNow := "-", "-"
	for _, p := range derivePipelines(m.snap, m.selLane) {
		if p.name == name {
			cpuNow, memNow = cpuText(p.load), memText(p.load)
		}
	}
	memVal := memNow + " now"
	if ring := m.stripRing(key); ring != nil {
		if peak := ring.memPeak(); peak > 0 {
			memVal += " · " + memBytes(peak) + " peak"
		}
	}
	statsStripRow(b, x, y+3, w, "CPU", m.stripCPU(key, w), cpuNow+" now")
	statsStripRow(b, x, y+4, w, "MEM", m.stripMem(key, w), memVal)
	b.text(x+2, y+5, ansiDim, "TIME")
	pt, hasTimes := pipeTimes(m.snap)[name]
	timeVal := ""
	if el := pipeElapsed(m.snap, name); el != "" {
		timeVal = el + " now"
	}
	if hasTimes {
		for _, part := range []string{pt.Avg + " avg", pt.Max + " max"} {
			if timeVal != "" {
				timeVal += " · "
			}
			timeVal += part
		}
	}
	switch {
	case hasTimes:
		stripX := x + 8
		stripW := x + w - 3 - len([]rune(timeVal)) - 2 - stripX
		if stripW >= 8 {
			b.text(stripX, y+5, ansiCyan, timeStripGlyphs(pt.Levels, stripW))
			b.text(x+w-3-len([]rune(timeVal)), y+5, "", timeVal)
		}
	case timeVal != "":
		b.text(x+8, y+5, "", timeVal)
	default:
		b.text(x+8, y+5, ansiDim, clipCells("no timed run recorded yet", w-10))
	}

	// TABLE: the tables this pipeline writes, from the journal aggregate.
	tableRows := pipelineTables(m.snap, name)
	if len(tableRows) == 0 {
		b.text(x+2, y+7, ansiDim, "TABLE")
		b.text(x+8, y+7, ansiDim, clipCells("no captured writes yet", w-10))
	} else {
		b.text(x+2, y+7, ansiDim, clipCells("TABLE                   OP        ROWS     Δ RUN   WATERMARK", w-4))
		for i, tr := range tableRows {
			if i >= 3 {
				break
			}
			line := fmt.Sprintf("%-22s  %-3s  %10d  %8s   %d", clipCells(tr.name, 22), shortOp(tr.op), tr.rows, fmt.Sprintf("%+d", tr.delta), tr.watermark)
			b.text(x+2, y+8+i, "", clipCells(line, w-4))
		}
	}
	tableN := len(tableRows)
	if tableN > 3 {
		tableN = 3
	}
	if tableN == 0 {
		tableN = 1 // the placeholder line
	}

	// Run history: the discovery path to run ids (⏎ opens the full screen).
	tblY := y + 8 + tableN
	nowY := y + h - 2
	tblH := nowY - tblY - 1
	if tblH >= 2 {
		visRuns := deriveRuns(m.snap, name, m.showAll)
		if len(visRuns) == 0 {
			hint := "no live runs · press a for full history"
			if m.showAll {
				hint = "no runs in history"
			}
			b.text(x+3, tblY, ansiDim, clipCells(hint, w-6))
		} else {
			sub := newScreenBuf(w-4, tblH)
			renderTable(sub, 0, sub.h, runsColumns(m, visRuns), selIndex(m.tblRun, m.runKeys()), colorless)
			b.blit(sub, x+2, tblY)
			visible := tblH - 1
			sel := selIndex(m.tblRun, m.runKeys())
			top := 0
			if sel >= visible {
				top = sel - visible + 1
			}
			keys := m.runKeys()
			for r := top; r < len(keys) && r-top < visible; r++ {
				m.addClick(psClick{x: x + 1, y: tblY + 1 + (r - top), w: w - 2, kind: psClickTableRow, name: keys[r]})
			}
		}
	}

	renderNowLine(b, m, x, nowY, w)
}

// renderNowLine paints the statistics pane's NOW row: the newest captured
// line of the watched run while it runs, absence otherwise — never a stale
// line dressed as live.
func renderNowLine(b *screenBuf, m *psModel, x, y, w int) {
	b.text(x+2, y, ansiDim, "NOW")
	target := m.logsTarget()
	run, ok := findRun(m.snap, target)
	if !ok || run.State != "running" || m.snap.LogsRun != target || len(m.snap.Logs) == 0 {
		b.text(x+8, y, ansiDim, clipCells("▸ no process alive · absence renders as absence", w-10))
		return
	}
	line := m.snap.Logs[len(m.snap.Logs)-1]
	b.text(x+8, y, "", clipCells("▸ "+line, w-10-6))
	b.text(x+w-3-len("live"), y, ansiCyan, "live")
}

// renderLaneStats is the statistics pane's lane shape: lane totals, lane load
// strips, and the member pipeline table (share-of-lane-time lands with #200).
func renderLaneStats(b *screenBuf, m *psModel, x, y, w, h int, colorless bool) {
	name := m.selLane
	borderSGR, titleSGR, title := paneChrome(m.pane == psPaneStats, colorless, "LANE · "+name)
	b.box(x, y, w, h, borderSGR, titleSGR, title)
	m.addClick(psClick{x: x, y: y, w: w, h: h, kind: psClickPane, pane: psPaneStats})
	if h < 6 || name == "" {
		if name == "" {
			b.text(x+3, y+2, ansiDim, "no lane selected")
		}
		return
	}

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
	memVal := memText(lane.load) + " now"
	if ring := m.stripRing(key); ring != nil {
		if peak := ring.memPeak(); peak > 0 {
			memVal += " · " + memBytes(peak) + " peak"
		}
	}
	statsStripRow(b, x, y+3, w, "CPU", m.stripCPU(key, w), cpuText(lane.load)+" now")
	statsStripRow(b, x, y+4, w, "MEM", m.stripMem(key, w), memVal)

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

// renderEventsPane paints the engine-wide events digest: its filter box and
// the list box, newest first. Rows are poller-derived state changes (#238
// phase 4) — never raw log text; the stamp is when this view observed the
// change.
func renderEventsPane(b *screenBuf, m *psModel, x, y, w, h int, colorless bool) {
	focused := m.pane == psPaneEvents
	rows := m.filteredEvents()
	right := ""
	if hidden := len(m.snap.Events) - len(rows); hidden > 0 {
		right = fmt.Sprintf("%d hidden", hidden)
	}
	renderFilterBox(b, x, y, w, "EVENTS · engine wide", "/ type to filter — pipeline, table, severity",
		focused, m.evtInput, m.evtFilter, right, colorless)
	m.addClick(psClick{x: x, y: y, w: w, h: psFilterBoxH, kind: psClickPsEvtFilter})

	ly := y + psFilterBoxH
	lh := h - psFilterBoxH
	hint := fmt.Sprintf("state changes only, never raw text · %d observed", len(m.snap.Events))
	b.box(x, ly, w, lh, ansiBorder, "", "")
	bottomHint(b, x, ly+lh-1, w, hint)
	m.addClick(psClick{x: x, y: ly, w: w, h: lh, kind: psClickPane, pane: psPaneEvents})

	if len(rows) == 0 {
		if len(m.snap.Events) == 0 {
			b.text(x+2, ly+1, ansiDim, clipCells("nothing observed yet · state changes land here as they happen", w-4))
		} else {
			b.text(x+2, ly+1, ansiDim, clipCells("no events match · esc clears the filter", w-4))
		}
		return
	}
	innerH := lh - 2
	// Newest first: the digest reads like notifications, not a tail.
	for i := 0; i < innerH && i < len(rows); i++ {
		e := rows[len(rows)-1-i]
		ry := ly + 1 + i
		b.text(x+2, ry, ansiDim, e.Stamp)
		g, sgr := e.Severity.glyph()
		b.text(x+12, ry, sgr, g)
		b.text(x+15, ry, "", clipCells(e.Text, w-17))
	}
}

// renderLogsFull paints the full-screen log view: the frame's investigation
// surface, one run's whole capture with follow and scrollback.
func renderLogsFull(b *screenBuf, m *psModel, x, y, w, h int, colorless bool) {
	target := m.logsTarget()
	title := "LOGS"
	if target != "" {
		mode := "following"
		if !m.follow {
			mode = "paused"
		}
		if run, ok := findRun(m.snap, target); ok {
			title = "LOGS · " + run.Pipeline + "/" + target + " · " + run.State
			if run.ExitCode != nil {
				title += fmt.Sprintf(" · exit %d", *run.ExitCode)
			}
			title += " · " + mode
		} else {
			title = "LOGS · " + target + " · " + mode
		}
	}
	borderSGR, titleSGR, title := paneChrome(true, colorless, title)
	b.box(x, y, w, h, borderSGR, titleSGR, title)

	innerH := h - 2
	if target == "" {
		b.text(x+2, y+1, ansiDim, "pick a run · ⏎ on a run row · or :logs <id>")
		return
	}
	logs := m.snap.Logs
	if m.snap.LogsRun != target {
		logs = nil
	}
	end := len(logs) - m.scroll
	if end < 0 {
		end = 0
	}
	start := end - innerH
	if start < 0 {
		start = 0
	}
	// The tail anchors to the pane's bottom like tail -f.
	shown := logs[start:end]
	yoff := innerH - len(shown)
	for i, line := range shown {
		paintLogLine(b, x+2, y+1+yoff+i, line)
	}
	b.text(x+3, y+h-1, ansiDim, " esc back · f follow · c cancel ")
	if len(logs) > 0 {
		tail := fmt.Sprintf(" %d lines ", len(logs))
		b.text(x+w-2-len([]rune(tail)), y+h-1, ansiDim, tail)
	}
}

// renderPsBanner paints the justified brand banner — one blank row above,
// one cell of side padding — and reports how many rows it spent (zero when
// the frame cannot afford or fit it). Color is one static diagonal brand
// gradient (banner.go). Depth is a right-edge extrusion: every glyph run
// casts one dark ▓ shadow cell. Sparkles twinkle over the surrounding blank
// cells: each position-hashed glint pops in and out on its own period,
// advanced one tick per absorbed poll (m.twinkle).
func renderPsBanner(b *screenBuf, m *psModel, w, h int, colorless bool) int {
	if h < psBannerMinHeight {
		return 0
	}
	art, _ := psBanner(w)
	if art == nil {
		return 0
	}
	shadow := map[[2]int]bool{}
	for i, row := range art {
		runes := []rune(row)
		for x, r := range runes {
			if r == ' ' {
				// The extrusion: a glyph run's right edge casts one shadow.
				if x > 0 && runes[x-1] != ' ' {
					sgr := bannerShadowSGR
					if colorless {
						sgr = ""
					}
					b.text(x, i+1, sgr, "▓")
					shadow[[2]int{x, i + 1}] = true
				}
				continue
			}
			sgr := bannerSweepSGR(x, i, w)
			if colorless {
				sgr = ""
			}
			b.text(x, i+1, sgr, string(r))
		}
	}
	// Sparkles: rows 0..3 (the breathing row included), blank cells only.
	blank := func(x, y int) bool {
		if shadow[[2]int{x, y}] {
			return false
		}
		if y == 0 {
			return true
		}
		runes := []rune(art[y-1])
		return x >= len(runes) || runes[x] == ' '
	}
	for y := 0; y <= len(art); y++ {
		for x := 0; x < w; x++ {
			glyph, sgr, ok := bannerSparkle(x, y, m.twinkle)
			if !ok || !blank(x, y) {
				continue
			}
			// Keep a one-cell quiet zone right of letters (the shadow edge).
			if y > 0 && x > 0 {
				runes := []rune(art[y-1])
				if x-1 < len(runes) && runes[x-1] != ' ' {
					continue
				}
			}
			if colorless {
				sgr = ""
			}
			b.text(x, y, sgr, glyph)
		}
	}
	return len(art) + 1
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
