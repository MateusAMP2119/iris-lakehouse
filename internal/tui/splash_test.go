package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MateusAMP2119/iris-lakehouse/internal/golden"
)

// splashPlain renders a splash frame's plain rune grid (the golden surface).
func splashPlain(m *psModel, frame, w, h int) string {
	b := renderSplashFrame(m, frame, w, h, false)
	return strings.Join(b.plainLines(), "\n") + "\n"
}

// litBits collects the set of lit dot ids across an art's rows.
func litBits(art []string) map[[3]int]bool {
	lit := map[[3]int]bool{}
	for y, row := range art {
		for x, r := range []rune(row) {
			if r == ' ' {
				continue
			}
			mask := int(r - 0x2800)
			for b := 0; b < 8; b++ {
				if mask&(1<<b) != 0 {
					lit[[3]int{y, x, b}] = true
				}
			}
		}
	}
	return lit
}

// TestBrailleAsset sanity-checks the checked-in art literals.
func TestBrailleAsset(t *testing.T) {
	for _, tc := range []struct {
		name       string
		art        []string
		rows, cols int
	}{
		{"logoSplash", logoSplash, 12, 16},
		{"logoMark", logoMark, 2, 8},
		{"logoStar", logoStar, 5, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.art) != tc.rows {
				t.Fatalf("%s has %d rows, want %d", tc.name, len(tc.art), tc.rows)
			}
			if w := logoWidth(tc.art); w > tc.cols {
				t.Fatalf("%s is %d cols wide, want <= %d", tc.name, w, tc.cols)
			}
			for y, row := range tc.art {
				for x, r := range []rune(row) {
					if r == ' ' {
						continue
					}
					if r <= 0x2800 || r > 0x28FF {
						t.Errorf("%s[%d][%d] = %q, want braille in (U+2800, U+28FF]", tc.name, y, x, r)
					}
				}
			}
		})
	}
}

// TestSplashLogoFrame proves the reveal is deterministic, monotone, and lands
// exactly on the full art.
func TestSplashLogoFrame(t *testing.T) {
	total := splashRevealFrames

	t.Run("frame 0 is dark", func(t *testing.T) {
		for _, row := range splashLogoFrame(0, total) {
			if strings.TrimSpace(row) != "" {
				t.Fatalf("frame 0 lit a cell: %q", row)
			}
		}
	})

	t.Run("final frame is the full logo", func(t *testing.T) {
		got := splashLogoFrame(total, total)
		for y, row := range got {
			if row != logoSplash[y] {
				t.Fatalf("final frame row %d = %q, want %q", y, row, logoSplash[y])
			}
		}
	})

	t.Run("reveal is monotone", func(t *testing.T) {
		prev := litBits(splashLogoFrame(0, total))
		for n := 1; n <= total; n++ {
			cur := litBits(splashLogoFrame(n, total))
			for d := range prev {
				if !cur[d] {
					t.Fatalf("frame %d lost dot %v lit at frame %d", n, d, n-1)
				}
			}
			if len(cur) < len(prev) {
				t.Fatalf("frame %d has %d dots, frame %d had %d", n, len(cur), n-1, len(prev))
			}
			prev = cur
		}
	})

	t.Run("reveal is deterministic", func(t *testing.T) {
		for n := 0; n <= total; n += 5 {
			a := strings.Join(splashLogoFrame(n, total), "\n")
			b := strings.Join(splashLogoFrame(n, total), "\n")
			if a != b {
				t.Fatalf("frame %d differs between calls", n)
			}
		}
	})
}

// TestSplashFrameGoldens pins the splash card geometry: the settled card at
// two sizes, and two mid-reveal frames (stable because the scatter order is
// hash-fixed).
func TestSplashFrameGoldens(t *testing.T) {
	m := newPsModel(psvFixture(), "remote 10.0.0.5:7433")
	t.Run("final 100x30", func(t *testing.T) {
		golden.Assert(t, []byte(splashPlain(m, splashRevealFrames, 100, 30)), "testdata/psv_splash_100x30.txt")
	})
	t.Run("final 80x24", func(t *testing.T) {
		golden.Assert(t, []byte(splashPlain(m, splashRevealFrames, 80, 24)), "testdata/psv_splash_80x24.txt")
	})
	t.Run("reveal frame 8 100x30", func(t *testing.T) {
		golden.Assert(t, []byte(splashPlain(m, 8, 100, 30)), "testdata/psv_splash_reveal_f08_100x30.txt")
	})
	t.Run("reveal frame 17 100x30", func(t *testing.T) {
		golden.Assert(t, []byte(splashPlain(m, 17, 100, 30)), "testdata/psv_splash_reveal_f17_100x30.txt")
	})
}

// TestSplashLifecycle drives teaProgram.Update through the splash phase: the
// tick chain runs the reveal then one hold timer, any key skips (and never
// reaches the dashboard grammar), quit keys quit, a too-small resize cancels,
// and after handoff no message schedules another tick.
func TestSplashLifecycle(t *testing.T) {
	newProg := func() teaProgram {
		return *newTeaProgram(newPsModel(psvFixture(), "local"), false)
	}
	step := func(t teaProgram, msg tea.Msg) (teaProgram, tea.Cmd) {
		mod, cmd := t.Update(msg)
		return mod.(teaProgram), cmd
	}

	t.Run("init starts the tick chain", func(t *testing.T) {
		p := newProg()
		if p.splash == nil {
			t.Fatal("new program has no splash phase")
		}
		if p.Init() == nil {
			t.Fatal("Init returned no command")
		}
	})

	t.Run("reveal ticks chain then hand to the hold timer", func(t *testing.T) {
		p := newProg()
		var cmd tea.Cmd
		for i := 0; i < splashRevealFrames; i++ {
			p, cmd = step(p, splashTickMsg{})
			if cmd == nil {
				t.Fatalf("tick %d returned no follow-up command", i)
			}
		}
		if p.splash == nil {
			t.Fatal("splash ended before the hold")
		}
		p, cmd = step(p, splashDoneMsg{})
		if p.splash != nil {
			t.Fatal("splashDoneMsg left the splash up")
		}
		if cmd != nil {
			t.Fatal("splashDoneMsg scheduled another command; the tick chain must die here")
		}
	})

	t.Run("any key skips and is consumed", func(t *testing.T) {
		p := newProg()
		before := p.m.pane
		p, cmd := step(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		if p.splash != nil {
			t.Fatal("key did not skip the splash")
		}
		if cmd != nil {
			t.Fatal("skip scheduled a command")
		}
		if p.m.pane != before {
			t.Fatal("skip key leaked into the dashboard grammar")
		}
		// The in-flight tick lands on the guard and schedules nothing.
		if p, cmd = step(p, splashTickMsg{}); cmd != nil {
			t.Fatal("stray splash tick scheduled a command after skip")
		}
		_ = p
	})

	t.Run("q quits from the splash", func(t *testing.T) {
		p := newProg()
		p, _ = step(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
		if !p.quitting {
			t.Fatal("q during splash did not quit")
		}
	})

	t.Run("too-small resize cancels the splash", func(t *testing.T) {
		p := newProg()
		p, _ = step(p, tea.WindowSizeMsg{Width: 40, Height: 10})
		if p.splash != nil {
			t.Fatal("tiny resize kept the splash")
		}
	})

	t.Run("roomy resize keeps the splash", func(t *testing.T) {
		p := newProg()
		p, _ = step(p, tea.WindowSizeMsg{Width: 120, Height: 40})
		if p.splash == nil {
			t.Fatal("roomy resize dropped the splash")
		}
	})
}
