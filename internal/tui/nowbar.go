package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/omAthalye/tvctl/internal/receiver"
)

// nowBar is the two-line now-playing block under the header. It owns the
// receiver connection state and the last now-playing document, and draws
// position with a Bubbles progress bar. Between events it extrapolates the
// position from the playback speed, so the bar moves every tick.
type nowBar struct {
	enabled bool // companion app set up
	state   string
	now     *receiver.Now
	at      time.Time // when now was received
	bar     progress.Model
}

func newNowBar(enabled bool) nowBar {
	bar := progress.New(progress.WithGradient("#5fafff", "#87d7af"), progress.WithoutPercentage())
	state := "not set up"
	if enabled {
		state = "offline"
	}
	return nowBar{enabled: enabled, state: state, bar: bar}
}

func (n *nowBar) set(now *receiver.Now) {
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
	return lipgloss.NewStyle().Foreground(warn).Render("●"), "receiver not set up · tv setup"
}

func (n *nowBar) View(w int) string {
	empty := styleBarOff.Render("  " + strings.Repeat("╌", max(0, w-4)))
	if !n.enabled {
		return styleDim.Render("  now-playing, screenshots and local files need the companion app: tv setup") + "\n" + empty
	}
	cur := n.now
	if cur == nil || cur.Source == nil {
		fg := ""
		if cur != nil && cur.Foreground != nil {
			fg = "  ·  " + shortApp(*cur.Foreground)
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
	left := "  " + symStyle.Render(sym) + " " + styleAccent.Render(shortApp(cur.App)) + "   "
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

func shortApp(pkg string) string {
	known := map[string]string{
		"com.netflix.ninja":                 "Netflix",
		"com.amazon.firetv.youtube":         "YouTube",
		"com.spotify.tv.android":            "Spotify",
		"com.amazon.firebat":                "Prime Video",
		"com.amazon.cloud9":                 "Silk",
		"com.amazon.venezia":                "Appstore",
		"com.spocky.projengmenu":            "Projectivy",
		"com.amazon.tv.launcher":            "Fire TV Home",
		"dev.tvctl.receiver":                "tvctl player",
		"com.onemainstream.sonyliv.android": "Sony LIV",
	}
	if n, ok := known[pkg]; ok {
		return n
	}
	return pkg
}
