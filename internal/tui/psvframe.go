package tui

// The #238 C1d frame: a catalog rail (filter box over a flush-left list,
// nothing folds), the statistics pane as the main surface, an engine-wide
// events pane, and a full-screen log view as the only raw-text surface.
// Raw log text never gets a resident pane — its three jobs split into the
// NOW line (live), the events digest (history), and the full-screen view
// (investigation).

import (
	"fmt"

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
	kind     int // 0 lane, 1 metrics, 2 pipeline, 3 blank
	lane     psLaneRow
	dead     int // lane rows: member pipelines whose latest run dead-lettered
	pipeline psPipelineRow
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
		if r.pipeline == "" {
			if len(entries) > 0 {
				entries = append(entries, catalogEntry{kind: 3})
			}
			if r.lane == m.selLane && m.selPipeline == "" {
				cursor = len(entries)
			}
			entries = append(entries, catalogEntry{kind: 0, lane: laneByName[r.lane], dead: deadByLane[r.lane]})
			entries = append(entries, catalogEntry{kind: 1, lane: laneByName[r.lane]})
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
		if i == cursor && (e.kind == 0 || e.kind == 2) {
			paintSelAccent(b, x+1, ry, colorless)
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
	if m.selPipeline != "" {
		renderPipelineStats(b, m, x, y, w, h, colorless)
		return
	}
	renderLaneStats(b, m, x, y, w, h, colorless)
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
		if r.State != "running" && r.State != "queued" {
			idLine = "last " + idLine
			if r.ExitCode != nil {
				idLine += fmt.Sprintf(" · exit %d", *r.ExitCode)
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
	b.text(x+8, y+5, ansiDim, clipCells("run durations arrive with engine timestamps (#200)", w-10))

	b.text(x+2, y+7, ansiDim, "TABLE")
	b.text(x+8, y+7, ansiDim, clipCells("table writes arrive with the journal aggregate (#238)", w-10))

	// Run history: the discovery path to run ids (⏎ opens the full screen).
	tblY := y + 9
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
			renderTable(sub, 0, sub.h, runsColumns(visRuns), selIndex(m.tblRun, m.runKeys()), colorless)
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
	renderTable(sub, 0, sub.h, pipelinesColumns(rows, w >= 90, m.markedPipes), selIndex(m.tblPipeline, m.pipelineKeys()), colorless)
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
// the list box. Until the events route lands (#238 phase 4) the list carries
// its placeholder fact.
func renderEventsPane(b *screenBuf, m *psModel, x, y, w, h int, colorless bool) {
	focused := m.pane == psPaneEvents
	renderFilterBox(b, x, y, w, "EVENTS · engine wide", "/ type to filter — pipeline, table, severity",
		focused, m.evtInput, m.evtFilter, "", colorless)
	m.addClick(psClick{x: x, y: y, w: w, h: psFilterBoxH, kind: psClickPsEvtFilter})

	ly := y + psFilterBoxH
	lh := h - psFilterBoxH
	b.box(x, ly, w, lh, ansiBorder, "", "")
	m.addClick(psClick{x: x, y: ly, w: w, h: lh, kind: psClickPane, pane: psPaneEvents})
	b.text(x+2, ly+1, ansiDim, clipCells("events arrive with the events route (#238) · state changes only, never raw text", w-4))
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

// renderPsBanner paints the justified brand banner rows and reports how many
// rows it spent (zero when the frame cannot afford or fit it).
func renderPsBanner(b *screenBuf, w, h int, colorless bool) int {
	if h < psBannerMinHeight {
		return 0
	}
	art := psBanner(w)
	if art == nil {
		return 0
	}
	for i, row := range art {
		sgr := bannerRowSGR(i)
		if colorless {
			sgr = ""
		}
		b.text(0, i, sgr, row)
	}
	return len(art)
}

// runsColumns builds the statistics pane's run history columns. WROTE and
// ELAPSED wait on their engine data (#238 phases 2 and 3).
func runsColumns(runs []api.PsRun) []psColumn {
	n := len(runs)
	return []psColumn{
		psCol("RUN", n, func(i int) string { return runs[i].ID }),
		psColStyled("STATE", n, func(i int) (string, string) {
			s := runs[i].State
			if s == "dead_lettered" {
				return "✖ dead", ansiRed
			}
			return s, psStateSGR(s)
		}),
		psCol("EXIT", n, func(i int) string { return exitCodeCell(runs[i].ExitCode) }),
		psCol("CPU", n, func(i int) string { return cpuText(runs[i].Load) }),
		psCol("MEM", n, func(i int) string { return memText(runs[i].Load) }),
	}
}
