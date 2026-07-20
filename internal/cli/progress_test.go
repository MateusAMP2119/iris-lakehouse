package cli

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestPadProgressLabelAligns(t *testing.T) {
	labels := []string{
		"• Setting up engine",
		"• Removing engine state",
		"• Removing binary and traces",
	}
	widths := map[int]bool{}
	for _, l := range labels {
		p := padProgressLabel(l)
		w := lipgloss.Width(p)
		if w != progressLabelCols {
			t.Errorf("padProgressLabel(%q) width = %d, want %d (%q)", l, w, progressLabelCols, p)
		}
		widths[w] = true
		// bar should start at the same column: prefix has no trailing junk
		if strings.TrimRight(p, " ") != l && !strings.HasPrefix(p, l) {
			t.Errorf("padding lost label content: got %q", p)
		}
	}
	if len(widths) != 1 {
		t.Errorf("labels produced multiple widths %v; bars will not align", widths)
	}
}

func TestFormatProgressPctWidth(t *testing.T) {
	for _, pct := range []int{0, 5, 10, 99, 100} {
		s := formatProgressPct(pct)
		if lipgloss.Width(s) != progressPctCols {
			t.Errorf("formatProgressPct(%d) = %q width %d, want %d", pct, s, lipgloss.Width(s), progressPctCols)
		}
	}
}
