package cli

import (
	"strings"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/golden"
)

// framePlain renders a model at a fixed geometry and joins the plain rune
// grid into the golden surface (styles asserted separately -- geometry never
// depends on them).
func framePlain(m *psModel, w, h int) string {
	b := renderPsFrame(m, w, h, false)
	return strings.Join(b.plainLines(), "\n") + "\n"
}

// TestPsFrameGoldens pins each screen's frame byte-for-byte at fixed
// geometries: the four levels, the colorless selection marker, the search
// overlay, and the too-small degradation.
func TestPsFrameGoldens(t *testing.T) {
	t.Run("ps-frame-goldens", func(t *testing.T) {
		target := "remote 10.0.0.5:7433"

		t.Run("lanes 80x24", func(t *testing.T) {
			m := newPsModel(psvFixture(), target)
			golden.Assert(t, []byte(framePlain(m, 80, 24)), "testdata/psv_lanes_80x24.txt")
		})

		t.Run("pipelines 80x24", func(t *testing.T) {
			m := newPsModel(psvFixture(), target)
			m.update(psKey{kind: psKeyEnter}) // into ingest
			golden.Assert(t, []byte(framePlain(m, 80, 24)), "testdata/psv_pipelines_80x24.txt")
		})

		t.Run("runs with history 100x30", func(t *testing.T) {
			m := newPsModel(psvFixture(), target)
			m.update(psKey{kind: psKeyEnter})
			m.update(key('j')) // hello_iris -> select load_orders instead
			m.update(key('j'))
			m.update(psKey{kind: psKeyEnter})
			m.update(key('a')) // whole history
			golden.Assert(t, []byte(framePlain(m, 100, 30)), "testdata/psv_runs_100x30.txt")
		})

		t.Run("run detail with log tail 80x24", func(t *testing.T) {
			m := newPsModel(psvFixture(), target)
			m.lane, m.pipeline = "ingest", "load_orders"
			m.openRun("14")
			m.snap.logs = []string{
				"fetching s3://orders/2026-07-15.csv",
				"1204 rows parsed",
				"upserting batch 3/12 into demo.orders",
				"upserting batch 4/12 into demo.orders",
			}
			golden.Assert(t, []byte(framePlain(m, 80, 24)), "testdata/psv_run_80x24.txt")
		})

		t.Run("search overlay 100x30", func(t *testing.T) {
			m := newPsModel(psvFixture(), target)
			m.update(key('/'))
			for _, r := range "ord" {
				m.update(key(r))
			}
			golden.Assert(t, []byte(framePlain(m, 100, 30)), "testdata/psv_search_100x30.txt")
		})

		t.Run("too small degrades to one line", func(t *testing.T) {
			m := newPsModel(psvFixture(), target)
			got := framePlain(m, 30, 5)
			if !strings.HasPrefix(got, "iris ps: terminal too small") {
				t.Fatalf("tiny frame = %q, want the advisory line", strings.SplitN(got, "\n", 2)[0])
			}
		})
	})
}

// TestPsFrameStyling proves the SGR layer: state colors land on their cells,
// the selected row inverts (or gains the marker when colorless), the emission
// carries zero escape bytes beyond cursor addressing when the painter is off,
// and no line ever exceeds the frame width.
func TestPsFrameStyling(t *testing.T) {
	t.Run("ps-frame-styling", func(t *testing.T) {
		t.Run("state cells carry their palette color", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.update(psKey{kind: psKeyEnter}) // ingest pipelines
			m.update(key('j'))                // off extract, so inversion hides no state cell
			b := renderPsFrame(m, 80, 24, false)
			out := string(b.render(painter{enabled: true}))
			for _, want := range []string{ansiCyan + "r", ansiYellow + "q", ansiGreen} {
				if !strings.Contains(out, want) {
					t.Errorf("frame carries no %q-styled cell", want)
				}
			}
			if !strings.Contains(out, ansiInverse) {
				t.Error("selected row is not inverted")
			}
		})

		t.Run("disabled painter emits no SGR", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			b := renderPsFrame(m, 80, 24, true)
			out := string(b.render(painter{}))
			for _, sgr := range []string{ansiInverse, ansiCyan, ansiGreen, ansiYellow, ansiRed, ansiDim, ansiReset} {
				if strings.Contains(out, sgr) {
					t.Errorf("colorless frame carries SGR %q", sgr)
				}
			}
			if !strings.Contains(strings.Join(b.plainLines(), "\n"), "> ") {
				t.Error("colorless frame carries no selection marker")
			}
		})

		t.Run("no line exceeds the frame width", func(t *testing.T) {
			m := newPsModel(psvFixture(), "remote very-long-hostname.example.internal:7433")
			m.snap.pipelines[0].Name = strings.Repeat("very_long_pipeline_name_", 5)
			m.absorb(m.snap)
			for _, screenSetup := range []func(){
				func() {},
				func() { m.update(psKey{kind: psKeyEnter}) },
			} {
				screenSetup()
				b := renderPsFrame(m, 60, 20, false)
				for i, line := range b.plainLines() {
					if n := len([]rune(line)); n > 60 {
						t.Errorf("line %d is %d runes wide, want <= 60: %q", i, n, line)
					}
				}
			}
		})

		t.Run("run detail drops the engine line, keeps the title role slot", func(t *testing.T) {
			m := newPsModel(psvFixture(), "")
			m.lane, m.pipeline = "ingest", "load_orders"
			m.openRun("14")
			lines := renderPsFrame(m, 80, 24, false).plainLines()
			if strings.Contains(lines[1], "ENGINE") {
				t.Error("run detail must drop the pinned engine line")
			}
			if !strings.Contains(lines[0], "leader · up 2h13m") {
				t.Errorf("title bar lost the role slot: %q", lines[0])
			}
			if !strings.Contains(lines[1], "running · CPU 51.0% · MEM 24.0MiB") {
				t.Errorf("fact line = %q", lines[1])
			}
		})
	})
}
