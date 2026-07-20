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

// progressTick advances the bar on a fixed cadence (platform-independent).
type progressTick time.Time

// progressModel is a short-lived Bubble Tea program: one labeled bar that
// fills 0→100% then quits. Used by uninstall (and setup) so install/uninstall
// ceremony looks the same on every platform — no raw ANSI \r loops.
type progressModel struct {
	prefix   string
	bar      progress.Model
	percent  float64
	width    int
	quitting bool
	step     float64
}

type progressDone struct{}

func newProgressModel(prefix string) progressModel {
	bar := progress.New(
		progress.WithDefaultGradient(),
		progress.WithWidth(24),
		progress.WithoutPercentage(),
	)
	return progressModel{
		prefix: prefix,
		bar:    bar,
		step:   0.08, // ~12 frames to full
	}
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
		m.width = msg.Width
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
		// ignore keys; bar is non-interactive
		return m, nil
	}
	return m, nil
}

func (m progressModel) View() string {
	if m.quitting && m.percent >= 1 {
		// Final line with plain percentage for logs/transcripts.
		return fmt.Sprintf("  %s %s %d%%\n", m.prefix, m.bar.ViewAs(1), 100)
	}
	pct := int(m.percent * 100)
	if pct > 100 {
		pct = 100
	}
	label := lipgloss.NewStyle().Render(m.prefix)
	return fmt.Sprintf("  %s %s %d%%", label, m.bar.View(), pct)
}

// runProgressBar runs a Bubble Tea progress bar to completion on out.
// No-ops when out is not a terminal (json/piped runs stay quiet), matching
// the previous uninstallProgressBar contract.
func runProgressBar(out io.Writer, prefix string) {
	if !writerIsTTY(out) {
		return
	}
	if _, noColor := os.LookupEnv("NO_COLOR"); noColor {
		// Still animate — progress works without color.
	}
	m := newProgressModel(prefix)
	p := tea.NewProgram(m, tea.WithOutput(out), tea.WithInput(nil))
	if _, err := p.Run(); err != nil {
		// Fallback one-liner so a failed TUI never blocks uninstall mid-step.
		fmt.Fprintf(out, "  %s done\n", prefix)
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

// ensure strings import used if View needs padding later
var _ = strings.TrimSpace
