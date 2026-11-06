package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/spinalshock/tvctl/internal/device"
)

// nowBar is the two-line now-playing block under the header. It owns the
// receiver connection state and the last now-playing document, and draws
// position with a Bubbles progress bar. Between events it extrapolates the
// position from the playback speed, so the bar moves every tick.
type nowBar struct {
	name    func(string) string // app id -> what people call it
	enabled bool                // the TV has a now-playing feed
	hint    string              // when it hasn't: what would give it one, if anything
	state   string
	now     *device.Playing
	at      time.Time // when now was received
	bar     progress.Model
}

// newNowBar is enabled when the TV has a now-playing feed; hint is what
// the bar says instead when it hasn't (see nowFeed).
func newNowBar(enabled bool, hint string) nowBar {
	bar := progress.New(progress.WithGradient("#5fafff", "#87d7af"), progress.WithoutPercentage())
	state := "offline"
	if !enabled {
		state = "not set up"
	}
	return nowBar{enabled: enabled, hint: hint, state: state, bar: bar, name: func(id string) string { return id }}
}

// nowFeed reports whether dev gives rich now-playing and, when it
// doesn't, whether the companion app would.
func nowFeed(dev device.Device) (enabled bool, hint string) {
	if _, ok := dev.(device.Player); ok {
		return true, ""
	}
	if _, can := dev.(device.Installer); can {
		return false, "now-playing, screenshots and local files need the companion app: tv companion"
	}
	return false, "this TV has no now-playing feed"
}

func (n *nowBar) set(now *device.Playing) {
	n.state = "online"
	n.now = now
	n.at = time.Now()
}

func (n *nowBar) online() { n.state = "online" }

// offline drops the document: never show frozen data from a dead link.
func (n *nowBar) offline() {
	n.state = "offline"
	n.now = nil
}

// position is the extrapolated playback position in ms.
func (n *nowBar) position() int64 {
	now := n.now
	pos := now.PositionMs
	if now.State == "playing" && now.Speed > 0 {
		pos += int64(time.Since(n.at).Seconds() * 1000 * now.Speed)
		if now.DurationMs > 0 && pos > now.DurationMs {
			pos = now.DurationMs
		}
	}
	return pos
}

// dot is the header's connection indicator and label.
func (n *nowBar) dot() (string, string) {
	switch n.state {
	case "online":
		return lipgloss.NewStyle().Foreground(okGreen).Render("●"), "online"
	case "offline":
		return styleErr.Render("●"), "offline"
	}
	return lipgloss.NewStyle().Foreground(warn).Render("●"), "no now-playing feed"
}

func (n *nowBar) View(w int) string {
	empty := styleBarOff.Render("  " + strings.Repeat("╌", max(0, w-4)))
	if !n.enabled {
		return styleDim.Render("  "+n.hint) + "\n" + empty
	}
	cur := n.now
	if cur == nil || cur.Source == nil {
		fg := ""
		if cur != nil && cur.Foreground != nil {
			fg = "  ·  " + n.name(*cur.Foreground)
		}
		return styleDim.Render("  nothing playing"+fg) + "\n" + empty
	}
	pos := n.position()
	sym := map[string]string{"playing": "▶", "paused": "⏸", "buffering": "◌", "stopped": "■"}[cur.State]
	if sym == "" {
		sym = "·"
	}
	symStyle := styleAccent
	if cur.State != "playing" {
		symStyle = styleDim
	}
	title := ""
	if cur.Title != nil {
		title = *cur.Title
	}
	if cur.Artist != nil && *cur.Artist != "" {
		title = *cur.Artist + "  —  " + title
	}
	left := "  " + symStyle.Render(sym) + " " + styleAccent.Render(n.name(cur.App)) + "   "
	avail := w - lipgloss.Width(left) - 2
	var line1 string
	if strings.TrimSpace(title) == "" {
		// Some apps (Netflix) publish no metadata at all; say so instead of
		// leaving a gap that looks like a bug.
		line1 = left + styleDim.Render("no title published by this app")
	} else {
		line1 = left + styleTitle.Render(ansi.Truncate(title, max(0, avail), "…"))
	}

	clock := fmtClock(pos)
	if cur.DurationMs > 0 {
		clock += " / " + fmtClock(cur.DurationMs)
	} else {
		clock += "  elapsed"
	}
	barW := max(10, w-4-lipgloss.Width(clock)-2)
	var bar string
	if cur.DurationMs > 0 {
		n.bar.Width = barW
		bar = n.bar.ViewAs(float64(pos) / float64(cur.DurationMs))
	} else {
		// Unknown length: a dashed line reads as "no end known", not "0%".
		bar = styleBarOff.Render(strings.Repeat("╌", barW))
	}
	return line1 + "\n" + "  " + bar + "  " + styleDim.Render(clock)
}

func fmtClock(ms int64) string {
	s := ms / 1000
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
