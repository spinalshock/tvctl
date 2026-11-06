package tui

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/omAthalye/tvctl/internal/device"
)

// Typing mode: while the TV has a text field open, the prompt mirrors it.
//
//	letters, backspace   edit the box; each change shows on the TV at once
//	arrows               move the TV's highlight
//	Enter                press the highlighted button on the TV
//	Esc                  back out (the remote's Back)
//
// The box always matches the TV's field: the companion app reports what
// the field holds, read on each 1.5 s keyboard check while a keyboard is up
// (so a restarted tvctl picks up where the TV is). Text the TV changes on its own
// (Enter on a digit, the remote) is adopted unless an edit of ours is still
// on its way. Without the companion app the remote protocol's keyboard poll
// stands in; it can't read the field, so the box starts empty.
//
// Typing takes over from whatever mode was active (keys mode included) and
// hands control back to it when the field closes or on Esc.

type typingState struct {
	live      bool          // the companion app is feeding field state
	sess      *device.Field // last state seen
	key       string        // the field the box belongs to
	dismissed string        // field backed out of with Esc: don't re-enter it
	label     string
	step      string
	password  bool

	buf, lastSent string // what the box holds, and what the TV last got
	sending       bool
	gen           atomic.Int64 // bumps on field change; a queued send then drops itself
	mu            sync.Mutex   // keeps sends to the TV in order

	kbClosed int   // consecutive closed polls (poll fallback)
	closeGen int64 // invalidates a pending leave when the field reopens
}

type (
	// inputMsg carries the field state (nil: unreadable).
	inputMsg struct{ st *device.Field }
	// textSentMsg: one send of the box to the TV finished.
	textSentMsg struct {
		sent  string
		gen   int64
		stale bool
		err   error
	}
	// leaveCheckMsg: the field looked closed a moment ago; is it still?
	leaveCheckMsg struct{ gen int64 }
	// typingErrMsg: Enter or Esc couldn't reach the TV.
	typingErrMsg struct{ err error }
)

// fetchInput reads the field: at start, and on each keyboard check while
// the TV has a keyboard up.
func (m *model) fetchInput() tea.Cmd {
	fr, ok := m.dev.(device.FieldReader)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		st, err := fr.Field(ctx)
		if err != nil {
			return inputMsg{}
		}
		return inputMsg{st: st}
	}
}

// onInput applies the TV's field state.
func (m *model) onInput(st *device.Field) tea.Cmd {
	if st == nil {
		return nil
	}
	t := &m.t
	t.live, t.sess = true, st
	if !st.Open {
		t.dismissed = ""
		if !m.typing {
			return nil
		}
		// Keyboards blink between steps; leave only if it stays closed.
		g := t.closeGen
		return tea.Tick(600*time.Millisecond, func(time.Time) tea.Msg { return leaveCheckMsg{gen: g} })
	}
	t.closeGen++
	if st.Key != t.dismissed {
		t.dismissed = ""
	}
	if !m.typing {
		if t.dismissed != "" {
			return nil
		}
		m.enterTyping()
	}
	newField := st.Key != t.key
	t.key, t.label, t.step, t.password = st.Key, firstNonEmpty(st.Prompt, st.Hint), st.Step, st.Password
	switch {
	case newField:
		m.resetLine()
		m.adopt(st.Text)
	case !st.Password && st.Text != m.console.input.Value() && m.lineClean():
		m.adopt(st.Text)
	}
	m.layout()
	return nil
}

// onPoll is the fallback when the companion app isn't feeding field state.
func (m *model) onPoll(msg kbMsg) {
	t := &m.t
	if t.live || msg.err {
		return
	}
	if msg.open {
		t.kbClosed = 0
		m.enterTyping()
		if m.console.input.Value() == "" && t.lastSent == "" && !t.sending && msg.text != "" {
			t.label = strings.TrimSpace(msg.text) // the remote protocol reports the placeholder here
		}
		return
	}
	t.kbClosed++
	if t.kbClosed >= 2 && m.typing {
		m.leaveTyping()
	}
}

func (m *model) updateTyping(msg tea.Msg) tea.Cmd {
	t := &m.t
	switch msg := msg.(type) {
	case inputMsg:
		return m.onInput(msg.st)
	case typingErrMsg:
		m.console.say(styleErr.Render("  " + msg.err.Error()))
	case leaveCheckMsg:
		if msg.gen == t.closeGen && m.typing && (t.sess == nil || !t.sess.Open) {
			m.leaveTyping()
		}
	case textSentMsg:
		if msg.stale || msg.gen != t.gen.Load() {
			return nil
		}
		t.sending = false
		if msg.err == nil {
			t.lastSent = msg.sent
		}
		if m.typing && t.buf != t.lastSent {
			t.sending = true
			return m.syncText()
		}
	}
	return nil
}

func (m *model) typingKey(msg tea.KeyMsg) tea.Cmd {
	t := &m.t
	switch msg.Type {
	case tea.KeyEsc:
		t.dismissed = t.key
		m.leaveTyping()
		return m.tvKey(device.KeyBack)
	case tea.KeyEnter:
		return m.pressSelect()
	case tea.KeyUp:
		return sendKey(device.KeyUp)
	case tea.KeyDown:
		return sendKey(device.KeyDown)
	case tea.KeyLeft:
		return sendKey(device.KeyLeft)
	case tea.KeyRight:
		return sendKey(device.KeyRight)
	case tea.KeyTab, tea.KeyShiftTab:
		return nil
	}
	cmd := m.console.Update(msg)
	m.console.suggests = nil // command completions are noise in a TV field
	m.layout()
	if v := m.console.input.Value(); v != t.buf {
		t.buf = v
		if !t.sending {
			t.sending = true
			cmd = tea.Batch(cmd, m.syncText())
		}
	}
	return cmd
}

// pressSelect is Enter: the box's latest text reaches the TV first, then
// the highlighted button is pressed, in that order.
func (m *model) pressSelect() tea.Cmd {
	t := &m.t
	buf, need := t.buf, t.buf != t.lastSent || t.sending
	t.gen.Add(1) // a send still queued drops itself; this one flushes
	t.sending, t.lastSent = false, buf
	dev, mu := m.dev, &t.mu
	return func() tea.Msg {
		mu.Lock()
		defer mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if need {
			if err := dev.Text(ctx, buf); err != nil {
				return typingErrMsg{err}
			}
		}
		if err := dev.Key(ctx, device.KeySelect); err != nil {
			return typingErrMsg{err}
		}
		return nil
	}
}

// tvKey presses one key on the TV, reporting a failure instead of hiding it.
func (m *model) tvKey(k device.Key) tea.Cmd {
	dev := m.dev
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := dev.Key(ctx, k); err != nil {
			return typingErrMsg{err}
		}
		return nil
	}
}

// syncText sends the box to the TV's field (which it replaces).
func (m *model) syncText() tea.Cmd {
	t := &m.t
	dev, buf, gen, cur, mu := m.dev, t.buf, t.gen.Load(), &t.gen, &t.mu
	return func() tea.Msg {
		mu.Lock()
		defer mu.Unlock()
		if cur.Load() != gen {
			return textSentMsg{gen: gen, stale: true}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		return textSentMsg{sent: buf, gen: gen, err: dev.Text(ctx, buf)}
	}
}

// enterTyping turns the prompt into the mirror; true on the way in.
func (m *model) enterTyping() bool {
	if m.typing {
		return false
	}
	m.typing = true
	m.t.kbClosed = 0
	m.console.input.SetValue("")
	m.resetLine()
	m.layout()
	return true
}

// leaveTyping hands control back to the mode that was active.
func (m *model) leaveTyping() {
	m.typing = false
	t := &m.t
	t.key, t.label, t.step, t.password, t.kbClosed = "", "", "", false, 0
	m.resetLine()
	m.console.input.SetValue("")
	m.layout()
}

func (m *model) resetLine() {
	t := &m.t
	t.buf, t.lastSent, t.sending = "", "", false
	t.gen.Add(1)
}

// adopt makes the TV's text the box's, already in sync.
func (m *model) adopt(text string) {
	m.console.input.SetValue(text)
	m.console.input.CursorEnd()
	m.t.buf, m.t.lastSent = text, text
}

// lineClean: nothing of ours is on its way, so the TV's text is current.
func (m *model) lineClean() bool {
	return m.console.input.Value() == m.t.lastSent && !m.t.sending
}

const typingPanelRows = 4

// typingPanel is the highlighted box shown while the TV wants text.
func (m *model) typingPanel(w int) string {
	t := &m.t
	inner := w - 4 // rounded border (2) + horizontal padding (2)
	if inner < 10 {
		inner = 10
	}
	what := styleDim.Render("text field")
	if t.label != "" {
		what = styleTitle.Render(truncRunes(t.label, inner-24))
	}
	head := styleAccent.Bold(true).Render("⌨ TYPING") + styleDim.Render("  → ") + what
	if t.step != "" {
		head += styleDim.Render("   step " + t.step)
	}
	if t.password {
		head += styleDim.Render("   password")
	}
	val := []rune(mask(m.console.input.Value(), t.password))
	line := stylePrompt.Render("› ") + caretLine(val, m.console.input.Position(), inner-4)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accent).
		Padding(0, 1).
		Width(inner).
		MaxHeight(typingPanelRows).
		Render(lipgloss.JoinVertical(lipgloss.Left, head, line))
}

func (m *model) typingFooter() string {
	return styleAccent.Render("TYPING ") + styleDim.Render("type to fill · arrows move · Enter presses the highlighted button · Esc backs out")
}

// caretLine draws val with a block cursor at pos, scrolled so the cursor is
// always visible within width columns.
func caretLine(val []rune, pos, width int) string {
	if width < 2 {
		width = 2
	}
	if pos > len(val) {
		pos = len(val)
	}
	start := 0
	if pos >= width {
		start = pos - width + 1
	}
	end := start + width
	if end > len(val) {
		end = len(val)
	}
	caret := lipgloss.NewStyle().Reverse(true)
	left := styleText.Render(string(val[start:pos]))
	if pos >= len(val) {
		return left + caret.Render(" ")
	}
	return left + caret.Render(string(val[pos])) + styleText.Render(string(val[pos+1:end]))
}

// truncRunes caps s to n runes with a trailing ellipsis.
func truncRunes(s string, n int) string {
	if n < 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

func mask(s string, password bool) string {
	if !password {
		return s
	}
	return strings.Repeat("•", len([]rune(s)))
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
