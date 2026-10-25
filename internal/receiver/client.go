// Package receiver is the Go client for the companion app's HTTP API
// (receiver/ in this repo). Plain HTTP on the LAN with an X-Token header.
package receiver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

const Port = 8434

type Client struct {
	Host  string
	Token string
	http  *http.Client
}

func New(host, token string) *Client {
	return &Client{Host: host, Token: token, http: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) base() string { return "http://" + net.JoinHostPort(c.Host, fmt.Sprint(Port)) }

// Status is the unauthenticated capability document.
type Status struct {
	Name    string          `json:"name"`
	Version string          `json:"version"`
	Paired  bool            `json:"paired"`
	Caps    map[string]bool `json:"caps"`
}

// Now is the now-playing document. Position is a snapshot; callers
// extrapolate with Speed and UpdatedMs when they want a ticking clock.
type Now struct {
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

// Event is one message from the /events socket.
type Event struct {
	Type    string          `json:"type"`
	Now     *Now            `json:"now,omitempty"`
	Package *string         `json:"package,omitempty"`
	Open    *bool           `json:"open,omitempty"`
	Raw     json.RawMessage `json:"-"`
}

// Reachable reports whether the receiver answers on its port at all.
func (c *Client) Reachable() bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(c.Host, fmt.Sprint(Port)), 700*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
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
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("X-Token", c.Token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("receiver: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("receiver: %s", e.Error)
	}
	if out != nil {
		if b, ok := out.(*[]byte); ok {
			*b = raw
			return nil
		}
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (c *Client) Status(ctx context.Context) (*Status, error) {
	var s Status
	if err := c.do(ctx, http.MethodGet, "/status", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Pair shows a PIN on the TV, asks for it, and returns the new token.
func (c *Client) Pair(ctx context.Context, ask func(string) (string, error)) (string, error) {
	if err := c.do(ctx, http.MethodPost, "/pair", nil, nil); err != nil {
		return "", err
	}
	pin, err := ask("PIN shown on the TV (tvctl receiver)")
	if err != nil {
		return "", err
	}
	var res struct {
		Token string `json:"token"`
	}
	if err := c.do(ctx, http.MethodPost, "/pair/verify", map[string]string{"pin": pin}, &res); err != nil {
		return "", err
	}
	if res.Token == "" {
		return "", errors.New("pairing returned no token")
	}
	c.Token = res.Token
	return res.Token, nil
}

func (c *Client) Now(ctx context.Context) (*Now, error) {
	var n Now
	if err := c.do(ctx, http.MethodGet, "/now", nil, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// Play opens url in the native player. needsForeground is true when the
// receiver parked the request because it lacks the overlay permission;
// the caller then brings the app forward through the TV's own API.
func (c *Client) Play(ctx context.Context, url, title string) (needsForeground bool, err error) {
	return c.startish(ctx, "/play", map[string]any{"url": url, "title": title})
}

func (c *Client) Open(ctx context.Context, url, pkg string) (bool, error) {
	return c.startish(ctx, "/open", map[string]any{"url": url, "package": pkg})
}

func (c *Client) Launch(ctx context.Context, pkg string) (bool, error) {
	return c.startish(ctx, "/launch", map[string]any{"package": pkg})
}

func (c *Client) startish(ctx context.Context, path string, body any) (bool, error) {
	var res struct {
		NeedsForeground bool `json:"needsForeground"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &res); err != nil {
		return false, err
	}
	return res.NeedsForeground, nil
}

// Toast shows a card for a few seconds. pos is a corner or edge
// ("top-right" default, "bottom-left", "top", …); "" keeps the default.
func (c *Client) Toast(ctx context.Context, text string, long bool, pos string) error {
	return c.do(ctx, http.MethodPost, "/toast", map[string]any{"text": text, "long": long, "pos": pos}, nil)
}

// Control drives the active media session of whatever app is playing:
// play, pause, toggle, stop, next, previous, seek (absolute ms), seek_by
// (relative ms, negative to rewind).
func (c *Client) Control(ctx context.Context, action string, positionMs int64) error {
	return c.do(ctx, http.MethodPost, "/control", map[string]any{"action": action, "position_ms": positionMs}, nil)
}

// Screenshot returns PNG bytes.
func (c *Client) Screenshot(ctx context.Context) ([]byte, error) {
	var png []byte
	if err := c.do(ctx, http.MethodGet, "/shot", nil, &png); err != nil {
		return nil, err
	}
	return png, nil
}

// Events streams until ctx ends or the socket drops. Each message is
// decoded and also kept raw for --json consumers.
func (c *Client) Events(ctx context.Context, fn func(Event) error) error {
	hdr := http.Header{}
	hdr.Set("X-Token", c.Token)
	ws, _, err := websocket.Dial(ctx, "ws://"+net.JoinHostPort(c.Host, fmt.Sprint(Port))+"/events",
		&websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		return fmt.Errorf("receiver events: %w", err)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(1 << 20)
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if string(data) == "pong" {
			continue
		}
		var ev Event
		if err := json.Unmarshal(data, &ev); err != nil {
			continue
		}
		ev.Raw = data
		if err := fn(ev); err != nil {
			return err
		}
	}
}

// Describe renders a now-playing document as one line for status bars.
func (n *Now) Describe() string {
	if n == nil || n.Source == nil {
		if n != nil && n.Foreground != nil {
			return "idle · " + *n.Foreground
		}
		return "idle"
	}
	title := ""
	if n.Title != nil {
		title = *n.Title
	}
	if n.Artist != nil && *n.Artist != "" {
		title = *n.Artist + " — " + title
	}
	pos := clock(n.PositionMs)
	if n.DurationMs > 0 {
		pos += "/" + clock(n.DurationMs)
	}
	sym := map[string]string{"playing": "▶", "paused": "⏸", "buffering": "…", "stopped": "■"}[n.State]
	if sym == "" {
		sym = n.State
	}
	return fmt.Sprintf("%s %s  %s  %s", sym, shortApp(n.App), title, pos)
}

func clock(ms int64) string {
	s := ms / 1000
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func shortApp(pkg string) string {
	known := map[string]string{
		"com.netflix.ninja":         "Netflix",
		"com.amazon.firetv.youtube": "YouTube",
		"com.spotify.tv.android":    "Spotify",
		"com.amazon.avod":           "Prime Video",
		"dev.tvctl.receiver":        "tvctl",
	}
	if n, ok := known[pkg]; ok {
		return n
	}
	return pkg
}

// OverlayOpts styles the on-screen text panel. Zero values mean "leave as
// is" so a stream of appends doesn't keep restating the look.
type OverlayOpts struct {
	Title    string  // header line; "" hides it (only sent when HasTitle)
	HasTitle bool    // Title is meaningful even when empty
	Pos      string  // bottom (default), top, left, right, full
	MaxLines int     // rows kept; older lines scroll off
	Opacity  float64 // panel background, 0..1
	Size     float64 // text size in sp
	Width    float64 // fraction of screen for left/right panels
}

// Overlay shows the panel (creating it if needed) with these lines. With
// appendMode the lines extend the buffer; otherwise they replace it.
func (c *Client) Overlay(ctx context.Context, lines []string, appendMode bool, o OverlayOpts) error {
	body := map[string]any{"lines": lines, "append": appendMode}
	if o.HasTitle {
		body["title"] = o.Title
	}
	if o.Pos != "" {
		body["pos"] = o.Pos
	}
	if o.MaxLines > 0 {
		body["max_lines"] = o.MaxLines
	}
	if o.Opacity > 0 {
		body["opacity"] = o.Opacity
	}
	if o.Size > 0 {
		body["size"] = o.Size
	}
	if o.Width > 0 {
		body["width"] = o.Width
	}
	return c.do(ctx, http.MethodPost, "/overlay", body, nil)
}

// OverlayHide removes the panel.
func (c *Client) OverlayHide(ctx context.Context) error {
	return c.do(ctx, http.MethodDelete, "/overlay", nil, nil)
}

// Dash shows a full-screen page: html rendered as sent (base is the URL
// relative fetches resolve against), or a url the TV loads itself.
// Returns needsForeground like Play.
func (c *Client) Dash(ctx context.Context, html, url, base string) (bool, error) {
	var out struct {
		NeedsForeground bool `json:"needsForeground"`
	}
	err := c.do(ctx, http.MethodPost, "/dash", map[string]any{"html": html, "url": url, "base": base}, &out)
	return out.NeedsForeground, err
}

// DashJS runs JavaScript in the open dashboard page.
func (c *Client) DashJS(ctx context.Context, js string) error {
	return c.do(ctx, http.MethodPost, "/dash", map[string]any{"js": js}, nil)
}

// DashClose closes the dashboard page.
func (c *Client) DashClose(ctx context.Context) error {
	return c.do(ctx, http.MethodDelete, "/dash", nil, nil)
}

// App is one launchable app as the receiver sees it. Unlike the TV's own
// list, this includes built-ins (Prime Video, Appstore, Gallery), because
// the receiver queries the launcher the way the home screen does.
type App struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Apps lists everything the launcher could start.
func (c *Client) Apps(ctx context.Context) ([]App, error) {
	var out struct {
		Apps []App `json:"apps"`
	}
	if err := c.do(ctx, http.MethodGet, "/apps", nil, &out); err != nil {
		return nil, err
	}
	return out.Apps, nil
}
