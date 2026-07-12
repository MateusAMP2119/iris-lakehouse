package cli

// The clack-style ceremony kit: the rail-and-box widget vocabulary the
// interactive quickstart tour renders with on a real terminal — an intro
// corner, a running left rail, boxed notes, an arrow-key select, a Yes/No
// confirm, and a spinner. Hand-rolled on the existing painter (no TUI
// framework); raw terminal mode comes from golang.org/x/term. Every widget
// is production-surface only: the tour's injectable seams (tourPick,
// tourInput) bypass this file entirely, so harnessed tests never meet raw
// mode. EXPERIMENT: not yet covered by spec contracts.

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

// The rail glyphs, matching the @clack/prompts visual language.
const (
	railTop  = "┌"
	railBar  = "│"
	railNote = "◇"
	railAsk  = "◆"
	railEnd  = "└"
)

// clackBoxWidth is a note box's inner column budget; longer body lines wrap.
const clackBoxWidth = 72

// clackIntro opens the rail: `┌  title` and one bare rail line.
func clackIntro(w io.Writer, p painter, title string) {
	fmt.Fprintf(w, "%s  %s\n", railTop, p.cyan(title))
	clackBar(w, p)
}

// clackBar prints one bare rail line.
func clackBar(w io.Writer, p painter) {
	fmt.Fprintln(w, p.dim(railBar))
}

// clackOutro closes the rail: `└  message`.
func clackOutro(w io.Writer, p painter, msg string) {
	fmt.Fprintf(w, "%s  %s\n", railEnd, msg)
}

// wrapLine hard-wraps one line to width columns on spaces, best effort.
func wrapLine(s string, width int) []string {
	if utf8.RuneCountInString(s) <= width {
		return []string{s}
	}
	var out []string
	words := strings.Fields(s)
	cur := ""
	for _, word := range words {
		if cur == "" {
			cur = word
			continue
		}
		if utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(word) > width {
			out = append(out, cur)
			cur = word
			continue
		}
		cur += " " + word
	}
	if cur != "" {
		out = append(out, cur)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// clackNote renders one boxed note hanging off the rail:
//
//	◇  Title ────────────────╮
//	│                        │
//	│  body                  │
//	│                        │
//	├────────────────────────╯
func clackNote(w io.Writer, p painter, title, body string) {
	var lines []string
	for _, raw := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		lines = append(lines, wrapLine(raw, clackBoxWidth-4)...)
	}
	inner := utf8.RuneCountInString(title) + 4
	for _, l := range lines {
		if n := utf8.RuneCountInString(l) + 4; n > inner {
			inner = n
		}
	}
	if inner > clackBoxWidth {
		inner = clackBoxWidth
	}
	pad := inner - utf8.RuneCountInString(title) - 3
	if pad < 1 {
		pad = 1
	}
	fmt.Fprintf(w, "%s  %s %s\n", railNote, p.cyan(title), p.dim(strings.Repeat("─", pad)+"╮"))
	blank := strings.Repeat(" ", inner)
	fmt.Fprintf(w, "%s%s%s\n", p.dim(railBar), blank, p.dim("│"))
	for _, l := range lines {
		fill := inner - 4 - utf8.RuneCountInString(l)
		if fill < 0 {
			fill = 0
		}
		fmt.Fprintf(w, "%s  %s%s  %s\n", p.dim(railBar), l, strings.Repeat(" ", fill), p.dim("│"))
	}
	fmt.Fprintf(w, "%s%s%s\n", p.dim(railBar), blank, p.dim("│"))
	fmt.Fprintf(w, "%s\n", p.dim("├"+strings.Repeat("─", inner)+"╯"))
}

// clackOption is one entry of the arrow-key select: a label and a dim hint.
type clackOption struct {
	label string
	hint  string
}

// The keys the raw-mode reader distinguishes.
type clackKey int

const (
	keyOther clackKey = iota
	keyUp
	keyDown
	keyLeft
	keyRight
	keyEnter
	keyQuit  // q, Esc, Ctrl-C, Ctrl-D, or EOF: the clean decline
	keyDigit // '1'..'9'; the digit value rides in the second return
)

// rawTTY is one raw-mode session over the process stdin.
type rawTTY struct {
	fd    int
	state *term.State
}

// openRawTTY switches the process stdin to raw mode. ok=false — not a
// terminal, or raw mode refused — means the caller must fall back to the
// cooked line dialogue.
func openRawTTY() (*rawTTY, bool) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, false
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, false
	}
	return &rawTTY{fd: fd, state: state}, true
}

// restore returns the terminal to cooked mode.
func (r *rawTTY) restore() { _ = term.Restore(r.fd, r.state) }

// readKey reads and classifies one keypress in raw mode.
func (r *rawTTY) readKey() (clackKey, int) {
	var b [1]byte
	n, err := os.Stdin.Read(b[:])
	if err != nil || n == 0 {
		return keyQuit, 0
	}
	switch b[0] {
	case 0x03, 0x04, 'q', 'Q': // Ctrl-C, Ctrl-D
		return keyQuit, 0
	case '\r', '\n':
		return keyEnter, 0
	case 0x1b: // ESC — bare, or the lead of an arrow sequence
		var seq [2]byte
		if n, err := os.Stdin.Read(seq[:1]); err != nil || n == 0 || seq[0] != '[' {
			return keyQuit, 0
		}
		if n, err := os.Stdin.Read(seq[1:]); err != nil || n == 0 {
			return keyQuit, 0
		}
		switch seq[1] {
		case 'A':
			return keyUp, 0
		case 'B':
			return keyDown, 0
		case 'C':
			return keyRight, 0
		case 'D':
			return keyLeft, 0
		}
		return keyOther, 0
	}
	if b[0] >= '1' && b[0] <= '9' {
		return keyDigit, int(b[0] - '0')
	}
	switch b[0] {
	case 'k':
		return keyUp, 0
	case 'j':
		return keyDown, 0
	case 'y', 'Y':
		return keyLeft, 0 // confirm shorthand: yes
	case 'n', 'N':
		return keyRight, 0 // confirm shorthand: no
	}
	return keyOther, 0
}

// In raw mode the terminal does no output post-processing: every line the
// widgets print while raw ends in \r\n explicitly.
const rawEOL = "\r\n"

// ansiUp moves the cursor up n lines; ansiClear erases the current line.
func ansiUp(w io.Writer, n int) { fmt.Fprintf(w, "\033[%dA", n) }
func ansiClearLine(w io.Writer) { fmt.Fprint(w, "\r\033[2K") }

// clackSelect renders the arrow-key radio select and returns the picked
// 1-based index. Up/Down (or j/k) move, 1-9 jump, Enter accepts, q/Esc/
// Ctrl-C decline. ok=false means raw mode was unavailable and nothing was
// rendered: the caller falls back to the numbered line dialogue.
func clackSelect(w io.Writer, p painter, question string, options []clackOption) (choice int, ans promptAnswer, ok bool) {
	tty, rawOK := openRawTTY()
	if !rawOK {
		return 0, answerQuit, false
	}
	defer tty.restore()

	fmt.Fprintf(w, "%s  %s %s%s", railAsk, question, p.dim("(↑/↓ move · Enter picks · q quits)"), rawEOL)
	idx := 0
	render := func() {
		for i, opt := range options {
			marker, label := "○", opt.label
			if i == idx {
				marker, label = p.green("●"), p.cyan(opt.label)
			}
			hint := ""
			if opt.hint != "" {
				hint = "  " + p.dim(opt.hint)
			}
			fmt.Fprintf(w, "%s  %s %s%s%s", p.dim(railBar), marker, label, hint, rawEOL)
		}
	}
	render()
	for {
		key, digit := tty.readKey()
		switch key {
		case keyQuit:
			fmt.Fprint(w, rawEOL)
			return 0, answerQuit, true
		case keyEnter:
			return idx + 1, answerProceed, true
		case keyUp:
			idx = (idx + len(options) - 1) % len(options)
		case keyDown:
			idx = (idx + 1) % len(options)
		case keyDigit:
			if digit >= 1 && digit <= len(options) {
				idx = digit - 1
			}
		default:
			continue
		}
		ansiUp(w, len(options))
		for range options {
			ansiClearLine(w)
			fmt.Fprint(w, "\033[1B")
		}
		ansiUp(w, len(options))
		render()
	}
}

// clackConfirm renders the Yes/No radio confirm and returns the answer.
// Left/Right (or y/n) toggle, Enter accepts, q/Esc/Ctrl-C decline. ok=false
// means raw mode was unavailable and nothing was rendered.
func clackConfirm(w io.Writer, p painter, question string, def bool) (yes bool, ans promptAnswer, ok bool) {
	tty, rawOK := openRawTTY()
	if !rawOK {
		return false, answerQuit, false
	}
	defer tty.restore()

	fmt.Fprintf(w, "%s  %s%s", railAsk, question, rawEOL)
	val := def
	render := func() {
		yesMark, noMark := "○", "○"
		yesLabel, noLabel := "Yes", "No"
		if val {
			yesMark, yesLabel = p.green("●"), p.cyan("Yes")
		} else {
			noMark, noLabel = p.green("●"), p.cyan("No")
		}
		fmt.Fprintf(w, "%s  %s %s / %s %s%s", p.dim(railBar), yesMark, yesLabel, noMark, noLabel, rawEOL)
	}
	render()
	for {
		key, _ := tty.readKey()
		switch key {
		case keyQuit:
			fmt.Fprint(w, rawEOL)
			return false, answerQuit, true
		case keyEnter:
			return val, answerProceed, true
		case keyLeft, keyUp:
			val = true
		case keyRight, keyDown:
			val = false
		default:
			continue
		}
		ansiUp(w, 1)
		ansiClearLine(w)
		render()
	}
}

// spinnerFrames is the braille spinner cycle.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// startSpinner animates `<frame> msg` in place on the ceremony surface and
// returns its stop function, which clears the line and prints `✓ done` — or,
// with an empty done, only clears (the failure path renders its own error).
// Call only when nothing else writes to w until stop.
func startSpinner(w io.Writer, p painter, msg string) func(done string) {
	if !p.enabled {
		fmt.Fprintf(w, "· %s\n", msg)
		return func(done string) {
			if done != "" {
				fmt.Fprintf(w, "✓ %s\n", done)
			}
		}
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		i := 0
		tick := time.NewTicker(80 * time.Millisecond)
		defer tick.Stop()
		for {
			fmt.Fprintf(w, "\r\033[2K%s %s", p.cyan(spinnerFrames[i%len(spinnerFrames)]), msg)
			select {
			case <-stopCh:
				return
			case <-tick.C:
				i++
			}
		}
	}()
	return func(done string) {
		close(stopCh)
		<-doneCh
		fmt.Fprint(w, "\r\033[2K")
		if done != "" {
			fmt.Fprintf(w, "%s %s\n", p.green("✓"), done)
		}
	}
}
