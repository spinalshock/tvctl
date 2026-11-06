package firetv

import (
	"context"

	"github.com/spinalshock/tvctl/internal/android"
	"github.com/spinalshock/tvctl/internal/device"
	"github.com/spinalshock/tvctl/internal/receiver"
	"github.com/spinalshock/tvctl/internal/youtube"
)

// withCompanion is a Fire TV that has the companion app installed: the
// stock client plus everything the app adds. The driver returns this type
// when the entry carries a companion token, so a capability assertion on
// the device tells the truth about this TV today. Where the app and the
// stock API overlap (apps, links, now-playing) the app wins, and the
// fallback lives here, once.
type withCompanion struct {
	*Client
	rc *receiver.Client
}

// foreground is the basic-mode fallback: when the app parks a request
// because it can't start activities from the background, the TV's own
// API can bring it forward.
func (w *withCompanion) foreground(ctx context.Context) func(needs bool, err error) error {
	return func(needs bool, err error) error {
		if err != nil {
			return err
		}
		if needs {
			return w.Client.Launch(ctx, receiver.Package)
		}
		return nil
	}
}

func (w *withCompanion) Playing(ctx context.Context) (*device.Playing, error) {
	return w.rc.Playing(ctx)
}

// Now is the coarse view of Playing: the app in front and its state.
func (w *withCompanion) Now(ctx context.Context) (device.Now, error) {
	p, err := w.rc.Playing(ctx)
	if err != nil {
		return device.Now{}, err
	}
	n := device.Now{App: p.App}
	if n.App == "" && p.Foreground != nil {
		n.App = *p.Foreground
	}
	if p.Source != nil {
		n.State = p.State
	}
	return n, nil
}

func (w *withCompanion) Events(ctx context.Context, fn func(device.Event) error) error {
	return w.rc.Events(ctx, fn)
}
func (w *withCompanion) Control(ctx context.Context, action string, positionMs int64) error {
	return w.rc.Control(ctx, action, positionMs)
}
func (w *withCompanion) Field(ctx context.Context) (*device.Field, error) { return w.rc.Field(ctx) }
func (w *withCompanion) Toast(ctx context.Context, text string, o device.ToastOpts) error {
	return w.rc.Toast(ctx, text, o)
}
func (w *withCompanion) Overlay(ctx context.Context, lines []string, appendMode bool, p device.Panel) error {
	return w.rc.Overlay(ctx, lines, appendMode, p)
}
func (w *withCompanion) OverlayHide(ctx context.Context) error { return w.rc.OverlayHide(ctx) }
func (w *withCompanion) Dash(ctx context.Context, html, link, base string) error {
	return w.foreground(ctx)(w.rc.Dash(ctx, html, link, base))
}
func (w *withCompanion) DashJS(ctx context.Context, js string) error { return w.rc.DashJS(ctx, js) }
func (w *withCompanion) DashClose(ctx context.Context) error         { return w.rc.DashClose(ctx) }
func (w *withCompanion) Screenshot(ctx context.Context) ([]byte, error) {
	return w.rc.Screenshot(ctx)
}
func (w *withCompanion) Home(ctx context.Context) (*device.Dashboard, error) { return w.rc.Home(ctx) }
func (w *withCompanion) SetHome(ctx context.Context, enabled *bool, tiles []device.Tile) (*device.Dashboard, error) {
	return w.rc.SetHome(ctx, enabled, tiles)
}
func (w *withCompanion) SetWallpaper(ctx context.Context, img []byte) (*device.Dashboard, error) {
	return w.rc.SetWallpaper(ctx, img)
}
func (w *withCompanion) ClearWallpaper(ctx context.Context) (*device.Dashboard, error) {
	return w.rc.ClearWallpaper(ctx)
}

// PlayURL plays a served file in the app's native player.
func (w *withCompanion) PlayURL(ctx context.Context, link, title string) error {
	return w.foreground(ctx)(w.rc.Play(ctx, link, title))
}
func (w *withCompanion) PlayerApp() string { return receiver.Package }

// Apps merges the TV's own list with the launcher's, which also sees
// built-ins (Prime Video, Appstore, Gallery). A launcher failure is not
// an error: the TV list still stands.
func (w *withCompanion) Apps(ctx context.Context) ([]device.App, error) {
	apps, err := w.Client.Apps(ctx)
	if err != nil {
		return nil, err
	}
	more, err := w.rc.Apps(ctx)
	if err != nil {
		return apps, nil
	}
	seen := make(map[string]bool, len(apps))
	for _, a := range apps {
		seen[a.ID] = true
	}
	for _, a := range more {
		if !seen[a.ID] {
			apps = append(apps, a)
		}
	}
	return apps, nil
}

// OpenURL hands a link to Android's own URL routing through the app, so
// Netflix titles deep-link and web pages open in the browser. YouTube
// links are steered to the YouTube app tvctl uses.
func (w *withCompanion) OpenURL(ctx context.Context, link string) error {
	pkg := ""
	if id, _ := youtube.VideoID(link); id != "" {
		pkg, _ = android.Package("youtube")
	}
	return w.foreground(ctx)(w.rc.Open(ctx, link, pkg))
}
