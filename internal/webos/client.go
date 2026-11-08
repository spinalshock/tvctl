package webos

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/spinalshock/tvctl/internal/device"
	"github.com/spinalshock/tvctl/internal/netx"
)

// LG's second-screen API (SSAP): JSON requests over a WebSocket on :3001
// (TLS with a self-signed certificate; this firmware refuses plain :3000).
// Pairing registers a permission manifest; the TV asks its owner to allow
// it and answers with a client key, which later connections present to
// skip the prompt. Remote buttons go over a second socket the TV hands out.

const port = 3001

// manifest is the permission set LG's own clients request. Unsigned
// manifests are accepted for the prompt pairing.
var manifest = map[string]any{
	"manifestVersion": 1,
	"appVersion":      "1.1",
	"permissions": []string{
		"APP_TO_APP", "CLOSE", "CONTROL_AUDIO", "CONTROL_DISPLAY",
		"CONTROL_INPUT_JOYSTICK", "CONTROL_INPUT_MEDIA_PLAYBACK",
		"CONTROL_INPUT_MEDIA_RECORDING", "CONTROL_INPUT_TEXT", "CONTROL_INPUT_TV",
		"CONTROL_MOUSE_AND_KEYBOARD", "CONTROL_POWER", "CONTROL_TV_SCREEN",
		"LAUNCH", "LAUNCH_WEBAPP", "READ_APP_STATUS", "READ_COUNTRY_INFO",
		"READ_CURRENT_CHANNEL", "READ_INPUT_DEVICE_LIST", "READ_INSTALLED_APPS",
		"READ_LGE_SDX", "READ_LGE_TV_INPUT_EVENTS", "READ_NETWORK_STATE",
		"READ_NOTIFICATIONS", "READ_POWER_STATE", "READ_RUNNING_APPS",
		"READ_SETTINGS", "READ_TV_CHANNEL_LIST", "READ_TV_CURRENT_TIME",
		"READ_UPDATE_INFO", "SEARCH", "TEST_OPEN", "TEST_PROTECTED",
		"TEST_SECURE", "UPDATE_FROM_REMOTE_APP", "WRITE_NOTIFICATION_ALERT",
		"WRITE_NOTIFICATION_TOAST", "WRITE_SETTINGS",
	},
}

// Client is one LG TV.
type Client struct {
	Host      string
	Name      string
	ID        string // device UUID; learned from the TV on connect
	MAC       string
	ClientKey string // from pairing
}

// name is what errors call this TV: its name, else its address.
func (c *Client) name() string {
	if c.Name != "" {
		return c.Name
	}
	return c.Host
}

// Asleep is true when the TV won't take commands: port closed, or (webOS
// keeps the port open in standby for Wake-on-LAN) the socket dies on
// hello, or the TV says it is in standby. One cheap request on the
// connection the verb will reuse, so it costs the verb nothing extra.
func (c *Client) Asleep(ctx context.Context) bool {
	if !netx.PortOpen(net.JoinHostPort(c.Host, fmt.Sprint(port))) {
		return true
	}
	cx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	state, err := c.powerState(cx)
	return err != nil || !awake(state)
}

// powerState is the TV's own word for it: "Active", "Screen Saver",
// "Screen Off", "Active Standby", "Suspend".
func (c *Client) powerState(ctx context.Context) (string, error) {
	var r struct {
		State string `json:"state"`
	}
	err := c.request(ctx, "com.webos.service.tvpower/power/getPowerState", nil, &r)
	return r.State, err
}

// awake is any state in which the TV takes commands. The screen saver
// and a blanked screen are still on; any launch or key clears them.
func awake(state string) bool {
	switch state {
	case "Active", "Screen Saver", "Screen Off":
		return true
	}
	return false
}

func (c *Client) Info() device.Info {
	id := c.ID
	if id == "" {
		id = c.Host
	}
	return device.Info{ID: id, Name: c.Name, Brand: brand, Host: c.Host, MAC: c.MAC}
}

// conn is an open, registered connection, kept for the process's lifetime
// so a burst of key presses (the TUI's remote mode) doesn't redo TLS and
// registration each time. One request at a time.
type conn struct {
	mu      sync.Mutex
	ws      *websocket.Conn
	inputMu sync.Mutex
	input   *websocket.Conn // button socket, opened on first key
	seq     int
	uuid    string
}

var (
	connsMu sync.Mutex
	conns   = map[string]*conn{} // host+key -> conn
	dialMu  sync.Mutex           // one registration at a time; the TUI's pollers overlap
)

func connect(ctx context.Context, url string) (*websocket.Conn, error) {
	ws, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: &http.Transport{
			// The TV's certificate is self-signed for its LAN address;
			// there is nothing to verify it against. The client key is
			// what authenticates us to the TV.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		}},
	})
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(8 << 20) // app lists carry icons
	return ws, nil
}

type message struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	URI     string          `json:"uri,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   string          `json:"error,omitempty"`
}

func send(ctx context.Context, ws *websocket.Conn, m map[string]any) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return ws.Write(ctx, websocket.MessageText, b)
}

func recv(ctx context.Context, ws *websocket.Conn) (*message, error) {
	_, b, err := ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	var m message
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// register opens a connection and registers it. With no key the TV shows
// its Allow prompt and this waits for the owner's answer.
func (c *Client) register(ctx context.Context) (*conn, string, error) {
	ws, err := connect(ctx, fmt.Sprintf("wss://%s:%d", c.Host, port))
	if err != nil {
		return nil, "", device.Failf("%s is off or unreachable", c.name()).Note(c.Host).Because(err)
	}
	cn := &conn{ws: ws}
	if err := send(ctx, ws, map[string]any{"id": "hello", "type": "hello", "payload": map[string]any{}}); err != nil {
		ws.CloseNow() // accepted the socket and dropped it: LG in standby does this
		return nil, "", device.Failf("%s is off or unreachable", c.name()).Note(c.Host).Because(err)
	}
	if m, err := recv(ctx, ws); err == nil && m.Type == "hello" {
		var h struct {
			UUID string `json:"deviceUUID"`
		}
		_ = json.Unmarshal(m.Payload, &h)
		cn.uuid = h.UUID
	}
	payload := map[string]any{"forcePairing": false, "pairingType": "PROMPT", "manifest": manifest}
	if c.ClientKey != "" {
		payload["client-key"] = c.ClientKey
	}
	if err := send(ctx, ws, map[string]any{"id": "register_0", "type": "register", "payload": payload}); err != nil {
		ws.CloseNow()
		return nil, "", device.Failf("%s is off or unreachable", c.name()).Note(c.Host).Because(err)
	}
	for {
		m, err := recv(ctx, ws)
		if err != nil {
			ws.CloseNow()
			if ctx.Err() != nil {
				return nil, "", device.Fail("the TV didn't answer").Note("was the prompt accepted?")
			}
			return nil, "", device.Failf("%s is off or unreachable", c.name()).Note(c.Host).Because(err)
		}
		switch m.Type {
		case "registered":
			var r struct {
				Key string `json:"client-key"`
			}
			_ = json.Unmarshal(m.Payload, &r)
			return cn, r.Key, nil
		case "error":
			ws.CloseNow()
			if c.ClientKey != "" {
				return nil, "", device.Fail("the TV refused this pairing").Note(m.Error).Notef("pair again: tv pair %s", c.Host)
			}
			return nil, "", device.Fail("pairing refused").Note(m.Error)
		}
		// "response" with pairingType PROMPT: the prompt is up; keep waiting.
	}
}

// open returns the cached connection, registering a new one if needed.
func (c *Client) open(ctx context.Context) (*conn, error) {
	if c.ClientKey == "" {
		return nil, device.Failf("not paired with %s", c.name()).Note("run tv pair")
	}
	k := c.Host + "|" + c.ClientKey
	dialMu.Lock()
	defer dialMu.Unlock()
	connsMu.Lock()
	cn := conns[k]
	connsMu.Unlock()
	if cn != nil {
		return cn, nil
	}
	cn, _, err := c.register(ctx)
	if err != nil {
		return nil, err
	}
	if cn.uuid != "" {
		c.ID = cn.uuid
	}
	connsMu.Lock()
	conns[k] = cn
	connsMu.Unlock()
	return cn, nil
}

// drop forgets a broken connection so the next call reconnects.
func (c *Client) drop(cn *conn) {
	connsMu.Lock()
	for k, v := range conns {
		if v == cn {
			delete(conns, k)
		}
	}
	connsMu.Unlock()
	cn.ws.CloseNow()
	cn.inputMu.Lock()
	if cn.input != nil {
		cn.input.CloseNow()
		cn.input = nil
	}
	cn.inputMu.Unlock()
}

// requestTimeout bounds one SSAP request. The TV answers in well under a
// second; once in a while it never answers at all, and a fresh connection
// does. So a stuck request costs this much, then one retry, instead of
// the verb's whole deadline.
const requestTimeout = 4 * time.Second

// request calls one SSAP method and decodes its payload into out (may be
// nil). A dead or silent connection is replaced once.
func (c *Client) request(ctx context.Context, uri string, payload, out any) error {
	for attempt := 0; ; attempt++ {
		cn, err := c.open(ctx)
		if err != nil {
			return err
		}
		rctx, cancel := context.WithTimeout(ctx, requestTimeout)
		res, err := cn.call(rctx, uri, payload)
		cancel()
		if err != nil {
			var se *ssapError
			if errors.As(err, &se) || attempt > 0 || ctx.Err() != nil {
				return err
			}
			c.drop(cn) // stale or silent connection: one fresh try
			continue
		}
		if out != nil {
			return json.Unmarshal(res, out)
		}
		return nil
	}
}

// ssapError is the TV declining a request, as opposed to a transport fault.
type ssapError struct{ uri, msg string }

func (e *ssapError) Error() string { return e.uri + ": " + e.msg }

func (cn *conn) call(ctx context.Context, uri string, payload any) (json.RawMessage, error) {
	cn.mu.Lock()
	defer cn.mu.Unlock()
	cn.seq++
	id := "r" + strconv.Itoa(cn.seq)
	if payload == nil {
		payload = map[string]any{}
	}
	if err := send(ctx, cn.ws, map[string]any{"id": id, "type": "request", "uri": "ssap://" + uri, "payload": payload}); err != nil {
		return nil, err
	}
	for {
		m, err := recv(ctx, cn.ws)
		if err != nil {
			return nil, err
		}
		if m.ID != id {
			continue // a stray event; we hold no subscriptions
		}
		if m.Type == "error" {
			return nil, &ssapError{uri, m.Error}
		}
		var rv struct {
			OK   *bool  `json:"returnValue"`
			Text string `json:"errorText"`
		}
		_ = json.Unmarshal(m.Payload, &rv)
		if rv.OK != nil && !*rv.OK {
			return nil, &ssapError{uri, rv.Text}
		}
		return m.Payload, nil
	}
}

// button presses a remote button over the pointer-input socket.
func (c *Client) button(ctx context.Context, name string) error {
	for attempt := 0; ; attempt++ {
		cn, err := c.open(ctx)
		if err != nil {
			return err
		}
		err = cn.press(ctx, name)
		if err == nil || attempt > 0 {
			return err
		}
		c.drop(cn)
	}
}

// press writes one button to the pointer socket, opening it on first use.
// Serialised with the connection's other traffic: key bursts and the
// TUI's polls overlap.
func (cn *conn) press(ctx context.Context, name string) error {
	cn.inputMu.Lock()
	defer cn.inputMu.Unlock()
	if cn.input == nil {
		var s struct {
			Path string `json:"socketPath"`
		}
		if _, err := cn.callInto(ctx, "com.webos.service.networkinput/getPointerInputSocket", &s); err != nil {
			return device.Fail("remote buttons unavailable").Because(err)
		}
		ws, err := connect(ctx, s.Path)
		if err != nil {
			return device.Fail("remote buttons unavailable").Because(err)
		}
		cn.input = ws
	}
	return cn.input.Write(ctx, websocket.MessageText, []byte("type:button\nname:"+name+"\n\n"))
}

func (cn *conn) callInto(ctx context.Context, uri string, out any) (json.RawMessage, error) {
	res, err := cn.call(ctx, uri, nil)
	if err == nil && out != nil {
		err = json.Unmarshal(res, out)
	}
	return res, err
}

// Pair shows the Allow prompt on the TV and waits up to a minute for it.
func (c *Client) Pair(ctx context.Context, _ func(string) (string, error)) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	c.ClientKey = ""
	cn, key, err := c.register(ctx)
	if err != nil {
		return "", err
	}
	if key == "" {
		cn.ws.CloseNow()
		return "", device.Fail("the TV registered us without a client key")
	}
	c.ClientKey = key
	if cn.uuid != "" {
		c.ID = cn.uuid
	}
	connsMu.Lock()
	conns[c.Host+"|"+key] = cn
	connsMu.Unlock()
	return key, nil
}
