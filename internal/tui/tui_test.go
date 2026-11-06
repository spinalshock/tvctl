package tui

import (
	"strings"
	"testing"

	"github.com/spinalshock/tvctl/internal/cli"
	"github.com/spinalshock/tvctl/internal/device"
)

// TestResolve pins the precedence rule: built-in, alias (bare opens, with
// words searches unless a verb owns the word), verb, link, app.
func TestResolve(t *testing.T) {
	m := &model{verbs: cli.Verbs()}
	cases := map[string][]string{
		"vol up 3":              {"vol", "up", "3"},
		"netflix":               {"open", "netflix"},
		"netflix dark":          {"search", "netflix", "dark"},
		"prime the boys":        {"search", "prime", "the", "boys"},
		"youtube lofi beats":    {"youtube", "lofi", "beats"},
		"YouTube lofi":          {"youtube", "lofi"},
		"https://youtu.be/x":    {"open", "https://youtu.be/x"},
		"youtube.com/watch?v=x": {"open", "youtube.com/watch?v=x"},
		"say switch to hdmi 1":  {"say", "switch", "to", "hdmi", "1"},
		"Sony LIV":              {"open", "Sony LIV"},
		"app netflix":           {"app", "netflix"},
		"wallpaper off":         {"home", "wallpaper", "off"},
		"home":                  {"key", "home"},
		"home on":               {"home", "on"},
	}
	for in, want := range cases {
		got, local := m.resolve(in)
		if local != nil {
			t.Fatalf("%q handled locally, want argv %v", in, want)
		}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Errorf("%q -> %v, want %v", in, got, want)
		}
	}
	for _, in := range []string{"help", "keys", "quit", "clear", "wallpaper"} {
		if _, local := m.resolve(in); local == nil {
			t.Errorf("%q should be handled locally", in)
		}
	}
}

func TestFuzzy(t *testing.T) {
	c := []string{"netflix", "now", "youtube", "volume_up", "vol", "Sony LIV"}
	if got := fuzzyRank("n", c, 6); len(got) == 0 || got[0].text != "netflix" && got[0].text != "now" {
		t.Errorf("n -> %v", got)
	}
	if got := fuzzyRank("vu", c, 6); len(got) == 0 || got[0].text != "volume_up" {
		t.Errorf("vu -> %v", got)
	}
	if got := fuzzyRank("zzz", c, 6); len(got) != 0 {
		t.Errorf("zzz matched %v", got)
	}
}

// TestNowBar checks the progress bar draws from the extrapolated position
// and that an unknown duration falls back to the dashed "elapsed" line.
func TestNowBar(t *testing.T) {
	title := "Taambdi Chaamdi"
	src := "session"
	n := newNowBar(true, "")
	n.name = func(id string) string { // the driver's job in the real app
		return map[string]string{"com.amazon.firetv.youtube": "YouTube", "com.netflix.ninja": "Netflix"}[id]
	}
	n.set(&device.Playing{App: "com.amazon.firetv.youtube", Source: &src, Title: &title,
		State: "playing", Speed: 1, PositionMs: 60_000, DurationMs: 240_000})
	v := n.View(80)
	if !strings.Contains(v, "YouTube") || !strings.Contains(v, title) {
		t.Fatalf("missing app/title: %q", v)
	}
	if !strings.Contains(v, "1:00 / 4:00") {
		t.Errorf("clock: %q", v)
	}
	if !strings.Contains(v, "█") {
		t.Errorf("no bar drawn: %q", v)
	}
	n.set(&device.Playing{App: "com.netflix.ninja", Source: &src, State: "playing", Speed: 1, PositionMs: 5_000})
	v = n.View(80)
	if !strings.Contains(v, "elapsed") || !strings.Contains(v, "╌") || !strings.Contains(v, "no title") {
		t.Errorf("unknown duration: %q", v)
	}
}
