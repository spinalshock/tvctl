// Package device defines the brand-independent contract that the CLI and TUI
// program against. Every TV brand lives in its own package and implements as
// many of these interfaces as its protocol allows. Callers type-assert for the
// optional ones ("does this device speak?") instead of checking a capability
// flag, which is the idiomatic Go way to express "may support".
package device

import "context"

// Key is a remote-control key in brand-neutral vocabulary. Each driver maps
// these onto whatever its protocol calls them (Fire TV: "dpad_up", Roku:
// "Up", webOS: "UP"). The string values double as the words users type.
type Key string

const (
	KeyHome       Key = "home"
	KeyBack       Key = "back"
	KeyMenu       Key = "menu"
	KeySelect     Key = "select"
	KeyUp         Key = "up"
	KeyDown       Key = "down"
	KeyLeft       Key = "left"
	KeyRight      Key = "right"
	KeyPlayPause  Key = "play"
	KeyFastFwd    Key = "ff"
	KeyRewind     Key = "rw"
	KeyVolumeUp   Key = "volume_up"
	KeyVolumeDown Key = "volume_down"
	KeyMute       Key = "mute"
	KeySleep      Key = "sleep"
	KeyWake       Key = "wake"
)

// AllKeys is the canonical list, used for completion and `help`.
var AllKeys = []Key{
	KeyHome, KeyBack, KeyMenu, KeySelect, KeyUp, KeyDown, KeyLeft, KeyRight,
	KeyPlayPause, KeyFastFwd, KeyRewind, KeyVolumeUp, KeyVolumeDown, KeyMute,
	KeySleep, KeyWake,
}

// App is an installed application as the TV reports it.
type App struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
}

// Info identifies a device for display and for the config store.
type Info struct {
	ID    string `json:"id"`    // stable per TV (Fire TV: device serial)
	Name  string `json:"name"`  // human name the TV advertises
	Brand string `json:"brand"` // "firetv", later "roku", "webos", ...
	Host  string `json:"host"`  // IP or hostname
}

// Device is the minimum every driver must provide.
type Device interface {
	Info() Info
	Key(ctx context.Context, k Key) error
	Text(ctx context.Context, s string) error
	Apps(ctx context.Context) ([]App, error)
	Launch(ctx context.Context, appID string) error
}

// Pairer is implemented by drivers that need a one-time handshake. ask is
// called when the TV shows something the user must read back (a PIN); the
// CLI answers from stdin, the TUI from a modal. The returned token is opaque
// to callers and is persisted by the config store.
type Pairer interface {
	Pair(ctx context.Context, ask func(prompt string) (string, error)) (token string, err error)
}

// Speaker is implemented by drivers that can deliver a spoken phrase to the
// TV's voice assistant (Fire TV's Alexa channel).
type Speaker interface {
	Say(ctx context.Context, phrase string) error
}

// URLOpener opens a web URL in the matching TV app (YouTube link -> YouTube).
type URLOpener interface {
	OpenURL(ctx context.Context, url string) error
}

// Waker brings a sleeping TV back; usually Wake-on-LAN plus a protocol nudge.
type Waker interface {
	Wake(ctx context.Context) error
}

// Statuser returns brand-specific diagnostics for `tv status --json`.
type Statuser interface {
	Status(ctx context.Context) (map[string]any, error)
}
