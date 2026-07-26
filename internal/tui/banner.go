package tui

import (
	"fmt"
	"strings"
)

// The idle view's brand banner: the same pre-rendered oh-my-logo art the
// installer prints (install.sh banner_wide/banner_stacked), tinted row by row
// with the installer's purple gradient. Treated as immutable.

// bannerWide is the one-block IRIS LAKEHOUSE art (installer's ≥128-col form).
var bannerWide = []string{
	"██╗  ██████╗   ██╗  ███████╗       ██╗        █████╗   ██╗  ██╗  ███████╗  ██╗  ██╗   ██████╗   ██╗   ██╗  ███████╗  ███████╗",
	"██║  ██╔══██╗  ██║  ██╔════╝       ██║       ██╔══██╗  ██║ ██╔╝  ██╔════╝  ██║  ██║  ██╔═══██╗  ██║   ██║  ██╔════╝  ██╔════╝",
	"██║  ██████╔╝  ██║  ███████╗       ██║       ███████║  █████╔╝   █████╗    ███████║  ██║   ██║  ██║   ██║  ███████╗  █████╗",
	"██║  ██╔══██╗  ██║  ╚════██║       ██║       ██╔══██║  ██╔═██╗   ██╔══╝    ██╔══██║  ██║   ██║  ██║   ██║  ╚════██║  ██╔══╝",
	"██║  ██║  ██║  ██║  ███████║       ███████╗  ██║  ██║  ██║  ██╗  ███████╗  ██║  ██║  ╚██████╔╝  ╚██████╔╝  ███████║  ███████╗",
	"╚═╝  ╚═╝  ╚═╝  ╚═╝  ╚══════╝       ╚══════╝  ╚═╝  ╚═╝  ╚═╝  ╚═╝  ╚══════╝  ╚═╝  ╚═╝   ╚═════╝    ╚═════╝   ╚══════╝  ╚══════╝",
}

// bannerStacked is the two-block form (installer's ≥92-col fallback), with one
// blank row between IRIS and LAKEHOUSE.
var bannerStacked = []string{
	"██╗  ██████╗   ██╗  ███████╗",
	"██║  ██╔══██╗  ██║  ██╔════╝",
	"██║  ██████╔╝  ██║  ███████╗",
	"██║  ██╔══██╗  ██║  ╚════██║",
	"██║  ██║  ██║  ██║  ███████║",
	"╚═╝  ╚═╝  ╚═╝  ╚═╝  ╚══════╝",
	"",
	"██╗        █████╗   ██╗  ██╗  ███████╗  ██╗  ██╗   ██████╗   ██╗   ██╗  ███████╗  ███████╗",
	"██║       ██╔══██╗  ██║ ██╔╝  ██╔════╝  ██║  ██║  ██╔═══██╗  ██║   ██║  ██╔════╝  ██╔════╝",
	"██║       ███████║  █████╔╝   █████╗    ███████║  ██║   ██║  ██║   ██║  ███████╗  █████╗",
	"██║       ██╔══██║  ██╔═██╗   ██╔══╝    ██╔══██║  ██║   ██║  ██║   ██║  ╚════██║  ██╔══╝",
	"███████╗  ██║  ██║  ██║  ██╗  ███████╗  ██║  ██║  ╚██████╔╝  ╚██████╔╝  ███████║  ███████╗",
	"╚══════╝  ╚═╝  ╚═╝  ╚═╝  ╚═╝  ╚══════╝  ╚═╝  ╚═╝   ╚═════╝    ╚═════╝   ╚══════╝  ╚══════╝",
}

// bannerText is the installer's plain fallback when neither art form fits.
const bannerText = "IRIS LAKEHOUSE"

// bannerIrisHalf and bannerLakeHalf are the ps frame's height-compact brand
// words: three half-block rows each, 2x-scaled 3x5 pixel letters. The frame
// joins them justified edge to edge (issue #238, C1d).
var bannerIrisHalf = []string{
	"▀▀██▀▀     ██▀▀▄▄     ▀▀██▀▀     ██▀▀▀▀",
	"  ██       ██▀▀▄▄       ██       ▀▀▀▀██",
	"▀▀▀▀▀▀     ▀▀  ▀▀     ▀▀▀▀▀▀     ▀▀▀▀▀▀",
}

var bannerLakeHalf = []string{
	"██         ▄▄▀▀▄▄     ██▄▄▀▀     ██▀▀▀▀     ██  ██     ██▀▀██     ██  ██     ██▀▀▀▀     ██▀▀▀▀",
	"██         ██▀▀██     ██▄▄       ██▀▀       ██▀▀██     ██  ██     ██  ██     ▀▀▀▀██     ██▀▀  ",
	"▀▀▀▀▀▀     ▀▀  ▀▀     ▀▀  ▀▀     ▀▀▀▀▀▀     ▀▀  ▀▀     ▀▀▀▀▀▀     ▀▀▀▀▀▀     ▀▀▀▀▀▀     ▀▀▀▀▀▀",
}

// psBannerMinGap is the smallest word gap the justified banner accepts.
const psBannerMinGap = 3

// psBanner joins the two brand words justified across w cells, or nil when
// they cannot fit with a readable gap.
func psBanner(w int) []string {
	iw, lw := len([]rune(bannerIrisHalf[0])), len([]rune(bannerLakeHalf[0]))
	gap := w - iw - lw
	if gap < psBannerMinGap {
		return nil
	}
	out := make([]string, len(bannerIrisHalf))
	for i := range out {
		out[i] = bannerIrisHalf[i] + strings.Repeat(" ", gap) + bannerLakeHalf[i]
	}
	return out
}

// bannerGradient is the installer's G1..G6 purple ramp, one stop per art row;
// stacked blocks cycle it so both halves fade the same way.
var bannerGradient = [6][3]int{
	{102, 126, 234}, {105, 115, 219}, {108, 106, 205},
	{112, 95, 191}, {115, 86, 177}, {118, 75, 162},
}

// bannerRowSGR is the gradient foreground for banner row i.
func bannerRowSGR(i int) string {
	g := bannerGradient[i%len(bannerGradient)]
	return rgb(g[0], g[1], g[2])
}

// The install progress bar's gradient endpoints (bubbles' default gradient),
// reused for the idle view's version bar and CPU mini-bar.
var (
	barGradFrom = [3]int{90, 86, 224}   // #5A56E0
	barGradTo   = [3]int{238, 111, 248} // #EE6FF8
)

// barGradAt blends the progress gradient at position i of w.
func barGradAt(i, w int) (r, g, b int) {
	t := 0.0
	if w > 1 {
		t = float64(i) / float64(w-1)
	}
	lerp := func(a, b int) int { return a + int(t*float64(b-a)+0.5) }
	return lerp(barGradFrom[0], barGradTo[0]),
		lerp(barGradFrom[1], barGradTo[1]),
		lerp(barGradFrom[2], barGradTo[2])
}

// renderGradientBar paints row y as an install-style progress bar: the full
// width filled with the gradient as background, left and right labels in bold
// near-black text riding on top. Labels clip against each other, left first.
func (b *screenBuf) renderGradientBar(x, y, w int, left, right string) {
	if w < 4 {
		return
	}
	row := make([]rune, w)
	for i := range row {
		row[i] = ' '
	}
	put := func(at int, s string) {
		for i, r := range []rune(s) {
			if at+i >= 1 && at+i < w-1 {
				row[at+i] = r
			}
		}
	}
	put(2, left)
	rr := []rune(right)
	start := w - 2 - len(rr)
	if min := 2 + len([]rune(left)) + 2; start < min {
		if room := w - 2 - min; room > 1 {
			rr = append(rr[:room-1], '…')
			start = min
		} else {
			rr = nil
		}
	}
	put(start, string(rr))
	for i, r := range row {
		cr, cg, cb := barGradAt(i, w)
		b.text(x+i, y, fmt.Sprintf("\033[1;38;2;18;18;26;48;2;%d;%d;%dm", cr, cg, cb), string(r))
	}
}

// renderMiniBar paints a w-cell progress-style CPU bar at (x, y): the filled
// head in the install gradient, the empty tail in dim shade.
func (b *screenBuf) renderMiniBar(x, y, w int, pct float64) {
	if w <= 0 {
		return
	}
	filled := int(pct/100*float64(w) + 0.5)
	if filled < 1 {
		filled = 1
	}
	if filled > w {
		filled = w
	}
	for i := range filled {
		cr, cg, cb := barGradAt(i, w)
		b.text(x+i, y, rgb(cr, cg, cb), "█")
	}
	for i := filled; i < w; i++ {
		b.text(x+i, y, ansiDim, "░")
	}
}
