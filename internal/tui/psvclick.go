package tui

// Mouse support for the live view: renders register click regions into the
// model as they paint, and one left press routes through the region under the
// cursor. Regions are rebuilt every frame, so hit-testing always matches the
// geometry on screen. Overlays and the frozen view ignore clicks.

// psClickKind names one clickable surface of the frame.
type psClickKind int

const (
	psClickPane psClickKind = iota
	psClickLane
	psClickRailPipeline
	psClickTableRow
	psClickCatalogRow
	psClickCatalogFilter
	psClickActionLogs
	psClickActionQuit
	psClickMarkPack     // a pack row's ○/● mark circle (inline list and overlay)
	psClickMarkPipeline // a pipeline row's ○/● mark circle (rail and table)
	psClickCatalogApply // the "apply N marked" affordance (inline list and overlay)
	psClickPsCatFilter  // the catalog pane's filter input box (#238 C1d)
	psClickPsEvtFilter  // the events pane's filter input box (#238 C1d)
	psClickRailTable    // a written-table row in the catalog (#238 phase 3)
)

// psClick is one clickable rectangle (h defaults to a single row).
type psClick struct {
	x, y, w, h int
	kind       psClickKind
	pane       psPane
	lane, name string
	idx        int
}

// addClick registers a region for the frame being rendered.
func (m *psModel) addClick(r psClick) {
	if r.h == 0 {
		r.h = 1
	}
	m.clicks = append(m.clicks, r)
}

// click routes one left press at (x, y). Later-registered regions win, so row
// targets registered after their pane's focus rectangle take precedence. The
// catalog overlay swallows every click but its own mark circles.
func (m *psModel) click(x, y int) {
	if m.search != nil || m.command != nil || m.frozen {
		return
	}
	for i := len(m.clicks) - 1; i >= 0; i-- {
		r := m.clicks[i]
		if x < r.x || x >= r.x+r.w || y < r.y || y >= r.y+r.h {
			continue
		}
		if m.catalog != nil && r.kind != psClickMarkPack && r.kind != psClickCatalogApply {
			continue
		}
		m.clickOn(r)
		return
	}
}

// clickOn applies one region's action.
func (m *psModel) clickOn(r psClick) {
	switch r.kind {
	case psClickPane:
		m.pane = r.pane
	case psClickLane:
		m.pane = psPaneLanes
		m.selectTree(psTreeRow{lane: r.lane})
	case psClickRailPipeline:
		m.pane = psPaneLanes
		m.selectTree(psTreeRow{lane: r.lane, pipeline: r.name})
	case psClickRailTable:
		m.pane = psPaneLanes
		m.selectTree(psTreeRow{lane: r.lane, table: r.name})
	case psClickTableRow:
		m.pane = psPaneStats
		if m.selPipeline != "" {
			if m.tblRun == r.name {
				m.enter() // second click pins the run's logs
				return
			}
			m.tblRun = r.name
			return
		}
		if m.tblPipeline == r.name {
			m.enter() // second click drills into the pipeline
			return
		}
		m.tblPipeline = r.name
	case psClickCatalogRow:
		// Rows only browse (cursor + preview); picking is the circles' job and
		// applying the ▶ button's — select-then-apply.
		if c := m.idleCat; c != nil {
			c.move(r.idx - c.sel)
		}
	case psClickCatalogFilter:
		if m.idleCat != nil {
			m.idleCat.searching = true
		}
	case psClickPsCatFilter:
		m.pane = psPaneLanes
		m.catInput = true
	case psClickPsEvtFilter:
		m.pane = psPaneEvents
		m.evtInput = true
	case psClickActionLogs:
		m.openCommand()
		m.command.input = []rune("logs ")
		m.command.syncSel()
	case psClickActionQuit:
		m.quit = true
	case psClickMarkPack:
		c := m.catalog
		if c == nil {
			c = m.idleCat
		}
		if c == nil || c.busy != "" || c.addingURL {
			return
		}
		c.toggleMarkAt(r.idx)
	case psClickMarkPipeline:
		m.togglePipeMark(r.name)
	case psClickCatalogApply:
		// Select-then-apply: circles picked the packs, this fires the batch
		// (the same immediate path as the a key).
		c := m.catalog
		if c == nil {
			c = m.idleCat
		}
		if c == nil || c.busy != "" || c.addingURL || len(c.batch()) == 0 {
			return
		}
		m.catalogApply(c)
	}
}
