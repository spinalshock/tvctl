package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/omAthalye/tvctl/internal/device"
)

func newTypingModel() *model {
	m := &model{console: newConsole()}
	m.width, m.height = 100, 30
	return m
}

func keys(m *model, s string) {
	for _, r := range s {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func field(key, prompt, step, text string) inputMsg {
	return inputMsg{st: &device.Field{Open: true, Key: key, Prompt: prompt, Step: step, Text: text}}
}

// The box matches the TV's field: its text on entry (so a restarted TUI
// picks up where the TV is), TV-side changes when nothing of ours is in
// flight, and the next field's text when focus moves.
func TestBoxMatchesTV(t *testing.T) {
	m := newTypingModel()
	m.Update(field("A", "Enter the IP Address", "1 of 5", "192.168.0.106"))
	if !m.typing || m.console.input.Value() != "192.168.0.106" || m.t.lastSent != "192.168.0.106" {
		t.Fatalf("field's text not adopted: typing=%v box=%q", m.typing, m.console.input.Value())
	}
	keys(m, "9")
	if !m.t.sending || m.t.buf != "192.168.0.1069" {
		t.Fatalf("edit not sent: sending=%v buf=%q", m.t.sending, m.t.buf)
	}
	m.Update(field("A", "Enter the IP Address", "1 of 5", "192.168.0.106")) // old state, our edit in flight
	if got := m.console.input.Value(); got != "192.168.0.1069" {
		t.Fatalf("in-flight edit overwritten: %q", got)
	}
	m.Update(textSentMsg{sent: "192.168.0.1069", gen: m.t.gen.Load()})
	m.Update(field("A", "Enter the IP Address", "1 of 5", "192.168.0.10691")) // Enter pressed a digit on the TV
	if got := m.console.input.Value(); got != "192.168.0.10691" {
		t.Fatalf("TV-side change not adopted: %q", got)
	}
	m.Update(field("B", "Enter the Gateway Address", "3 of 5", ""))
	if m.console.input.Value() != "" || m.t.label != "Enter the Gateway Address" || !strings.Contains(m.typingPanel(100), "3 of 5") {
		t.Fatalf("next field not taken: box=%q label=%q", m.console.input.Value(), m.t.label)
	}
	// Closed: leave only if it stays closed past the check.
	m.Update(inputMsg{st: &device.Field{Open: false}})
	gen := m.t.closeGen
	m.Update(field("B", "Enter the Gateway Address", "3 of 5", "")) // blinked back
	m.Update(leaveCheckMsg{gen: gen})
	if !m.typing {
		t.Fatal("left although the field reopened")
	}
	m.Update(inputMsg{st: &device.Field{Open: false}})
	m.Update(leaveCheckMsg{gen: m.t.closeGen})
	if m.typing {
		t.Fatal("didn't leave a closed field")
	}
}

// Arrows and Enter go to the TV (Enter presses the highlighted button) and
// leave the box alone.
func TestKeysGoToTV(t *testing.T) {
	m := newTypingModel()
	m.Update(field("A", "", "", ""))
	keys(m, "12")
	for _, k := range []tea.KeyType{tea.KeyUp, tea.KeyDown, tea.KeyLeft, tea.KeyRight, tea.KeyEnter} {
		if _, cmd := m.Update(tea.KeyMsg{Type: k}); cmd == nil {
			t.Errorf("key %v sent nothing to the TV", k)
		}
	}
	if got := m.console.input.Value(); got != "12" || !m.typing {
		t.Fatalf("keys changed the box or left typing: box=%q typing=%v", got, m.typing)
	}
}

// A send queued before the field changed is dropped, not recorded.
func TestStaleSendDropped(t *testing.T) {
	m := newTypingModel()
	m.Update(field("A", "", "", ""))
	keys(m, "a")
	old := m.t.gen.Load()
	m.Update(field("B", "", "", ""))
	m.Update(textSentMsg{sent: "a", gen: old})
	if m.t.lastSent != "" {
		t.Fatalf("stale send recorded: %q", m.t.lastSent)
	}
}

// Esc backs out, and the same field doesn't pull typing straight back.
func TestEscDoesNotBounce(t *testing.T) {
	m := newTypingModel()
	m.Update(field("A", "Search", "", ""))
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(field("A", "Search", "", ""))
	if m.typing {
		t.Fatal("bounced back into the field just backed out of")
	}
	m.Update(field("B", "Password", "", ""))
	if !m.typing {
		t.Fatal("a new field should open typing")
	}
}

// Typing overlays keys mode: it takes the keys while the field is open and
// hands them back to keys mode when it closes or on Esc.
func TestTypingOverlaysKeys(t *testing.T) {
	m := newTypingModel()
	m.mode = modeKeys
	m.Update(field("A", "Search", "", ""))
	keys(m, "dark")
	if got := m.console.input.Value(); got != "dark" {
		t.Fatalf("keys mode swallowed typing: box=%q", got)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.typing || m.mode != modeKeys {
		t.Fatalf("after Esc: typing=%v mode=%v, want keys mode back", m.typing, m.mode)
	}
	m.Update(field("B", "Search", "", ""))
	m.Update(inputMsg{st: &device.Field{Open: false}})
	m.Update(leaveCheckMsg{gen: m.t.closeGen})
	if m.typing || m.mode != modeKeys {
		t.Fatalf("after the field closed: typing=%v mode=%v, want keys mode back", m.typing, m.mode)
	}
}

// Without the companion app the keyboard poll stands in: one stray bad
// probe doesn't drop the box; two closed reads leave.
func TestPollFallback(t *testing.T) {
	m := newTypingModel()
	m.Update(kbMsg{open: true, text: "Enter the IP Address"})
	keys(m, "192")
	m.Update(kbMsg{err: true})
	m.Update(kbMsg{open: false})
	if !m.typing || m.console.input.Value() != "192" {
		t.Fatalf("one bad probe dropped the box: typing=%v box=%q", m.typing, m.console.input.Value())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if got := m.console.input.Value(); got != "19" {
		t.Fatalf("backspace: %q", got)
	}
	m.Update(kbMsg{open: false})
	m.Update(kbMsg{open: false})
	if m.typing {
		t.Fatal("two closed probes should leave")
	}
}

// A failed Enter or Esc says so in the scrollback instead of doing nothing.
func TestTypingErrorShown(t *testing.T) {
	m := newTypingModel()
	m.Update(field("A", "", "", ""))
	m.Update(typingErrMsg{errors.New("TV not reachable")})
	if !strings.Contains(strings.Join(m.console.lines, "\n"), "TV not reachable") {
		t.Fatal("error not shown")
	}
}

// A closed keyboard while not typing leaves the prompt alone: the poll
// fallback used to "leave typing" on every check, wiping what was typed.
func TestClosedPollKeepsPrompt(t *testing.T) {
	m := newTypingModel()
	keys(m, "youtube ")
	for i := 0; i < 3; i++ {
		m.Update(kbMsg{open: false})
	}
	if v := m.console.input.Value(); v != "youtube " {
		t.Fatalf("prompt wiped by keyboard polls: %q", v)
	}
}
