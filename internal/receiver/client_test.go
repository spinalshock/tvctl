package receiver

import (
	"strings"
	"testing"

	"github.com/omAthalye/tvctl/internal/device"
)

func TestDescribe(t *testing.T) {
	if Describe(nil) != "idle" {
		t.Error("nil")
	}
	fg := "com.netflix.ninja"
	if got := Describe(&device.Playing{Foreground: &fg}); got != "idle · com.netflix.ninja" {
		t.Errorf("foreground only: %q", got)
	}
	src, title, artist := "session", "Song", "Band"
	got := Describe(&device.Playing{Source: &src, App: "com.spotify.tv.android", Title: &title, Artist: &artist,
		State: "playing", PositionMs: 65_000, DurationMs: 3_600_000})
	for _, want := range []string{"▶", "Spotify", "Band — Song", "1:05/1:00:00"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}
