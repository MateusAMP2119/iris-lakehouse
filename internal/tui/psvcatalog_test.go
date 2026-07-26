package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
)

// openLoadedCatalog opens the overlay and absorbs a two-pack listing.
func openLoadedCatalog(m *psModel) {
	m.update(key(':'))
	typeLine(m, "catalog")
	m.update(psKey{kind: psKeyEnter})
	req := m.takeCatalogReq()
	m.absorbCatalog(psCatalogMsg{kind: psCatalogList, seq: req.seq, packs: []api.CatalogPack{
		{Name: "quake-monitor", Installed: false, Pipelines: []string{"quake_feed", "quake_report"}},
		{Name: "dlq-demo"},
	}})
}

// TestPsCatalogOverlay proves the overlay state machine (#219): listing, the
// select-then-apply gate over the picked circles, apply-and-return, inline failures.
func TestPsCatalogOverlay(t *testing.T) {
	t.Run("ps-catalog-overlay", func(t *testing.T) {
		t.Run("list absorb fills packs and clears loading", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			openLoadedCatalog(m)
			c := m.catalog
			if c == nil || c.loading || len(c.packs) != 2 {
				t.Fatalf("overlay = %+v, want two packs loaded", c)
			}
		})

		t.Run("a failed list banners inline, view stands", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.update(key(':'))
			typeLine(m, "catalog")
			m.update(psKey{kind: psKeyEnter})
			req := m.takeCatalogReq()
			m.absorbCatalog(psCatalogMsg{kind: psCatalogList, seq: req.seq, err: "catalog list failed: missing scope"})
			if m.catalog == nil || !strings.Contains(m.catalog.banner, "missing scope") || m.quit {
				t.Fatalf("overlay = %+v, want the inline banner", m.catalog)
			}
		})

		t.Run("enter with nothing picked only nudges toward the circles", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			openLoadedCatalog(m)
			m.update(psKey{kind: psKeyEnter})
			if m.catalogReq != nil {
				t.Fatal("enter with no picked packs must not park anything")
			}
			if !strings.Contains(m.catalog.banner, "nothing picked") {
				t.Fatalf("banner = %q, want the pick nudge", m.catalog.banner)
			}
			m.update(key('a'))
			if m.catalogReq != nil {
				t.Fatal("'a' with no picked packs must not park either")
			}
		})

		t.Run("enter fires the picked batch straight", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			openLoadedCatalog(m)
			m.update(key(' ')) // pick quake-monitor
			m.update(psKey{kind: psKeyEnter})
			req := m.takeCatalogReq()
			if req == nil || req.kind != psCatalogApply || req.pack != "quake-monitor" || !req.force {
				t.Fatalf("enter parked %+v, want the batch head's apply", req)
			}
			if m.catalog.busy == "" {
				t.Error("in-flight apply must lock the overlay busy")
			}
		})

		t.Run("'a' fires the picked batch and success returns to the main frame", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			openLoadedCatalog(m)
			m.update(key(' ')) // pick quake-monitor
			m.update(key('a'))
			areq := m.takeCatalogReq()
			if areq == nil || areq.kind != psCatalogApply || !areq.force {
				t.Fatalf("'a' parked %+v, want the forced install+apply", areq)
			}
			m.absorbCatalog(psCatalogMsg{kind: psCatalogApply, seq: areq.seq, res: &api.CatalogInstallResult{Pack: "quake-monitor", ApplyOrder: []string{"x", "y", "z"}}})
			if m.catalog != nil {
				t.Fatal("a successful apply must close the overlay")
			}
			if !strings.Contains(m.note, "quake-monitor applied (3 declarations)") {
				t.Errorf("note = %q, want the applied summary", m.note)
			}
		})

		t.Run("a not-leader apply banners inline with the leader hint", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			openLoadedCatalog(m)
			m.update(key(' '))
			m.update(key('a'))
			areq := m.takeCatalogReq()
			m.absorbCatalog(psCatalogMsg{kind: psCatalogApply, seq: areq.seq, err: "catalog install failed: this daemon is not the leader · leader: 10.0.0.9:7433"})
			if m.catalog == nil || !strings.Contains(m.catalog.banner, "leader: 10.0.0.9") {
				t.Fatalf("overlay = %+v, want the inline not-leader banner", m.catalog)
			}
		})

		t.Run("busy overlay swallows keys until the outcome lands", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			openLoadedCatalog(m)
			m.update(key(' '))
			m.update(key('a'))
			m.takeCatalogReq()
			m.update(psKey{kind: psKeyEnter})
			if m.catalogReq != nil {
				t.Fatal("keys during a busy action must not park new requests")
			}
		})

		t.Run("esc closes the overlay", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			openLoadedCatalog(m)
			m.update(psKey{kind: psKeyEsc})
			if m.catalog != nil {
				t.Fatal("esc must close the overlay")
			}
		})

		t.Run("a stale outcome for a superseded request is dropped", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.update(key(':'))
			typeLine(m, "catalog")
			m.update(psKey{kind: psKeyEnter})
			req1 := m.takeCatalogReq()
			m.update(psKey{kind: psKeyEsc}) // close mid-fetch
			m.update(key(':'))
			typeLine(m, "catalog")
			m.update(psKey{kind: psKeyEnter})
			req2 := m.takeCatalogReq()
			m.absorbCatalog(psCatalogMsg{kind: psCatalogList, seq: req1.seq, packs: []api.CatalogPack{{Name: "stale"}}})
			if !m.catalog.loading || len(m.catalog.packs) != 0 {
				t.Fatalf("overlay = %+v, want the first fetch's late outcome dropped", m.catalog)
			}
			m.absorbCatalog(psCatalogMsg{kind: psCatalogList, seq: req2.seq, packs: []api.CatalogPack{{Name: "quake-monitor"}}})
			if m.catalog.loading || len(m.catalog.packs) != 1 {
				t.Fatalf("overlay = %+v, want the live fetch absorbed", m.catalog)
			}
			// A late list must not unlock a busy apply or retarget the batch.
			m.update(key(' ')) // pick quake-monitor
			m.update(key('a'))
			req3 := m.takeCatalogReq()
			m.absorbCatalog(psCatalogMsg{kind: psCatalogList, seq: req2.seq, packs: nil})
			if m.catalog.busy == "" || len(m.catalog.packs) != 1 {
				t.Fatalf("overlay = %+v, want the superseded list dropped while applying", m.catalog)
			}
			m.absorbCatalog(psCatalogMsg{kind: psCatalogApply, seq: req3.seq, res: &api.CatalogInstallResult{Pack: "quake-monitor"}})
			if m.catalog != nil {
				t.Fatal("the in-flight apply's own outcome must still land")
			}
		})
	})
}

// TestPsCatalogBatch proves the space-marked batch apply: marks toggle, 'a'
// chains one apply per marked pack through the single-request loop, a failure
// stops the chain naming the skipped tail, and a refreshed listing prunes
// marks whose packs vanished.
func TestPsCatalogBatch(t *testing.T) {
	t.Run("ps-catalog-batch", func(t *testing.T) {
		markBoth := func(m *psModel) {
			m.update(key(' ')) // quake-monitor
			m.update(psKey{kind: psKeyDown})
			m.update(key(' ')) // dlq-demo
		}

		t.Run("space toggles the mark under the cursor", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			openLoadedCatalog(m)
			m.update(key(' '))
			if !m.catalog.marked["quake-monitor"] {
				t.Fatalf("marked = %v, want quake-monitor marked", m.catalog.marked)
			}
			m.update(key(' '))
			if len(m.catalog.marked) != 0 {
				t.Fatalf("marked = %v, want the second space to unmark", m.catalog.marked)
			}
		})

		t.Run("'a' applies the whole batch, one pack per outcome", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			openLoadedCatalog(m)
			markBoth(m)
			m.update(key('a'))
			req := m.takeCatalogReq()
			if req == nil || req.kind != psCatalogApply || req.pack != "quake-monitor" {
				t.Fatalf("batch head parked %+v, want quake-monitor's apply", req)
			}
			if !strings.Contains(m.catalog.busy, "(1/2)") {
				t.Errorf("busy = %q, want the (1/2) progress", m.catalog.busy)
			}
			m.absorbCatalog(psCatalogMsg{kind: psCatalogApply, seq: req.seq, res: &api.CatalogInstallResult{Pack: "quake-monitor"}})
			req = m.takeCatalogReq()
			if req == nil || req.kind != psCatalogApply || req.pack != "dlq-demo" {
				t.Fatalf("chained request = %+v, want dlq-demo's apply", req)
			}
			if m.catalog == nil || !strings.Contains(m.catalog.busy, "(2/2)") {
				t.Fatalf("overlay = %+v, want it open and busy (2/2)", m.catalog)
			}
			m.absorbCatalog(psCatalogMsg{kind: psCatalogApply, seq: req.seq, res: &api.CatalogInstallResult{Pack: "dlq-demo"}})
			if m.catalog != nil {
				t.Fatal("the last batch success must close the overlay")
			}
			if !strings.Contains(m.note, "2 packs applied") {
				t.Errorf("note = %q, want the batch summary", m.note)
			}
		})

		t.Run("the idle card's batch survives the workspace filling mid-flight", func(t *testing.T) {
			// The empty workspace's inline card owns the batch state.
			m := newPsModel(Snapshot{Ps: api.PsPayload{Engine: api.PsEngine{Version: "dev"}}}, "")
			req := m.takeCatalogReq()
			if req == nil || req.kind != psCatalogList {
				t.Fatalf("idle card parked %+v, want its list fetch", req)
			}
			m.absorbCatalog(psCatalogMsg{kind: psCatalogList, seq: req.seq,
				packs: []api.CatalogPack{{Name: "quake-monitor"}, {Name: "dlq-demo"}}})
			m.idleCat.marked = map[string]bool{"quake-monitor": true, "dlq-demo": true}
			m.catalogApply(m.idleCat)
			head := m.takeCatalogReq()
			if head == nil || head.pack != "quake-monitor" {
				t.Fatalf("batch head = %+v, want quake-monitor", head)
			}

			// Applying the first pack registers work: the workspace stops being
			// empty, but the card still owns the rest of the batch.
			m.absorb(psvFixture())
			if m.idleCat == nil {
				t.Fatal("a working card must outlive the empty workspace, else the batch strands")
			}
			m.absorbCatalog(psCatalogMsg{kind: psCatalogApply, seq: head.seq,
				res: &api.CatalogInstallResult{Pack: "quake-monitor"}})
			next := m.takeCatalogReq()
			if next == nil || next.pack != "dlq-demo" {
				t.Fatalf("chained request = %+v, want dlq-demo's apply", next)
			}
			if !strings.Contains(m.note, "dlq-demo") {
				t.Errorf("note = %q, want the off-frame progress", m.note)
			}
			m.absorbCatalog(psCatalogMsg{kind: psCatalogApply, seq: next.seq,
				res: &api.CatalogInstallResult{Pack: "dlq-demo"}})
			if !strings.Contains(m.note, "2 packs applied") {
				t.Errorf("note = %q, want the batch summary", m.note)
			}
			// Batch done: the next poll retires the surface.
			m.absorb(psvFixture())
			if m.idleCat != nil {
				t.Error("an idle card with nothing in flight must retire with the empty workspace")
			}
		})

		t.Run("a mid-batch failure banners and drops the tail", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			openLoadedCatalog(m)
			markBoth(m)
			m.update(key('a'))
			req := m.takeCatalogReq()
			m.absorbCatalog(psCatalogMsg{kind: psCatalogApply, seq: req.seq, err: "catalog install failed: boom"})
			c := m.catalog
			if c == nil || !strings.Contains(c.banner, "boom") || !strings.Contains(c.banner, "1 marked packs skipped") {
				t.Fatalf("banner = %q, want the failure plus the skipped count", c.banner)
			}
			if len(c.queue) != 0 || m.catalogReq != nil {
				t.Fatalf("queue = %v, want the batch cleared with nothing parked", c.queue)
			}
		})

		t.Run("a refreshed listing prunes marks of vanished packs", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			openLoadedCatalog(m)
			markBoth(m)
			m.parkCatalogReqFor(m.catalog, psCatalogReq{kind: psCatalogList})
			req := m.takeCatalogReq()
			m.absorbCatalog(psCatalogMsg{kind: psCatalogList, seq: req.seq, packs: []api.CatalogPack{{Name: "dlq-demo"}}})
			if m.catalog.marked["quake-monitor"] || !m.catalog.marked["dlq-demo"] {
				t.Fatalf("marked = %v, want only dlq-demo to survive", m.catalog.marked)
			}
		})
	})
}

// TestPsCatalogAddSource proves the '+' add-source prompt: typed URL, Enter
// parks the request, a success chains a list refresh, a failure banners inline.
func TestPsCatalogAddSource(t *testing.T) {
	t.Run("ps-catalog-add-source", func(t *testing.T) {
		typeURL := func(m *psModel, url string) {
			for _, r := range url {
				m.update(key(r))
			}
		}

		t.Run("+ opens the prompt, enter parks the add request", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			openLoadedCatalog(m)
			m.update(key('+'))
			if !m.catalog.addingURL {
				t.Fatal("+ must open the add-source prompt")
			}
			typeURL(m, "https://cat.example/catalog.json")
			m.update(psKey{kind: psKeyEnter})
			req := m.takeCatalogReq()
			if req == nil || req.kind != psCatalogAddSource || req.url != "https://cat.example/catalog.json" {
				t.Fatalf("enter parked %+v, want the add-source request", req)
			}
			if m.catalog.addingURL || m.catalog.busy == "" {
				t.Fatalf("prompt = %+v, want it closed and the surface busy", m.catalog)
			}
		})

		t.Run("a success banners the grown count and chains a list refresh", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			openLoadedCatalog(m)
			m.update(key('+'))
			typeURL(m, "https://cat.example/catalog.json")
			m.update(psKey{kind: psKeyEnter})
			req := m.takeCatalogReq()
			m.absorbCatalog(psCatalogMsg{kind: psCatalogAddSource, seq: req.seq, sources: []string{"a", "b"}})
			if !strings.Contains(m.catalog.banner, "source added (2 configured)") {
				t.Fatalf("banner = %q, want the grown count", m.catalog.banner)
			}
			next := m.takeCatalogReq()
			if next == nil || next.kind != psCatalogList {
				t.Fatalf("chained request = %+v, want the list refresh", next)
			}
		})

		t.Run("a refusal banners inline and keeps the surface", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			openLoadedCatalog(m)
			m.update(key('+'))
			typeURL(m, "https://dead/catalog.json")
			m.update(psKey{kind: psKeyEnter})
			req := m.takeCatalogReq()
			m.absorbCatalog(psCatalogMsg{kind: psCatalogAddSource, seq: req.seq, err: "catalog source refused: no catalog.json here"})
			if m.catalog == nil || !strings.Contains(m.catalog.banner, "no catalog.json here") {
				t.Fatalf("overlay = %+v, want the inline refusal banner", m.catalog)
			}
		})

		t.Run("esc closes the prompt without parking anything", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			openLoadedCatalog(m)
			m.update(key('+'))
			typeURL(m, "https://x")
			m.update(psKey{kind: psKeyEsc})
			if m.catalog == nil || m.catalog.addingURL || m.catalogReq != nil {
				t.Fatalf("esc must close only the prompt: %+v", m.catalog)
			}
		})
	})
}

// TestRunPsLoopCatalogWiring proves the loop hands parked overlay requests to the
// runner and absorbs their outcomes back into the frame.
func TestRunPsLoopCatalogWiring(t *testing.T) {
	t.Run("run-ps-loop-catalog-wiring", func(t *testing.T) {
		keys := make(chan psKey, 16)
		catalogMsgs := make(chan psCatalogMsg, 4)
		var mu sync.Mutex
		var got []psCatalogReq
		out := &syncBuffer{}
		v := &psView{
			out: out, p: painter{}, size: func() (int, int) { return 100, 30 },
			keys: keys, polls: make(chan psPollMsg, 1), notes: make(chan string, 1),
			cancelCh:    make(chan string, 4),
			catalogMsgs: catalogMsgs,
			runCatalog: func(req psCatalogReq) {
				mu.Lock()
				got = append(got, req)
				mu.Unlock()
				catalogMsgs <- psCatalogMsg{kind: psCatalogList, seq: req.seq, packs: []api.CatalogPack{{Name: "quake-monitor", Source: "https://cat.example/catalog.json"}}}
			},
		}
		m := newPsModel(psvFixture(), "")
		done := make(chan error, 1)
		go func() { done <- runPsLoop(context.Background(), v, m) }()

		keys <- key(':')
		for _, r := range "catalog" {
			keys <- key(r)
		}
		keys <- psKey{kind: psKeyEnter}
		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(out.String(), "quake-monitor") {
			if time.Now().After(deadline) {
				t.Fatal("the absorbed listing never rendered")
			}
			time.Sleep(5 * time.Millisecond)
		}
		keys <- psKey{kind: psKeyEsc}
		keys <- key('q')
		if err := <-done; err != nil {
			t.Fatalf("loop exit = %v, want nil", err)
		}
		mu.Lock()
		defer mu.Unlock()
		// Two listing reads, both deliberate: the background pack cache the
		// view opens with (the detail pane's RETENTION row reads it) and the
		// overlay's own refresh when it opens. Neither is on the poll tick --
		// the listing resolves packs over the network, so it never rides one.
		if len(got) != 2 {
			t.Fatalf("runner saw %+v, want the pack cache read plus the overlay's", got)
		}
		for i, req := range got {
			if req.kind != psCatalogList {
				t.Errorf("request %d = %+v, want a list request", i, req)
			}
		}
	})
}
