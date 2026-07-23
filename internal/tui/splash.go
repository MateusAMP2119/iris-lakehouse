package tui

import (
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The open of `iris ps` plays a short splash: the star brand-mark materializes
// dot by dot inside a centered card, holds, then hands off to the dashboard.
// The tick chain lives and dies with the splash — after handoff the program
// returns to its zero-tick regime so terminal text selection survives (see the
// Init comment in tea.go).
const (
	splashFrameInterval = 40 * time.Millisecond // 25fps, reveal only
	splashRevealFrames  = 25                    // ~1s reveal
	splashHold          = 700 * time.Millisecond
	splashCardW         = 40
	splashCardH         = 19
	splashMinW          = 44
	splashMinH          = 20
)

// splashTickMsg advances the reveal; splashDoneMsg ends the hold.
type (
	splashTickMsg struct{}
	splashDoneMsg struct{}
)

// splashState is the splash phase marker; a nil pointer means splash over.
type splashState struct {
	frame int
}

// splashTickCmd schedules the next reveal frame.
func splashTickCmd() tea.Cmd {
	return tea.Tick(splashFrameInterval, func(time.Time) tea.Msg { return splashTickMsg{} })
}

// splashHoldCmd schedules the handoff after the fully-revealed hold.
func splashHoldCmd() tea.Cmd {
	return tea.Tick(splashHold, func(time.Time) tea.Msg { return splashDoneMsg{} })
}

// splashLogoFrame returns logoSplash with only the first k dots lit, k scaled
// by n/total in a fixed scatter order; n >= total is the full logo. The order
// is a Knuth multiplicative hash over dot ids — deterministic across builds,
// spatially scattered, and monotone by construction (frame n ⊆ frame n+1).
func splashLogoFrame(n, total int) []string {
	if total <= 0 || n >= total {
		return logoSplash
	}
	if n < 0 {
		n = 0
	}

	type dot struct {
		y, x, bit int
	}
	var dots []dot
	rows := make([][]rune, len(logoSplash))
	for y, row := range logoSplash {
		rows[y] = []rune(row)
		for x, r := range rows[y] {
			if r == ' ' {
				continue
			}
			mask := int(r - 0x2800)
			for b := 0; b < 8; b++ {
				if mask&(1<<b) != 0 {
					dots = append(dots, dot{y: y, x: x, bit: b})
				}
			}
		}
	}
	cols := logoWidth(logoSplash)
	// Dot ids are small non-negative (bounded by the art size), so the uint32
	// hash conversion cannot overflow meaningfully.
	id := func(d dot) uint32 { return uint32(d.y*cols+d.x)*8 + uint32(d.bit) } //nolint:gosec // bounded by the art size
	sort.Slice(dots, func(i, j int) bool {
		di, dj := id(dots[i]), id(dots[j])
		ki, kj := di*2654435761, dj*2654435761
		if ki != kj {
			return ki < kj
		}
		return di < dj
	})

	masks := make([][]rune, len(rows))
	for y := range rows {
		masks[y] = make([]rune, len(rows[y]))
	}
	for _, d := range dots[:len(dots)*n/total] {
		masks[d.y][d.x] |= 1 << d.bit
	}
	out := make([]string, len(masks))
	for y, rowMasks := range masks {
		var sb strings.Builder
		for _, m := range rowMasks {
			if m == 0 {
				sb.WriteRune(' ')
			} else {
				sb.WriteRune(0x2800 + m)
			}
		}
		out[y] = strings.TrimRight(sb.String(), " ")
	}
	return out
}

// splashSeg is one styled run of a centered splash-card line.
type splashSeg struct {
	sgr, s string
}

// renderSplashFrame draws the centered splash card; frame drives the reveal.
func renderSplashFrame(m *psModel, frame, w, h int, colorless bool) *screenBuf {
	b := newScreenBuf(w, h)
	_ = colorless // SGRs drop at emission under a disabled painter

	cx := (w - splashCardW) / 2
	cy := (h - splashCardH) / 2
	b.box(cx, cy, splashCardW, splashCardH, ansiBorder, "", "")

	centered := func(ry int, segs []splashSeg) {
		total := 0
		for _, seg := range segs {
			total += len([]rune(seg.s))
		}
		x := cx + (splashCardW-total)/2
		for _, seg := range segs {
			b.text(x, ry, seg.sgr, seg.s)
			x += len([]rune(seg.s))
		}
	}

	artW := logoWidth(logoSplash)
	for i, row := range splashLogoFrame(frame, splashRevealFrames) {
		b.text(cx+(splashCardW-artW)/2, cy+2+i, ansiMagenta, row)
	}

	e := m.snap.Ps.Engine
	centered(cy+15, []splashSeg{{ansiMagenta, "iris"}, {"", " engine"}})
	meta := []splashSeg{}
	if e.Version != "" {
		meta = append(meta, splashSeg{ansiDim, e.Version})
	}
	if e.Role != "" {
		if len(meta) > 0 {
			meta = append(meta, splashSeg{ansiDim, " · "})
		}
		meta = append(meta, splashSeg{psRoleSGR(e.Role), strings.ToUpper(e.Role)})
	}
	if e.Uptime != "" {
		if len(meta) > 0 {
			meta = append(meta, splashSeg{ansiDim, " · "})
		}
		meta = append(meta, splashSeg{"", "up " + e.Uptime})
	}
	centered(cy+16, meta)
	centered(cy+17, []splashSeg{{ansiDim, "any key to continue"}})
	return b
}
