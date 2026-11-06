package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// console is the terminal part of the TUI: a prompt with history and fuzzy
// completion, the scrollback above it, and a spinner while a command runs.
// It knows nothing about TVs. When the user presses Enter it emits a
// submitMsg and the root model decides what the line means.
type console struct {
	input    textinput.Model
	view     viewport.Model
	spin     spinner.Model
	lines    []string // scrollback, already styled
	history  []string
	histPos  int
	suggests []match
	sugSel   int
	running  string // command currently executing, "" when idle
	startAt  time.Time
	width    int
	height   int

	// complete is injected by the root, which owns the verb and app lists.
	complete func(value string) []match
}

// submitMsg is what Enter produces. The root turns it into a command.
type submitMsg struct{ line string }

func newConsole() console {
	in := textinput.New()
	in.Prompt = "❯ "
	in.PromptStyle = stylePrompt
	in.Placeholder = "command, app, search or link · help"
	in.Focus()
	in.CharLimit = 512
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(styleAccent))
	return console{input: in, view: viewport.New(80, 20), spin: sp}
}

// Update handles keys while the prompt is active, plus spinner frames.
// Special keys are matched on msg.Type, never msg.String(): Bubble Tea
// batches runes that arrive together into one message whose String() is the
// concatenation, so a fast "up" would otherwise look like the up arrow.
func (c *console) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if c.running == "" {
			return nil // a stale frame after the command finished
		}
		var cmd tea.Cmd
		c.spin, cmd = c.spin.Update(msg)
		return cmd
	case tea.KeyMsg:
		return c.key(msg)
	}
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(msg)
	return cmd
}

func (c *console) key(msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyCtrlL:
		c.clear()
		return nil
	case tea.KeyTab:
		if len(c.suggests) > 0 {
			c.accept(c.suggests[c.sugSel].text)
			c.sugSel = 0
			c.recomplete()
		}
		return nil
	case tea.KeyShiftTab:
		if len(c.suggests) > 0 {
			c.sugSel = (c.sugSel + 1) % len(c.suggests)
		}
		return nil
	case tea.KeyUp:
		if c.histPos > 0 {
			c.histPos--
			c.input.SetValue(c.history[c.histPos])
			c.input.CursorEnd()
		}
		return nil
	case tea.KeyDown:
		if c.histPos < len(c.history)-1 {
			c.histPos++
			c.input.SetValue(c.history[c.histPos])
			c.input.CursorEnd()
		} else {
			c.histPos = len(c.history)
			c.input.SetValue("")
		}
		return nil
	case tea.KeyEnter:
		line := strings.TrimSpace(c.input.Value())
		c.input.SetValue("")
		c.suggests = nil
		c.layout(c.width, c.height)
		if line == "" {
			return nil
		}
		c.history = append(c.history, line)
		c.histPos = len(c.history)
		return func() tea.Msg { return submitMsg{line} }
	case tea.KeyEsc:
		c.input.SetValue("")
		c.suggests = nil
		return nil
	}
	var cmd tea.Cmd
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 && !msg.Paste {
		// Runes that arrived in one read (fast typing, some pastes) come as
		// one message. The text input matches its own bindings by string,
		// so a batch spelling "up" or "down" would hit the suggestion
		// bindings and vanish. Feed them one rune at a time instead.
		for _, r := range msg.Runes {
			c.input, cmd = c.input.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
	} else {
		c.input, cmd = c.input.Update(msg)
	}
	c.recomplete()
	c.layout(c.width, c.height)
	return cmd
}

// begin marks a command as running and starts the spinner.
func (c *console) begin(line string) tea.Cmd {
	c.running = line
	c.startAt = time.Now()
	return c.spin.Tick
}

// done records a finished command's output in the scrollback.
func (c *console) done(cmd, out string, err error, took time.Duration) {
	c.running = ""
	c.say(stylePrompt.Render("› ") + styleBold.Render(cmd) + styleDim.Render("   "+fmtSecs(took)))
	out = strings.TrimRight(out, "\n")
	if err != nil {
		// The plain output is the one-sentence form (what an assistant
		// reads aloud); here a person is debugging, so show the whole
		// cause chain instead.
		out = err.Error()
	}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimPrefix(l, "tv: ")
		if err != nil {
			c.say("  " + styleErr.Render(l))
		} else {
			c.say("  " + styleText.Render(l))
		}
	}
}

func (c *console) recomplete() {
	if c.complete == nil {
		c.suggests = nil
		return
	}
	c.suggests = c.complete(c.input.Value())
	if c.sugSel >= len(c.suggests) {
		c.sugSel = 0
	}
}

// accept replaces the word being typed with the chosen suggestion.
func (c *console) accept(s string) {
	val := c.input.Value()
	if strings.HasSuffix(val, " ") {
		c.input.SetValue(val + s + " ")
	} else {
		fields := strings.Fields(val)
		fields[len(fields)-1] = s
		c.input.SetValue(strings.Join(fields, " ") + " ")
	}
	c.input.CursorEnd()
}

// say appends one line to the scrollback and keeps the view pinned to the
// bottom, like a terminal.
func (c *console) say(line string) {
	c.lines = append(c.lines, line)
	if len(c.lines) > 2000 {
		c.lines = c.lines[len(c.lines)-2000:]
	}
	c.refresh()
}

func (c *console) clear() {
	c.lines = nil
	c.refresh()
}

// layout gives the console its box: the scrollback takes whatever is left
// after the suggestion rows and the prompt.
func (c *console) layout(width, height int) {
	c.width, c.height = width, height
	h := height - len(c.suggests) - 1 // prompt line
	if h < 3 {
		h = 3
	}
	c.view.Width = width
	c.view.Height = h
	c.input.Width = width - 4
	c.refresh()
}

// refresh pins the newest output just above the prompt, like a terminal:
// when there's less content than space, blank lines are added on top.
func (c *console) refresh() {
	lines := c.lines
	if pad := c.view.Height - len(lines); pad > 0 {
		lines = append(make([]string, pad), lines...)
	}
	c.view.SetContent(strings.Join(lines, "\n"))
	c.view.GotoBottom()
}

// View is scrollback, suggestions, then the prompt. The status line under
// the prompt belongs to the root, which knows the mode.
func (c *console) View() string {
	parts := []string{c.view.View()}
	for i, sg := range c.suggests {
		line := "  " + highlight(sg)
		if i == c.sugSel {
			line = styleSugSel.Render(line)
		} else {
			line = styleSug.Render(line)
		}
		parts = append(parts, line)
	}
	parts = append(parts, c.input.View())
	return strings.Join(parts, "\n")
}

// status is the running indicator: spinner, command, elapsed time.
func (c *console) status() string {
	if c.running == "" {
		return ""
	}
	return c.spin.View() + " " + styleDim.Render(c.running+"  "+fmtSecs(time.Since(c.startAt)))
}

func fmtSecs(d time.Duration) string {
	return strings.TrimSuffix(strings.TrimSuffix(d.Round(100*time.Millisecond).String(), "0s"), ".") + ""
}

// highlight bolds the matched characters of a suggestion.
func highlight(s match) string {
	var b strings.Builder
	pi := 0
	for i, r := range s.text {
		if pi < len(s.pos) && s.pos[pi] == i {
			b.WriteString(styleHit.Render(string(r)))
			pi++
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
