package tui

import (
	"fmt"
	"strings"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
)

// This file is the `iris ps` catalog overlay's state machine (#219): pack list
// left, live preview right, select-then-apply — ○/● circles (space or click)
// pick packs; ⏎, 'a', and the ▶ button fire the batch straight, each pack
// riding /catalog/install (apply=true) in catalog order. All outcomes land as
// messages on the single-writer loop; a failure banners inline and never
// tears the view down.

// psCatalogReqKind names one overlay action the loop hands to the runner.
type psCatalogReqKind int

// The overlay's actions.
const (
	psCatalogList psCatalogReqKind = iota
	psCatalogApply
	psCatalogAddSource
)

// psCatalogReq is one action request the model parks for the loop.
type psCatalogReq struct {
	kind      psCatalogReqKind
	pack      string
	pipelines []string // the pack members to install, nil for the whole pack
	url       string   // the index URL an add-source request carries
	force     bool
	seq       int // correlation id; the outcome echoes it back
}

// psCatalogMsg is one action outcome the loop absorbs.
type psCatalogMsg struct {
	kind     psCatalogReqKind
	packs    []api.CatalogPack
	warnings []string
	res      *api.CatalogInstallResult
	sources  []string // the updated source list an add-source success carries
	err      string   // inline failure text; "" on success
	seq      int      // echo of the request's correlation id; stale outcomes are dropped
}

// psCatalogRow is one line of a catalog list. A catalog is browsed by pipeline
// -- what an operator is actually looking for is a scraper, not the crate it
// shipped in -- so a pack contributes one row per pipeline it declares. A pack
// whose index carries no member list still contributes a row naming itself,
// rather than vanishing from a list it belongs in.
//
// Picking is per pipeline. The install request carries the marked members of
// one pack and the leader takes their closure -- in-pack depends_on, and the
// rest of any lane a composer orders -- so what lands is always applicable even
// when the marks were not.
type psCatalogRow struct {
	// pipeline is the declared pipeline this row names, "" for a pack that
	// declares none.
	pipeline string
	// pack is the pack the row belongs to and the unit picking it marks.
	pack api.CatalogPack
}

// label is the row's leading cell: the pipeline, or the pack when it names none.
func (r psCatalogRow) label() string {
	if r.pipeline != "" {
		return r.pipeline
	}
	return r.pack.Name
}

// key identifies the row in the mark set. It is pack-qualified because two
// catalogs may ship a pipeline of the same name, and marking one must not mark
// the other.
func (r psCatalogRow) key() string {
	return r.pack.Name + "\x00" + r.pipeline
}

// psCatalog is one catalog surface's state: the overlay, or the idle card's
// inline searchable list.
type psCatalog struct {
	loading     bool
	packs       []api.CatalogPack
	sel         int             // cursor within visible() (the query-filtered list)
	query       []rune          // inline filter; the overlay never sets it
	searching   bool            // inline: printable keys extend the query
	busy        string          // in-flight action label, "" when idle
	banner      string          // inline error or notice
	pending     int             // seq of the one in-flight request; only its outcome is absorbed
	marked      map[string]bool // space-marked pack names for a batch apply
	queue       []psCatalogPick // remaining picks of the live batch, head in flight
	done        int             // packs already applied in the live batch
	appliedRows int             // pipelines the live batch has landed so far
	addingURL   bool            // the add-source URL prompt is open
	urlInput    []rune          // the prompt's typed URL
}

// updateAddSource routes a keypress while the add-source prompt is open:
// typing edits the URL, Enter submits it, Esc closes the prompt.
func (m *psModel) updateAddSource(c *psCatalog, k psKey) {
	switch k.kind {
	case psKeyCtrlC:
		m.quit = true
	case psKeyEsc:
		c.addingURL, c.urlInput = false, nil
	case psKeyBackspace:
		if len(c.urlInput) > 0 {
			c.urlInput = c.urlInput[:len(c.urlInput)-1]
		}
	case psKeyEnter:
		url := strings.TrimSpace(string(c.urlInput))
		if url == "" {
			return
		}
		c.addingURL, c.urlInput = false, nil
		c.banner, c.busy = "", "adding source "+url+"…"
		m.parkCatalogReqFor(c, psCatalogReq{kind: psCatalogAddSource, url: url})
	case psKeyRune:
		c.urlInput = append(c.urlInput, k.r)
	}
}

// toggleMark flips the batch mark on the pack under the cursor.
func (c *psCatalog) toggleMark() {
	c.toggleMarkAt(c.sel)
}

// toggleMarkAt flips the batch mark on the i-th visible row (the ○/● circle's
// click target). One row, one mark: a pipeline picks alone.
func (c *psCatalog) toggleMarkAt(i int) {
	vis := c.visible()
	if i < 0 || i >= len(vis) {
		return
	}
	key := vis[i].key()
	if c.marked == nil {
		c.marked = map[string]bool{}
	}
	if c.marked[key] {
		delete(c.marked, key)
	} else {
		c.marked[key] = true
	}
	c.banner = ""
}

// toggleMarkAll marks every visible row, or clears them all when they are
// already marked. It follows the filter: with a query typed it is "all of what
// I am looking at", not "all of the catalog".
func (c *psCatalog) toggleMarkAll() {
	vis := c.visible()
	if len(vis) == 0 {
		return
	}
	c.banner = ""
	allMarked := true
	for _, r := range vis {
		if !c.marked[r.key()] {
			allMarked = false
			break
		}
	}
	if allMarked {
		for _, r := range vis {
			delete(c.marked, r.key())
		}
		return
	}
	if c.marked == nil {
		c.marked = map[string]bool{}
	}
	for _, r := range vis {
		c.marked[r.key()] = true
	}
}

// working reports whether the surface owns an in-flight request or an
// unfinished batch — state that must outlive the view that opened it.
func (c *psCatalog) working() bool {
	return c.busy != "" || len(c.queue) > 0
}

// psCatalogPick is one pack's share of the marked rows: the pack, and the
// pipelines picked from it. An empty Pipelines means the pack declares none and
// was picked whole.
type psCatalogPick struct {
	pack      string
	pipelines []string
	// members is the pack's whole roster. A whole-pack pick deliberately sends no
	// pipeline list, so this is the only record of which rows it will clear.
	members []string
}

// batch groups the marked rows into one install per pack, in catalog order
// (nil when none marked). Install is per pack plus a member list, so twenty
// marks inside one pack are one request, not twenty.
func (c *psCatalog) batch() []psCatalogPick {
	if len(c.marked) == 0 {
		return nil
	}
	var out []psCatalogPick
	for _, p := range c.packs {
		pick := psCatalogPick{pack: p.Name, members: p.Pipelines}
		hit := false
		if len(p.Pipelines) == 0 {
			hit = c.marked[psCatalogRow{pack: p}.key()]
		}
		for _, name := range p.Pipelines {
			if c.marked[psCatalogRow{pipeline: name, pack: p}.key()] {
				pick.pipelines = append(pick.pipelines, name)
				hit = true
			}
		}
		// Every member marked is a whole-pack install: sending the full list
		// would work, but an empty one is what the API already means by it.
		if len(pick.pipelines) == len(p.Pipelines) {
			pick.pipelines = nil
		}
		if hit {
			out = append(out, pick)
		}
	}
	return out
}

// unmarkInstalled clears the marks for everything one landed install carried and
// tallies it. The leader reports the members it actually took, which is the
// requested picks plus their closure; falling back to the pick's own list keeps
// an older leader (or a whole-pack install, which reports every member) honest.
func (c *psCatalog) unmarkInstalled(res *api.CatalogInstallResult, pick psCatalogPick) {
	landed := pick.pipelines
	if len(landed) == 0 {
		landed = pick.members // a whole-pack pick clears every row it listed
	}
	if res != nil && len(res.Pipelines) > 0 {
		landed = res.Pipelines
	}
	if len(landed) == 0 {
		// A pack declaring no members: its single row is keyed on the bare pack.
		delete(c.marked, psCatalogRow{pack: api.CatalogPack{Name: pick.pack}}.key())
		c.appliedRows++
		return
	}
	for _, name := range landed {
		key := psCatalogRow{pipeline: name, pack: api.CatalogPack{Name: pick.pack}}.key()
		if c.marked[key] {
			delete(c.marked, key)
		}
	}
	c.appliedRows += len(landed)
}

// markedRows counts the picked rows, which is what the apply affordance shows:
// an operator marked pipelines and expects that number back, not a pack count.
func (c *psCatalog) markedRows() int {
	n := 0
	for _, p := range c.packs {
		if len(p.Pipelines) == 0 {
			if c.marked[psCatalogRow{pack: p}.key()] {
				n++
			}
			continue
		}
		for _, name := range p.Pipelines {
			if c.marked[psCatalogRow{pipeline: name, pack: p}.key()] {
				n++
			}
		}
	}
	return n
}

// openCatalog opens the overlay in its loading state and parks the list request.
func (m *psModel) openCatalog() {
	m.catalog = &psCatalog{loading: true}
	m.parkCatalogReqFor(m.catalog, psCatalogReq{kind: psCatalogList})
}

// openIdleCatalog opens the idle card's inline catalog and parks its list fetch.
func (m *psModel) openIdleCatalog() {
	m.idleCat = &psCatalog{loading: true}
	m.parkCatalogReqFor(m.idleCat, psCatalogReq{kind: psCatalogList})
}

// parkCatalogReqFor stamps the request with a fresh seq and marks it the
// surface's one in-flight action.
func (m *psModel) parkCatalogReqFor(c *psCatalog, req psCatalogReq) {
	m.catalogSeq++
	req.seq = m.catalogSeq
	c.pending = req.seq
	m.catalogReq = &req
}

// openPackCache parks the one background pack listing the detail pane's
// RETENTION block reads. It is deliberately NOT on the poller: ListPacks
// resolves every pack over the network with a SHA-256 verify, so a per-tick
// read would be a fetch storm against every configured source for as long as
// the view is open. One fetch at open, refreshed only when the overlay
// refetches or an install lands.
func (m *psModel) openPackCache() {
	if m.catalogReq != nil {
		return // a surface already owns this tick's request
	}
	m.catalogSeq++
	m.packsReq = m.catalogSeq
	m.catalogReq = &psCatalogReq{kind: psCatalogList, seq: m.packsReq}
}

// packsFor names the installed packs that declare the pipeline.
func (m *psModel) packsFor(pipeline string) []string {
	var out []string
	for _, p := range m.packs {
		if !p.Installed {
			continue
		}
		for _, name := range p.Pipelines {
			if name == pipeline {
				out = append(out, p.Name)
				break
			}
		}
	}
	return out
}

// takeCatalogReq hands the loop the parked request, once.
func (m *psModel) takeCatalogReq() *psCatalogReq {
	r := m.catalogReq
	m.catalogReq = nil
	return r
}

// visible is the query-filtered pack list (the full list on an empty query).
// Matches name, description, and tags, case-insensitively.
func (c *psCatalog) visible() []psCatalogRow {
	q := strings.ToLower(strings.TrimSpace(string(c.query)))
	var out []psCatalogRow
	for _, p := range c.packs {
		// A pipeline matches on its own name as well as its pack's, so filtering
		// for "lusa" finds the pipeline and filtering for the pack finds all of it.
		packHay := strings.ToLower(p.Name + " " + p.Description + " " + strings.Join(p.Tags, " "))
		if len(p.Pipelines) == 0 {
			if q == "" || strings.Contains(packHay, q) {
				out = append(out, psCatalogRow{pack: p})
			}
			continue
		}
		for _, name := range p.Pipelines {
			if q == "" || strings.Contains(packHay, q) || strings.Contains(strings.ToLower(name), q) {
				out = append(out, psCatalogRow{pipeline: name, pack: p})
			}
		}
	}
	return out
}

// selected returns the row under the cursor, nil on an empty (filtered) list.
func (c *psCatalog) selected() *psCatalogRow {
	vis := c.visible()
	if c.sel < 0 || c.sel >= len(vis) {
		return nil
	}
	return &vis[c.sel]
}

// move shifts the cursor within the visible list and clears any banner.
func (c *psCatalog) move(delta int) {
	c.banner = ""
	n := len(c.visible())
	if n == 0 {
		c.sel = 0
		return
	}
	c.sel += delta
	if c.sel < 0 {
		c.sel = 0
	}
	if c.sel >= n {
		c.sel = n - 1
	}
}

// updateCatalog routes a keypress while the overlay is open.
func (m *psModel) updateCatalog(k psKey) {
	c := m.catalog
	if c.busy != "" && k.kind != psKeyCtrlC {
		return // one action at a time; the outcome message unlocks the overlay
	}
	if c.addingURL {
		m.updateAddSource(c, k)
		return
	}
	switch k.kind {
	case psKeyCtrlC:
		m.quit = true
	case psKeyEsc:
		m.catalog = nil
	case psKeyUp:
		m.catalogMove(-1)
	case psKeyDown:
		m.catalogMove(1)
	case psKeyEnter:
		m.catalogApply(c)
	case psKeyRune:
		switch k.r {
		case 'q':
			m.catalog = nil
		case 'j':
			m.catalogMove(1)
		case 'k':
			m.catalogMove(-1)
		case 'a', 'A':
			m.catalogApply(c)
		case ' ':
			c.toggleMark()
		case '*':
			c.toggleMarkAll()
		case '+':
			c.addingURL, c.urlInput = true, nil
			c.banner = ""
		}
	}
}

// updateIdleCatalog routes a keypress at the idle card's inline catalog.
// Returns false when the key is not the catalog's, so it falls through to the
// normal idle bindings (q quit, : commands, c overlay).
func (m *psModel) updateIdleCatalog(k psKey) bool {
	c := m.idleCat
	if c.busy != "" && k.kind != psKeyCtrlC {
		return true // one action at a time; the outcome message unlocks the list
	}
	if c.addingURL {
		m.updateAddSource(c, k)
		return true
	}
	if c.searching {
		switch k.kind {
		case psKeyCtrlC:
			m.quit = true
		case psKeyEsc:
			c.searching = false
			c.query = nil
			c.move(0)
		case psKeyEnter:
			c.searching = false
		case psKeyBackspace:
			if len(c.query) > 0 {
				c.query = c.query[:len(c.query)-1]
				c.move(0)
			}
		case psKeyUp:
			c.move(-1)
		case psKeyDown:
			c.move(1)
		case psKeyRune:
			c.query = append(c.query, k.r)
			c.move(0)
		}
		return true
	}
	switch k.kind {
	case psKeyUp:
		c.move(-1)
		return true
	case psKeyDown:
		c.move(1)
		return true
	case psKeyEnter:
		// Inline enter is the same select-then-apply as the overlay: circles
		// pick, enter fires the batch straight.
		m.catalogApply(c)
		return true
	case psKeyEsc:
		if len(c.query) > 0 {
			c.query = nil
			c.move(0)
			return true
		}
	case psKeyRune:
		switch k.r {
		case '/':
			c.searching = true
			return true
		case 'j':
			c.move(1)
			return true
		case 'k':
			c.move(-1)
			return true
		case 'a', 'A':
			m.catalogApply(c)
			return true
		case ' ':
			c.toggleMark()
			return true
		case '*':
			c.toggleMarkAll()
			return true
		case '+':
			c.addingURL, c.urlInput = true, nil
			c.banner = ""
			return true
		}
	}
	return false
}

// catalogMove shifts the overlay's pack cursor.
func (m *psModel) catalogMove(delta int) {
	m.catalog.move(delta)
}

// psCatalogPickHint nudges toward the circles when apply fires with nothing picked.
const psCatalogPickHint = "nothing picked · ␣ or click ○ to pick · * for all"

// catalogApply fires the picked batch: one install+apply per marked pack, in
// catalog order, chained through the single-request loop. No confirm — enter,
// 'a', and the ▶ button all apply straight; with nothing picked they only
// nudge toward the circles.
func (m *psModel) catalogApply(c *psCatalog) {
	batch := c.batch()
	if len(batch) == 0 {
		c.banner = psCatalogPickHint
		return
	}
	c.banner = ""
	c.queue, c.done = batch, 0
	m.parkBatchHead(c)
}

// parkBatchHead requests install+apply for the batch queue's head.
func (m *psModel) parkBatchHead(c *psCatalog) {
	total := c.done + len(c.queue)
	next := c.queue[0]
	// Name what is going in: a narrowed pick reads as its pipelines, since the
	// pack name alone would suggest the whole shelf is landing.
	what := next.pack
	switch n := len(next.pipelines); {
	case n == 1:
		what = next.pipelines[0]
	case n > 1:
		what = fmt.Sprintf("%d of %s", n, next.pack)
	}
	c.busy = fmt.Sprintf("applying %s… (%d/%d)", what, c.done+1, total)
	if c == m.idleCat && !psIsEmptyWorkspace(m) {
		m.note = c.busy // the inline card is off-frame by now; the footer carries it
	}
	m.parkCatalogReqFor(c, psCatalogReq{kind: psCatalogApply, pack: next.pack, pipelines: next.pipelines, force: true})
}

// absorbCatalog folds one action outcome into whichever surface owns the
// request; an outcome for a closed or superseded request is dropped.
func (m *psModel) absorbCatalog(cm psCatalogMsg) {
	// A listing outcome always refreshes the pack cache, whichever surface
	// asked for it -- including the background fetch that owns no surface.
	if cm.kind == psCatalogList && cm.err == "" {
		m.packs = cm.packs
	}
	c := m.catalog
	if c == nil || cm.seq != c.pending {
		c = m.idleCat
	}
	if c == nil || cm.seq != c.pending {
		return
	}
	c.busy = ""
	switch cm.kind {
	case psCatalogList:
		c.loading = false
		c.packs = cm.packs
		c.banner = cm.err
		if cm.err == "" && len(cm.warnings) > 0 {
			c.banner = strings.Join(cm.warnings, " · ")
		}
		if c.sel >= len(c.visible()) {
			c.sel = 0
		}
		// Marks live only as long as their row stays listed: a pack dropping out
		// of the catalog takes its pipelines' marks with it, and so does a pack
		// that merely stopped declaring one of them.
		alive := map[string]bool{}
		for _, p := range c.packs {
			if len(p.Pipelines) == 0 {
				alive[psCatalogRow{pack: p}.key()] = true
			}
			for _, name := range p.Pipelines {
				alive[psCatalogRow{pipeline: name, pack: p}.key()] = true
			}
		}
		for key := range c.marked {
			if !alive[key] {
				delete(c.marked, key)
			}
		}
	case psCatalogAddSource:
		if cm.err != "" {
			c.banner = cm.err
			return
		}
		// The new source is live daemon-side: banner the grown count and chain
		// a list refresh so its packs land in this same surface.
		c.loading = true
		c.banner = fmt.Sprintf("source added (%d configured) · reloading", len(cm.sources))
		m.parkCatalogReqFor(c, psCatalogReq{kind: psCatalogList})
	case psCatalogApply:
		if cm.err != "" {
			c.banner = cm.err
			if skipped := len(c.queue) - 1; skipped > 0 {
				c.banner = fmt.Sprintf("%s · %d marked pack(s) skipped", cm.err, skipped)
			}
			c.queue, c.done = nil, 0
			return
		}
		// A batch head landing: pop it, unmark every row it carried, and chain
		// the next request (the loop drains the park right after this absorb).
		// The result's member list is what to clear, not the marks: the leader's
		// closure may have installed a sibling the operator never picked, and
		// leaving that one circled would invite a second install of it.
		if len(c.queue) > 0 {
			landed := c.queue[0]
			c.queue = c.queue[1:]
			c.done++
			c.unmarkInstalled(cm.res, landed)
			if len(c.queue) > 0 {
				m.parkBatchHead(c)
				return
			}
		}
		applied, pipelines := c.done, c.appliedRows
		c.queue, c.done, c.appliedRows = nil, 0, 0
		// Install-then-apply-then-watch: close the overlay (the inline idle
		// catalog leaves with the empty workspace itself); the 1s poll shows
		// the queued and running rows landing in the main frame.
		if c == m.catalog {
			m.catalog = nil
		}
		// One pack names what landed; several just count. A narrowed install says
		// the pipeline, because that is the thing that was picked.
		switch decls := len(cm.res.ApplyOrder); {
		case applied > 1:
			m.note = fmt.Sprintf("%d packs applied · runs landing", applied)
		case len(cm.res.Pipelines) == 1:
			m.note = fmt.Sprintf("%s applied (%d declarations) · runs landing", cm.res.Pipelines[0], decls)
		case len(cm.res.Pipelines) > 1 && pipelines < len(cm.res.Pipelines):
			// The leader's closure pulled in more than was picked; say the total.
			m.note = fmt.Sprintf("%d pipelines from %s applied (%d declarations) · runs landing", len(cm.res.Pipelines), cm.res.Pack, decls)
		default:
			m.note = fmt.Sprintf("%s applied (%d declarations) · runs landing", cm.res.Pack, decls)
		}
	}
}
