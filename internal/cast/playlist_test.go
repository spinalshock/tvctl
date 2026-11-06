package cast

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/omAthalye/tvctl/internal/device"
)

type fakePlayer struct{ urls []string }

func (f *fakePlayer) PlayURL(_ context.Context, url, _ string) error {
	f.urls = append(f.urls, url)
	return nil
}
func (f *fakePlayer) PlayerApp() string { return "dev.fake.player" }

func TestExpandAndFinished(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"b.mp4", "a.mkv", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := Expand([]string{dir})
	if err != nil || len(files) != 2 || filepath.Base(files[0]) != "a.mkv" {
		t.Fatalf("Expand: %v %v", files, err)
	}
	if _, err := Expand([]string{filepath.Join(dir, "notes.txt"), dir}); err != nil {
		t.Fatalf("a named file always counts: %v", err)
	}
	if _, err := Expand([]string{t.TempDir()}); err == nil {
		t.Fatal("empty dir should be an error")
	}

	p := &Playlist{Files: files, mp: &fakePlayer{}, grace: 3}
	ours, other := "dev.fake.player", "com.netflix.ninja"
	step := func(n *device.Playing) (bool, bool) { return p.Observe(n) }
	// Grace: early documents are ignored until our player reports playing.
	if a, l := step(&device.Playing{App: other, Foreground: &other}); a || l {
		t.Fatal("grace should swallow the first document")
	}
	if a, l := step(&device.Playing{App: ours, State: "playing"}); a || l {
		t.Fatal("our player starting is not an event")
	}
	if a, l := step(&device.Playing{App: ours, State: "playing"}); a || l {
		t.Fatal("still playing")
	}
	if a, l := step(&device.Playing{App: ours, State: "stopped"}); !a || l {
		t.Fatal("our player stopping means advance")
	}
	if a, l := step(&device.Playing{App: other, Foreground: &other}); a || !l {
		t.Fatal("someone else in front means the viewer left")
	}
	if a, l := step(&device.Playing{App: other, Foreground: &ours}); a || l {
		t.Fatal("we are still in front, just no session yet")
	}
	if a, l := step(nil); a || l {
		t.Fatal("nil is not an event")
	}
}
