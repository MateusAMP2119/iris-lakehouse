package tui

import (
	"reflect"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
)

// psvFixture is a two-lane snapshot: ingest carries a running and a queued
// run plus an idle member, reporting is fully idle, and one laneless pipeline
// (solo) stands as its own anonymous lane. Runs are newest first, as the wire
// orders them.
func psvFixture() Snapshot {
	exit0, exit3 := 0, 3
	return Snapshot{Ps: api.PsPayload{
		Engine: api.PsEngine{Version: "dev", Role: "leader", PID: 42, Uptime: "2h13m",
			QueuedRuns: 1, RunningRuns: 1, Load: &api.PsLoad{CPUPercent: 3.2, RSSBytes: 126 << 20}},
		Retention: &api.PsRetention{Retain: 1000},
		Runs: []api.PsRun{
			{ID: "14", Pipeline: "load_orders", Lane: "ingest", State: "running", Cause: "loop",
				Load: &api.PsLoad{CPUPercent: 51, RSSBytes: 24 << 20}, Elapsed: "2m14s"},
			{ID: "12", Pipeline: "extract", Lane: "ingest", State: "queued", Cause: "manual"},
			{ID: "9", Pipeline: "load_orders", Lane: "ingest", State: "succeeded", ExitCode: &exit0, Duration: "3m2s", Cause: "loop"},
			{ID: "6", Pipeline: "load_orders", Lane: "ingest", State: "dead_lettered", ExitCode: &exit3, Duration: "1m34s", Cause: "replay"},
			{ID: "2", Pipeline: "solo", State: "succeeded", ExitCode: &exit0, Duration: "40ms", Cause: "manual"},
		},
		PipelineTimes: []api.PsPipelineTime{
			{Pipeline: "load_orders", Runs: 2, Last: "1m34s", Avg: "2m18s", P50: "1m34s", Max: "3m2s", Levels: []int{8, 5}},
			{Pipeline: "solo", Runs: 1, Last: "40ms", Avg: "40ms", P50: "40ms", Max: "40ms", Levels: []int{8}},
		},
	},
		Journal: foldJournal(nil, api.JournalActivity{Watermark: 8110, Groups: []api.JournalActivityGroup{
			{RunID: 9, Pipeline: "load_orders", Schema: "demo", Table: "orders", Op: "insert",
				Rows: 1204, MinID: 6800, MaxID: 7920, UndoOpen: 0, UndoPromoted: 1204},
			{RunID: 14, Pipeline: "load_orders", Schema: "demo", Table: "orders", Op: "insert",
				Rows: 1187, MinID: 7921, MaxID: 8110, UndoOpen: 1187, UndoPromoted: 0},
			{RunID: 2, Pipeline: "solo", Schema: "demo", Table: "audit", Op: "update",
				Rows: 12, MinID: 6500, MaxID: 6512, UndoOpen: 0, UndoPromoted: 12},
		}}),
		Shapes: map[string]api.TableShape{
			"demo.orders": {Schema: "demo", Table: "orders", PrimaryKey: []string{"id"}, Columns: []api.ColumnShape{
				{Name: "id", Type: "bigint", PgType: "bigint", PrimaryKey: true},
				{Name: "placed_at", Type: "timestamptz", PgType: "timestamp with time zone", Nullable: true},
				{Name: "customer", Type: "text", PgType: "text", Nullable: true},
				{Name: "total", Type: "numeric(12,2)", PgType: "numeric(12,2)", Nullable: true},
				{Name: "currency", Type: "varchar(3)", PgType: "character varying(3)", Nullable: true},
				{Name: "lane", Type: "text", PgType: "text", Nullable: true},
				{Name: "settled", Type: "bool", PgType: "boolean", Nullable: true},
			}},
		},
		Commits: map[string]psCommitMark{
			"solo":        {Stamp: "14:29:41", Seq: 1},
			"load_orders": {Stamp: "14:31:07", Seq: 3},
		},
		Pipelines: []api.PipelineListItem{
			{Name: "extract", Active: true, Lane: "ingest"},
			{Name: "hello_iris", Active: false, Lane: "ingest"},
			{Name: "load_orders", Active: true, Lane: "ingest"},
			{Name: "monthly", Active: false, Lane: "reporting"},
			{Name: "solo", Active: false},
		},
	}
}

// TestPsDerivations proves the pure table derivations: lane rollup with
// anonymous own-lanes, per-lane pipeline rows with idle members and latest
// states, and the runs filter.
func TestPsDerivations(t *testing.T) {
	t.Run("ps-derivations", func(t *testing.T) {
		s := psvFixture()

		t.Run("lanes roll up counts, loads, and anonymous own-lanes", func(t *testing.T) {
			lanes := deriveLanes(s)
			names := make([]string, len(lanes))
			for i, l := range lanes {
				names[i] = l.name
			}
			if want := []string{"ingest", "reporting", "solo"}; !reflect.DeepEqual(names, want) {
				t.Fatalf("lane names = %v, want %v", names, want)
			}
			ingest := lanes[0]
			if ingest.pipelines != 3 || ingest.queued != 1 || ingest.running != 1 {
				t.Errorf("ingest = %+v, want 3 pipelines, 1 queued, 1 running", ingest)
			}
			if ingest.load == nil || ingest.load.CPUPercent != 51 {
				t.Errorf("ingest load = %+v, want the running run's 51%%", ingest.load)
			}
			if rep := lanes[1]; rep.load != nil || rep.queued != 0 || rep.running != 0 {
				t.Errorf("idle reporting lane = %+v, want zero counts and nil load", rep)
			}
			if solo := lanes[2]; solo.pipelines != 1 {
				t.Errorf("laneless pipeline must stand as its own lane: %+v", solo)
			}
		})

		t.Run("a lane's pipelines include idle members with dash latest", func(t *testing.T) {
			rows := derivePipelines(s, "ingest")
			byName := map[string]psPipelineRow{}
			for _, r := range rows {
				byName[r.name] = r
			}
			if len(rows) != 3 {
				t.Fatalf("ingest pipelines = %+v, want extract, hello_iris, load_orders", rows)
			}
			if lo := byName["load_orders"]; lo.latest != "running" || lo.running != 1 || lo.load == nil {
				t.Errorf("load_orders = %+v, want latest running with live load", lo)
			}
			if ex := byName["extract"]; ex.latest != "queued" || ex.queued != 1 || ex.load != nil {
				t.Errorf("extract = %+v, want latest queued, one queued, nil load", ex)
			}
			if hi := byName["hello_iris"]; hi.latest != "-" {
				t.Errorf("idle hello_iris latest = %q, want dash", hi.latest)
			}
		})

		t.Run("run filter: queued+running default, whole history under all", func(t *testing.T) {
			def := deriveRuns(s, "load_orders", false)
			if len(def) != 1 || def[0].ID != "14" {
				t.Fatalf("default runs = %+v, want only running 14", def)
			}
			all := deriveRuns(s, "load_orders", true)
			ids := make([]string, len(all))
			for i, r := range all {
				ids[i] = r.ID
			}
			if want := []string{"14", "9", "6"}; !reflect.DeepEqual(ids, want) {
				t.Errorf("history runs = %v, want %v (newest first)", ids, want)
			}
		})
	})
}

// key is shorthand for a rune keypress.
func key(r rune) psKey { return psKey{kind: psKeyRune, r: r} }

// TestPsModelUpdate proves the dashboard state machine: the lanes tree, the
// pane focus cycle, table drilling, the cancel target, selection stability
// across re-polls, the cancel confirm, and quit paths.
func TestPsModelUpdate(t *testing.T) {
	t.Run("ps-model-update", func(t *testing.T) {
		t.Run("opens on the first lane's first pipeline, cursored on its run", func(t *testing.T) {
			m := newPsModel(psvFixture(), "local /tmp/iris.sock")
			if m.pane != psPaneLanes || m.selLane != "ingest" || m.selPipeline != "extract" {
				t.Fatalf("initial cursor: pane %d lane %q pipeline %q", m.pane, m.selLane, m.selPipeline)
			}
			if m.cancelTarget() != "12" {
				t.Fatalf("initial cancel target = %q, want extract's only run 12", m.cancelTarget())
			}
		})

		t.Run("tree walk visits pipelines only, skipping the lane headings", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.update(key('j')) // extract -> hello_iris
			if m.selPipeline != "hello_iris" || m.selTable != "" {
				t.Fatalf("after j: pipeline %q table %q", m.selPipeline, m.selTable)
			}
			m.update(key('j'))
			if m.selPipeline != "load_orders" {
				t.Fatalf("after jj: pipeline %q, want load_orders", m.selPipeline)
			}
			m.update(key('j')) // past ingest's last member: straight into the next lane
			if m.selLane != "reporting" || m.selPipeline != "monthly" {
				t.Fatalf("after jjj: lane %q pipeline %q, want reporting/monthly", m.selLane, m.selPipeline)
			}
			m.update(key('k')) // back over the lane boundary, no heading in between
			if m.selLane != "ingest" || m.selPipeline != "load_orders" {
				t.Fatalf("k back over the lane boundary: lane %q pipeline %q", m.selLane, m.selPipeline)
			}
		})

		t.Run("t cycles the lane's written tables and back off", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.update(key('t'))
			if m.selTable != "demo.orders" {
				t.Fatalf("t opened table %q, want demo.orders", m.selTable)
			}
			m.update(key('t')) // ingest owns one table: the next step closes it
			if m.selTable != "" {
				t.Fatalf("t past the last table left %q selected", m.selTable)
			}
			m.update(key('t'))
			m.update(psKey{kind: psKeyLeft}) // left closes an open TABLE view
			if m.selTable != "" {
				t.Fatalf("left with a table open left %q selected", m.selTable)
			}
		})

		t.Run("tab cycles panes", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			for _, want := range []psPane{psPaneStats, psPaneLanes} {
				m.update(psKey{kind: psKeyTab})
				if m.pane != want {
					t.Fatalf("pane = %d, want %d", m.pane, want)
				}
			}
		})

		t.Run("detail pane selects a run, left climbs back to the rail", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.update(key('j'))
			m.update(key('j')) // load_orders: the lane member with history
			m.update(psKey{kind: psKeyTab})
			if m.pane != psPaneStats || m.selPipeline != "load_orders" {
				t.Fatalf("tab: pane %d pipeline %q", m.pane, m.selPipeline)
			}
			m.update(key('a')) // whole history in
			if !m.showAll {
				t.Fatal("a did not widen the runs table")
			}
			m.update(key('j')) // 14 -> 9
			if m.tblRun != "9" || m.cancelTarget() != "9" {
				t.Fatalf("the run cursor is the selection: cursor %q target %q", m.tblRun, m.cancelTarget())
			}
			m.update(psKey{kind: psKeyEnter}) // a run row has nowhere to drill
			if m.pane != psPaneStats || m.tblRun != "9" {
				t.Fatalf("enter on a run row must be inert: pane %d cursor %q", m.pane, m.tblRun)
			}
			m.update(psKey{kind: psKeyLeft})
			if m.pane != psPaneLanes {
				t.Fatalf("left in the statistics pane must hand focus back, pane %d", m.pane)
			}
		})

		t.Run("a is inert outside the statistics pane", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.update(key('a')) // lanes pane
			if m.showAll {
				t.Fatal("a toggled history from the lanes pane")
			}
			m.update(psKey{kind: psKeyTab}) // statistics pane, scoped to a pipeline
			m.update(key('a'))
			if !m.showAll {
				t.Fatal("a did not widen the pipeline's runs table")
			}
			m.update(psKey{kind: psKeyTab}) // back to the rail
			m.update(key('a'))
			if !m.showAll {
				t.Fatal("a outside the detail pane must not touch the toggle")
			}
		})

		t.Run("selection survives a re-poll reorder and clamps when gone", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			for range 3 {
				m.update(key('j')) // past ingest's three members onto reporting/monthly
			}
			if m.selLane != "reporting" {
				t.Fatalf("selLane = %q", m.selLane)
			}
			m.absorb(psvFixture()) // same entities: selection sticks
			if m.selLane != "reporting" {
				t.Errorf("selection lost across re-poll: %q", m.selLane)
			}
			s2 := psvFixture() // reporting vanishes: clamp to first
			s2.Pipelines = s2.Pipelines[:3]
			m.absorb(s2)
			if m.selLane != "ingest" {
				t.Errorf("vanished selection clamps to first lane, got %q", m.selLane)
			}
		})

		t.Run("cancel confirm arms on a running target and disarms on anything but y", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.selectTree(psTreeRow{lane: "ingest", pipeline: "load_orders"}) // cursor lands on running 14
			m.pane = psPaneStats
			m.update(key('c'))
			if !m.confirmCancel {
				t.Fatal("c on a running target did not arm the confirm")
			}
			if got := m.update(key('n')); len(got) != 0 || m.confirmCancel {
				t.Fatalf("n must disarm without cancelling, got %q", got)
			}
			m.update(key('c'))
			if got := m.update(key('y')); len(got) != 1 || got[0] != "14" {
				t.Fatalf("y must confirm the cancel, got %q", got)
			}
			m.tblRun = "9" // terminal target: c never arms
			m.update(key('c'))
			if m.confirmCancel {
				t.Fatal("c armed a cancel on a terminal run")
			}
		})

		t.Run("rings grow one sample per absorb, absence marked", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.absorb(psvFixture())
			eng := m.rings[""]
			if len(eng.cpu) != 2 || eng.cpu[1] != 3.2 {
				t.Fatalf("engine ring = %+v, want two samples ending 3.2", eng.cpu)
			}
			if ing := m.rings["l:ingest"]; len(ing.cpu) != 2 || ing.cpu[1] != 51 {
				t.Fatalf("ingest ring = %+v, want the running run's 51", ing.cpu)
			}
			// The host answered, so a lane with nothing running burned nothing:
			// a real zero, not an absent slot.
			if rep := m.rings["l:reporting"]; rep.cpu[1] != 0 {
				t.Fatalf("idle lane ring on a probed host = %+v, want 0", rep.cpu)
			}
			if lo := m.rings["p:load_orders"]; lo.mem[1] != 24<<20 || lo.memPeak() != 24<<20 {
				t.Fatalf("pipeline ring mem = %+v", lo.mem)
			}
		})

		t.Run("an unprobed host marks idle lanes absent", func(t *testing.T) {
			blind := psvFixture()
			blind.Ps.Engine.Load = nil // the collector's probe found nothing
			m := newPsModel(blind, "")
			next := psvFixture()
			next.Ps.Engine.Load = nil
			next.Ps.SampleTick++
			m.absorb(next)
			if rep := m.rings["l:reporting"]; rep.cpu[len(rep.cpu)-1] != psNoSample {
				t.Fatalf("idle lane ring on an unprobed host = %+v, want psNoSample", rep.cpu)
			}
			if got := cpuText(m.scopeLoad(nil)); got != "-" {
				t.Errorf("unprobed scope reads %q, want a dash", got)
			}
		})

		t.Run("a probed idle scope reads zero, not a dash", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			if got := cpuText(m.scopeLoad(nil)); got != "0.0%" {
				t.Errorf("probed idle CPU = %q, want 0.0%%", got)
			}
			if got := memText(m.scopeLoad(nil)); got != "0B" {
				t.Errorf("probed idle MEM = %q, want 0B", got)
			}
			sample := &api.PsLoad{CPUPercent: 51, RSSBytes: 24 << 20}
			if got := cpuText(m.scopeLoad(sample)); got != "51.0%" {
				t.Errorf("a real sample reads %q, want it verbatim", got)
			}
		})

		t.Run("a sample tick gates the ring push", func(t *testing.T) {
			first := psvFixture()
			first.Ps.SampleTick = 3
			m := newPsModel(first, "")
			if eng := m.rings[""]; len(eng.cpu) != 1 {
				t.Fatalf("engine ring after open = %+v, want one sample", eng.cpu)
			}
			same := psvFixture() // the collector has not ticked: no push
			same.Ps.SampleTick = 3
			m.absorb(same)
			if eng := m.rings[""]; len(eng.cpu) != 1 {
				t.Fatalf("engine ring after a same-tick poll = %+v, want still one sample", eng.cpu)
			}
			next := psvFixture() // tick 5: the missed tick 4 fills absent
			next.Ps.SampleTick = 5
			m.absorb(next)
			eng := m.rings[""]
			if len(eng.cpu) != 3 || eng.cpu[1] != psNoSample || eng.cpu[2] != 3.2 {
				t.Fatalf("engine ring after a tick jump = %+v, want [3.2, no-sample, 3.2]", eng.cpu)
			}
		})

		t.Run("a collector restart resets the tick gate", func(t *testing.T) {
			first := psvFixture()
			first.Ps.SampleTick = 40
			m := newPsModel(first, "")
			back := psvFixture() // a restarted daemon answers with a small tick
			back.Ps.SampleTick = 2
			m.absorb(back)
			eng := m.rings[""]
			// The ring keeps what it saw before the restart, so the two sides are
			// separated by one absent slot rather than spliced into a run of
			// samples that never happened.
			if len(eng.cpu) != 3 || eng.cpu[1] != psNoSample || eng.cpu[2] != 3.2 {
				t.Fatalf("engine ring after a tick regression = %+v, want [3.2, no-sample, 3.2]", eng.cpu)
			}
			if m.lastTick != 2 {
				t.Errorf("lastTick = %d, want the restarted collector's 2", m.lastTick)
			}
		})

		// The daemon mints a lane's series the moment it first catches a run
		// there, so a just-woken lane's recorded series is a few slots old while
		// the client has watched it idle for minutes. The re-seed must deepen,
		// never replace, or the strip restarts on every wake.
		t.Run("a re-seed never shortens a ring a woken lane already grew", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			grown := make([]float64, 60)
			mem := make([]int64, 60)
			m.rings["l:ingest"] = &psRing{cpu: grown, mem: mem}
			m.rings["l:reporting"] = &psRing{cpu: grown, mem: mem}

			s := psvFixture()
			s.Ps.SampleTick = 99
			s.Ps.History = &api.PsHistory{
				FineIntervalSeconds: 2, CoarseIntervalSeconds: 60,
				Series: []api.PsSeries{
					// ingest just woke: three slots against the client's sixty.
					{Key: "lane:ingest", CPU: []float64{51, 51, 51}, RSS: []int64{1, 2, 3}},
				},
			}
			m.absorb(s)

			if got := m.rings["l:ingest"]; got == nil || len(got.cpu) != 60 {
				t.Errorf("woken lane ring = %d slots, want the 60 the client observed", len(got.cpuSamples()))
			}
			// reporting is absent from the payload entirely: the daemon having no
			// series is not evidence that nothing was observed.
			if got := m.rings["l:reporting"]; got == nil || len(got.cpu) != 60 {
				t.Errorf("unrecorded lane ring = %v, want the client's own observations kept", got.cpuSamples())
			}
			// A deeper recorded series still wins -- that is what backfill is for.
			deep := make([]float64, 120)
			s2 := psvFixture()
			s2.Ps.SampleTick = 100
			s2.Ps.History = &api.PsHistory{Series: []api.PsSeries{
				{Key: "lane:ingest", CPU: deep, RSS: make([]int64, 120)},
			}}
			m.absorb(s2)
			if got := m.rings["l:ingest"]; len(got.cpu) != 120 {
				t.Errorf("backfilled ring = %d slots, want the daemon's deeper 120", len(got.cpu))
			}
		})

		t.Run("a history payload re-seeds the rings", func(t *testing.T) {
			s := psvFixture()
			s.Ps.SampleTick = 40
			s.Ps.History = &api.PsHistory{
				FineIntervalSeconds: 2, CoarseIntervalSeconds: 60,
				Series: []api.PsSeries{
					{Key: "engine", CPU: []float64{1, 2, 3}, RSS: []int64{10, 20, 30},
						CoarseCPU: []float64{9}, CoarseRSS: []int64{90}},
					{Key: "lane:ingest", CPU: []float64{51}, RSS: []int64{24 << 20}},
					{Key: "pipeline:load_orders", CPU: []float64{51}, RSS: []int64{24 << 20}},
					{Key: "wat:unknown", CPU: []float64{7}, RSS: []int64{7}},
				},
			}
			m := newPsModel(s, "")
			if eng := m.rings[""]; len(eng.cpu) != 3 || eng.cpu[2] != 3 {
				t.Fatalf("engine ring = %+v, want the wire fine series", eng.cpu)
			}
			if c := m.coarse[""]; c == nil || len(c.cpu) != 1 || c.cpu[0] != 9 || c.mem[0] != 90 {
				t.Fatalf("engine coarse ring = %+v, want the wire coarse series", c)
			}
			if ing := m.rings["l:ingest"]; ing == nil || ing.cpu[0] != 51 {
				t.Fatalf("lane ring = %+v, want the wire series under the l: key", ing)
			}
			if lo := m.rings["p:load_orders"]; lo == nil || lo.mem[0] != 24<<20 {
				t.Fatalf("pipeline ring = %+v, want the wire series under the p: key", lo)
			}
			if len(m.rings) != 3 {
				t.Errorf("rings = %d entries, want 3 (the unknown wire key is skipped)", len(m.rings))
			}
			if m.lastTick != 40 {
				t.Errorf("lastTick = %d, want the payload's 40", m.lastTick)
			}
			// The next tick appends to the re-seeded ring, no re-seed needed.
			live := psvFixture()
			live.Ps.SampleTick = 41
			m.absorb(live)
			if eng := m.rings[""]; len(eng.cpu) != 4 || eng.cpu[3] != 3.2 {
				t.Fatalf("engine ring after the next tick = %+v, want the live sample appended", eng.cpu)
			}
		})

		t.Run("h toggles the history view anywhere", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.update(key('h'))
			if !m.histView {
				t.Fatal("h did not toggle the history view on")
			}
			m.update(psKey{kind: psKeyTab})
			m.update(key('h'))
			if m.histView {
				t.Fatal("h did not toggle the history view back off")
			}
		})

		t.Run("q and ctrl-c quit", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.update(key('q'))
			if !m.quit {
				t.Fatal("q did not quit")
			}
			m = newPsModel(psvFixture(), "")
			m.update(psKey{kind: psKeyCtrlC})
			if !m.quit {
				t.Fatal("ctrl-c did not quit")
			}
		})
	})
}

// TestPsSearch proves the fuzzy matcher and the overlay's state: scoring
// order, per-keystroke narrowing, kind-aware jumps, and Esc closing.
func TestPsSearch(t *testing.T) {
	t.Run("ps-search", func(t *testing.T) {
		t.Run("fuzzy scoring", func(t *testing.T) {
			cases := []struct {
				query, cand string
				match       bool
			}{
				{"ord", "load_orders", true},
				{"ORD", "load_orders", true},
				{"lo", "load_orders", true},
				{"xyz", "load_orders", false},
				{"", "anything", true},
				{"14", "14 · running", true},
			}
			for _, tc := range cases {
				if _, ok := fuzzyScore(tc.query, tc.cand); ok != tc.match {
					t.Errorf("fuzzyScore(%q, %q) match = %v, want %v", tc.query, tc.cand, ok, tc.match)
				}
			}
			word, _ := fuzzyScore("or", "load_orders") // word-start o
			mid, _ := fuzzyScore("or", "sensor")       // mid-word
			if word <= mid {
				t.Errorf("word-start match must outscore mid-word: %d <= %d", word, mid)
			}
		})

		t.Run("narrowing, jump, and esc", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.openSearch()
			if m.search == nil {
				t.Fatal(":search did not open the overlay")
			}
			// Empty query holds every entity: 3 lanes + 5 pipelines + 5 runs.
			if got := len(m.search.hits); got != 13 {
				t.Fatalf("empty-query hits = %d, want 13", got)
			}
			for _, r := range "ord" {
				m.update(key(r))
			}
			for _, h := range m.search.hits {
				if _, ok := fuzzyScore("ord", h.label); !ok {
					t.Errorf("hit %q does not match the query", h.label)
				}
			}
			if m.search.hits[0].label != "load_orders" {
				t.Fatalf("best hit = %+v, want the load_orders pipeline", m.search.hits[0])
			}
			m.update(psKey{kind: psKeyEnter})
			if m.search != nil || m.pane != psPaneStats || m.selPipeline != "load_orders" || m.selLane != "ingest" {
				t.Fatalf("enter on a pipeline hit must select it and focus the table: %+v", m)
			}
		})

		t.Run("run hit lands the detail pane's run cursor", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.openSearch()
			m.update(key('1'))
			m.update(key('4'))
			m.update(psKey{kind: psKeyEnter})
			if m.pane != psPaneStats || m.tblRun != "14" {
				t.Fatalf("run jump landed wrong: pane %d cursor %q", m.pane, m.tblRun)
			}
			if m.selLane != "ingest" || m.selPipeline != "load_orders" {
				t.Errorf("run jump selection: lane %q pipeline %q", m.selLane, m.selPipeline)
			}
		})

		t.Run("esc closes and only closes", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.openSearch()
			m.update(psKey{kind: psKeyEsc})
			if m.search != nil || m.quit {
				t.Fatal("esc must close the overlay and nothing else")
			}
			m.update(psKey{kind: psKeyEsc}) // outside the overlay: inert
			if m.quit || m.pane != psPaneLanes {
				t.Fatal("esc outside the overlay must be inert")
			}
		})

		t.Run("j and k are literal query characters while open", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.openSearch()
			m.update(key('j'))
			m.update(key('k'))
			if got := string(m.search.query); got != "jk" {
				t.Fatalf("query = %q, want jk", got)
			}
			if m.selLane != "ingest" || m.selPipeline != "extract" {
				t.Error("j/k moved the backdrop selection while the overlay was open")
			}
		})
	})
}

// TestPsBulkCancel proves the space-marked bulk cancel: marks toggle on
// pipeline rows in the rail and the table, c arms one y/N confirm over every
// marked pipeline's running runs, y returns them all, and marks are pruned
// with their pipelines.
func TestPsBulkCancel(t *testing.T) {
	t.Run("ps-bulk-cancel", func(t *testing.T) {
		t.Run("space marks the pipelines-table cursor", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.update(psKey{kind: psKeyTab}) // table pane, cursor on extract
			m.update(key(' '))
			if !m.markedPipes["extract"] {
				t.Fatalf("marked = %v, want extract", m.markedPipes)
			}
			m.update(key(' '))
			if len(m.markedPipes) != 0 {
				t.Fatalf("marked = %v, want the second space to unmark", m.markedPipes)
			}
		})

		t.Run("space marks the rail's pipeline row", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			if m.selPipeline == "" {
				t.Fatal("fixture cursor did not land on a pipeline row")
			}
			m.update(key(' '))
			if !m.markedPipes[m.selPipeline] {
				t.Fatalf("marked = %v, want %s", m.markedPipes, m.selPipeline)
			}
		})

		t.Run("c without running runs among the marks just notes", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.update(psKey{kind: psKeyTab})
			m.update(key(' ')) // extract: queued only
			m.update(key('c'))
			if m.confirmBulk || m.note == "" {
				t.Fatalf("confirmBulk=%v note=%q, want the no-running-runs note", m.confirmBulk, m.note)
			}
		})

		t.Run("c arms, y cancels every marked running run and clears marks", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.update(key(' '))               // extract, from the rail
			m.update(psKey{kind: psKeyDown}) // hello_iris
			m.update(psKey{kind: psKeyDown}) // load_orders (running run 14)
			m.update(key(' '))
			m.update(key('c'))
			if !m.confirmBulk {
				t.Fatal("c with marked running pipelines must arm the bulk confirm")
			}
			got := m.update(key('y'))
			if len(got) != 1 || got[0] != "14" {
				t.Fatalf("y returned %v, want [14]", got)
			}
			if m.markedPipes != nil || m.confirmBulk {
				t.Fatalf("marks = %v, want cleared after the confirmed cancel", m.markedPipes)
			}
		})

		t.Run("anything but y disarms and keeps the marks", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.update(psKey{kind: psKeyDown})
			m.update(psKey{kind: psKeyDown}) // load_orders
			m.update(key(' '))
			m.update(key('c'))
			if got := m.update(key('n')); len(got) != 0 || m.confirmBulk {
				t.Fatalf("n returned %v, want a disarm without cancels", got)
			}
			if !m.markedPipes["load_orders"] {
				t.Fatal("disarming must keep the marks")
			}
		})

		t.Run("absorb prunes marks whose pipelines vanished", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.update(psKey{kind: psKeyTab})
			m.update(key(' ')) // extract
			s := psvFixture()
			s.Pipelines = s.Pipelines[2:] // drop extract and hello_iris from the listing
			s.Ps.Runs = s.Ps.Runs[:1]     // keep only load_orders' running run
			m.absorb(s)
			if len(m.markedPipes) != 0 {
				t.Fatalf("marked = %v, want extract pruned with its pipeline", m.markedPipes)
			}
		})
	})
}
