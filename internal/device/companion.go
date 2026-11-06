package device

import (
	"context"
	"encoding/json"
	"errors"
	"io"
)

// What follows are capabilities a TV's own API rarely has and an add-on
// running on the TV can provide: a rich now-playing document for any app,
// an event stream, transport control, the focused text field, overlays,
// full-screen pages, screenshots, a home dashboard, playing a URL in a
// native player. On Android-based TVs the companion app (receiver/ in this
// repo) provides them; a driver that has it composes these interfaces in.
// Callers assert them like any other optional capability and never know
// where the answer comes from.

// Playing is the rich now-playing document: what app is in front, what is
// playing and where it is. Pointers are "unknown"; Source nil means no
// media session at all. Position is a snapshot: extrapolate with Speed and
// UpdatedMs for a ticking clock.
type Playing struct {
	Foreground *string `json:"foreground"`
	Source     *string `json:"source"`
	App        string  `json:"app,omitempty"`
	Title      *string `json:"title,omitempty"`
	Artist     *string `json:"artist,omitempty"`
	Album      *string `json:"album,omitempty"`
	State      string  `json:"state"`
	PositionMs int64   `json:"position_ms"`
	DurationMs int64   `json:"duration_ms"`
	Speed      float64 `json:"speed"`
	UpdatedMs  int64   `json:"updated_ms"`
}

// Player reports what is playing, for any app, with title and position.
type Player interface {
	Playing(ctx context.Context) (*Playing, error)
}

// Event is one message from a TV's event stream: a now-playing change, a
// foreground-app change, or the keyboard opening and closing.
type Event struct {
	Type    string          `json:"type"`
	Now     *Playing        `json:"now,omitempty"`
	Package *string         `json:"package,omitempty"`
	Open    *bool           `json:"open,omitempty"`
	Raw     json.RawMessage `json:"-"`
}

// EventSource streams events until ctx ends or the link drops.
type EventSource interface {
	Events(ctx context.Context, fn func(Event) error) error
}

// ErrNothingPlaying is Control's answer when no media session can take
// the action; callers fall back to the remote's media keys.
var ErrNothingPlaying = errors.New("nothing is playing that can be controlled")

// Transport drives the active media session of whatever app is playing:
// play, pause, toggle, stop, next, previous, seek (absolute ms), seek_by
// (relative ms, negative to rewind).
type Transport interface {
	Control(ctx context.Context, action string, positionMs int64) error
}

// Field is the TV's focused text field. Prompt and Step come from
// keyboards that title and count their fields; Key changes when focus
// moves to another field; Text is empty while the field shows its hint,
// and always for passwords.
type Field struct {
	Open     bool   `json:"open"`
	Prompt   string `json:"prompt"`
	Step     string `json:"step"`
	Hint     string `json:"hint"`
	Text     string `json:"text"`
	Password bool   `json:"password"`
	Key      string `json:"key"`
}

// FieldReader reads the focused text field, which a remote protocol's
// keyboard poll can't (it can't tell a placeholder from text).
type FieldReader interface {
	Field(ctx context.Context) (*Field, error)
}

// Panel styles the on-screen text overlay. Zero values mean "leave as is"
// so a stream of appends doesn't keep restating the look.
type Panel struct {
	Title    string  // header line; "" hides it (only sent when HasTitle)
	HasTitle bool    // Title is meaningful even when empty
	Pos      string  // bottom (default), top, left, right, full
	MaxLines int     // rows kept; older lines scroll off
	Opacity  float64 // panel background, 0..1
	Size     float64 // text size
	Width    float64 // fraction of the screen for left/right panels
}

// Overlayer pins text over whatever is on screen.
type Overlayer interface {
	Overlay(ctx context.Context, lines []string, appendMode bool, p Panel) error
	OverlayHide(ctx context.Context) error
}

// Dasher shows a full-screen web page: HTML pushed as-is (base is what
// relative fetches resolve against) or a URL the TV loads itself.
type Dasher interface {
	Dash(ctx context.Context, html, url, base string) error
	DashJS(ctx context.Context, js string) error
	DashClose(ctx context.Context) error
}

// Screenshotter returns the screen as PNG.
type Screenshotter interface {
	Screenshot(ctx context.Context) ([]byte, error)
}

// Tile is one tile on the home dashboard: an app (by id) or an input
// ("hdmi1".."hdmi4"). Label and Installed are filled in by the TV.
type Tile struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Label     string `json:"label,omitempty"`
	Installed bool   `json:"installed,omitempty"`
}

// Dashboard is the home dashboard's settings on the TV.
type Dashboard struct {
	Enabled   bool   `json:"enabled"`
	Tiles     []Tile `json:"tiles"`
	Wallpaper bool   `json:"wallpaper"`
}

// HomeDashboard manages a home screen of tiles that replaces the TV's own.
type HomeDashboard interface {
	Home(ctx context.Context) (*Dashboard, error)
	SetHome(ctx context.Context, enabled *bool, tiles []Tile) (*Dashboard, error)
	SetWallpaper(ctx context.Context, img []byte) (*Dashboard, error)
	ClearWallpaper(ctx context.Context) (*Dashboard, error)
}

// MediaPlayer plays a URL (a file served from this machine) in a native
// player. PlayerApp is the app id that player reports in Playing, so a
// playlist can tell its own items ending from the viewer leaving.
type MediaPlayer interface {
	PlayURL(ctx context.Context, url, title string) error
	PlayerApp() string
}

// Installer puts the companion add-on on the TV and returns its pairing
// token, to be kept with the device's config. Only platforms that can
// take one implement it.
type Installer interface {
	InstallCompanion(ctx context.Context, out io.Writer, ask func(prompt string) (string, error)) (token string, err error)
}
