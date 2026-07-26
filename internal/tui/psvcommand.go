package tui

import (
	"fmt"
	"strings"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
)

// This file is the `iris ps` command palette (#218, reworked): a dedicated
// COMMANDS overlay — list + detail + prompt — beside the :search telescope.
// The closed roster stays small; each entry carries usage, description, and
// category so the right pane is a living cheat-sheet, not a vim empire.

// psCmdCategory groups palette rows for the section headers.
type psCmdCategory string

const (
	psCmdNav    psCmdCategory = "navigate"
	psCmdWatch  psCmdCategory = "watch"
	psCmdAction psCmdCategory = "action"
	psCmdMeta   psCmdCategory = "meta"
)

// psCmdSpec is one registered palette command: the name the user types, its
// usage line, a one-sentence description, and optional key chords that do the
// same thing outside the palette.
type psCmdSpec struct {
	name     string
	usage    string
	summary  string
	detail   string
	category psCmdCategory
	keys     string // display-only chords, e.g. "/  :"
}

// psCommandRoster is the closed, stable-order command set the palette lists.
// Order is the tab-cycle order and the default list order.
var psCommandRoster = []psCmdSpec{
	{
		name: "catalog", usage: ":catalog", summary: "Browse and install pipeline packs",
		detail:   "Opens the catalog overlay over the dashboard: pack list on the left, README and tree on the right. Install and apply without leaving iris ps.",
		category: psCmdNav, keys: ":catalog",
	},
	{
		name: "search", usage: ":search [query]", summary: "Fuzzy-find lanes, pipelines, runs",
		detail:   "Opens the telescope search overlay. An optional query is applied immediately. Outside the palette, / filters the focused pane instead.",
		category: psCmdNav, keys: "/",
	},
	{
		name: "all", usage: ":all", summary: "Toggle full run history in the table",
		detail:   "When a pipeline's runs table is open, flip between live (queued + running) and the whole history. Same as the a key in that pane.",
		category: psCmdWatch, keys: "a",
	},
	{
		name: "history", usage: ":history", summary: "Toggle day-deep load strips",
		detail:   "Swaps every heat strip between the live fine ring and the coarse per-bucket history the daemon keeps. Same as the h key.",
		category: psCmdWatch, keys: "h",
	},
	{
		name: "cancel", usage: ":cancel", summary: "Cancel the run under the cursor",
		detail:   "Arms a y/N confirm for the run the detail pane's table cursor sits on, when that run is still running. Same as the c key in that pane.",
		category: psCmdAction, keys: "c",
	},
	{
		name: "help", usage: ":help", summary: "Keyboard reference",
		detail:   "Highlights this entry and parks the key map in the detail pane. Scroll the list for every command's chords.",
		category: psCmdMeta, keys: "?",
	},
	{
		name: "q", usage: ":q", summary: "Quit iris ps",
		detail:   "Leaves the live view and restores the terminal. Same as q or Ctrl-C.",
		category: psCmdMeta, keys: "q  Ctrl-C",
	},
}

// psCommands is the closed name roster tab cycles through (stable order).
var psCommands []string

func init() {
	psCommands = make([]string, len(psCommandRoster))
	for i, c := range psCommandRoster {
		psCommands[i] = c.name
	}
}

// psCommand is the open palette's state: typed input, filtered list selection,
// an inline error, and tab-completion cursor.
type psCommand struct {
	input   []rune
	err     string
	sel     int    // index into filtered()
	cycling bool   // a tab cycle is live; any edit ends it
	base    string // the input captured when the cycle started
	comp    int    // next completion index
	browse  bool   // opened via '?' — start focused on help, empty input ok
}

// openCommand opens the ':' palette with an empty prompt.
func (m *psModel) openCommand() {
	m.command = &psCommand{}
	m.command.syncSel()
}

// openCommandHelp opens the palette focused on the help entry (the ? key).
func (m *psModel) openCommandHelp() {
	m.command = &psCommand{browse: true}
	// Land the selection on "help" so the detail pane is the key map.
	for i, c := range m.command.filtered() {
		if c.name == "help" {
			m.command.sel = i
			return
		}
	}
	m.command.syncSel()
}

// updateCommand routes a keypress while the palette is open: typing filters,
// arrows move the list, tab cycles completions, Enter dispatches, Esc closes.
func (m *psModel) updateCommand(k psKey) {
	c := m.command
	switch k.kind {
	case psKeyCtrlC:
		m.quit = true
	case psKeyEsc:
		m.command = nil
	case psKeyRune:
		// In browse mode, j/k move the list like the search overlay's arrows
		// would — only when the prompt is still empty so typed filters win.
		if c.browse && len(c.input) == 0 && (k.r == 'j' || k.r == 'k') {
			if k.r == 'j' {
				c.moveSel(1)
			} else {
				c.moveSel(-1)
			}
			return
		}
		c.input = append(c.input, k.r)
		c.err, c.cycling, c.browse = "", false, false
		c.syncSel()
	case psKeyBackspace:
		if len(c.input) == 0 {
			m.command = nil
			return
		}
		c.input = c.input[:len(c.input)-1]
		c.err, c.cycling, c.browse = "", false, false
		c.syncSel()
	case psKeyUp:
		c.moveSel(-1)
	case psKeyDown:
		c.moveSel(1)
	case psKeyTab:
		m.completeCommand()
	case psKeyEnter:
		m.runCommand(strings.TrimSpace(string(c.input)))
	}
}

// moveSel shifts the filtered-list cursor, wrapping at the ends.
func (c *psCommand) moveSel(delta int) {
	list := c.filtered()
	if len(list) == 0 {
		c.sel = 0
		return
	}
	c.sel += delta
	if c.sel < 0 {
		c.sel = len(list) - 1
	}
	if c.sel >= len(list) {
		c.sel = 0
	}
	c.err = ""
}

// syncSel clamps the selection after a filter change and prefers a name that
// still prefixes the typed head when possible.
func (c *psCommand) syncSel() {
	list := c.filtered()
	if len(list) == 0 {
		c.sel = 0
		return
	}
	head := strings.TrimSpace(string(c.input))
	if name, _, ok := strings.Cut(head, " "); ok {
		head = name
	}
	if head != "" {
		for i, spec := range list {
			if strings.HasPrefix(spec.name, head) {
				c.sel = i
				return
			}
		}
	}
	if c.sel >= len(list) {
		c.sel = len(list) - 1
	}
}

// filtered returns the roster rows matching the typed command name prefix.
// No command takes an argument, so the whole input is the name prefix.
func (c *psCommand) filtered() []psCmdSpec {
	prefix := strings.TrimSpace(string(c.input))
	var out []psCmdSpec
	for _, spec := range psCommandRoster {
		if prefix == "" || strings.HasPrefix(spec.name, prefix) {
			out = append(out, spec)
		}
	}
	return out
}

// selected returns the currently highlighted roster entry, if any.
func (c *psCommand) selected() (psCmdSpec, bool) {
	list := c.filtered()
	if len(list) == 0 || c.sel < 0 || c.sel >= len(list) {
		return psCmdSpec{}, false
	}
	return list[c.sel], true
}

// runCommand dispatches one typed command; an unknown one answers inline and
// never tears the view down. An empty line on a selected no-arg command runs
// the selection (browse mode / arrow-then-enter).
func (m *psModel) runCommand(line string) {
	c := m.command
	if line == "" {
		if c.browse {
			// Empty enter in help browse just keeps the palette open on help.
			return
		}
		spec, ok := c.selected()
		if !ok {
			m.command = nil
			return
		}
		line = spec.name
	}
	name, arg, _ := strings.Cut(line, " ")
	arg = strings.TrimSpace(arg)
	switch name {
	case "q":
		m.quit = true
	case "catalog":
		m.command = nil
		m.openCatalog()
	case "search":
		m.command = nil
		m.openSearch()
		if arg != "" {
			m.search.query = []rune(arg)
			m.search.rematch(m.snap)
		}
	case "all":
		if m.selPipeline == "" {
			m.commandErr("open a pipeline's runs first (⏎ on a pipeline)")
			return
		}
		m.showAll = !m.showAll
		m.tblRun = clampKey(m.tblRun, m.runKeys())
		m.pane = psPaneStats
		m.command = nil
		if m.showAll {
			m.note = "showing full run history"
		} else {
			m.note = "showing live runs only"
		}
	case "history":
		m.histView = !m.histView
		m.command = nil
		if m.histView {
			m.note = "load strips: day-deep history · the lane summary always shows it"
		} else {
			m.note = "load strips: live · the lane summary always shows the day"
		}
	case "cancel":
		run, ok := findRun(m.snap, m.cancelTarget())
		if !ok || run.State != "running" {
			m.commandErr("no running run under the current selection")
			return
		}
		m.confirmCancel = true
		m.command = nil
	case "help":
		// Stay open; land on the help row so the detail pane is the key map.
		c.input = nil
		c.err, c.cycling, c.browse = "", false, true
		for i, spec := range c.filtered() {
			if spec.name == "help" {
				c.sel = i
				return
			}
		}
		c.syncSel()
	default:
		// If the typed head is a prefix of exactly one command, accept it.
		if matches := c.filtered(); len(matches) == 1 && !strings.Contains(line, " ") {
			m.runCommand(matches[0].name)
			return
		}
		m.commandErr("unknown command :" + name)
	}
}

// commandErr parks an inline error on the open prompt row.
func (m *psModel) commandErr(msg string) {
	m.command.err = msg
	m.command.cycling = false
}

// completeCommand cycles tab completion over the base input: command names.
func (m *psModel) completeCommand() {
	c := m.command
	if !c.cycling {
		c.base, c.comp, c.cycling = string(c.input), 0, true
	}
	cands := commandCompletions(c.base)
	if len(cands) == 0 {
		c.cycling = false
		return
	}
	c.input = []rune(cands[c.comp%len(cands)])
	c.comp++
	c.err = ""
	c.syncSel()
}

// commandCompletions lists the completions for a prompt prefix, in stable order.
func commandCompletions(base string) []string {
	if strings.Contains(base, " ") {
		return nil
	}
	var out []string
	for _, cmd := range psCommands {
		if strings.HasPrefix(cmd, base) {
			out = append(out, cmd)
		}
	}
	return out
}

// commandDetailBody is the right-pane text for the selected command (or the
// global key map when help is selected / browse mode).
func commandDetailBody(spec psCmdSpec, width int) []string {
	if width < 8 {
		width = 8
	}
	var lines []string
	if spec.name == "help" {
		lines = append(lines, wrapWords("Keyboard reference for iris ps. Select a command on the left for its detail, or type : to filter.", width)...)
		lines = append(lines, "")
		lines = append(lines, "GLOBAL")
		lines = append(lines, "  tab        cycle panes")
		lines = append(lines, "  ↑↓ j/k     move")
		lines = append(lines, "  ⏎ →        drill")
		lines = append(lines, "  ←          ascend")
		lines = append(lines, "  /          filter focused pane")
		lines = append(lines, "  :          commands")
		lines = append(lines, "  ?          this help")
		lines = append(lines, "  p          freeze (select & copy)")
		lines = append(lines, "  h          history strips")
		lines = append(lines, "  q          quit")
		lines = append(lines, "")
		lines = append(lines, "CATALOG / STATISTICS")
		lines = append(lines, "  a          all / live runs")
		lines = append(lines, "  ␣          mark pipeline")
		lines = append(lines, "  c          cancel marked runs")
		return lines
	}
	lines = append(lines, wrapWords(spec.summary, width)...)
	lines = append(lines, "")
	if spec.detail != "" {
		lines = append(lines, wrapWords(spec.detail, width)...)
		lines = append(lines, "")
	}
	lines = append(lines, "Usage  "+spec.usage)
	if spec.keys != "" {
		lines = append(lines, "Keys   "+spec.keys)
	}
	lines = append(lines, "Group  "+string(spec.category))
	return lines
}

// wrapWords soft-wraps s to width runes on word boundaries.
func wrapWords(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	var cur string
	for _, w := range words {
		if cur == "" {
			cur = w
			continue
		}
		if len([]rune(cur))+1+len([]rune(w)) <= width {
			cur += " " + w
			continue
		}
		lines = append(lines, cur)
		cur = w
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// commandListLabel is one left-pane row: marker + name + · summary clip.
func commandListLabel(spec psCmdSpec, selected bool, width int) string {
	marker := "  "
	if selected {
		marker = "▸ "
	}
	name := spec.name
	// "▸ name · summary…" — keep the name intact, clip only the summary.
	prefix := marker + name
	rest := width - len([]rune(prefix))
	if rest < 4 {
		return clipCells(prefix, width)
	}
	sum := " · " + spec.summary
	if len([]rune(sum)) > rest {
		sum = string([]rune(sum)[:rest-1]) + "…"
	}
	return prefix + sum
}

// commandRunRowLabel labels a run completion row in argument mode.
func commandRunRowLabel(run api.PsRun, selected bool, width int) string {
	marker := "  "
	if selected {
		marker = "▸ "
	}
	label := fmt.Sprintf("%s%-6s %s  %s", marker, run.ID, run.State, run.Pipeline)
	return clipCells(label, width)
}
