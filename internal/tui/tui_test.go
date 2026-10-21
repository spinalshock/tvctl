package tui

import (
	"strings"
	"testing"

	"github.com/omAthalye/tvctl/internal/cli"
)

// TestResolve pins the precedence rule: verb, then URL, then app.
func TestResolve(t *testing.T) {
	m := &model{verbs: cli.Verbs()}
	cases := map[string][]string{
		"vol up 3":              {"vol", "up", "3"},
		"netflix":               {"app", "netflix"},
		"https://youtu.be/x":    {"url", "https://youtu.be/x"},
		"youtube.com/watch?v=x": {"url", "youtube.com/watch?v=x"},
		"say switch to hdmi 1":  {"say", "switch", "to", "hdmi", "1"},
		"Sony LIV":              {"app", "Sony LIV"},
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
	for _, in := range []string{"help", "keys", "quit", "clear"} {
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
