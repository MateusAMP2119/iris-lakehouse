package cli

import (
	"reflect"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
)

// psvFixture is a two-lane snapshot: ingest carries a running and a queued
// run plus an idle member, reporting is fully idle, and one laneless pipeline
// (solo) stands as its own anonymous lane. Runs are newest first, as the wire
// orders them.
func psvFixture() psSnapshot {
	exit0, exit3 := 0, 3
	return psSnapshot{
		ps: api.PsPayload{
			Engine: api.PsEngine{Version: "dev", Role: "leader", PID: 42, Uptime: "2h13m",
				QueuedRuns: 1, RunningRuns: 1, Load: &api.PsLoad{CPUPercent: 3.2, RSSBytes: 126 << 20}},
			Runs: []api.PsRun{
				{ID: "14", Pipeline: "load_orders", Lane: "ingest", State: "running",
					Load: &api.PsLoad{CPUPercent: 51, RSSBytes: 24 << 20}},
				{ID: "12", Pipeline: "extract", Lane: "ingest", State: "queued"},
				{ID: "9", Pipeline: "load_orders", Lane: "ingest", State: "succeeded", ExitCode: &exit0},
				{ID: "6", Pipeline: "load_orders", Lane: "ingest", State: "dead_lettered", ExitCode: &exit3},
				{ID: "2", Pipeline: "solo", State: "succeeded", ExitCode: &exit0},
			},
		},
		pipelines: []api.PipelineListItem{
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
// states, and the level-3 run filter.
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

// TestPsModelUpdate proves the navigation state machine: descent and ascent
// across the four screens, selection stability across re-polls, the level-3
// history toggle, the cancel confirm arm/disarm, and quit paths.
func TestPsModelUpdate(t *testing.T) {
	t.Run("ps-model-update", func(t *testing.T) {
		t.Run("descend to a run and back, breadcrumb tracking", func(t *testing.T) {
			m := newPsModel(psvFixture(), "local /tmp/iris.sock")
			if m.breadcrumb() != "iris ps" {
				t.Fatalf("home breadcrumb = %q", m.breadcrumb())
			}
			m.update(psKey{kind: psKeyEnter}) // ingest
			if m.screen != psScreenPipelines || m.lane != "ingest" {
				t.Fatalf("after enter: screen %d lane %q", m.screen, m.lane)
			}
			m.update(key('j')) // extract -> hello_iris
			m.update(psKey{kind: psKeyEnter})
			if m.screen != psScreenRuns || m.pipeline != "hello_iris" {
				t.Fatalf("after second enter: screen %d pipeline %q", m.screen, m.pipeline)
			}
			m.update(psKey{kind: psKeyLeft})
			m.update(key('k')) // back to extract
			m.update(psKey{kind: psKeyRight})
			if m.pipeline != "extract" {
				t.Fatalf("pipeline = %q, want extract", m.pipeline)
			}
			m.update(psKey{kind: psKeyEnter}) // run 12
			if m.screen != psScreenRun || m.runID != "12" || m.wantFocus != "12" || !m.follow {
				t.Fatalf("run screen: %+v", m)
			}
			if got := m.breadcrumb(); got != "iris ps · ingest · extract · run 12" {
				t.Errorf("breadcrumb = %q", got)
			}
			m.update(psKey{kind: psKeyLeft})
			if m.screen != psScreenRuns || m.wantFocus != "" {
				t.Errorf("ascend from run: screen %d focus %q, want runs screen and no focus", m.screen, m.wantFocus)
			}
		})

		t.Run("selection survives a re-poll reorder and clamps when gone", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.update(key('j')) // reporting
			if m.selLane != "reporting" {
				t.Fatalf("selLane = %q", m.selLane)
			}
			s := psvFixture() // same entities: selection sticks
			m.absorb(s)
			if m.selLane != "reporting" {
				t.Errorf("selection lost across re-poll: %q", m.selLane)
			}
			s2 := psvFixture() // reporting vanishes: clamp to first
			s2.pipelines = s2.pipelines[:3]
			m.absorb(s2)
			if m.selLane != "ingest" {
				t.Errorf("vanished selection clamps to first lane, got %q", m.selLane)
			}
		})

		t.Run("a toggles history on the runs screen only", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.update(key('a'))
			if m.showAll {
				t.Fatal("a toggled history off the runs screen")
			}
			m.update(psKey{kind: psKeyEnter})
			m.update(psKey{kind: psKeyEnter}) // extract runs
			m.update(key('a'))
			if !m.showAll {
				t.Fatal("a did not toggle history on the runs screen")
			}
		})

		t.Run("cancel confirm arms on a running run and disarms on anything but y", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.lane, m.pipeline = "ingest", "load_orders"
			m.openRun("14")
			m.update(key('c'))
			if !m.confirmCancel {
				t.Fatal("c on a running run did not arm the confirm")
			}
			if got := m.update(key('n')); got != "" || m.confirmCancel {
				t.Fatalf("n must disarm without cancelling, got %q", got)
			}
			m.update(key('c'))
			if got := m.update(key('y')); got != "14" {
				t.Fatalf("y must confirm the cancel, got %q", got)
			}
			m.openRun("9") // terminal run: c never arms
			m.update(key('c'))
			if m.confirmCancel {
				t.Fatal("c armed a cancel on a terminal run")
			}
		})

		t.Run("follow toggles and scroll clamps on the run screen", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.openRun("14")
			m.snap.logs = []string{"a", "b", "c", "d"}
			m.update(key('f'))
			if m.follow {
				t.Fatal("f did not stop following")
			}
			for range 10 {
				m.update(key('k'))
			}
			if m.scroll != 4 {
				t.Errorf("scroll = %d, want clamped at 4 held lines", m.scroll)
			}
			m.update(key('j'))
			if m.scroll != 3 {
				t.Errorf("scroll after one down = %d, want 3", m.scroll)
			}
			m.update(key('f'))
			if !m.follow || m.scroll != 0 {
				t.Errorf("f must resume following at the tail: follow %v scroll %d", m.follow, m.scroll)
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
			m.update(key('/'))
			if m.search == nil {
				t.Fatal("/ did not open search")
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
			if m.search != nil || m.screen != psScreenRuns || m.pipeline != "load_orders" || m.lane != "ingest" {
				t.Fatalf("enter on a pipeline hit must land on its runs: %+v", m)
			}
		})

		t.Run("run hit jumps to the detail screen with focus", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.update(key('/'))
			m.update(key('1'))
			m.update(key('4'))
			m.update(psKey{kind: psKeyEnter})
			if m.screen != psScreenRun || m.runID != "14" || m.wantFocus != "14" {
				t.Fatalf("run jump landed wrong: screen %d run %q focus %q", m.screen, m.runID, m.wantFocus)
			}
			if m.lane != "ingest" || m.pipeline != "load_orders" {
				t.Errorf("run jump breadcrumb: lane %q pipeline %q", m.lane, m.pipeline)
			}
		})

		t.Run("esc closes and only closes", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.update(key('/'))
			m.update(psKey{kind: psKeyEsc})
			if m.search != nil || m.quit {
				t.Fatal("esc must close the overlay and nothing else")
			}
			m.update(psKey{kind: psKeyEsc}) // outside the overlay: inert
			if m.quit || m.screen != psScreenLanes {
				t.Fatal("esc outside the overlay must be inert")
			}
		})

		t.Run("j and k are literal query characters while open", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.update(key('/'))
			m.update(key('j'))
			m.update(key('k'))
			if got := string(m.search.query); got != "jk" {
				t.Fatalf("query = %q, want jk", got)
			}
			if m.selLane != m.laneKeys()[0] {
				t.Error("j/k moved the backdrop selection while the overlay was open")
			}
		})
	})
}
