package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/omAthalye/tvctl/internal/device"
)

// The toast: a command's outcome worth announcing ("launched Netflix",
// "playing: …", a new wallpaper) shows as a card in the middle of the
// screen for a moment. The scrollback keeps the line as the record; the
// toast is what catches the eye. Commands mark these outcomes themselves
// (cli.Env.Notice), so routine acks and listings never pop up.

const toastFor = 2500 * time.Millisecond

type toastState struct {
	text string
	gen  int // a newer toast outlives an older one's timer
}

// toastDoneMsg: the toast with this generation has had its time.
type toastDoneMsg struct{ gen int }

func (m *model) showToast(text string) tea.Cmd {
	m.toast.text = m.friendly(text)
	m.toast.gen++
	g := m.toast.gen
	return tea.Tick(toastFor, func(time.Time) tea.Msg { return toastDoneMsg{gen: g} })
}

func (m *model) toastDone(msg toastDoneMsg) {
	if msg.gen == m.toast.gen {
		m.toast.text = ""
	}
}

// friendly swaps any app id in a notice for the app's name, through the
// driver and the app list cached at start.
func (m *model) friendly(text string) string {
	words := strings.Fields(text)
	for i, w := range words {
		words[i] = m.appTitle(w)
	}
	return strings.Join(words, " ")
}

// appTitle is what people call an app id: the driver's name for it, else
// the title in the cached app list, else the id itself.
func (m *model) appTitle(id string) string {
	if an, ok := m.dev.(device.AppNamer); ok {
		if n := an.AppName(id); n != "" {
			return n
		}
	}
	for _, a := range m.apps {
		if a.ID == id && a.Name != "" {
			return a.Name
		}
	}
	return id
}

var styleToast = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(okGreen).
	Padding(1, 3)

// withToast lays the toast over the middle of the rendered frame.
func (m *model) withToast(frame string) string {
	if m.toast.text == "" {
		return frame
	}
	max := m.width - 10
	if max < 12 {
		max = 12
	}
	body := lipgloss.NewStyle().Foreground(okGreen).Bold(true).Render("✓  ") +
		styleTitle.Render(truncRunes(m.toast.text, max))
	return overlay(frame, styleToast.Render(body), m.width)
}

// overlay places box centred over bg, keeping bg's text on either side.
func overlay(bg, box string, width int) string {
	rows := strings.Split(bg, "\n")
	card := strings.Split(box, "\n")
	bw := lipgloss.Width(box)
	x := (width - bw) / 2
	if x < 0 {
		x = 0
	}
	y := (len(rows) - len(card)) / 2
	if y < 0 {
		y = 0
	}
	for i, line := range card {
		r := y + i
		if r >= len(rows) {
			break
		}
		under := rows[r]
		left := ansi.Truncate(under, x, "")
		if pad := x - ansi.StringWidth(left); pad > 0 {
			left += strings.Repeat(" ", pad)
		}
		rows[r] = left + line + ansi.TruncateLeft(under, x+bw, "")
	}
	return strings.Join(rows, "\n")
}
