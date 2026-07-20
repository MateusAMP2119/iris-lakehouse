package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Shared geometry so every install/uninstall progress line lines up:
//
//	  {label padded to labelCols} {bar width barCols} {pct right-aligned 4}
//
// Labels are measured with lipgloss.Width so emoji (🧹) still pad correctly.
const (
	progressLabelCols = 34
	progressBarCols   = 24
	progressPctCols   = 4 // "  0%" .. "100%"
)

// progressTick advances the bar on a fixed cadence (platform-independent).
type progressTick time.Time

// progressModel is a short-lived Bubble Tea program: one labeled bar that
// fills 0→100% then quits. Used by uninstall and setup so ceremony looks the
// same on every platform — no raw ANSI \r loops.
type progressModel struct {
	prefix   string
	bar      progress.Model
	percent  float64
	quitting bool
	step     float64
}

type progressDone struct{}

func newProgressModel(prefix string) progressModel {
	bar := progress.New(
		progress.WithDefaultGradient(),
		progress.WithWidth(progressBarCols),
		progress.WithoutPercentage(),
	)
	return progressModel{
		prefix: padProgressLabel(prefix),
		bar:    bar,
		step:   0.08, // ~12 frames to full
	}
}

// padProgressLabel left-aligns label into a fixed display-width column so the
// bar always starts on the same horizontal cell across all call sites.
func padProgressLabel(label string) string {
	label = strings.TrimSpace(label)
	w := lipgloss.Width(label)
	if w >= progressLabelCols {
		// Truncate by runes roughly; keep simple — callers use short labels.
		return label
	}
	return label + strings.Repeat(" ", progressLabelCols-w)
}

func formatProgressPct(pct int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return fmt.Sprintf("%*d%%", progressPctCols-1, pct)
}

func (m progressModel) Init() tea.Cmd {
	return tea.Batch(m.bar.Init(), tickProgress())
}

func tickProgress() tea.Cmd {
	return tea.Tick(40*time.Millisecond, func(t time.Time) tea.Msg {
		return progressTick(t)
	})
}

func (m progressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m, nil
	case progressTick:
		m.percent += m.step
		if m.percent >= 1 {
			m.percent = 1
			m.quitting = true
			cmd := m.bar.SetPercent(1)
			return m, tea.Batch(cmd, func() tea.Msg { return progressDone{} })
		}
		cmd := m.bar.SetPercent(m.percent)
		return m, tea.Batch(cmd, tickProgress())
	case progress.FrameMsg:
		var cmd tea.Cmd
		var prog tea.Model
		prog, cmd = m.bar.Update(msg)
		m.bar = prog.(progress.Model)
		return m, cmd
	case progressDone:
		return m, tea.Quit
	case tea.KeyMsg:
		return m, nil
	}
	return m, nil
}

func (m progressModel) View() string {
	pct := int(m.percent * 100)
	if pct > 100 {
		pct = 100
	}
	barView := m.bar.View()
	if m.quitting && m.percent >= 1 {
		barView = m.bar.ViewAs(1)
		return fmt.Sprintf("  %s %s %s\n", m.prefix, barView, formatProgressPct(100))
	}
	return fmt.Sprintf("  %s %s %s", m.prefix, barView, formatProgressPct(pct))
}

// runProgressBar runs a Bubble Tea progress bar to completion on out.
// No-ops when out is not a terminal (json/piped runs stay quiet).
func runProgressBar(out io.Writer, prefix string) {
	if !writerIsTTY(out) {
		return
	}
	m := newProgressModel(prefix)
	p := tea.NewProgram(m, tea.WithOutput(out), tea.WithInput(nil))
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(out, "  %s %s\n", padProgressLabel(prefix), "done")
	}
}

func writerIsTTY(out io.Writer) bool {
	f, ok := out.(*os.File)
	if !ok {
		return false
	}
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice != 0
}
