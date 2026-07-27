package tui

import (
	"fmt"
	"strings"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
)

// This file renders the `iris ps` dashboard: a cell-grid screen buffer the
// frame composer writes plain runes into, with per-cell SGR attributes applied
// only at emission. Layout math never sees an escape code, so clipping, row
// selection accents, and the search overlay's splicing stay correct by
// construction -- no ANSI-aware string surgery anywhere. Each frame emits as
// one cursor-home write with per-line clear-to-EOL (never a full-screen clear),
// so redraws are flicker-free.
//
// The frame is two panes: the catalog rail on the left and the detail pane
// filling the rest. Narrow terminals shed: under psRailMinWidth the rail
// goes; under psMinWidth the frame degrades to a single advisory line.

// The dashboard's width tiers and floor.
const (
	psMinWidth     = 40
	psMinHeight    = 8
	psRailMinWidth = 70
	// psHeaderH is the statusline's row budget at every frame height: one row,
	// its top and bottom edges drawn as SGR rules on that same row.
	psHeaderH = 1
	// psHeaderGap is the blank row parting the statusline from the panes, so
	// its underline and the rail's overline do not read as one thick rule.
	psHeaderGap = 1
	// psPaneGap is the blank gutter between the rail and the right column, on
	// top of the two border columns already parting them: two columns, so the
	// rail's hairline chrome reads as its own card rather than as the neighbour
	// pane's left edge.
	psPaneGap = 2
	// psFrameMarginX is the blank column the frame keeps against each terminal
	// edge so its chrome never touches the sides.
	psFrameMarginX = 1
)

// psCell is one screen cell: its rune and the SGR code painting it ("" plain).
type psCell struct {
	r   rune
	sgr string
}

// screenBuf is one frame's cell grid.
type screenBuf struct {
	w, h  int
	cells []psCell
}

// newScreenBuf builds a blank frame of the given geometry.
func newScreenBuf(w, h int) *screenBuf {
	b := &screenBuf{w: w, h: h, cells: make([]psCell, w*h)}
	for i := range b.cells {
		b.cells[i].r = ' '
	}
	return b
}

// text writes s at (x, y) in the given SGR, clipping at the right edge and
// never wrapping.
func (b *screenBuf) text(x, y int, sgr, s string) {
	if y < 0 || y >= b.h {
		return
	}
	for _, r := range s {
		if x >= b.w {
			return
		}
		if x >= 0 {
			b.cells[y*b.w+x] = psCell{r: r, sgr: sgr}
		}
		x++
	}
}

// The rail's selection reads in two tiers, both background washes that keep
// every cell's own foreground on top — no inverse video, no white bar.
// ansiLaneBg is the quiet one every row of the cursor's lane wears; ansiSelBg
// is the brighter one the cursor row alone takes.
var (
	ansiLaneBg = bgRGB(30, 33, 54)
	ansiSelBg  = bgRGB(58, 63, 104)
)

// paintSelAccent marks the cursor row: the whole row takes the selection
// background, keeping each cell's own color on top. The colorless painter
// keeps the ">" marker, its only visible channel.
func paintSelAccent(b *screenBuf, x, y, w int, colorless bool) {
	if colorless {
		b.text(x, y, "", ">")
		return
	}
	b.tintRow(x, y, w)
}

// paintLaneWash tints a row of the cursor's lane. Colorless has no channel for
// a block this soft, so it paints nothing; the ">" stays the cursor's alone.
func paintLaneWash(b *screenBuf, x, y, w int, colorless bool) {
	if colorless {
		return
	}
	b.tintLane(x, y, w)
}

// layerSGR prefixes w cells of row y from x with an SGR, keeping each cell's
// own paint underneath it.
func (b *screenBuf) layerSGR(x, y, w int, prefix string) {
	if y < 0 || y >= b.h {
		return
	}
	for xx := x; xx < x+w && xx < b.w; xx++ {
		c := &b.cells[y*b.w+xx]
		if !strings.HasPrefix(c.sgr, prefix) {
			c.sgr = prefix + c.sgr
		}
	}
}

// tintRow layers the selection background under w cells of row y from x.
func (b *screenBuf) tintRow(x, y, w int) { b.layerSGR(x, y, w, ansiSelBg) }

// tintLane layers the lane wash under w cells of row y from x.
func (b *screenBuf) tintLane(x, y, w int) { b.layerSGR(x, y, w, ansiLaneBg) }

// ruleRow layers the overline/underline pair over w cells of row y: a bordered
// row's top and bottom edges as text attributes rather than two rows of glyphs.
func (b *screenBuf) ruleRow(x, y, w int) { b.layerSGR(x, y, w, ansiHRule) }

// underlineRow layers the underline alone over w cells of row y: one edge, the
// half every terminal draws.
func (b *screenBuf) underlineRow(x, y, w int) { b.layerSGR(x, y, w, ansiURule) }

// dimAll repaints the whole frame dim -- the search overlay's backdrop.
func (b *screenBuf) dimAll() {
	for i := range b.cells {
		b.cells[i].sgr = ansiDim
	}
}

// box clears a rectangle and draws a rounded border around it, with an
// optional title spliced into the top edge in its own SGR.
func (b *screenBuf) box(x, y, w, h int, sgr, titleSGR, title string) {
	if w < 2 || h < 2 {
		return
	}
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			if yy < 0 || yy >= b.h || xx < 0 || xx >= b.w {
				continue
			}
			b.cells[yy*b.w+xx] = psCell{r: ' '}
		}
	}
	horiz := strings.Repeat("─", w-2)
	b.text(x, y, sgr, "┌"+horiz+"┐")
	b.text(x, y+h-1, sgr, "└"+horiz+"┘")
	for yy := y + 1; yy < y+h-1; yy++ {
		b.text(x, yy, sgr, "│")
		b.text(x+w-1, yy, sgr, "│")
	}
	if title != "" {
		if room := w - 6; room > 0 && len([]rune(title)) > room {
			title = string([]rune(title)[:room])
		}
		b.text(x+2, y, titleSGR, " "+title+" ")
	}
}

// hairBox is the statusline's chrome as a rectangle: side pipes as glyphs, no
// corners, every row's interior filled with border chrome so the horizontal
// edges the caller layers on later (see ruleRow, underlineRow) stay brand
// coloured across the gaps between content.
func (b *screenBuf) hairBox(x, y, w, h int, sgr string) {
	if w < 2 || h < 1 {
		return
	}
	row := "│" + strings.Repeat(" ", w-2) + "│"
	for yy := y; yy < y+h; yy++ {
		b.text(x, yy, sgr, row)
	}
}

// render emits the frame as ANSI: cursor home, then per row a clear-to-EOL
// FIRST (an erase after a full-width row would eat the border's last column
// in the terminal's wrap-pending state) and the coalesced SGR runs, rows
// joined by \r\n (raw mode does no output post-processing), no trailing
// newline so the last row never scrolls, and a clear-below at the end so a
// frame shorter than the last one leaves no residue. With a disabled painter
// the emission is the same geometry with zero SGR bytes.
func (b *screenBuf) render(p painter) []byte {
	var out strings.Builder
	out.WriteString("\x1b[H")
	for y := range b.h {
		if y > 0 {
			out.WriteString("\r\n")
		}
		out.WriteString("\x1b[K")
		open := ""
		for x := range b.w {
			c := b.cells[y*b.w+x]
			sgr := c.sgr
			if !p.enabled {
				sgr = ""
			}
			if sgr != open {
				if open != "" {
					out.WriteString(ansiReset)
				}
				if sgr != "" {
					out.WriteString(sgr)
				}
				open = sgr
			}
			out.WriteRune(c.r)
		}
		if open != "" {
			out.WriteString(ansiReset)
		}
	}
	out.WriteString("\x1b[J")
	return []byte(out.String())
}

// plainLines renders the frame as plain text lines (test golden surface).
func (b *screenBuf) plainLines() []string {
	lines := make([]string, b.h)
	for y := range b.h {
		var sb strings.Builder
		for x := range b.w {
			sb.WriteRune(b.cells[y*b.w+x].r)
		}
		lines[y] = strings.TrimRight(sb.String(), " ")
	}
	return lines
}

// blit copies a sub-frame's cells into the frame at (x, y).
func (b *screenBuf) blit(src *screenBuf, x, y int) {
	for yy := range src.h {
		for xx := range src.w {
			tx, ty := x+xx, y+yy
			if tx < 0 || tx >= b.w || ty < 0 || ty >= b.h {
				continue
			}
			b.cells[ty*b.w+tx] = src.cells[yy*src.w+xx]
		}
	}
}

// psStateSGR maps a run state to its column color.
func psStateSGR(state string) string {
	switch state {
	case "running":
		return ansiCyan
	case "queued":
		return ansiYellow
	case "succeeded":
		return ansiGreen
	case "dead_lettered":
		return ansiRed
	default:
		return ""
	}
}

// psRoleSGR maps the engine role to its badge tint.
func psRoleSGR(role string) string {
	switch role {
	case "leader":
		return ansiGreen
	case "standby":
		return ansiYellow
	default:
		return ansiDim
	}
}

// heatCell quantizes one CPU-percent sample into the 4-tone heat ramp.
// psNoSample renders as an empty cell so absence never reads as idle-green.
func heatCell(pct float64) (rune, string) {
	switch {
	case pct < 0:
		return ' ', ""
	case pct < 25:
		return '░', ansiGreen
	case pct < 50:
		return '▒', ansiYellow
	case pct < 75:
		return '▓', ansiOrange
	default:
		return '█', ansiRed
	}
}

// renderHeatStrip draws the newest w samples right-aligned at (x, y): one
// cell per poll, newest right, older samples falling off the left edge.
func (b *screenBuf) renderHeatStrip(x, y, w int, samples []float64) {
	if w <= 0 {
		return
	}
	if len(samples) > w {
		samples = samples[len(samples)-w:]
	}
	sx := x + w - len(samples)
	for i, s := range samples {
		r, sgr := heatCell(s)
		if r != ' ' {
			b.text(sx+i, y, sgr, string(r))
		}
	}
}

// fitSamples compresses a history to at most w slots: when the recorded
// history is wider than the strip, each cell carries the maximum of its share,
// so a short spike stays visible at any zoom. A cell is absent only when its
// whole share is absent.
func fitSamples(samples []float64, w int) []float64 {
	if w <= 0 || len(samples) <= w {
		return samples
	}
	out := make([]float64, w)
	for i := range out {
		cell := float64(psNoSample)
		for _, s := range samples[i*len(samples)/w : (i+1)*len(samples)/w] {
			if s != psNoSample && (cell == psNoSample || s > cell) {
				cell = s
			}
		}
		out[i] = cell
	}
	return out
}

// stripRing resolves the ring a strip draws for the current view: the fine
// ring live, the coarse (day-deep) ring under the 'h' history toggle.
func (m *psModel) stripRing(key string) *psRing {
	if m.histView {
		return m.coarse[key]
	}
	return m.rings[key]
}

// ringCPU shapes one ring's CPU history for a strip of width w, compressed so
// the whole ring spans the strip instead of scrolling off it. Nil-safe.
func ringCPU(r *psRing, w int) []float64 {
	return fitSamples(r.cpuSamples(), w)
}

// ringMem shapes one ring's memory history for a strip of width w, scaled to
// percent-of-peak and compressed the same way. Nil-safe.
func ringMem(r *psRing, w int) []float64 {
	if r == nil {
		return nil
	}
	return fitSamples(memStripSamples(r), w)
}

// stripCPU is a strip's CPU samples for the current view.
func (m *psModel) stripCPU(key string, w int) []float64 { return ringCPU(m.stripRing(key), w) }

// stripMem is a strip's memory samples for the current view.
func (m *psModel) stripMem(key string, w int) []float64 { return ringMem(m.stripRing(key), w) }

// dayRing is the ring the rail's lane summary draws for a strip of width w,
// whatever the 'h' toggle says. The summary's span is everything recorded, up
// to the coarse ring's day-deep ceiling -- so it takes the coarse ring once
// that carries enough buckets to fill the strip, and the fine ring before then.
// Both say "everything recorded"; the fine one just says it at higher
// resolution while the day is still young, which is what keeps a minute-old
// engine showing a full minute instead of two lonely cells.
func (m *psModel) dayRing(key string, w int) *psRing {
	if c := m.coarse[key]; c != nil && len(c.cpu) >= w {
		return c
	}
	if f := m.rings[key]; f != nil && len(f.cpu) > 0 {
		return f
	}
	return m.coarse[key]
}

func (m *psModel) dayCPU(key string, w int) []float64 { return ringCPU(m.dayRing(key, w), w) }

func (m *psModel) dayMem(key string, w int) []float64 { return ringMem(m.dayRing(key, w), w) }

// memStripSamples rescales a ring's memory history to percent-of-peak, so the
// heat ramp reads relative pressure within the visible window.
func memStripSamples(r *psRing) []float64 {
	peak := r.memPeak()
	out := make([]float64, len(r.mem))
	for i, m := range r.mem {
		if r.cpu[i] == psNoSample && m == 0 {
			out[i] = psNoSample
			continue
		}
		if peak == 0 {
			out[i] = 0
			continue
		}
		out[i] = float64(m) / float64(peak) * 100
	}
	return out
}

// psColumn is one table column: header, cell values, and the per-row SGR.
type psColumn struct {
	header string
	cells  []string
	sgr    []string // nil, or one SGR per cell
}

// psCol builds one plain column from a per-row accessor.
func psCol(header string, n int, cell func(int) string) psColumn {
	c := psColumn{header: header, cells: make([]string, n)}
	for i := range n {
		c.cells[i] = cell(i)
	}
	return c
}

// psColStyled builds one column whose cells carry a per-row SGR.
func psColStyled(header string, n int, cell func(int) (string, string)) psColumn {
	c := psColumn{header: header, cells: make([]string, n), sgr: make([]string, n)}
	for i := range n {
		c.cells[i], c.sgr[i] = cell(i)
	}
	return c
}

// renderTable lays a uniform table into b: header row, then one row per
// entry, the selected row marked with a left accent bar (or "> " when
// colorless). Column widths fit the widest cell; rows window over the height
// keeping the selection visible.
func renderTable(b *screenBuf, y, bodyH int, cols []psColumn, selRow int, colorless bool) {
	if len(cols) == 0 || bodyH < 2 {
		return
	}
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = len([]rune(c.header))
		for _, cell := range c.cells {
			if l := len([]rune(cell)); l > widths[i] {
				widths[i] = l
			}
		}
	}
	// Always leave a column for the selection accent (▌ or ">").
	marker := 2
	x := marker
	starts := make([]int, len(cols))
	for i := range cols {
		starts[i] = x
		x += widths[i] + 2
	}

	for i, c := range cols {
		b.text(starts[i], y, ansiDim, c.header)
	}
	rows := len(cols[0].cells)
	visible := bodyH - 1
	// Keep the selected row on screen: scroll the window over the rows.
	top := 0
	if selRow >= visible {
		top = selRow - visible + 1
	}
	for r := top; r < rows && r-top < visible; r++ {
		ry := y + 1 + (r - top)
		for i, c := range cols {
			sgr := ""
			if c.sgr != nil {
				sgr = c.sgr[r]
			}
			if sgr == "" && c.cells[r] == "-" {
				sgr = ansiDim // absent samples recede
			}
			b.text(starts[i], ry, sgr, c.cells[r])
		}
		if r == selRow {
			if colorless {
				b.text(0, ry, "", "> ")
			} else {
				b.tintRow(0, ry, b.w)
			}
		}
	}
}

// selIndex finds the cursor's row index among keys (0 when absent).
func selIndex(sel string, keys []string) int {
	for i, k := range keys {
		if k == sel {
			return i
		}
	}
	return 0
}

// paneChrome picks a pane's border and title paint: warm accent when the pane
// holds the focus, receding border chrome otherwise; a colorless focused pane
// marks its title.
func paneChrome(focused, colorless bool, title string) (borderSGR, titleSGR, t string) {
	if focused {
		// Colour is the focus signal; a colourless frame has to say it in text.
		if colorless {
			if title != "" && !strings.HasPrefix(title, "[") {
				title = "[" + title + "]"
			}
			return ansiBorder, "", title
		}
		return ansiMagenta, ansiMagenta, title
	}
	return ansiBorder, ansiDim, title
}

// paneTitle paints a pane's title row: the bracketed mark at x+2 in the
// wordmark's ramp, flat in titleSGR when the frame is colorless. The row's
// edges are the caller's deferred ruleRow.
func paneTitle(b *screenBuf, x, y int, titleSGR, mark string, colorless bool) {
	if colorless {
		b.text(x+2, y, titleSGR, mark)
		return
	}
	b.gradText(x+2, y, mark)
}

// paneSubject right-aligns a pane's subject on its title row: the name in the
// brand violet, the dead-letter cross riding just before it. Red, not violet --
// every other cross in the frame is red.
func paneSubject(b *screenBuf, x, y, w int, name string, dead bool) {
	if name == "" {
		return
	}
	room := w - 6 - len([]rune(name))
	if dead {
		room -= 2
	}
	if room < 4 {
		return // the mark alone; a clipped subject reads worse than none
	}
	nx := x + w - 2 - len([]rune(name))
	b.text(nx, y, ansiMagenta, name)
	if dead {
		b.text(nx-2, y, ansiRed, "✖")
	}
}

// paneHint right-aligns a dim hint on a pane's bottom row, so it wears the
// rule the caller defers rather than punching a hole in it.
func paneHint(b *screenBuf, x, y, w int, hint string) {
	if hint == "" || len([]rune(hint))+6 > w {
		return
	}
	b.text(x+w-2-len([]rune(hint)), y, ansiDim, hint)
}

// psIsEmptyWorkspace reports the quiet-engine zero state: nothing registered,
// nothing running. The four-pane grid is the wrong surface there — it reads as
// a broken dashboard — so the frame swaps in a guided empty card instead.
func psIsEmptyWorkspace(m *psModel) bool {
	if len(m.snap.Pipelines) > 0 || len(m.snap.Ps.Runs) > 0 {
		return false
	}
	// Residents without a listing still imply registered work.
	if len(m.snap.Ps.Residents) > 0 {
		return false
	}
	return len(deriveLanes(m.snap)) == 0
}

// renderPsFrame composes the whole dashboard for the current model state.
func renderPsFrame(m *psModel, w, h int, colorless bool) *screenBuf {
	m.clicks = m.clicks[:0] // regions are rebuilt with every frame
	if w < psMinWidth || h < psMinHeight {
		b := newScreenBuf(w, 1)
		b.text(0, 0, "", "iris ps: terminal too small")
		return b
	}
	b := newScreenBuf(w, h)

	// The frame breathes against the terminal edge; a terminal with no columns
	// to spare keeps every one of them.
	mx := psFrameMarginX
	if w < psMinWidth+2*mx {
		mx = 0
	}
	fw := w - 2*mx

	// Footer is transient only (confirm, advisory, freeze, overlays) — no
	// always-on shortcuts strip or socket target.
	footerH := 0
	if psFooterNeeded(m) {
		footerH = 1
		renderPsFooter(b, m, mx, fw)
	}

	// Quiet engine: no full-width top chrome. Status splits into two chips
	// above the welcome card inside the body; overlays still compose on top.
	switch {
	case psIsEmptyWorkspace(m):
		renderEmptyWorkspace(b, m, mx, 0, fw, h-footerH, colorless)
	default:
		renderPsHeader(b, m, mx, 0, fw)
		top := psHeaderH + psHeaderGap
		paneH := h - top - footerH // rows between header and optional footer

		railW := 0
		if fw >= psRailMinWidth {
			railW = fw / 4
			if railW > 38 {
				railW = 38
			}
			if railW < 26 {
				railW = 26
			}
			renderCatalogPane(b, m, mx, top, railW, paneH, colorless)
		}

		x := mx + railW
		if railW > 0 {
			x += psPaneGap
		}
		renderStatsPane(b, m, x, top, mx+fw-x, paneH, colorless)
	}

	switch {
	case m.search != nil:
		renderSearchOverlay(b, m)
	case m.catalog != nil:
		renderCatalogOverlay(b, m)
	case m.command != nil:
		renderCommandOverlay(b, m)
	}
	return b
}

// emptyActions are the compact GET STARTED rows kept for tight terminals.
var emptyActions = []struct{ label, key string }{
	{"Browse the pack catalog", "c"},
	{"Register your pipeline", "iris declare apply"},
	{"Quit", "q"},
}

// idleActions are the idle card's action rows beside the status box: key
// left, label right. Catalog browsing lives inline below, so it needs no row.
var idleActions = []struct{ key, label string }{
	{"?", "keyboard reference"},
	{"q", "quit"},
}

// idleBoxH is the status/actions box height: border + five content rows.
const idleBoxH = 7

// idleStackH is the idle card's vertical budget for a banner of n rows:
// banner, gap, version bar, gap, boxes, gap.
func idleStackH(bannerRows int) int {
	return bannerRows + 1 + 1 + 1 + idleBoxH + 1
}

// idleCatMinH is the smallest inline catalog box worth drawing: borders,
// search row, and a few packs.
const idleCatMinH = 7

// pickIdleBanner chooses the widest installer banner form that fits — first
// preferring forms that leave room for the inline catalog below the boxes,
// then falling back to a bare stack: wide one-block art, stacked two-block
// art, else the plain text line.
func pickIdleBanner(w, h int) ([]string, bool) {
	forms := [][]string{bannerWide, bannerStacked, {bannerText}}
	for _, art := range forms {
		if logoWidth(art) <= w && idleStackH(len(art))+idleCatMinH+1 <= h {
			return art, true
		}
	}
	for _, art := range forms {
		if logoWidth(art) <= w && idleStackH(len(art)) <= h {
			return art, true
		}
	}
	return nil, false
}

// renderEmptyWorkspace paints the zero-state: engine is alive, nothing is
// registered yet. Roomy terminals get the installer banner, a gradient
// version bar, and status/actions boxes. Tighter terminals keep a compact
// GET STARTED box, then fall back to a one-line nudge.
func renderEmptyWorkspace(b *screenBuf, m *psModel, x, y, w, h int, colorless bool) {
	if h < 3 || w < 20 {
		b.text(x+2, y+1, ansiDim, "no pipelines yet · press c for catalog")
		return
	}

	// Soft margins: content floats, not boxed.
	const hpad, vpad = 3, 1
	innerW := w - 2*hpad
	innerH := h - 2*vpad
	ox, oy := x+hpad, y+vpad
	if innerW < 26 || innerH < 5 {
		b.text(ox, oy, ansiDim, "no pipelines yet · c catalog")
		return
	}
	_ = colorless // accent/dim SGR apply; a disabled painter drops them at emit

	if art, ok := pickIdleBanner(innerW, innerH); ok && innerW >= 56 {
		renderIdleCard(b, m, ox, oy, innerW, innerH, art)
		return
	}
	renderCompactEmpty(b, ox, oy, innerW, innerH)
}

// renderIdleCard is the roomy zero-state: the installer's gradient banner,
// the version bar styled like the install progress bar, then the status and
// actions boxes over an idle status line.
func renderIdleCard(b *screenBuf, m *psModel, ox, oy, innerW, innerH int, art []string) {
	e := m.snap.Ps.Engine

	// Compact horizontally: the bar and boxes hug the banner's width instead
	// of stretching across a wide terminal. The one-line text fallback has no
	// real width of its own, so it borrows the stacked art's.
	cardW := logoWidth(art)
	if len(art) == 1 {
		cardW = logoWidth(bannerStacked)
	}
	if cardW > innerW {
		cardW = innerW
	}
	cx := ox + (innerW-cardW)/2 // stack floats centered

	// The inline catalog takes the leftover rows below the boxes, capped so
	// the stack stays composed; too little room drops it for this frame size.
	catH, catExtra := 0, 0
	if m.idleCat != nil {
		if room := innerH - idleStackH(len(art)) - 1; room >= idleCatMinH {
			catH = room
			if catH > 14 {
				catH = 14
			}
			catExtra = catH + 1 // box plus its gap row before the idle line
		}
	}
	y := oy + (innerH-idleStackH(len(art))-catExtra)/2

	// Banner rows in the installer gradient; a stacked art's second block
	// restarts the ramp at its own first row.
	gi := 0
	for _, row := range art {
		if row == "" {
			gi = 0
			y++
			continue
		}
		sgr := bannerRowSGR(gi)
		if len(art) == 1 {
			sgr = ansiMagenta // plain-text fallback line
		}
		b.text(cx, y, sgr, row)
		gi++
		y++
	}
	y++

	right := ""
	if e.PID != 0 {
		right = fmt.Sprintf("pid %d", e.PID)
	}
	b.renderGradientBar(cx, y, cardW, orDefault(e.Version, "dev"), right)
	y += 2

	statusW := (cardW - 2) / 2
	renderIdleStatusBox(b, m, cx, y, statusW)
	renderIdleQuietBlock(b, m, cx+statusW+4, y+1, cardW-statusW-4)
	y += idleBoxH + 1

	if catH > 0 {
		renderIdleCatalogBox(b, m, cx, y, cardW, catH)
	}
}

// clipEll bounds s to w cells, marking a cut with a trailing ellipsis.
func clipEll(s string, w int) string {
	r := []rune(s)
	if w <= 0 {
		return ""
	}
	if len(r) <= w {
		return s
	}
	return string(r[:w-1]) + "…"
}

// psSpinnerFrames is the braille spinner the catalog box shows while a fetch
// or install is in flight; the event loop advances the phase.
var psSpinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// renderIdleCatalogBox paints the idle card's searchable catalog: a bordered
// filter input on top carrying the "catalog · N packs" title, the pack list
// box below it (name, tags, description columns, full-row selection
// highlight), and the key hint riding the list's bottom border.
func renderIdleCatalogBox(b *screenBuf, m *psModel, x, y, w, h int) {
	c, spin := m.idleCat, m.spin
	title := "catalog"
	if !c.loading {
		title += fmt.Sprintf(" · %d packs", len(c.packs))
	}

	// Filter input box, titled; the pack list box sits directly under it.
	b.box(x, y, w, 3, ansiBorder, ansiMagenta, title)
	switch {
	case c.searching:
		b.text(x+2, y+1, ansiYellow, "/")
		b.text(x+4, y+1, "", clipCells(string(c.query)+"█", w-8))
	case len(c.query) > 0:
		b.text(x+2, y+1, ansiYellow, "/")
		b.text(x+4, y+1, "", clipCells(string(c.query), w-8))
	default:
		b.text(x+2, y+1, ansiDim, clipCells("/ type to filter — name, tag, text", w-6))
	}

	m.addClick(psClick{x: x, y: y, w: w, h: 3, kind: psClickCatalogFilter})
	b.box(x, y+3, w, h-3, ansiBorder, "", "")

	// Key hint spliced into the list's bottom border, right-aligned. With
	// circles picked it becomes the clickable apply affordance.
	if n := len(c.batch()); n > 0 {
		button := fmt.Sprintf("▶ apply %d marked", n)
		if hx := x + w - 3 - len([]rune(button)); hx > x+2 {
			b.text(hx, y+h-1, ansiMagenta, " "+button+" ")
			m.addClick(psClick{x: hx, y: y + h - 1, w: len([]rune(button)) + 2, kind: psClickCatalogApply})
		}
	} else {
		hint := "␣ pick · + source · ↑↓ browse · ⏎ apply picked"
		if hx := x + w - 3 - len([]rune(hint)); hx > x+2 {
			b.text(hx, y+h-1, ansiDim, " "+hint+" ")
		}
	}

	listY := y + 4
	listH := h - 5
	banner := c.banner
	switch {
	case c.addingURL:
		banner = "add source url: " + string(c.urlInput) + "█  · ⏎ add · esc cancel"
	case c.busy != "":
		banner = string(psSpinnerFrames[spin%len(psSpinnerFrames)]) + " " + c.busy
	}
	if banner != "" {
		listH--
		b.text(x+3, y+h-2, ansiYellow, clipCells(banner, w-6))
	}
	if listH < 1 {
		return
	}

	vis := c.visible()
	switch {
	case c.loading:
		b.text(x+3, listY, ansiCyan, string(psSpinnerFrames[spin%len(psSpinnerFrames)]))
		b.text(x+5, listY, ansiDim, "loading catalog…")
		return
	case len(vis) == 0 && len(c.query) > 0:
		b.text(x+3, listY, ansiDim, clipCells("no packs match "+string(c.query), w-6))
		return
	case len(vis) == 0:
		b.text(x+3, listY, ansiDim, "no packs")
		return
	}

	// Column layout: name, tags, description, each sized to its widest cell.
	nameW, tagsW := 0, 0
	rowTags := func(p api.CatalogPack) string { return strings.Join(p.Tags, ",") }
	for _, p := range vis {
		n := len([]rune(p.Name))
		if p.Installed {
			n += 2 // " ●"
		}
		if n > nameW {
			nameW = n
		}
		if t := len([]rune(rowTags(p))); t > tagsW {
			tagsW = t
		}
	}
	// Leading mark circles: ○ unpicked, ● picked; clicking one toggles it.
	const markW = 2
	nameX := x + 3 + markW
	tagsX := nameX + nameW + 6
	descX := tagsX + tagsW + 6
	right := x + w - 3

	// Full-row selection highlight: soft violet backdrop, bright bold name.
	const selBG = "\033[48;2;44;40;66m"
	top := 0
	if c.sel >= listH {
		top = c.sel - listH + 1
	}
	for i := top; i < len(vis) && i-top < listH; i++ {
		p := vis[i]
		ry := listY + (i - top)
		m.addClick(psClick{x: x + 1, y: ry, w: w - 2, kind: psClickCatalogRow, idx: i})
		nameSGR, metaSGR := "", ansiDim
		if i == c.sel {
			for cxx := x + 2; cxx < x+w-2; cxx++ {
				b.text(cxx, ry, selBG, " ")
			}
			nameSGR = selBG + "\033[1;38;2;235;235;245m"
			metaSGR = selBG + "\033[38;2;154;150;174m"
		}
		if c.marked[p.Name] {
			b.text(x+3, ry, ansiMagenta, "●")
		} else {
			b.text(x+3, ry, metaSGR, "○")
		}
		m.addClick(psClick{x: x + 3, y: ry, w: 1, kind: psClickMarkPack, idx: i})
		nx := nameX
		b.text(nx, ry, nameSGR, clipEll(p.Name, right-nx))
		nx += len([]rune(p.Name))
		if p.Installed && nx+2 <= right {
			b.text(nx, ry, ansiGreen, " ●")
		}
		if tags := rowTags(p); tags != "" && tagsX < right {
			b.text(tagsX, ry, metaSGR, clipEll(tags, min(tagsW, right-tagsX)))
		}
		if p.Description != "" && descX < right {
			b.text(descX, ry, metaSGR, clipEll(p.Description, right-descX))
		}
	}
	if more := len(vis) - top - listH; more > 0 {
		b.text(x+3, y+h-1, ansiDim, fmt.Sprintf("─ %d more ─", more))
	}
}

// renderIdleStatusBox paints the idle card's left box: state, queue depth,
// CPU mini-bar, memory, last run.
func renderIdleStatusBox(b *screenBuf, m *psModel, x, y, w int) {
	e := m.snap.Ps.Engine
	b.box(x, y, w, idleBoxH, ansiBorder, ansiMagenta, "status")
	lx, vx := x+2, x+12
	row := 0
	put := func(label, sgr, val string) {
		b.text(lx, y+1+row, ansiDim, label)
		b.text(vx, y+1+row, sgr, clipCells(val, x+w-2-vx))
		row++
	}
	put("state", ansiGreen, "idle")
	queue := "empty"
	if e.QueuedRuns > 0 {
		queue = fmt.Sprintf("%d", e.QueuedRuns)
	}
	put("queue", "", queue)

	b.text(lx, y+1+row, ansiDim, "cpu")
	pct := 0.0
	if e.Load != nil {
		pct = e.Load.CPUPercent
	}
	barW := 14
	if max := x + w - 2 - vx - len(" 100.0%"); barW > max {
		barW = max
	}
	if barW > 0 {
		b.renderMiniBar(vx, y+1+row, barW, pct)
		b.text(vx+barW+1, y+1+row, "", cpuText(e.Load))
	} else {
		b.text(vx, y+1+row, "", cpuText(e.Load))
	}
	row++
	put("mem", "", memText(e.Load))
	put("up", "", orDefault(e.Uptime, "—"))
}

// renderIdleQuietBlock paints the borderless block beside the status box,
// aligned with its first content row: a ceremony quote and the clickable
// action key rows.
func renderIdleQuietBlock(b *screenBuf, m *psModel, x, y, w int) {
	lines := wrapWords("“"+m.quote.Text+"”", w)
	if len(lines) > 2 {
		lines = lines[:2]
		lines[1] = clipEll(lines[1], w-1) + "”"
	}
	for i, ln := range lines {
		b.text(x, y+i, ansiAccent, ln)
	}
	ay := y + len(lines)
	b.text(x, ay, ansiDim, clipCells("- "+m.quote.Author, w))
	ay++
	if len(lines) == 1 {
		ay++ // breathing room when the quote is short
	}
	lx := x + 12 // label column clears the widest key
	for i, a := range idleActions {
		b.text(x, ay+i, ansiYellow, a.key)
		b.text(lx, ay+i, "", clipCells(a.label, x+w-lx))
		kind := psClickActionHelp
		if a.key == "q" {
			kind = psClickActionQuit
		}
		m.addClick(psClick{x: x, y: ay + i, w: w, kind: kind})
	}
}

// renderCompactEmpty is the tight zero-state: a small GET STARTED box, key
// column left, label right.
func renderCompactEmpty(b *screenBuf, ox, oy, innerW, innerH int) {
	const lead, keyW = 3, 20
	cardW := 60
	if cardW > innerW {
		cardW = innerW
	}
	cardH := 2 + len(emptyActions)
	cardX := ox + (innerW-cardW)/2
	cardY := oy
	if cardH < innerH {
		cardY = oy + (innerH-cardH)/2
	}
	b.box(cardX, cardY, cardW, cardH, ansiBorder, ansiDim, "GET STARTED")
	for i, a := range emptyActions {
		ry := cardY + 1 + i
		b.text(cardX+1+lead, ry, ansiCyan, a.key)
		label := a.label
		if room := cardW - 2 - lead - keyW - 1; len([]rune(label)) > room && room > 0 {
			label = string([]rune(label)[:room-1]) + "…"
		}
		b.text(cardX+1+lead+keyW, ry, ansiDim, label)
	}
}

// renderPsHeader paints the statusline: one row boxed as tight as a cell grid
// allows — side pipes as glyphs, top and bottom edges as SGR rules on the row
// itself (see ansiHRule) — carrying the letterspaced wordmark and identity
// left, live CPU/MEM and the compact run tail right-aligned. The chrome wears
// the brand violet the focused pane wears: it is the frame's identity, never a
// pane you move off. The empty workspace does not use it (see
// renderWelcomeCard).
func renderPsHeader(b *screenBuf, m *psModel, x0, y, w int) {
	e := m.snap.Ps.Engine
	// Blank interior cells wear border chrome so the rules stay violet across
	// the gap between identity and load readout.
	b.text(x0, y, ansiBorder, "│"+strings.Repeat(" ", max(w-2, 0))+"│")
	defer b.ruleRow(x0, y, w) // the edges, whatever the row ends up carrying

	x := x0 + 2
	put := func(sgr, s string) {
		b.text(x, y, sgr, s)
		x += len([]rune(s))
	}

	if w >= psWordmarkMinWidth {
		x = b.renderWordmark(x, y)
	} else {
		put(ansiMagenta, "[IRIS]")
	}
	if e.Version != "" {
		put(ansiDim, "   ")
		put(ansiDim, e.Version)
	}
	put(ansiDim, "   ")
	put(psRoleSGR(e.Role), strings.ToUpper(orDefault(e.Role, "engine")))
	if e.Uptime != "" {
		put(ansiDim, "  up ")
		put("", e.Uptime)
	}
	put(ansiDim, fmt.Sprintf("  pid %d", e.PID))
	idEnd := x

	tail := psHeaderTail(e.RunningRuns, e.QueuedRuns, deadPipelines(m.snap))
	nx, ok := renderHeaderLoad(b, m, y, idEnd, x0+w-2, spansWidth(tail))
	if !ok {
		return // identity only; the panes still carry the numbers
	}
	x = nx
	for _, s := range tail {
		put(s.sgr, s.text)
	}
}

// psSpan is one styled run of statusline text.
type psSpan struct {
	sgr, text string
}

// spansWidth is the cell width a span run occupies.
func spansWidth(spans []psSpan) int {
	n := 0
	for _, s := range spans {
		n += len([]rune(s.text))
	}
	return n
}

// psHeaderTail is the statusline's compact run tail: running and queued as
// r/q digits, dead as an inverted chip once anything is dead-lettered.
func psHeaderTail(running, queued int64, dead int) []psSpan {
	rc, qc := ansiCyan, ansiYellow
	if running == 0 {
		rc = ansiDim
	}
	if queued == 0 {
		qc = ansiDim
	}
	spans := []psSpan{
		{ansiDim, "  "}, {rc, fmt.Sprintf("%dr", running)},
		{ansiDim, "  "}, {qc, fmt.Sprintf("%dq", queued)},
		{ansiDim, "  "},
	}
	if dead > 0 {
		return append(spans, psSpan{ansiInverse + ansiRed, fmt.Sprintf(" %d DEAD ", dead)})
	}
	return append(spans, psSpan{ansiDim, "0✖"})
}

// psHdrCPUValW and psHdrMemValW are the header's reserved readout columns:
// "100.0%" and "1023.9MiB" are the widest ordinary readings, and a reading
// wider than its column simply takes the room it needs.
const (
	psHdrCPUValW = 6
	psHdrMemValW = 9
)

// padLeft right-aligns s in a w-wide field, leaving it whole when it overflows.
func padLeft(s string, w int) string {
	if n := len([]rune(s)); n < w {
		return strings.Repeat(" ", w-n) + s
	}
	return s
}

// renderHeaderLoad right-aligns the CPU heat strip and CPU/MEM readout on
// row y ending at column right, reserving extraW cells after MEM for the
// caller's tail; the strip shrinks, then the whole block sheds, when the
// identity block leaves no room. Returns the x after MEM and whether anything
// was drawn.
func renderHeaderLoad(b *screenBuf, m *psModel, y, idEnd, right, extraW int) (int, bool) {
	e := m.snap.Ps.Engine
	// Fixed readout columns: the block is right-anchored, so a value that grows
	// a character would otherwise slide the whole strip sideways (and, once the
	// width is tight, re-fit it) every time the number changed.
	cpu := " " + padLeft(cpuText(e.Load), psHdrCPUValW)
	mem := "  MEM " + padLeft(memText(e.Load), psHdrMemValW)
	stripW := 30
	fixed := len("CPU ") + len([]rune(cpu+mem)) + extraW
	if avail := right - idEnd - 3; fixed+stripW > avail {
		stripW = avail - fixed
		if stripW < 8 {
			stripW = 0
		}
		if fixed+stripW > avail {
			return 0, false
		}
	}
	x := right - (fixed + stripW)
	b.text(x, y, ansiDim, "CPU ")
	x += len("CPU ")
	b.renderHeatStrip(x, y, stripW, m.stripCPU("", stripW))
	x += stripW
	b.text(x, y, "", cpu)
	x += len([]rune(cpu))
	b.text(x, y, ansiDim, mem)
	x += len([]rune(mem))
	return x, true
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// cpuSamples is the ring's CPU history (nil-safe for a ring not yet grown).
func (r *psRing) cpuSamples() []float64 {
	if r == nil {
		return nil
	}
	return r.cpu
}

// footerHint is one shortcuts-bar entry: accent key + dim label.
type footerHint struct {
	key, desc string
}

// psFooterNeeded reports whether the bottom row should be reserved for a
// transient message or mode-specific key hints. Idle browsing has no footer.
func psFooterNeeded(m *psModel) bool {
	if m.command != nil || m.search != nil || m.catalog != nil {
		return true
	}
	if m.confirmCancel || m.confirmBulk || m.frozen {
		return true
	}
	if len(m.markedPipes) > 0 {
		return true
	}
	return m.note != "" || m.warn != ""
}

// renderPsFooter paints the last row for transient state only: command palette
// hints/errors, freeze/confirm/search/catalog keys, or note/warn text.
func renderPsFooter(b *screenBuf, m *psModel, x0, w int) {
	y := b.h - 1
	maxHints := w - 2
	if maxHints < 8 {
		maxHints = w - 1
	}

	if m.command != nil {
		if m.command.err != "" {
			b.text(x0+1, y, ansiYellow, clipCells(m.command.err, maxHints))
			return
		}
		paintFooterHints(b, x0+1, y, maxHints, []footerHint{
			{"↑↓", "select"}, {"tab", "complete"}, {"⏎", "run"}, {"esc", "close"},
		})
		return
	}

	advisory := m.note
	if advisory == "" {
		advisory = m.warn
	}
	if advisory != "" {
		b.text(x0+1, y, ansiYellow, clipCells(advisory, maxHints))
		return
	}

	paintFooterHints(b, x0+1, y, maxHints, psFooterHints(m))
}

// paintFooterHints draws "key desc · key desc …" with accent keys, clipping
// whole entries when the bar runs out of room (never mid-glyph soup). A hint
// with an empty key paints desc only (dim) — used for free-text guidance.
func paintFooterHints(b *screenBuf, x, y, maxW int, hints []footerHint) {
	if maxW <= 0 || len(hints) == 0 {
		return
	}
	cur := x
	end := x + maxW
	for i, h := range hints {
		// " · " between entries
		sep := ""
		if i > 0 {
			sep = " · "
		}
		chunk := sep
		if h.key != "" {
			chunk += h.key
			if h.desc != "" {
				chunk += " " + h.desc
			}
		} else {
			chunk += h.desc
		}
		need := len([]rune(chunk))
		if cur+need > end {
			break
		}
		if sep != "" {
			b.text(cur, y, ansiDim, sep)
			cur += len([]rune(sep))
		}
		if h.key != "" {
			b.text(cur, y, ansiMagenta, h.key)
			cur += len([]rune(h.key))
			if h.desc != "" {
				b.text(cur, y, ansiDim, " "+h.desc)
				cur += 1 + len([]rune(h.desc))
			}
		} else if h.desc != "" {
			b.text(cur, y, ansiDim, h.desc)
			cur += len([]rune(h.desc))
		}
	}
}

// psFooterHints returns mode-specific keys for transient footer states.
// Idle browsing has no footer (see psFooterNeeded).
func psFooterHints(m *psModel) []footerHint {
	if m.frozen {
		return []footerHint{
			{"p", "resume"}, {"", "select text · copy with the terminal"},
		}
	}
	if m.search != nil {
		return []footerHint{{"⏎", "jump"}, {"esc", "close"}}
	}
	if m.confirmCancel {
		return []footerHint{{"y", "cancel " + m.cancelTarget()}, {"N", "keep"}}
	}
	if m.confirmBulk {
		runs := len(m.bulkCancelRuns())
		return []footerHint{
			{"y", fmt.Sprintf("cancel %d running runs in %d marked pipelines", runs, len(m.markedPipes))},
			{"N", "keep"},
		}
	}
	if m.catalog != nil {
		return []footerHint{{"↑↓", "move"}, {"⏎", "install"}, {"esc", "close"}}
	}
	if n := len(m.markedPipes); n > 0 {
		return []footerHint{
			{"␣", "mark"}, {"c", fmt.Sprintf("cancel %d marked", n)},
		}
	}
	return nil
}

// pipelinesColumns builds the pipelines table's columns behind the leading
// mark-circle column. The timing columns carry the engine's rendered spans
// (#238 phase 2): ELAPSED from the pipeline's running run, LAST and AVG from
// its aggregate block; a pipeline with no timed run renders dashes. A narrow
// pane sheds them whole rather than clipping their headers.
func pipelinesColumns(m *psModel, rows []psPipelineRow, wide bool, marked map[string]bool) []psColumn {
	n := len(rows)
	// Leading mark circles: ○ unpicked, ● picked; clicking one toggles it.
	cols := []psColumn{
		psColStyled("", n, func(i int) (string, string) {
			if marked[rows[i].name] {
				return "●", ansiMagenta
			}
			return "○", ansiDim
		}),
	}
	cols = append(cols,
		psCol("PIPELINE", n, func(i int) string { return rows[i].name }),
		psColStyled("LATEST", n, func(i int) (string, string) { return rows[i].latest, psStateSGR(rows[i].latest) }),
		psCol("Q", n, func(i int) string { return fmt.Sprintf("%d", rows[i].queued) }),
		psCol("R", n, func(i int) string { return fmt.Sprintf("%d", rows[i].running) }),
		psCol("CPU", n, func(i int) string { return cpuText(m.scopeLoad(rows[i].load)) }),
		psCol("MEM", n, func(i int) string { return memText(m.scopeLoad(rows[i].load)) }),
	)
	if wide {
		times := pipeTimes(m.snap)
		cols = append(cols,
			psCol("ELAPSED", n, func(i int) string { return orDash(pipeElapsed(m.snap, rows[i].name)) }),
			psCol("LAST", n, func(i int) string { return orDash(times[rows[i].name].Last) }),
			psCol("AVG", n, func(i int) string { return orDash(times[rows[i].name].Avg) }),
		)
	}
	return cols
}

// renderCommandOverlay paints the dedicated COMMANDS section over a dimmed
// frame: filterable roster on the left,
// a living detail pane on the right, and a cyan prompt bar on the bottom.
func renderCommandOverlay(b *screenBuf, m *psModel) {
	b.dimAll()
	c := m.command

	ow := b.w * 9 / 10
	oh := b.h * 8 / 10
	if ow < 36 {
		ow = b.w - 2
		if ow < 20 {
			ow = b.w
		}
	}
	if oh < 10 {
		oh = b.h - 2
		if oh < 6 {
			oh = b.h
		}
	}
	ox := (b.w - ow) / 2
	oy := (b.h - oh) / 2
	leftW := ow * 2 / 5
	if leftW < 22 {
		leftW = ow / 2
	}
	if leftW > 42 {
		leftW = 42
	}
	promptH := 3
	listH := oh - promptH

	// Left: COMMANDS list.
	b.box(ox, oy, leftW, listH, ansiBorder, ansiCyan, "COMMANDS")
	// Right: ABOUT / detail for the selection.
	px := ox + leftW + 1
	pw := ow - leftW - 1
	if pw < 12 {
		pw = 0
	}

	innerH := listH - 2
	if innerH < 1 {
		innerH = 1
	}

	list := c.filtered()
	top := 0
	if innerH > 0 && c.sel >= innerH {
		top = c.sel - innerH + 1
	}
	for i := top; i < len(list) && i-top < innerH; i++ {
		spec := list[i]
		ry := oy + 1 + (i - top)
		// Category tag in dim, then the command row.
		label := commandListLabel(spec, i == c.sel, leftW-4)
		b.text(ox+2, ry, "", label)
		if i == c.sel {
			paintSelAccent(b, ox+1, ry, leftW-1, false)
		}
	}
	if len(list) == 0 {
		b.text(ox+2, oy+1, ansiYellow, clipCells("no matching commands", leftW-4))
	}
	if top+innerH < len(list) {
		b.text(ox+2, oy+listH-1, ansiDim, fmt.Sprintf("─ %d more ─", len(list)-top-innerH))
	}

	if pw > 0 {
		title := "ABOUT"
		var spec psCmdSpec
		var ok bool
		if spec, ok = c.selected(); ok {
			title = "ABOUT · " + spec.name
		}
		b.box(px, oy, pw, listH, ansiBorder, ansiCyan, title)
		if ok {
			body := commandDetailBody(spec, pw-4)
			for i, ln := range body {
				if i >= listH-2 {
					break
				}
				sgr := ""
				if strings.HasPrefix(ln, "Usage") || strings.HasPrefix(ln, "Keys") || strings.HasPrefix(ln, "Group") {
					sgr = ansiDim
				}
				if ln == "GLOBAL" || ln == "TABLE" {
					sgr = ansiCyan
				}
				b.text(px+2, oy+1+i, sgr, clipCells(ln, pw-4))
			}
		}
	}

	// Prompt bar spanning the full overlay width.
	b.box(ox, oy+listH, ow, promptH, ansiBorder, ansiCyan, "")
	b.text(ox+2, oy+listH+1, ansiCyan, ":")
	b.text(ox+3, oy+listH+1, "", string(c.input)+"█")
	if c.err != "" {
		// Inline error rides the right side of the prompt when there is room.
		msg := "· " + c.err
		at := ox + 4 + len([]rune(string(c.input))) + 1
		if at < ox+ow-4 {
			b.text(at, oy+listH+1, ansiYellow, clipCells(msg, ox+ow-2-at))
		}
	} else {
		hint := "tab · ↑↓ · ⏎ · esc"
		if c.browse {
			hint = "browse · type to filter · esc"
		}
		at := ox + ow - 2 - len([]rune(hint))
		if at > ox+4+len([]rune(string(c.input))) {
			b.text(at, oy+listH+1, ansiDim, hint)
		}
	}
}

// lookupCmd finds a roster entry by name.
func lookupCmd(name string) (psCmdSpec, bool) {
	for _, c := range psCommandRoster {
		if c.name == name {
			return c, true
		}
	}
	return psCmdSpec{}, false
}

// renderSearchOverlay splices the telescope-style overlay over the dimmed
// frame: results above the prompt on the left, the live preview on the right.
func renderSearchOverlay(b *screenBuf, m *psModel) {
	b.dimAll()
	s := m.search

	ow := b.w * 9 / 10
	oh := b.h * 8 / 10
	ox := (b.w - ow) / 2
	oy := (b.h - oh) / 2
	leftW := ow * 2 / 5
	promptH := 3
	resultsH := oh - promptH

	b.box(ox, oy, leftW, resultsH, ansiBorder, ansiDim, "results")
	b.box(ox, oy+resultsH, leftW, promptH, ansiBorder, ansiDim, "")
	b.text(ox+2, oy+resultsH+1, ansiCyan, "> ")
	b.text(ox+4, oy+resultsH+1, "", string(s.query)+"▏")

	// Results list, bottom-anchored: the best hit (index 0) sits nearest the
	// prompt; the selection renders inverted. The list windows over the hits
	// so a selection moved past the pane height stays visible.
	innerH := resultsH - 2
	top := 0
	if innerH > 0 && s.sel >= innerH {
		top = s.sel - innerH + 1
	}
	for i := top; i < len(s.hits) && i-top < innerH; i++ {
		h := s.hits[i]
		ry := oy + resultsH - 2 - (i - top)
		b.text(ox+2, ry, "", "  ")
		b.text(ox+4, ry, ansiDim, fmt.Sprintf("%-8s", h.kind.kindTag()))
		b.text(ox+14, ry, "", h.label)
		if i == s.sel {
			paintSelAccent(b, ox+1, ry, leftW-1, false)
		}
	}

	// Preview pane follows the selection.
	px := ox + leftW + 1
	pw := ow - leftW - 1
	title := "preview"
	if s.sel < len(s.hits) {
		title = "preview · " + s.hits[s.sel].label
	}
	b.box(px, oy, pw, oh, ansiBorder, ansiDim, title)
	if s.sel < len(s.hits) {
		renderSearchPreview(b, m, s.hits[s.sel], px+2, oy+1, pw-4, oh-2)
	}
}

// renderSearchPreview fills the preview pane for one hit off the held
// snapshot: a lane previews its pipeline table, a pipeline its run table, a
// run its fact row.
func renderSearchPreview(b *screenBuf, m *psModel, h psHit, x, y, w, ph int) {
	sub := newScreenBuf(w, ph)
	switch h.kind {
	case psHitLane:
		renderTable(sub, 0, ph, pipelinesColumns(m, derivePipelines(m.snap, h.lane), w >= 90, nil), -1, false)
	case psHitPipeline:
		renderTable(sub, 0, ph, runsColumns(m, deriveRuns(m.snap, h.pipeline, true)), -1, false)
	case psHitRun:
		if run, ok := findRun(m.snap, h.runID); ok {
			fact := run.State
			if run.ExitCode != nil {
				fact += " · exit " + exitCodeCell(run.ExitCode)
			}
			if run.State == "running" {
				fact += " · CPU " + cpuText(run.Load) + " · MEM " + memText(run.Load)
			}
			sub.text(0, 0, psStateSGR(run.State), "run "+run.ID+" · "+run.Pipeline)
			sub.text(0, 1, "", fact)
		}
	}
	b.blit(sub, x, y)
}

// clipCells bounds s to w cells for a box-interior line.
func clipCells(s string, w int) string {
	r := []rune(s)
	if w < 0 {
		return ""
	}
	if len(r) > w {
		return string(r[:w])
	}
	return s
}

// renderCatalogOverlay splices the catalog picker over the dimmed frame (#219):
// pack list left with installed badges and tags, live preview right (README,
// pipeline tree, requires, sha), banner and key hints on the bottom band.
func renderCatalogOverlay(b *screenBuf, m *psModel) {
	b.dimAll()
	c := m.catalog

	ow := b.w * 9 / 10
	oh := b.h * 8 / 10
	ox := (b.w - ow) / 2
	oy := (b.h - oh) / 2
	leftW := ow * 2 / 5
	footH := 3
	listH := oh - footH

	b.box(ox, oy, leftW, listH, ansiBorder, ansiDim, "catalog")

	// Pack list, selection inverted; installed and shadowed badges plus tags ride
	// the row. The list windows over the packs so a selection moved past the pane
	// height stays visible (the search overlay's rule).
	innerH := listH - 2
	top := 0
	if innerH > 0 && c.sel >= innerH {
		top = c.sel - innerH + 1
	}
	for i := top; i < len(c.packs) && i-top < innerH; i++ {
		p := c.packs[i]
		label := p.Name
		if p.Installed {
			label += " ●installed"
		}
		if p.Shadowed {
			label += " (shadowed)"
		}
		if len(p.Tags) > 0 {
			label += "  " + strings.Join(p.Tags, ",")
		}
		row := oy + 1 + (i - top)
		// Mark circle: ○ unpicked, ● picked; clicking one toggles it.
		if c.marked[p.Name] {
			b.text(ox+2, row, ansiMagenta, "●")
		} else {
			b.text(ox+2, row, ansiDim, "○")
		}
		m.addClick(psClick{x: ox + 2, y: row, w: 1, kind: psClickMarkPack, idx: i})
		b.text(ox+4, row, "", clipCells(label, leftW-6))
		if i == c.sel {
			paintSelAccent(b, ox+1, row, leftW-1, false)
		}
	}
	if top+innerH < len(c.packs) {
		b.text(ox+2, oy+listH-1, ansiDim, fmt.Sprintf("─ %d more ─", len(c.packs)-top-innerH))
	}
	if c.loading {
		b.text(ox+2, oy+1, ansiDim, "loading…")
	} else if len(c.packs) == 0 {
		b.text(ox+2, oy+1, ansiDim, "no packs")
	}

	// Preview pane follows the selection.
	px := ox + leftW + 1
	pw := ow - leftW - 1
	title := "preview"
	if p := c.selected(); p != nil {
		title = "preview · " + p.Name
	}
	b.box(px, oy, pw, listH, ansiBorder, ansiDim, title)
	if p := c.selected(); p != nil {
		tx, ty, tw := px+2, oy+1, pw-4
		line := func(sgr, s string) {
			if ty < oy+listH-1 {
				b.text(tx, ty, sgr, clipCells(s, tw))
				ty++
			}
		}
		line(ansiCyan, p.Name+"  ["+p.Source+"]")
		if p.Description != "" {
			line("", p.Description)
		}
		if p.Requires != "" {
			line(ansiDim, "requires "+p.Requires)
		}
		if p.SHA256 != "" {
			line(ansiDim, "sha256 "+shortDigest(p.SHA256))
		}
		if len(p.Pipelines) > 0 {
			line("", "pipelines: "+strings.Join(p.Pipelines, ", "))
		}
		if len(p.ApplyOrder) > 0 {
			line(ansiDim, "apply order:")
			for _, step := range p.ApplyOrder {
				line(ansiDim, "  "+step)
			}
		}
		if p.Readme != "" {
			line("", "")
			for _, rl := range strings.Split(p.Readme, "\n") {
				line(ansiDim, rl)
			}
		}
	}

	// Bottom band: banner (yellow) above the key hints.
	b.box(ox, oy+listH, ow, footH, ansiBorder, ansiDim, "")
	hint := "␣ pick · ⏎ apply picked · + source · esc close"
	button := "" // the clickable select-then-apply affordance, when circles are picked
	if n := len(c.batch()); n > 0 {
		hint = "␣ mark · esc close"
		button = fmt.Sprintf("▶ apply %d marked", n)
	}
	switch {
	case c.addingURL:
		hint, button = "add source url: "+string(c.urlInput)+"█  · ⏎ add · esc cancel", ""
	case c.busy != "":
		hint, button = c.busy, ""
	}
	hintY := oy + listH + 1
	if c.banner != "" {
		b.text(ox+2, hintY, ansiYellow, clipCells(c.banner, ow-4))
		hintY = oy + listH + footH - 1
	}
	hx := ox + 2
	if button != "" {
		b.text(hx, hintY, ansiMagenta, button)
		m.addClick(psClick{x: hx, y: hintY, w: len([]rune(button)), kind: psClickCatalogApply})
		hx += len([]rune(button)) + 3
		b.text(hx-3, hintY, ansiDim, " · ")
	}
	b.text(hx, hintY, ansiDim, clipCells(hint, ox+ow-2-hx))
}
