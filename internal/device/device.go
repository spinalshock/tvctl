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
	KeyStop       Key = "stop"
)

// AllKeys is the canonical list, used for completion and `help`.
var AllKeys = []Key{
	KeyHome, KeyBack, KeyMenu, KeySelect, KeyUp, KeyDown, KeyLeft, KeyRight,
	KeyPlayPause, KeyFastFwd, KeyRewind, KeyVolumeUp, KeyVolumeDown, KeyMute,
	KeySleep, KeyWake, KeyStop,
}

// App is an installed application as the TV reports it.
type App struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
}

// Info identifies a device for display and for the config store.
type Info struct {
	ID    string `json:"id"`            // stable per TV (Fire TV: device serial)
	Name  string `json:"name"`          // human name the TV advertises
	Brand string `json:"brand"`         // "firetv", "webos", later "roku", "tizen", …
	Host  string `json:"host"`          // IP or hostname
	MAC   string `json:"mac,omitempty"` // when the driver learned it; drives Wake-on-LAN
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

// PINPairer is a Pairer whose handshake is two independent requests: one
// that puts a PIN on the TV, one that sends it back. Nothing needs to stay
// open in between, so a host with no user at the keyboard can run them as
// two separate commands (`tv pair`, then `tv pin 1234`). Fire TV is one.
type PINPairer interface {
	Pairer
	ShowPIN(ctx context.Context) error
	VerifyPIN(ctx context.Context, pin string) (token string, err error)
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

// MACLearner asks the TV for its own hardware address, so Wake-on-LAN
// works for a TV paired before the address was recorded. The CLI falls
// back to the local ARP cache for drivers without it.
type MACLearner interface {
	LearnMAC(ctx context.Context) string
}

// Sleeper tells, cheaply and without waking anything, whether the TV is
// in a state Wake would fix. Verbs that change something on the TV check
// this first and wake it, so "play X" works on a TV that is off.
type Sleeper interface {
	Asleep(ctx context.Context) bool
}

// KeyboardWatcher reports whether the TV is currently asking for text.
type KeyboardWatcher interface {
	KeyboardOpen(ctx context.Context) (open bool, current string, err error)
}

// Statuser returns brand-specific diagnostics for `tv status --json`.
type Statuser interface {
	Status(ctx context.Context) (map[string]any, error)
}

// Now is what the TV is playing, as far as its own API tells: the
// foreground app and a coarse state. Drivers with richer data (the
// companion app) have their own documents; this is the brand-neutral floor.
// The app's display name is the caller's to resolve (AppNamer, app list).
type Now struct {
	App   string `json:"app"`             // app id, or "" when nothing is known
	State string `json:"state,omitempty"` // "playing", "paused", "" (unknown)
}

// Nower reports the foreground app and playback state without the
// companion app.
type Nower interface {
	Now(ctx context.Context) (Now, error)
}

// Input is one external input (HDMI port, tuner, …) as the TV names it.
type Input struct {
	ID        string `json:"id"`    // the TV's own id ("HDMI_1")
	Name      string `json:"name"`  // short name users say: "hdmi1"
	Label     string `json:"label"` // the owner's label on the TV ("PS5")
	Connected bool   `json:"connected"`
}

// InputSwitcher lists and switches external inputs directly, as opposed
// to asking the TV's voice assistant to do it. SwitchInput takes an ID
// exactly as Inputs returned it; matching what people say against names
// and labels is the caller's job, done once for every brand.
type InputSwitcher interface {
	Inputs(ctx context.Context) ([]Input, error)
	SwitchInput(ctx context.Context, id string) error
}

// AppNamer is implemented by drivers that know apps beyond what the TV's
// own list says: the id behind a well-known name ("YouTube" is one package
// on Fire TV and another on webOS) and the name behind an id the list
// omits (system apps). Callers ask here first, then the live app list.
type AppNamer interface {
	AppID(name string) (id string, ok bool)
	AppName(id string) string // "" when unknown
}

// ToastOpts is how long and where a toast shows; TVs that can't choose
// ignore it.
type ToastOpts struct {
	Long bool
	Pos  string // "top-right" (default), "bottom-left", "top", …; "" default
}

// Toaster shows a short notification over whatever is on screen.
type Toaster interface {
	Toast(ctx context.Context, text string, o ToastOpts) error
}
