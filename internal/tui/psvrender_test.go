package tui

import (
	"strings"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/golden"
	"github.com/MateusAMP2119/iris-lakehouse/internal/quotes"
)

// framePlain renders a model at a fixed geometry and joins the plain rune
// grid into the golden surface (styles asserted separately -- geometry never
// depends on them).
func framePlain(m *psModel, w, h int) string {
	b := renderPsFrame(m, w, h, false)
	return strings.Join(b.plainLines(), "\n") + "\n"
}

// psvHistory is the recorded load history a live view always opens on -- the
// CLI seed fetches ?history=1 -- with a fine series per strip and a coarse one
// for the rail's day-deep lane summary. reporting is a lane idle all day: real
// zeros, not absence.
func psvHistory() *api.PsHistory {
	return &api.PsHistory{
		FineIntervalSeconds: 2, CoarseIntervalSeconds: 60,
		Series: []api.PsSeries{
			{Key: "engine",
				CPU: []float64{2.1, 4.4, 12.0, 3.9, 3.2}, RSS: []int64{120 << 20, 122 << 20, 124 << 20, 125 << 20, 126 << 20},
				CoarseCPU:  []float64{24.9, 8.1, 3.6, 51.2, 12.0, 6.4, 3.2},
				CoarseRSS:  []int64{110 << 20, 114 << 20, 118 << 20, 130 << 20, 124 << 20, 125 << 20, 126 << 20},
				CoarseRows: []int64{0, 620, 1204, 1187, psNoSample, 340, 36}},
			{Key: "lane:ingest",
				CPU: []float64{0, 48, 51, 51, 51}, RSS: []int64{0, 20 << 20, 22 << 20, 24 << 20, 24 << 20},
				CoarseCPU: []float64{0, 0, 62.5, 51, 51, 0, 51},
				CoarseRSS: []int64{0, 0, 30 << 20, 24 << 20, 24 << 20, 0, 24 << 20}},
			{Key: "lane:reporting",
				CPU: []float64{0, 0, 0, 0, 0}, RSS: []int64{0, 0, 0, 0, 0},
				CoarseCPU: []float64{0, 0, 0, 0, 0, 0, 0}, CoarseRSS: []int64{0, 0, 0, 0, 0, 0, 0}},
			{Key: "pipeline:load_orders",
				CPU: []float64{0, 48, 51, 51, 51}, RSS: []int64{0, 20 << 20, 22 << 20, 24 << 20, 24 << 20},
				CoarseCPU:  []float64{0, 0, 62.5, 51, 51, 0, 51},
				CoarseRSS:  []int64{0, 0, 30 << 20, 24 << 20, 24 << 20, 0, 24 << 20},
				CoarseRows: []int64{0, 0, 1204, 1187, 0, psNoSample, 36}},
		},
	}
}

// psvSeeded is the golden fixture's model opened on a history-carrying payload,
// so both ring tiers are populated the way a real view's are.
func psvSeeded(target string) *psModel {
	s := psvFixture()
	s.Ps.History = psvHistory()
	m := newPsModel(s, target)
	// The pack cache as a completed background fetch: the detail pane's
	// RETENTION row reads it, so the goldens pin it with data.
	m.packs = []api.CatalogPack{
		{Name: "orders-etl", Installed: true, Pipelines: []string{"load_orders", "extract"}},
		{Name: "quake-monitor", Installed: false, Pipelines: []string{"monthly"}},
	}
	return m
}

// TestPsFrameGoldens pins the dashboard byte-for-byte at each width tier:
// full width, narrowed, rail shed, plus the runs table, the table shape, the
// search overlay, and the too-small degradation.
func TestPsFrameGoldens(t *testing.T) {
	t.Run("ps-frame-goldens", func(t *testing.T) {
		target := "remote 10.0.0.5:7433"

		t.Run("full width 150x40", func(t *testing.T) {
			m := psvSeeded(target)
			golden.Assert(t, []byte(framePlain(m, 150, 40)), "testdata/psv_dashboard_150x40.txt")
		})

		t.Run("narrowed 100x30", func(t *testing.T) {
			m := psvSeeded(target)
			golden.Assert(t, []byte(framePlain(m, 100, 30)), "testdata/psv_dashboard_100x30.txt")
		})

		t.Run("narrow 80x24", func(t *testing.T) {
			m := psvSeeded(target)
			golden.Assert(t, []byte(framePlain(m, 80, 24)), "testdata/psv_dashboard_80x24.txt")
		})

		t.Run("no rail 60x20", func(t *testing.T) {
			m := psvSeeded(target)
			golden.Assert(t, []byte(framePlain(m, 60, 20)), "testdata/psv_dashboard_60x20.txt")
		})

		t.Run("runs table with history 150x40", func(t *testing.T) {
			m := psvSeeded(target)
			m.update(key('j'))              // extract -> hello_iris
			m.update(key('j'))              // -> load_orders, the lane member with history
			m.update(psKey{kind: psKeyTab}) // focus its runs table
			golden.Assert(t, []byte(framePlain(m, 150, 40)), "testdata/psv_runs_150x40.txt")
		})

		t.Run("table view 150x40", func(t *testing.T) {
			m := psvSeeded(target)
			m.selectTable("ingest", "demo.orders")
			golden.Assert(t, []byte(framePlain(m, 150, 40)), "testdata/psv_table_150x40.txt")
		})

		t.Run("parked pipeline 150x40", func(t *testing.T) {
			// The other half of the DISPATCH block: a lane parked on the
			// watermark, its member's gate closed on an up-to-date upstream.
			m := psvSeeded(target)
			m.selectLane("reporting")
			m.selPipeline = "monthly"
			golden.Assert(t, []byte(framePlain(m, 150, 40)), "testdata/psv_parked_150x40.txt")
		})

		t.Run("lane shape 150x40", func(t *testing.T) {
			m := psvSeeded(target)
			m.selectLane("ingest")
			m.selPipeline = "" // a lane row: the pane charts the lane, not a member
			golden.Assert(t, []byte(framePlain(m, 150, 40)), "testdata/psv_lane_150x40.txt")
		})

		t.Run("catalog filter typed 150x40", func(t *testing.T) {
			m := psvSeeded(target)
			m.update(key('/'))
			for _, r := range "ord" {
				m.update(key(r))
			}
			golden.Assert(t, []byte(framePlain(m, 150, 40)), "testdata/psv_filter_150x40.txt")
		})

		t.Run("search overlay 100x30", func(t *testing.T) {
			m := psvSeeded(target)
			m.update(key('/'))
			for _, r := range "ord" {
				m.update(key(r))
			}
			golden.Assert(t, []byte(framePlain(m, 100, 30)), "testdata/psv_search_100x30.txt")
		})

		t.Run("commands palette 100x30", func(t *testing.T) {
			m := psvSeeded(target)
			m.update(key(':'))
			golden.Assert(t, []byte(framePlain(m, 100, 30)), "testdata/psv_commands_100x30.txt")
		})

		t.Run("empty workspace 100x30", func(t *testing.T) {
			// Default-ish geometry: mid-size mark + status + compact actions.
			m := newPsModel(Snapshot{Ps: api.PsPayload{
				Engine: api.PsEngine{Version: "dev", Role: "leader", PID: 7, Uptime: "12s"},
			}}, "unix:///home/tiger/.iris/engine.sock")
			m.quote = quotes.Farewell[0] // pin the random pick for the golden
			golden.Assert(t, []byte(framePlain(m, 100, 30)), "testdata/psv_empty_100x30.txt")
		})

		t.Run("empty workspace tiny terminal falls back", func(t *testing.T) {
			m := newPsModel(Snapshot{Ps: api.PsPayload{
				Engine: api.PsEngine{Version: "dev", Role: "leader", PID: 7, Uptime: "12s"},
			}}, "unix:///home/tiger/.iris/engine.sock")
			// Too short for the spacious card; must still render the guide.
			got := framePlain(m, 60, 12)
			if !strings.Contains(got, "GET STARTED") {
				t.Fatalf("tiny empty frame missing guidance:\n%s", got)
			}
		})

		t.Run("short terminal one-line header 100x14", func(t *testing.T) {
			m := psvSeeded(target)
			golden.Assert(t, []byte(framePlain(m, 100, 14)), "testdata/psv_short_100x14.txt")
		})

		t.Run("too small degrades to one line", func(t *testing.T) {
			m := psvSeeded(target)
			got := framePlain(m, 30, 5)
			if !strings.HasPrefix(got, "iris ps: terminal too small") {
				t.Fatalf("tiny frame = %q, want the advisory line", strings.SplitN(got, "\n", 2)[0])
			}
		})
	})
}

// TestPsFrameStyling proves the SGR layer: state colors land on their cells,
// the focused pane's border is the brand violet, the selection tints its whole row
// (or "> " when colorless), heat cells quantize into the ramp, and the
// emission carries zero escape bytes beyond cursor addressing when the painter
// is off.
func TestPsFrameStyling(t *testing.T) {
	t.Run("ps-frame-styling", func(t *testing.T) {
		t.Run("state cells and focus border carry their palette color", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.update(key('j')) // off the lane row...
			m.update(key('j')) // ...and off extract, so selection accent hides no state dot
			b := renderPsFrame(m, 150, 40, false)
			out := string(b.render(painter{enabled: true}))
			// The focused pane wears the brand violet; unfocused chrome recedes.
			for _, want := range []string{ansiCyan + "●", ansiYellow + "●", ansiGreen + "LEADER", ansiMagenta + "│"} {
				if !strings.Contains(out, want) {
					t.Errorf("frame carries no %q-styled cell", want)
				}
			}
			if !strings.Contains(out, ansiSelBg) {
				t.Error("selected row carries no background tint")
			}
		})

		t.Run("the cursor's lane washes as a block, the cursor row brighter", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			b := renderPsFrame(m, 150, 40, false)
			// The rail row naming s, read at a cell inside the wash.
			sgrOf := func(s string) string {
				t.Helper()
				for y, line := range b.plainLines() {
					if rail := []rune(line); len(rail) > 34 && strings.Contains(string(rail[:34]), s) {
						return b.cells[y*b.w+5].sgr
					}
				}
				t.Fatalf("no rail row names %q", s)
				return ""
			}
			for _, tc := range []struct{ row, want string }{
				{"extract", ansiSelBg},     // the cursor
				{"ingest", ansiLaneBg},     // its lane's heading
				{"hello_iris", ansiLaneBg}, // a sibling in the same lane
				{"reporting", ""},          // another lane: no wash
				{"monthly", ""},
			} {
				got := sgrOf(tc.row)
				if tc.want == "" {
					if strings.Contains(got, ansiSelBg) || strings.Contains(got, ansiLaneBg) {
						t.Errorf("row %q sgr = %q, want no wash", tc.row, got)
					}
					continue
				}
				if !strings.HasPrefix(got, tc.want) {
					t.Errorf("row %q sgr = %q, want the %q wash", tc.row, got, tc.want)
				}
				// One background per row -- never both prefixes stacked.
				if strings.Contains(got, ansiSelBg) && strings.Contains(got, ansiLaneBg) {
					t.Errorf("row %q carries both washes: %q", tc.row, got)
				}
			}
		})

		t.Run("heat strip quantizes the ramp", func(t *testing.T) {
			b := newScreenBuf(10, 1)
			b.renderHeatStrip(0, 0, 10, []float64{psNoSample, 3, 30, 60, 90})
			line := b.plainLines()[0]
			if !strings.HasSuffix(line, "░▒▓█") {
				t.Fatalf("heat strip = %q, want tone per quartile, absence blank", line)
			}
			if got := b.cells[9].sgr; got != ansiRed {
				t.Errorf("hot cell sgr = %q, want red", got)
			}
			if got := b.cells[6].sgr; got != ansiGreen {
				t.Errorf("cool cell sgr = %q, want green", got)
			}
		})

		t.Run("fitSamples compresses by per-cell maximum", func(t *testing.T) {
			short := []float64{1, 2, 3}
			if got := fitSamples(short, 5); len(got) != 3 {
				t.Fatalf("fitSamples must pass a narrow history through, got %v", got)
			}
			wide := []float64{psNoSample, psNoSample, 10, 90, 5, 5, psNoSample, 20}
			got := fitSamples(wide, 4)
			if len(got) != 4 {
				t.Fatalf("fitSamples width = %d, want 4", len(got))
			}
			// Cells of two: [absent absent] [10 90] [5 5] [absent 20] -- an
			// all-absent share stays absent, a spike survives its share.
			if got[0] != psNoSample || got[1] != 90 || got[2] != 5 || got[3] != 20 {
				t.Errorf("fitSamples = %v, want [no-sample, 90, 5, 20]", got)
			}
		})

		t.Run("the history toggle swaps the strips to the coarse rings", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.coarse[""] = &psRing{cpu: []float64{90, 90, 90}, mem: []int64{1, 1, 1}}
			live := m.stripCPU("", 10)
			m.histView = true
			hist := m.stripCPU("", 10)
			if len(hist) != 3 || hist[0] != 90 {
				t.Fatalf("history strip = %v, want the coarse ring", hist)
			}
			if len(live) == len(hist) && live[0] == hist[0] {
				t.Error("live and history strips read the same ring")
			}
		})

		// The strip's span is min(recorded, ring depth): while the history is
		// narrower than the strip it grows a cell at a time from the right, and
		// once it is wider the whole bar spans the ring. That is the whole
		// growing-window rule, so assert it on the shaping helpers directly.
		t.Run("a strip spans what has been recorded, up to the ring's depth", func(t *testing.T) {
			if got := ringCPU(nil, 10); got != nil {
				t.Errorf("ringCPU(nil) = %v, want nil", got)
			}
			if got := ringMem(nil, 10); got != nil {
				t.Errorf("ringMem(nil) = %v, want nil", got)
			}
			// Narrower than the strip: passed through whole, so renderHeatStrip
			// right-aligns it and the left stays unpainted.
			young := &psRing{cpu: []float64{3, 40, 80}, mem: []int64{1 << 20, 2 << 20, 4 << 20}}
			if got := ringCPU(young, 12); len(got) != 3 || got[2] != 80 {
				t.Errorf("young ring = %v, want its 3 samples untouched", got)
			}
			b := newScreenBuf(12, 1)
			b.renderHeatStrip(0, 0, 12, ringCPU(young, 12))
			if line := b.plainLines()[0]; line != "         ░▒█" {
				t.Errorf("young strip = %q, want the bar grown in from the right", line)
			}
			// Wider than the strip: compressed to exactly the width.
			old := &psRing{}
			for i := range 100 {
				old.cpu = append(old.cpu, float64(i))
				old.mem = append(old.mem, int64(i))
			}
			if got := ringCPU(old, 12); len(got) != 12 || got[11] != 99 {
				t.Errorf("full ring = %v, want 12 cells ending at the newest max", got)
			}
		})

		t.Run("the rail's lane summary fills its strip from the deepest ring that can", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			// A young day: 3 coarse buckets cannot fill a 10-cell strip, so the
			// fine ring carries it -- a minute-old engine shows a full minute.
			m.coarse["l:ingest"] = &psRing{cpu: []float64{90, 90, 90}, mem: []int64{1, 1, 1}}
			m.rings["l:ingest"] = &psRing{cpu: []float64{5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5}, mem: make([]int64, 12)}
			if got := m.dayCPU("l:ingest", 10); len(got) != 10 || got[0] != 5 {
				t.Errorf("young day strip = %v, want the fine ring filling the strip", got)
			}
			// Once the coarse ring can fill the strip it takes over, widening
			// the window from minutes toward the day.
			deep := &psRing{}
			for range 40 {
				deep.cpu = append(deep.cpu, 90)
				deep.mem = append(deep.mem, 1)
			}
			m.coarse["l:ingest"] = deep
			day := m.dayCPU("l:ingest", 10)
			if len(day) != 10 || day[0] != 90 {
				t.Fatalf("grown day strip = %v, want the coarse ring", day)
			}
			m.histView = true
			if toggled := m.dayCPU("l:ingest", 10); len(toggled) != len(day) || toggled[0] != day[0] {
				t.Errorf("the toggle reached the lane summary: %v then %v", day, toggled)
			}
			// A lane the collector never recorded has no ring on either tier.
			if got := m.dayCPU("l:nowhere", 10); got != nil {
				t.Errorf("unrecorded lane day strip = %v, want nil", got)
			}
		})

		t.Run("disabled painter emits no SGR", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			b := renderPsFrame(m, 150, 40, true)
			out := string(b.render(painter{}))
			for _, sgr := range []string{ansiInverse, ansiCyan, ansiGreen, ansiYellow, ansiRed, ansiOrange, ansiDim, ansiReset, ansiMagenta, ansiBorder, ansiAccent} {
				if strings.Contains(out, sgr) {
					t.Errorf("colorless frame carries SGR %q", sgr)
				}
			}
			if !strings.Contains(strings.Join(b.plainLines(), "\n"), ">") {
				t.Error("colorless frame carries no selection marker")
			}
		})

		t.Run("no line exceeds the frame width", func(t *testing.T) {
			m := newPsModel(psvFixture(), "remote very-long-hostname.example.internal:7433")
			m.snap.Pipelines[0].Name = strings.Repeat("very_long_pipeline_name_", 5)
			m.absorb(m.snap)
			for _, geo := range []struct{ w, h int }{{150, 40}, {100, 30}, {80, 24}, {60, 20}, {45, 10}} {
				b := renderPsFrame(m, geo.w, geo.h, false)
				for i, line := range b.plainLines() {
					if n := len([]rune(line)); n > geo.w {
						t.Errorf("%dx%d line %d is %d runes wide: %q", geo.w, geo.h, i, n, line)
					}
				}
			}
		})

		t.Run("framed statusline is the frame's first chrome and names the engine", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			lines := renderPsFrame(m, 150, 40, false).plainLines()
			// The statusline owns row 0 whole: its horizontal edges are SGR
			// rules, so no box-glyph row precedes or follows it.
			row := lines[0]
			if strings.ContainsAny(row, "┌┐└┘─") {
				t.Errorf("statusline edges must be SGR rules, not glyph rows, got %q", row)
			}
			for _, want := range []string{psWordmark(), "dev", "LEADER", "pid 42", "up 2h13m", "1r", "1q"} {
				if !strings.Contains(row, want) {
					t.Errorf("statusline %q missing %q", row, want)
				}
			}
			if strings.Contains(row, "█") {
				t.Error("statusline must not carry banner art")
			}
			// Side pipes stay glyphs, hugging the content by one space.
			if !strings.HasPrefix(row, strings.Repeat(" ", psFrameMarginX)+"│ ") || !strings.HasSuffix(row, " │") {
				t.Errorf("statusline must sit between pipes with one space of padding, got %q", row)
			}
			// A blank row parts the statusline from the panes; the rail carries
			// the same chrome below it, and the right column's boxes open there.
			if got := strings.TrimSpace(lines[psHeaderH]); got != "" {
				t.Errorf("row %d must part statusline from panes, got %q", psHeaderH, got)
			}
			top := psHeaderH + psHeaderGap
			if want := strings.Repeat(" ", psFrameMarginX) + "│"; !strings.HasPrefix(lines[top], want) {
				t.Errorf("panes must start at row %d, got %q", top, lines[top])
			}
			// Every surface is a hairline card: the pane row carries its two
			// marks and no box glyph anywhere -- the edges are SGR rules.
			if !strings.Contains(lines[top], "[CATALOG]") || !strings.Contains(lines[top], "[PIPELINE]") {
				t.Errorf("row %d carries no pane marks, got %q", top, lines[top])
			}
			if strings.ContainsAny(lines[top], "┌┐└┘─") {
				t.Errorf("row %d carries box glyphs, got %q", top, lines[top])
			}
		})

		t.Run("dead runs invert the statusline count chip", func(t *testing.T) {
			s := psvFixture()
			s.Ps.Runs[0].State = "dead_lettered"
			m := newPsModel(s, "")
			top := renderPsFrame(m, 150, 40, false).plainLines()[0]
			if !strings.Contains(top, "DEAD") {
				t.Errorf("statusline %q missing the dead chip", top)
			}
		})

		t.Run("narrow frame trades the letterspaced mark for the load readout", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			top := renderPsFrame(m, 100, 30, false).plainLines()[0]
			if strings.Contains(top, psWordmark()) {
				t.Errorf("100-col statusline should drop the mark: %q", top)
			}
			for _, want := range []string{"IRIS", "CPU", "MEM"} {
				if !strings.Contains(top, want) {
					t.Errorf("narrow statusline %q missing %q", top, want)
				}
			}
		})

		t.Run("catalog rail is one hairline card: filter, tree, lane summary", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			rail := []string{}
			for _, ln := range renderPsFrame(m, 150, 40, false).plainLines() {
				if len([]rune(ln)) > 36 {
					rail = append(rail, string([]rune(ln)[:36]))
				}
			}
			joined := strings.Join(rail, "\n")
			for _, want := range []string{"[CATALOG]", "/ type to filter", "ingest", "3 pipelines",
				"● extract", "LANE · ingest", "last 14:31:07", "demo.orders", "+1187", "CPU", "MEM"} {
				if !strings.Contains(joined, want) {
					t.Errorf("rail missing %q:\n%s", want, joined)
				}
			}
			// The statusline's chrome: side pipes only, every horizontal edge an
			// SGR rule, and one card — no inner border row splits filter from tree.
			if n := strings.Count(joined, "[CATALOG]"); n != 1 {
				t.Errorf("rail draws %d CATALOG cards, want 1", n)
			}
			if strings.ContainsAny(joined, "┌┐└┘─") {
				t.Errorf("rail edges must be SGR rules, not glyphs:\n%s", joined)
			}
		})

		t.Run("rail edges are SGR rules on the rows they bound", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			b := renderPsFrame(m, 150, 40, false)
			// Rail column 1, from the row under the statusline to the frame's last.
			cell := func(y int) psCell { return b.cells[y*b.w+psFrameMarginX] }
			top := psHeaderH + psHeaderGap
			if got := cell(top).sgr; !strings.HasPrefix(got, ansiHRule) {
				t.Errorf("title row sgr = %q, want the overline/underline pair", got)
			}
			for _, y := range []int{top + 1, b.h - 1} { // filter row, bottom edge
				if got := cell(y).sgr; !strings.HasPrefix(got, ansiURule) {
					t.Errorf("row %d sgr = %q, want an underline", y, got)
				}
			}
			// The gradient mark, not the pane title's flat paint.
			if got := b.cells[top*b.w+psFrameMarginX+2].sgr; !strings.Contains(got, ansiBold) {
				t.Errorf("title sgr = %q, want the wordmark's bold ramp", got)
			}
		})

		t.Run("detail pane is one hairline card, marked and subjected", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.selectTable("ingest", "demo.orders")
			// Row indices are kept aligned with the frame's, so a short row
			// never slides the title out from under its own index.
			var pane []string
			for _, ln := range renderPsFrame(m, 150, 40, false).plainLines() {
				r := []rune(ln)
				if len(r) <= 40 {
					pane = append(pane, "")
					continue
				}
				pane = append(pane, string(r[40:]))
			}
			joined := strings.Join(pane, "\n")
			if strings.ContainsAny(joined, "┌┐└┘─") {
				t.Errorf("detail edges must be SGR rules, not glyphs:\n%s", joined)
			}
			// The mark names the kind of surface; the subject names the instance.
			if n := strings.Count(joined, "[TABLE]"); n != 1 {
				t.Errorf("detail draws %d TABLE cards, want 1", n)
			}
			title := pane[psHeaderH+psHeaderGap]
			if !strings.Contains(title, "demo.orders") {
				t.Errorf("title row carries no subject: %q", title)
			}
			// No internal vertical divider: the columns are held apart by air.
			if row := pane[psHeaderH+psHeaderGap+2]; strings.Count(row, "│") != 2 {
				t.Errorf("a content row must carry the two pipes only: %q", row)
			}
		})

		t.Run("no frame row carries a box glyph", func(t *testing.T) {
			for _, geo := range []struct{ w, h int }{{150, 40}, {100, 30}, {60, 20}} {
				m := newPsModel(psvFixture(), "")
				frame := strings.Join(renderPsFrame(m, geo.w, geo.h, false).plainLines(), "\n")
				if strings.ContainsAny(frame, "┌┐└┘─") {
					t.Errorf("%dx%d frame carries box glyphs:\n%s", geo.w, geo.h, frame)
				}
			}
		})

		t.Run("detail pane edges are SGR rules on the rows they bound", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			b := renderPsFrame(m, 150, 40, false)
			top := psHeaderH + psHeaderGap
			paneX := psFrameMarginX + 37 + psPaneGap // rail width at 150 cols
			cell := func(x, y int) psCell { return b.cells[y*b.w+x] }
			if got := cell(paneX, top).sgr; !strings.HasPrefix(got, ansiHRule) {
				t.Errorf("title row sgr = %q, want the overline/underline pair", got)
			}
			if got := cell(paneX, b.h-1).sgr; !strings.HasPrefix(got, ansiURule) {
				t.Errorf("bottom row sgr = %q, want an underline", got)
			}
			if got := cell(paneX+2, top).sgr; !strings.Contains(got, ansiBold) {
				t.Errorf("mark sgr = %q, want the wordmark's bold ramp", got)
			}
			// The bottom rule spans the pane in one colour. hairBox fills every
			// blank with chrome so the rule keeps its hue across the gaps; a
			// blit reaching this row would replace those cells wholesale and
			// leave the rule with nothing underneath -- exactly ansiURule and
			// no chrome, which is the seam this pins shut.
			for x := paneX; x < b.w-psFrameMarginX; x++ {
				c := cell(x, b.h-1)
				if !strings.HasPrefix(c.sgr, ansiURule) {
					t.Fatalf("bottom rule breaks at column %d: sgr %q", x, c.sgr)
				}
				if c.sgr == ansiURule {
					t.Fatalf("bottom rule has no chrome under it at column %d -- a blit reached the gutter", x)
				}
			}
		})

		t.Run("section heads wear the underline, bounded by their column", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			b := renderPsFrame(m, 150, 40, false)
			paneX := psFrameMarginX + 37 + psPaneGap
			p := splitPane(paneX+2, 150-2*psFrameMarginX-37-psPaneGap-4)
			if !p.split {
				t.Fatal("150 columns must split the detail pane")
			}
			headY := psHeaderH + psHeaderGap + 1 // OUTPUT, the first spec head
			for x := p.lx; x < p.lx+p.lw; x++ {
				if got := b.cells[headY*b.w+x].sgr; !strings.HasPrefix(got, ansiURule) {
					t.Fatalf("section head not ruled at column %d: sgr %q", x, got)
				}
			}
			// The rule is the spec column's, not the pane's: the gutter cell
			// just left of the wide column must be clear of it.
			if got := b.cells[headY*b.w+p.rx-1].sgr; strings.HasPrefix(got, ansiURule) {
				t.Errorf("section rule leaked into the gutter: sgr %q", got)
			}
		})

		t.Run("lane summary reports only what the journal and digest recorded", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			if got := m.laneTables()["ingest"]; len(got) != 1 || got[0] != "demo.orders" {
				t.Errorf("ingest tables = %v, want [demo.orders]", got)
			}
			if got := m.laneLastCommit("ingest"); got != "14:31:07" {
				t.Errorf("ingest last commit = %q, want the digest's newest commit stamp", got)
			}
			if got := m.laneLastCommit("reporting"); got != "" {
				t.Errorf("a lane with no observed commit = %q, want empty", got)
			}
			// A lane heading is never a cursor stop, in any snapshot.
			for _, r := range m.navRows() {
				if r.pipeline == "" {
					t.Errorf("nav row %+v is a lane heading", r)
				}
			}
		})

		t.Run("short terminal carries the same framed statusline", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			top := renderPsFrame(m, 100, 14, false).plainLines()[0]
			for _, want := range []string{"IRIS", "dev", "LEADER", "pid 42"} {
				if !strings.Contains(top, want) {
					t.Errorf("short header %q missing %q", top, want)
				}
			}
		})

		t.Run("empty workspace shows banner, version bar, and status/actions boxes", func(t *testing.T) {
			m := newPsModel(Snapshot{Ps: api.PsPayload{
				Engine: api.PsEngine{Version: "dev", Role: "leader", PID: 7, Uptime: "12s"},
			}}, "")
			m.quote = quotes.Farewell[0] // pin the random pick
			lines := renderPsFrame(m, 100, 30, false).plainLines()
			frame := strings.Join(lines, "\n")
			for _, want := range []string{
				"IRIS LAKEHOUSE", // 100x30 leaves art no room beside the catalog
				"state", "idle", "queue", "empty", "mem", "12s",
				quotes.Farewell[0].Text, quotes.Farewell[0].Author, "keyboard reference", "quit",
				"catalog", "type to filter", "loading catalog…", "⏎ apply picked",
			} {
				if !strings.Contains(frame, want) {
					t.Errorf("empty frame missing %q:\n%s", want, frame)
				}
			}
			for _, bad := range []string{"running", "queued"} {
				if strings.Contains(frame, bad) {
					t.Errorf("empty frame must not show hollow count %q:\n%s", bad, frame)
				}
			}
			// Version bar: one row carries version left and role/uptime/pid right.
			barOK := false
			for _, ln := range lines {
				if strings.Contains(ln, "dev") && strings.Contains(ln, "pid 7") {
					barOK = true
				}
			}
			if !barOK {
				t.Fatalf("no version bar row with identity in:\n%s", frame)
			}
		})
	})
}
