package tui

// The detail pane's two-column shape: a narrow spec column on the left
// (OUTPUT, SCHEMA, OPS, RETENTION) and the wide column on the right carrying
// the write-rate bar over the run table. Both the pipeline shape and the table
// shape are this layout -- they differ only in which table the spec column
// reads and which runs the wide column lists, so the blocks are shared and the
// two entry points are thin. A pane too narrow to hold both columns stacks
// them instead of clipping either.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
)

const (
	// psSpecMinW floors the spec column: a clipped table name plus its label.
	psSpecMinW = 22
	// psSpecMaxW ceilings it: the widest label/value pair, whole.
	psSpecMaxW = 30
	// psRunsCoreW is the run table's irreducible width: RUN WROTE OP STATE
	// ELAPSED. Below it the columns stack rather than split.
	psRunsCoreW = 37
	// psRunsCauseW is the width that also affords TRIGGER.
	psRunsCauseW = 50
	// psRunsFullW is the width that affords every column.
	psRunsFullW = 62
	// psSpecGap parts two spec blocks.
	psSpecGap = 1
	// psSchemaMaxRows caps the SCHEMA block's listed columns; the heading
	// carries the true count, so a cap never hides that there are more.
	psSchemaMaxRows = 6
)

// paneSplit is the detail pane's resolved column geometry. When split is
// false the columns stack: lx/lw describe the single column.
type paneSplit struct {
	lx, lw int // spec column
	rx, rw int // wide column
	split  bool
}

// splitPane resolves the two-column geometry over an interior of width iw
// starting at column ix. The spec column takes two sevenths, clamped to its
// floor and ceiling (the rail's clamp idiom); the wide column takes the rest
// less a three-cell gutter. Nothing is drawn in the gutter -- the rail holds
// its name from its badge the same way. Too little left for the run table's
// core columns and the pane stacks instead.
func splitPane(ix, iw int) paneSplit {
	lw := min(max(iw*2/7, psSpecMinW), psSpecMaxW)
	rw := iw - lw - 3
	if lw+3 >= iw || rw < psRunsCoreW {
		return paneSplit{lx: ix, lw: iw, rx: ix, rw: iw}
	}
	return paneSplit{lx: ix, lw: lw, rx: ix + lw + 3, rw: rw, split: true}
}

// runsTier is how many run-table columns a wide column of width w affords:
// the full set, the set less JOURNAL, or the core five. Columns shed whole --
// a clipped header reads as a bug, a missing one as a narrow terminal.
func runsTier(w int) int {
	switch {
	case w >= psRunsFullW:
		return 2
	case w >= psRunsCauseW:
		return 1
	default:
		return 0
	}
}

// specHead paints one spec section heading: the title dim uppercase, an
// optional dim right-aligned count beside it, and the row's own underline --
// the block delimiter the rail uses, costing no extra row.
func specHead(b *screenBuf, x, y, w int, title, right string) {
	b.text(x, y, ansiDim, clipCells(title, w))
	if right != "" && len([]rune(title))+len([]rune(right))+2 <= w {
		b.text(x+w-len([]rune(right)), y, ansiDim, right)
	}
	b.underlineRow(x, y, w)
}

// specTypeW is the type column the SCHEMA block's rows share: the widest type
// among them, so no row re-cuts its neighbours as the list scrolls and no
// token is clipped (loadValW's rule, over a declared shape).
func specTypeW(cols []api.ColumnShape) int {
	w := 0
	for _, c := range cols {
		if n := len([]rune(c.Type)); n > w {
			w = n
		}
	}
	return w
}

// specFieldW is the value column a labelled spec row leaves after its label:
// frozen by the label, so a value that grows never re-cuts the label.
func specFieldW(w int, label string) int { return w - len([]rune(label)) - 2 }

// specRow paints one label/value row: the label left dim, the value right
// aligned in a valW column so a value that grows a character never shifts its
// neighbours (loadValW's rule, applied to text).
func specRow(b *screenBuf, x, y, w, valW int, label, val, valSGR string) {
	b.text(x, y, ansiDim, clipCells(label, w-valW-1))
	if len([]rune(val)) > valW {
		val = clipCells(val, valW)
	}
	b.text(x+w-len([]rune(val)), y, valSGR, val)
}

// specStripRow paints one labeled heat-strip row inside the spec column: the
// label left dim, the bar between, the reading right-aligned in a frozen valW
// column so the bar holds still as the numbers move (statsStripRow's rule at
// the spec column's tighter margins).
func specStripRow(b *screenBuf, x, y, w, valW int, label string, samples func(int) []float64, val string) {
	b.text(x, y, ansiDim, label)
	stripX := x + len([]rune(label)) + 1
	stripW := x + w - valW - 1 - stripX
	if stripW >= 6 {
		b.renderHeatStrip(stripX, y, stripW, samples(stripW))
	}
	b.text(x+w-len([]rune(val)), y, "", val)
}

// specScope is what the spec column describes: the table its blocks read, the
// pipeline that owns it (empty on the table shape), and the runs the
// retention block counts.
type specScope struct {
	table    string // "schema.table"; empty when nothing has been written yet
	pipeline string
	runs     []api.PsRun
}

// pipelineSpecScope scopes the spec column to the selected pipeline: its
// busiest written table and its whole recorded run history.
func pipelineSpecScope(m *psModel) specScope {
	sc := specScope{pipeline: m.selPipeline, runs: deriveRuns(m.snap, m.selPipeline, true)}
	if tables := pipelineTables(m.snap, m.selPipeline); len(tables) > 0 {
		sc.table = tables[0].name
	}
	return sc
}

// tableSpecScope scopes the spec column to the selected table and the runs of
// the pipeline that writes it.
func tableSpecScope(m *psModel) specScope {
	writer := m.snap.Journal.tableWriter(m.selTable)
	return specScope{table: m.selTable, pipeline: writer, runs: deriveRuns(m.snap, writer, true)}
}

// renderSpecOutput paints the OUTPUT block: the written table, the rows the
// journal captured into it, and the highest journal id it has reached. It
// never sheds -- a detail pane that cannot name its table is not a detail pane.
func renderSpecOutput(b *screenBuf, m *psModel, sc specScope, x, y, w int) int {
	specHead(b, x, y, w, "OUTPUT", "")
	if sc.table == "" {
		b.text(x, y+1, ansiDim, clipCells("no captured writes yet", w))
		return 2
	}
	rows, journalID, _, _ := m.snap.Journal.tableTotals(sc.table)
	specRow(b, x, y+1, w, specFieldW(w, "table"), "table", sc.table, "")
	specRow(b, x, y+2, w, 10, "rows captured", fmt.Sprintf("%d", rows), "")
	specRow(b, x, y+3, w, 10, "journal id", fmt.Sprintf("%d", journalID), ansiDim)
	// Clicking the table name is the doorway to the TABLE shape.
	m.addClick(psClick{x: x, y: y + 1, w: w, kind: psClickRailTable, lane: m.selLane, name: sc.table})
	return 4
}

// renderSpecLoad paints the LOAD block: the pipeline's CPU and resident
// strips and, when the engine has timed a run, its elapsed levels. A table
// owns no process, so the block renders only under the pipeline shape.
func renderSpecLoad(b *screenBuf, m *psModel, sc specScope, x, y, w, maxH int) int {
	if maxH < 3 || sc.pipeline == "" {
		return 0
	}
	key := "p:" + sc.pipeline
	load := m.scopeLoad(nil)
	for _, p := range derivePipelines(m.snap, m.selLane) {
		if p.name == sc.pipeline {
			load = m.scopeLoad(p.load)
		}
	}
	cpuVal, memVal := cpuText(load), memText(load)
	valW := loadValW(cpuVal, memVal)
	specHead(b, x, y, w, "LOAD", "")
	specStripRow(b, x, y+1, w, valW, "cpu", func(n int) []float64 { return m.stripCPU(key, n) }, cpuVal)
	specStripRow(b, x, y+2, w, valW, "mem", func(n int) []float64 { return m.stripMem(key, n) }, memVal)
	if maxH < 4 {
		return 3
	}
	pt, hasTimes := pipeTimes(m.snap)[sc.pipeline]
	el := pipeElapsed(m.snap, sc.pipeline)
	switch {
	case hasTimes:
		// The engine quantizes elapsed into levels, so this bar is glyphs the
		// daemon chose, not a strip this view scaled -- hence no sampler.
		val := pt.Max + " max"
		b.text(x, y+3, ansiDim, "time")
		b.text(x+w-len([]rune(val)), y+3, "", val)
		b.text(x+5, y+3, ansiCyan, timeStripGlyphs(pt.Levels, w-6-len([]rune(val))))
	case el != "":
		specRow(b, x, y+3, w, 10, "time", el+" now", "")
	default:
		b.text(x, y+3, ansiDim, clipCells("time  no timed run yet", w))
	}
	return 4
}

// renderSpecSchema paints the SCHEMA block: the declared columns and the type
// tokens the operator wrote. The heading carries the true column count, so
// the row cap never hides that there are more.
func renderSpecSchema(b *screenBuf, m *psModel, sc specScope, x, y, w, maxH int) int {
	if maxH < 2 {
		return 0
	}
	shape, declared := m.snap.Shapes[sc.table]
	if !declared {
		// Either the route has not answered yet or the table is not declared
		// under schemas/. Neither is a shape this view may invent.
		specHead(b, x, y, w, "SCHEMA", "")
		b.text(x, y+1, ansiDim, clipCells("no declared shape", w))
		return 2
	}
	specHead(b, x, y, w, "SCHEMA", fmt.Sprintf("· %d cols", len(shape.Columns)))
	room := min(maxH-1, psSchemaMaxRows)
	shown := shape.Columns
	if len(shown) > room {
		shown = shown[:room-1] // the last row goes to the +N more marker
	}
	typeW := specTypeW(shown)
	row := 0
	for _, c := range shown {
		name := c.Name
		if c.PrimaryKey {
			name = "· " + name
		}
		specRow(b, x, y+1+row, w, typeW, name, c.Type, ansiDim)
		row++
	}
	if rest := len(shape.Columns) - len(shown); rest > 0 {
		b.text(x, y+1+row, ansiDim, clipCells(fmt.Sprintf("+%d more", rest), w))
		row++
	}
	return row + 1
}

// renderSpecOps paints the OPS block: the undo ledger and the per-op split of
// the captured writes.
func renderSpecOps(b *screenBuf, m *psModel, sc specScope, x, y, w, maxH int) int {
	if maxH < 2 || sc.table == "" {
		return 0
	}
	_, _, undoOpen, undoPromoted := m.snap.Journal.tableTotals(sc.table)
	specHead(b, x, y, w, "OPS", "")
	specRow(b, x, y+1, w, 10, "undo open", fmt.Sprintf("%d", undoOpen), "")
	if maxH < 3 {
		return 2
	}
	specRow(b, x, y+2, w, 10, "promoted", fmt.Sprintf("%d", undoPromoted), ansiDim)
	if maxH < 4 {
		return 3
	}
	b.text(x, y+3, ansiDim, clipCells(compactOps(m.snap.Journal, sc.table), w))
	return 4
}

// compactOps is the per-op write split abbreviated for the spec column, ops
// that wrote nothing left out -- the full words never fit here, and a line
// clipped mid-word reads as a bug.
func compactOps(j *psJournal, name string) string {
	var parts []string
	for _, op := range []string{"insert", "update", "delete"} {
		var rows int64
		for _, k := range j.tableKeys() {
			t := j.Tables[k]
			if t.Schema+"."+t.Table == name && t.Op == op {
				rows += t.Rows
			}
		}
		if rows > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", shortOp(op), rows))
		}
	}
	if len(parts) == 0 {
		return "no writes"
	}
	return strings.Join(parts, " · ")
}

// renderSpecRetention paints the RETENTION block. Retention in iris is
// count-based and clockless, so the floor is a run id and the ledger is a
// count -- never a timestamp the engine does not hold.
func renderSpecRetention(b *screenBuf, m *psModel, sc specScope, x, y, w, maxH int) int {
	if maxH < 2 || len(sc.runs) == 0 {
		return 0
	}
	oldest := sc.runs[len(sc.runs)-1].ID
	specHead(b, x, y, w, "RETENTION", "")
	specRow(b, x, y+1, w, 10, "oldest kept", "run "+oldest, "")
	if maxH < 3 {
		return 2
	}
	// The kept count reads against the configured ceiling when the engine
	// reports one: a count-based retention with no visible ceiling says
	// nothing about how close pruning is.
	kept := fmt.Sprintf("%d", len(sc.runs))
	if r := m.snap.Ps.Retention; r != nil && r.Retain > 0 {
		kept = fmt.Sprintf("%d / %d", len(sc.runs), r.Retain)
	}
	specRow(b, x, y+2, w, 12, "runs kept", kept, ansiDim)
	if maxH < 4 {
		return 3
	}
	// Packs come from the cached listing, which resolves over the network and
	// so is fetched once rather than polled: an empty cache reads as absence.
	if packs := m.packsFor(sc.pipeline); len(packs) > 0 {
		specRow(b, x, y+3, w, specFieldW(w, "pack"), "pack", packs[0], ansiDim)
		return 4
	}
	return 3
}

// dispatchOf is the pipeline's live dispatch row and its lane's, from the ps
// readout's dispatch block. Both are nil when the answering node is a standby
// (it dispatches nothing) or the leader has not reconciled yet -- absence, never
// a fabricated "idle".
func (m *psModel) dispatchOf(pipeline string) (*api.PsDispatchPipeline, *api.PsDispatchLane) {
	d := m.snap.Ps.Dispatch
	if d == nil || pipeline == "" {
		return nil, nil
	}
	var row *api.PsDispatchPipeline
	for i := range d.Pipelines {
		if d.Pipelines[i].Pipeline == pipeline {
			row = &d.Pipelines[i]
			break
		}
	}
	if row == nil {
		return nil, nil
	}
	for i := range d.Lanes {
		if d.Lanes[i].Lane == row.Lane {
			return row, &d.Lanes[i]
		}
	}
	return row, nil
}

// dispatchStateSGR colours a lane's disposition: passing is live, parked recedes,
// eligible is about to move.
func dispatchStateSGR(state string) string {
	switch state {
	case api.DispatchPassing:
		return ansiGreen
	case api.DispatchParked:
		return ansiDim
	default:
		return ansiCyan
	}
}

// verdictSGR colours one gate edge's verdict: a poisoned edge is the only alarm,
// an open one is the only good news, and the waiting states recede.
func verdictSGR(verdict string) string {
	switch verdict {
	case "poisoned":
		return ansiRed
	case "open":
		return ansiGreen
	default:
		return ansiDim
	}
}

// gateLine renders a gate resolution for the block's summary row. An ungated
// pipeline is the doctrine's own answer to "when does this run": nothing gates it,
// so every pass runs it -- said plainly rather than as the bare word "ungated".
func gateLine(gate string) (string, string) {
	switch gate {
	case api.DispatchGateUngated:
		return "runs each pass", ansiDim
	case api.DispatchGateOpen:
		return "open", ansiGreen
	case api.DispatchGatePoisoned:
		return "poisoned", ansiRed
	case api.DispatchGateClosed:
		return "closed", ansiDim
	default:
		return "", ""
	}
}

// wakeText names the causes that unpark a lane: the care set spelled out when it
// fits the column, counted when it does not. A lane with an empty care set wakes on
// anything -- the walk built without care-sets -- which is a real answer, not a gap.
func wakeText(cares []string, w int) string {
	if len(cares) == 0 {
		return "any cause"
	}
	joined := strings.Join(cares, " · ")
	if len([]rune(joined)) <= w {
		return joined
	}
	return fmt.Sprintf("%d pipelines", len(cares))
}

// renderSpecDispatch paints the DISPATCH block: why this pipeline is or is not
// running, in the dispatcher's own vocabulary. Iris has no schedule -- no cron, no
// next-fire time, no backoff -- so the block never answers "when"; it answers what
// the lane is doing (passing, parked on the watermark, eligible), what its gate last
// decided, which upstreams it is still waiting on, and which causes would wake it.
//
// It renders only under a pipeline scope and only from a leader's readout: a standby
// answers /ps without a dispatch block, and the block disappears rather than
// claiming an idle engine.
func renderSpecDispatch(b *screenBuf, m *psModel, sc specScope, x, y, w, maxH int) int {
	if maxH < 2 || sc.pipeline == "" {
		return 0
	}
	row, lane := m.dispatchOf(sc.pipeline)
	if row == nil {
		return 0
	}
	specHead(b, x, y, w, "DISPATCH", "")
	n := 1
	// The lane's disposition first: it is the answer to the question the block
	// exists for, and the only row that never sheds.
	state, stateSGR := api.DispatchEligible, ansiCyan
	if lane != nil {
		state, stateSGR = lane.State, dispatchStateSGR(lane.State)
	}
	specRow(b, x, y+n, w, 10, "state", state, stateSGR)
	n++

	// The gate, then the per-edge ledger behind it: a closed gate is only
	// actionable once you can see which upstream it is waiting on.
	if val, sgr := gateLine(row.Gate); val != "" && n < maxH {
		specRow(b, x, y+n, w, specFieldW(w, "gate"), "gate", val, sgr)
		n++
	}
	for _, e := range row.Edges {
		if n >= maxH {
			break
		}
		specRow(b, x, y+n, w, 10, "  "+e.Upstream, e.Verdict, verdictSGR(e.Verdict))
		n++
	}

	// Position in the lane's serial walk: member N of M is why a pipeline whose
	// own gate is open can still be waiting -- the member ahead of it is running.
	if n < maxH && row.Lane != "" {
		pos := row.Lane
		if row.Members > 1 {
			pos = fmt.Sprintf("%s · %d of %d", row.Lane, row.Pos, row.Members)
		}
		specRow(b, x, y+n, w, specFieldW(w, "lane"), "lane", pos, ansiDim)
		n++
	}
	if n < maxH && lane != nil && lane.Passes > 0 {
		specRow(b, x, y+n, w, 10, "passes", fmt.Sprintf("%d", lane.Passes), ansiDim)
		n++
	}
	if n < maxH && lane != nil {
		specRow(b, x, y+n, w, specFieldW(w, "wakes on"), "wakes on", wakeText(lane.Cares, specFieldW(w, "wakes on")), ansiDim)
		n++
	}
	return n
}

// renderSpecColumn stacks the spec blocks down the left column within the height
// budget, parted by one blank row. Blocks shed from the bottom: OUTPUT always
// renders, RETENTION is the first to go. DISPATCH sits directly under OUTPUT --
// "why is this not running" outranks how much it costs or what shape it writes.
func renderSpecColumn(b *screenBuf, m *psModel, sc specScope, x, y, w, h int) {
	row := renderSpecOutput(b, m, sc, x, y, w)
	blocks := []func(int, int) int{
		func(yy, budget int) int { return renderSpecDispatch(b, m, sc, x, yy, w, budget) },
		func(yy, budget int) int { return renderSpecLoad(b, m, sc, x, yy, w, budget) },
		func(yy, budget int) int { return renderSpecSchema(b, m, sc, x, yy, w, budget) },
		func(yy, budget int) int { return renderSpecOps(b, m, sc, x, yy, w, budget) },
		func(yy, budget int) int { return renderSpecRetention(b, m, sc, x, yy, w, budget) },
	}
	for _, block := range blocks {
		budget := h - row - psSpecGap
		if budget < 2 {
			return
		}
		if n := block(y+row+psSpecGap, budget); n > 0 {
			row += psSpecGap + n
		}
	}
}

// renderRowsBar paints the wide column's head: the captured-rows heading with
// its readings right-aligned, and the bar itself.
//
// Two sources, each authoritative at its own zoom. The bar and the peak come
// from the daemon's coarse row buckets (a minute each, a day deep), re-seeded
// once a minute. `now` comes from the journal activity poll the view already
// runs every second, so the live reading never lags the bar's cadence.
//
// The window total is labelled by the ring's ACTUAL filled depth, never
// "today": the daemon has no midnight -- its clock is the sampling host's
// zone, not the operator's -- and the ring rolls at its cap, not at 00:00.
func renderRowsBar(b *screenBuf, m *psModel, key, table string, x, y, w int) int {
	ring := m.rowRings[key]
	if ring == nil || len(ring.buckets) == 0 {
		return renderRowsBarLive(b, m, table, x, y, w)
	}
	// The newest bucket is still filling, so it may not set the peak: a
	// half-sealed bucket compared against whole ones understates nothing but
	// would make the peak jitter downward as the window rolls.
	var peak, total int64
	for i, v := range ring.buckets {
		if v == psNoSample {
			continue
		}
		total += v
		if i < len(ring.buckets)-1 && v > peak {
			peak = v
		}
	}
	head := "ROWS / " + rowsBucketLabel(ring.bucketSeconds) + " · " + rowsWindowLabel(ring)
	right := fmt.Sprintf("%d now · %d peak · %d in %s",
		int64(m.snap.Journal.latestRate(table)), peak, total, rowsWindowLabel(ring))
	b.text(x, y, ansiDim, clipCells(head, w))
	if len([]rune(head))+len([]rune(right))+2 <= w {
		b.text(x+w-len([]rune(right)), y, ansiDim, right)
	}
	b.underlineRow(x, y, w) // the wide column's head takes the spec column's rule
	if peak <= 0 {
		b.text(x, y+1, ansiDim, clipCells("no captured writes in the window", w))
		return 2
	}
	b.renderHeatStrip(x, y+1, w, fitSamples(rowsPercentOfPeak(ring.buckets, peak), w))
	return 2
}

// renderRowsBarLive is the head before any recorded history has arrived: the
// per-poll deltas the journal fold already holds, named for what they are so
// the pane never dresses seconds of samples as a day of them.
func renderRowsBarLive(b *screenBuf, m *psModel, table string, x, y, w int) int {
	head := "ROWS / POLL · LIVE"
	rate := m.snap.Journal.rateOf(table)
	var latest, peak, total float64
	for _, v := range rate {
		if v > peak {
			peak = v
		}
		total += v
	}
	if len(rate) > 0 {
		latest = rate[len(rate)-1]
	}
	if len(rate) == 0 || peak == 0 {
		b.text(x, y, ansiDim, clipCells(head, w))
		b.underlineRow(x, y, w)
		b.text(x, y+1, ansiDim, clipCells("no captured writes observed yet", w))
		return 2
	}
	right := fmt.Sprintf("%d now · %d peak · %d in view", int64(latest), int64(peak), int64(total))
	b.text(x, y, ansiDim, clipCells(head, w))
	if len([]rune(head))+len([]rune(right))+2 <= w {
		b.text(x+w-len([]rune(right)), y, ansiDim, right)
	}
	b.underlineRow(x, y, w)
	scaled := make([]float64, len(rate))
	for i, v := range rate {
		scaled[i] = v / peak * 100
	}
	b.renderHeatStrip(x, y+1, w, fitSamples(scaled, w))
	return 2
}

// rowsPercentOfPeak scales the buckets against the window peak for the strip,
// keeping absence absent. A bucket that counted no rows scales to a real zero
// -- the strip's lowest tone, not a gap.
func rowsPercentOfPeak(buckets []int64, peak int64) []float64 {
	out := make([]float64, len(buckets))
	for i, v := range buckets {
		if v == psNoSample {
			out[i] = psNoSample
			continue
		}
		out[i] = float64(v) / float64(peak) * 100
	}
	return out
}

// rowsBucketLabel names one bucket's span for the heading.
func rowsBucketLabel(seconds int) string {
	switch {
	case seconds <= 0:
		return "BUCKET"
	case seconds%3600 == 0:
		return "HOUR"
	case seconds%60 == 0:
		return fmt.Sprintf("%dMIN", seconds/60)
	}
	return fmt.Sprintf("%dS", seconds)
}

// rowsWindowLabel names how much time the ring actually covers -- its filled
// depth, not its capacity. A daemon up three hours says 3h, never 24H.
func rowsWindowLabel(r *psRowRing) string {
	secs := len(r.buckets) * r.bucketSeconds
	switch {
	case secs <= 0:
		return "window"
	case secs >= 3600:
		return fmt.Sprintf("%dh", secs/3600)
	case secs >= 60:
		return fmt.Sprintf("%dm", secs/60)
	}
	return fmt.Sprintf("%ds", secs)
}

// detailRun is one row of the detail pane's run table: the run's identity and
// state from the payload, its captured writes from the journal aggregate.
type detailRun struct {
	id      string
	wrote   int64
	hasRows bool
	op      string
	state   string
	elapsed string
	minID   int64
	maxID   int64
	// cause is why the run was minted, the TRIGGER column: loop, manual,
	// replay, or propagated. Empty on a run the engine did not record one for.
	cause string
}

// pipelineDetailRuns lists the selected pipeline's runs, newest first, each
// carrying the writes the journal recorded for it.
func pipelineDetailRuns(m *psModel, sc specScope) []detailRun {
	j := m.snap.Journal
	runs := deriveRuns(m.snap, sc.pipeline, true)
	out := make([]detailRun, 0, len(runs))
	for _, r := range runs {
		lo, hi := j.runRange(r.ID)
		d := detailRun{
			id: r.ID, state: r.State, elapsed: runSpan(r),
			minID: lo, maxID: hi, cause: r.Cause,
		}
		if rows := j.runWrote(r.ID); rows != 0 {
			d.wrote, d.hasRows = rows, true
		}
		if w, ok := j.runWrite(r.ID, sc.table); ok {
			d.op = shortOp(w.Op)
		}
		out = append(out, d)
	}
	return out
}

// tableDetailRuns lists the runs that wrote the selected table, newest write
// first, each scoped to its writes into that one table.
func tableDetailRuns(m *psModel, sc specScope) []detailRun {
	j := m.snap.Journal
	if j == nil {
		return nil
	}
	var out []detailRun
	for id, per := range j.ByRun {
		w, ok := per[sc.table]
		if !ok {
			continue
		}
		d := detailRun{
			id: id, wrote: signedRows(w), hasRows: true, op: shortOp(w.Op),
			minID: w.MinID, maxID: w.MaxID, state: "-",
		}
		if run, ok := findRun(m.snap, id); ok {
			d.state, d.elapsed, d.cause = run.State, runSpan(run), run.Cause
		}
		out = append(out, d)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].maxID > out[b].maxID })
	return out
}

// runWriteColumns builds the detail pane's run table for a wide column of the
// given tier: the core five always, plus TRIGGER, plus JOURNAL.
func runWriteColumns(rows []detailRun, tier int) []psColumn {
	n := len(rows)
	cols := []psColumn{
		psCol("RUN", n, func(i int) string { return rows[i].id }),
		psCol("WROTE", n, func(i int) string {
			if !rows[i].hasRows {
				return "-"
			}
			return fmt.Sprintf("%+d", rows[i].wrote)
		}),
		psCol("OP", n, func(i int) string { return orDash(rows[i].op) }),
		psColStyled("STATE", n, func(i int) (string, string) {
			if rows[i].state == "dead_lettered" {
				return "✖ dead", ansiRed
			}
			return orDash(rows[i].state), psStateSGR(rows[i].state)
		}),
		psCol("ELAPSED", n, func(i int) string { return orDash(rows[i].elapsed) }),
	}
	if tier >= 2 {
		cols = append(cols, psCol("JOURNAL", n, func(i int) string {
			if rows[i].maxID == 0 {
				return "-"
			}
			return fmt.Sprintf("%d → %d", rows[i].minID, rows[i].maxID)
		}))
	}
	if tier >= 1 {
		cols = append(cols, psCol("TRIGGER", n, func(i int) string { return orDash(rows[i].cause) }))
	}
	return cols
}

// renderRunsTable blits the run table into the wide column and registers one
// clickable region per visible row.
func renderRunsTable(b *screenBuf, m *psModel, rows []detailRun, x, y, w, h int, colorless bool) {
	if h < 2 {
		return
	}
	if len(rows) == 0 {
		b.text(x, y, ansiDim, clipCells("no runs recorded", w))
		return
	}
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = r.id
	}
	sel := selIndex(m.tblRun, keys)
	sub := newScreenBuf(w, h)
	renderTable(sub, 0, h, runWriteColumns(rows, runsTier(w)), sel, colorless)
	b.blit(sub, x, y)
	visible := h - 1
	top := 0
	if sel >= visible {
		top = sel - visible + 1
	}
	for r := top; r < len(keys) && r-top < visible; r++ {
		m.addClick(psClick{x: x - 1, y: y + 1 + (r - top), w: w + 2, kind: psClickTableRow, name: keys[r]})
	}
}

// renderDetailPane paints the shared two-column body inside an already-drawn
// box: the spec column, the rule, the rate bar, and the run table. A pane too
// narrow to split stacks the spec blocks above the table instead.
func renderDetailPane(b *screenBuf, m *psModel, sc specScope, rows []detailRun, x, y, w, h int, colorless bool) {
	ix, iy := x+2, y+1
	iw, ih := w-4, h-2
	p := splitPane(ix, iw)

	if !p.split {
		// Stacked: the spec column takes what it needs off the top, the run
		// table takes the rest. The rate bar sheds -- a column this narrow
		// cannot carry a bar and a table both.
		specH := min(ih/2, 9)
		renderSpecColumn(b, m, sc, ix, iy, iw, specH)
		renderRunsTable(b, m, rows, ix, iy+specH+1, iw, ih-specH-1, colorless)
		return
	}

	renderSpecColumn(b, m, sc, p.lx, iy, p.lw, ih)

	barH := 0
	if ih >= 10 {
		barH = renderRowsBar(b, m, rowRingKey(sc), sc.table, p.rx, iy, p.rw) + 1
	}
	renderRunsTable(b, m, rows, p.rx, iy+barH, p.rw, ih-barH, colorless)
}

// rowRingKey is the row ring the pane's bar reads: the scope's pipeline, or
// the engine-wide ring when no pipeline owns the scope.
func rowRingKey(sc specScope) string {
	if sc.pipeline != "" {
		return "p:" + sc.pipeline
	}
	return ""
}

// detailDead reports the pane subject's newest run dead-lettering -- the cross
// riding beside the title's name.
func detailDead(runs []api.PsRun) bool {
	return len(runs) > 0 && runs[0].State == "dead_lettered"
}
