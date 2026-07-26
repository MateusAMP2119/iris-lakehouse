package tui

import (
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
)

// TestPsClick proves the mouse layer: rendering registers regions and a left
// press routes through the one under the cursor.
func TestPsClick(t *testing.T) {
	region := func(m *psModel, kind psClickKind, idx int) psClick {
		t.Helper()
		for _, r := range m.clicks {
			if r.kind == kind && r.idx == idx {
				return r
			}
		}
		t.Fatalf("no click region kind=%d idx=%d registered", kind, idx)
		return psClick{}
	}

	t.Run("idle catalog: row clicks only browse, never install", func(t *testing.T) {
		m := newPsModel(Snapshot{Ps: api.PsPayload{
			Engine: api.PsEngine{Version: "dev", Role: "leader", PID: 7, Uptime: "12s"},
		}}, "")
		m.idleCat.loading = false
		m.idleCat.packs = []api.CatalogPack{{Name: "alpha"}, {Name: "beta"}}
		m.takeCatalogReq() // drain the surface's own list fetch
		renderPsFrame(m, 100, 30, false)

		r := region(m, psClickCatalogRow, 1)
		m.click(r.x, r.y)
		if m.idleCat.sel != 1 {
			t.Fatalf("click should select row 1, sel=%d", m.idleCat.sel)
		}
		m.click(r.x, r.y)
		m.click(r.x, r.y)
		if m.idleCat.busy != "" || m.catalogReq != nil {
			t.Fatalf("repeat row clicks must stay browse-only, got %+v req=%+v", m.idleCat, m.catalogReq)
		}
	})

	t.Run("idle filter and quit regions", func(t *testing.T) {
		m := newPsModel(Snapshot{Ps: api.PsPayload{
			Engine: api.PsEngine{Version: "dev", Role: "leader", PID: 7, Uptime: "12s"},
		}}, "")
		renderPsFrame(m, 100, 30, false)

		f := region(m, psClickCatalogFilter, 0)
		m.click(f.x, f.y)
		if !m.idleCat.searching {
			t.Fatal("filter click should focus the search")
		}
		m.idleCat.searching = false
		q := region(m, psClickActionQuit, 0)
		m.click(q.x, q.y)
		if !m.quit {
			t.Fatal("quit row click should quit")
		}
	})

	t.Run("dashboard: pane focus and table row selection", func(t *testing.T) {
		m := newPsModel(psvFixture(), "")
		renderPsFrame(m, 150, 40, false)

		var logsPane, tableRow psClick
		for _, r := range m.clicks {
			if r.kind == psClickPane && r.pane == psPaneEvents {
				logsPane = r
			}
			if r.kind == psClickTableRow && tableRow.w == 0 {
				tableRow = r
			}
		}
		if logsPane.w == 0 || tableRow.w == 0 {
			t.Fatalf("dashboard regions missing: logs=%+v row=%+v", logsPane, tableRow)
		}
		m.click(logsPane.x+1, logsPane.y+1)
		if m.pane != psPaneEvents {
			t.Fatalf("logs pane click should focus logs, pane=%d", m.pane)
		}
		// The rail opens on a pipeline, so the statistics pane lists its runs.
		m.click(tableRow.x, tableRow.y)
		if m.pane != psPaneStats || m.tblRun != tableRow.name {
			t.Fatalf("table row click should focus the pane and select %q, got pane=%d sel=%q",
				tableRow.name, m.pane, m.tblRun)
		}
	})

	t.Run("overlays swallow clicks", func(t *testing.T) {
		m := newPsModel(Snapshot{Ps: api.PsPayload{
			Engine: api.PsEngine{Version: "dev", Role: "leader", PID: 7, Uptime: "12s"},
		}}, "")
		renderPsFrame(m, 100, 30, false)
		q := region(m, psClickActionQuit, 0)
		m.openCommand()
		m.click(q.x, q.y)
		if m.quit {
			t.Fatal("clicks must be inert under an open overlay")
		}
	})

	t.Run("pack mark circle click fills and empties it", func(t *testing.T) {
		m := newPsModel(Snapshot{Ps: api.PsPayload{
			Engine: api.PsEngine{Version: "dev", Role: "leader", PID: 7, Uptime: "12s"},
		}}, "")
		m.idleCat.loading = false
		m.idleCat.packs = []api.CatalogPack{{Name: "alpha"}, {Name: "beta"}}
		renderPsFrame(m, 100, 30, false)
		r := region(m, psClickMarkPack, 1)
		m.click(r.x, r.y)
		if !m.idleCat.marked["beta"] {
			t.Fatalf("marked = %v, want the clicked circle filled", m.idleCat.marked)
		}
		if m.idleCat.sel == 1 {
			t.Error("a circle click must not move the row cursor")
		}
		renderPsFrame(m, 100, 30, false)
		r = region(m, psClickMarkPack, 1)
		m.click(r.x, r.y)
		if len(m.idleCat.marked) != 0 {
			t.Fatalf("marked = %v, want the second click to empty the circle", m.idleCat.marked)
		}
	})

	t.Run("pipeline marks come from the rail circle and the scoped pane", func(t *testing.T) {
		m := newPsModel(psvFixture(), "")
		renderPsFrame(m, 150, 40, false)
		var rail *psClick
		for i := range m.clicks {
			if m.clicks[i].kind == psClickMarkPipeline {
				rail = &m.clicks[i]
				break
			}
		}
		if rail == nil {
			t.Fatalf("want a rail mark circle, clicks=%+v", m.clicks)
		}
		m.click(rail.x, rail.y)
		if !m.markedPipes[rail.name] {
			t.Fatalf("marked = %v, want %q filled from the rail circle", m.markedPipes, rail.name)
		}
		// Space in the statistics pane marks the pipeline it is scoped to.
		m.update(key('j'))
		m.update(psKey{kind: psKeyTab})
		m.update(key(' '))
		if !m.markedPipes[m.selPipeline] {
			t.Fatalf("marked = %v, want the scoped %q marked", m.markedPipes, m.selPipeline)
		}
	})

	t.Run("select-then-apply: circle clicks then the apply button fires the batch", func(t *testing.T) {
		m := newPsModel(Snapshot{Ps: api.PsPayload{
			Engine: api.PsEngine{Version: "dev", Role: "leader", PID: 7, Uptime: "12s"},
		}}, "")
		m.idleCat.loading = false
		m.idleCat.packs = []api.CatalogPack{{Name: "alpha"}, {Name: "beta"}}
		renderPsFrame(m, 100, 30, false)
		r := region(m, psClickMarkPack, 0)
		m.click(r.x, r.y)
		renderPsFrame(m, 100, 30, false)
		r = region(m, psClickMarkPack, 1)
		m.click(r.x, r.y)
		renderPsFrame(m, 100, 30, false) // the apply button renders once circles are picked
		a := region(m, psClickCatalogApply, 0)
		m.click(a.x, a.y)
		if m.idleCat.busy == "" || m.catalogReq == nil || m.catalogReq.kind != psCatalogApply {
			t.Fatalf("apply click should start the batch, busy=%q req=%+v", m.idleCat.busy, m.catalogReq)
		}
		if got := len(m.idleCat.queue); got != 2 || m.idleCat.queue[0] != "alpha" {
			t.Fatalf("queue = %v, want both packs with alpha in flight at the head", m.idleCat.queue)
		}
	})

	t.Run("overlay apply button clicks through the overlay guard", func(t *testing.T) {
		m := newPsModel(psvFixture(), "")
		m.update(key(':'))
		typeLine(m, "catalog")
		m.update(psKey{kind: psKeyEnter})
		req := m.takeCatalogReq()
		m.absorbCatalog(psCatalogMsg{kind: psCatalogList, seq: req.seq, packs: []api.CatalogPack{{Name: "alpha"}, {Name: "beta"}}})
		m.update(key(' ')) // mark alpha under the cursor
		renderPsFrame(m, 100, 30, false)
		a := region(m, psClickCatalogApply, 0)
		m.click(a.x, a.y)
		if m.catalog == nil || m.catalog.busy == "" || m.catalogReq == nil || m.catalogReq.kind != psCatalogApply || m.catalogReq.pack != "alpha" {
			t.Fatalf("overlay apply click should start the batch, req=%+v", m.catalogReq)
		}
	})

	t.Run("overlay mark circles click through the overlay guard", func(t *testing.T) {
		m := newPsModel(psvFixture(), "")
		m.update(key(':'))
		typeLine(m, "catalog")
		m.update(psKey{kind: psKeyEnter})
		req := m.takeCatalogReq()
		m.absorbCatalog(psCatalogMsg{kind: psCatalogList, seq: req.seq, packs: []api.CatalogPack{{Name: "alpha"}, {Name: "beta"}}})
		renderPsFrame(m, 100, 30, false)
		r := region(m, psClickMarkPack, 0)
		m.click(r.x, r.y)
		if !m.catalog.marked["alpha"] {
			t.Fatalf("marked = %v, want the overlay circle to fill", m.catalog.marked)
		}
		if m.quit {
			t.Fatal("the overlay must still swallow non-circle clicks")
		}
	})
}
