// Package firetv drives Amazon Fire TV devices over the same local HTTPS API
// the official Fire TV phone app uses. No ADB anywhere in this package.
//
// Protocol summary (learned from the TV's own remote service and the
// community write-ups it matches):
//
//   - POST http://tv:8009/apps/FireTVRemote     starts the API server (DIAL)
//   - HTTPS on :8080, self-signed cert, so we skip verification
//   - Every request carries x-api-key (a constant baked into the phone app),
//     a User-Agent the server allowlists, and after pairing x-client-token.
//   - POST /v1/FireTV/pin/display {"friendlyName"}   show PIN on screen
//   - POST /v1/FireTV/pin/verify  {"pin"}            -> {"description": token}
//   - POST /v1/FireTV?action=<key>                   remote keys
//   - POST /v1/media?action=play|scan                media keys
//   - GET  /v1/FireTV/appsV2                         installed apps
//   - POST /v1/FireTV/app/<package>                  launch app
//   - POST /v1/FireTV/keyboard {"text"}              type into focused field
//   - GET  /v1/FireTV/keyboard                       keyboard state
//   - GET  /v1/FireTV/properties                     device facts
package firetv

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/spinalshock/tvctl/internal/device"
	"github.com/spinalshock/tvctl/internal/netx"
	"github.com/spinalshock/tvctl/internal/wol"
)

const (
	apiPort = 8080
	// apiKey is not a secret: it identifies "the Fire TV app" to the TV.
	// The per-pairing client token is the actual credential.
	apiKey = "0987654321"
	// userAgent must match what the TV allowlists; a custom UA returns
	// "Request uri is unauthorized" on every control route.
	userAgent = "okhttp/4.10.0"
	brand     = "firetv"
)

// Client is one Fire TV. Zero Token means "not paired yet": only the pin
// routes work in that state.
type Client struct {
	Host   string
	Name   string
	Serial string
	MAC    string
	Token  string
	http   *http.Client

	companionToken string // the companion app's pairing token, if installed
}

// New builds a client. The TLS config skips verification because the TV
// presents a self-signed certificate for its LAN IP; there is nothing to
// verify against. Pairing (the PIN) is what authenticates the TV to us.
func New(host, name, token string) *Client {
	return &Client{
		Host:  host,
		Name:  name,
		Token: token,
		http: &http.Client{
			Timeout: 8 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
				// The server speaks HTTP/1.1 only; h2 upgrade returns 400.
				ForceAttemptHTTP2: false,
				TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
			},
		},
	}
}

func (c *Client) Info() device.Info {
	id := c.Serial
	if id == "" {
		id = c.Host
	}
	return device.Info{ID: id, Name: c.Name, Brand: brand, Host: c.Host, MAC: c.MAC}
}

// name is what errors call this TV: its name, else its address.
func (c *Client) name() string {
	if c.Name != "" {
		return c.Name
	}
	return c.Host
}

// Asleep is one TCP probe of the remote service. Fire TV stops that
// service when idle even while on, so "asleep" here means "needs the
// DIAL kick and wake key that Wake sends", which is harmless when the TV
// was merely idle.
func (c *Client) Asleep(context.Context) bool {
	return !netx.PortOpen(net.JoinHostPort(c.Host, fmt.Sprint(apiPort)))
}

func (c *Client) base() string {
	return "https://" + net.JoinHostPort(c.Host, fmt.Sprint(apiPort))
}

// ensureServer makes sure :8080 is accepting connections, kicking it through
// DIAL if not. We probe with a raw TCP dial rather than an HTTP call because
// it's cheaper and we don't want a request counted before auth headers are
// set. Ten half-second polls is generous; the service usually appears in
// under a second.
func (c *Client) ensureServer(ctx context.Context) error {
	addr := net.JoinHostPort(c.Host, fmt.Sprint(apiPort))
	if netx.PortOpen(addr) {
		return nil
	}
	if err := dialLaunch(ctx, c.Host, "FireTVRemote", ""); err != nil {
		return device.Failf("%s is off or unreachable", c.name()).Note(c.Host).Because(err)
	}
	for i := 0; i < 10; i++ {
		if netx.PortOpen(addr) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return device.Failf("%s's remote service didn't start", c.name())
}

// apiError is what the TV returns for every failure: {"description": "..."}.
type apiError struct {
	Description string `json:"description"`
}

type passiveKey struct{}

// Passive marks ctx so calls made with it never kick the remote service.
// Kicking is a DIAL launch, and Fire TV treats a DIAL launch as a reason
// to wake up. A user pressing a key may want that; a background poll must
// never cause it, or a TV switched off with the remote keeps coming back.
func Passive(ctx context.Context) context.Context {
	return context.WithValue(ctx, passiveKey{}, true)
}

// do is the single choke point for authenticated calls. It guarantees the
// server is up (unless the context is Passive), stamps the three headers,
// and turns non-2xx into a readable error carrying the TV's own description.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	if ctx.Value(passiveKey{}) != nil {
		if !netx.PortOpen(net.JoinHostPort(c.Host, fmt.Sprint(apiPort))) {
			return device.ErrAsleep
		}
	} else if err := c.ensureServer(ctx); err != nil {
		return err
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("User-Agent", userAgent)
	// Content-Type with charset is required on media routes, harmless on
	// the rest; omitting the charset makes the scan action return
	// "Bad arguments supplied".
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if c.Token != "" {
		req.Header.Set("x-client-token", c.Token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e apiError
		_ = json.Unmarshal(raw, &e)
		if e.Description == "" {
			e.Description = http.StatusText(resp.StatusCode)
		}
		if resp.StatusCode == 403 && c.Token == "" {
			return device.Failf("not paired with %s", c.name()).Note("run tv pair")
		}
		return device.Fail(e.Description).Notef("%s %s, HTTP %d", method, path, resp.StatusCode)
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Pair runs the on-screen PIN handshake interactively: ShowPIN, then ask()
// for what the user read, then VerifyPIN. Hosts without a user at hand
// (the TUI, a voice assistant) call the two halves as separate commands.
func (c *Client) Pair(ctx context.Context, ask func(prompt string) (string, error)) (string, error) {
	if err := c.ShowPIN(ctx); err != nil {
		return "", err
	}
	pin, err := ask("PIN shown on the TV")
	if err != nil {
		return "", err
	}
	return c.VerifyPIN(ctx, pin)
}

// ShowPIN asks the TV to display a 4-digit PIN. friendlyName is what the
// dialog shows ("To pair with <name>...").
func (c *Client) ShowPIN(ctx context.Context) error {
	c.Token = ""
	return c.do(ctx, http.MethodPost, "/v1/FireTV/pin/display", map[string]string{"friendlyName": friendlyName()}, nil)
}

// VerifyPIN completes pairing. It is a plain request with no session to
// keep open, so it may run in a different process from ShowPIN. On success
// the token is stored on the client and returned for the config store.
func (c *Client) VerifyPIN(ctx context.Context, pin string) (string, error) {
	c.Token = ""
	var res apiError // reuse: success also comes back as {"description": token}
	if err := c.do(ctx, http.MethodPost, "/v1/FireTV/pin/verify", map[string]string{"pin": pin}, &res); err != nil {
		return "", err
	}
	if res.Description == "" || len(res.Description) < 8 {
		return "", device.Fail("pairing rejected").Notef("%q", res.Description)
	}
	c.Token = res.Description
	return c.Token, nil
}

// keyActions maps the neutral vocabulary to the TV's query-string names.
// play/ff/rw go through the media route and are handled in Key directly.
var keyActions = map[device.Key]string{
	device.KeyHome:       "home",
	device.KeyBack:       "back",
	device.KeyMenu:       "menu",
	device.KeySelect:     "select",
	device.KeyUp:         "dpad_up",
	device.KeyDown:       "dpad_down",
	device.KeyLeft:       "dpad_left",
	device.KeyRight:      "dpad_right",
	device.KeyVolumeUp:   "volume_up",
	device.KeyVolumeDown: "volume_down",
	device.KeyMute:       "mute",
	device.KeySleep:      "sleep",
	device.KeyWake:       "wakeup",
}

// Key sends one key press. Media transport keys use a different route and,
// for scan (ff/rw), a body describing the direction and a key-down event;
// the TV treats a single keyDown as one "skip" step.
func (c *Client) Key(ctx context.Context, k device.Key) error {
	switch k {
	case device.KeyPlayPause:
		return c.do(ctx, http.MethodPost, "/v1/media?action=play", nil, nil)
	case device.KeyFastFwd, device.KeyRewind:
		dir := "forward"
		if k == device.KeyRewind {
			dir = "back"
		}
		body := map[string]any{"direction": dir, "keyAction": map[string]string{"keyActionType": "keyDown"}}
		return c.do(ctx, http.MethodPost, "/v1/media?action=scan", body, nil)
	}
	a, ok := keyActions[k]
	if !ok {
		return device.Failf("%s has no %s key", c.name(), k)
	}
	return c.do(ctx, http.MethodPost, "/v1/FireTV?action="+a, nil, nil)
}

// Text types a whole string into whatever field has focus on the TV.
// The route is /keyboard; /text exists but rejects every body shape.
func (c *Client) Text(ctx context.Context, s string) error {
	return c.do(ctx, http.MethodPost, "/v1/FireTV/keyboard", map[string]string{"text": s}, nil)
}

// Keyboard is the TV's on-screen keyboard state as the API reports it.
// State is "hidden" or a visible state; Text is the field's current
// contents when known.
type Keyboard struct {
	Mode  *string `json:"mode"`
	State string  `json:"state"`
	Text  *string `json:"text"`
}

// KeyboardState polls the keyboard; cheap enough to call every second.
func (c *Client) KeyboardState(ctx context.Context) (*Keyboard, error) {
	var k Keyboard
	if err := c.do(ctx, http.MethodGet, "/v1/FireTV/keyboard", nil, &k); err != nil {
		return nil, err
	}
	return &k, nil
}

// appV2 is the TV's app record; we keep only what the UI needs.
type appV2 struct {
	AppID       string `json:"appId"`
	Name        string `json:"name"`
	IsInstalled bool   `json:"isInstalled"`
}

func (c *Client) Apps(ctx context.Context) ([]device.App, error) {
	var raw []appV2
	if err := c.do(ctx, http.MethodGet, "/v1/FireTV/appsV2", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]device.App, 0, len(raw))
	for _, a := range raw {
		out = append(out, device.App{ID: a.AppID, Name: a.Name, Installed: a.IsInstalled})
	}
	return out, nil
}

// Launch starts an app by package name. Sideloaded apps work too even though
// appsV2 only lists store apps; the route is a plain package launch.
func (c *Client) Launch(ctx context.Context, appID string) error {
	return c.do(ctx, http.MethodPost, "/v1/FireTV/app/"+appID, nil, nil)
}

// Status returns the properties document: device type, serial (dsn),
// OS build, and which optional features the TV claims to support.
func (c *Client) Status(ctx context.Context) (map[string]any, error) {
	var m map[string]any
	if err := c.do(ctx, http.MethodGet, "/v1/FireTV/properties", nil, &m); err != nil {
		return nil, err
	}
	if dsn, _ := m["dsn"].(string); dsn != "" {
		c.Serial = dsn
	}
	return m, nil
}

// Wake sends a Wake-on-LAN magic packet when we know the MAC (only useful if
// the user enabled WoL on the TV), then kicks the service and sends the
// protocol-level wakeup key, which is what actually turns the screen on
// from network standby.
func (c *Client) Wake(ctx context.Context) error {
	if c.MAC != "" {
		_ = wol.Send(c.MAC)
	}
	if err := c.ensureServer(ctx); err != nil {
		return err
	}
	return c.Key(ctx, device.KeyWake)
}

// KeyboardOpen implements device.KeyboardWatcher. It is a status probe,
// polled by the TUI, so it is always passive: it reports ErrAsleep rather
// than waking the TV to ask.
func (c *Client) KeyboardOpen(ctx context.Context) (bool, string, error) {
	k, err := c.KeyboardState(Passive(ctx))
	if err != nil {
		return false, "", err
	}
	cur := ""
	if k.Text != nil {
		cur = *k.Text
	}
	return k.State != "" && k.State != "hidden", cur, nil
}
