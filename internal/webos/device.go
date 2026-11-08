package webos

import (
	"context"
	"strings"
	"time"

	"github.com/spinalshock/tvctl/internal/device"
	"github.com/spinalshock/tvctl/internal/dial"
	"github.com/spinalshock/tvctl/internal/netx"
	"github.com/spinalshock/tvctl/internal/wol"
	"github.com/spinalshock/tvctl/internal/youtube"
)

const brand = "webos"

func init() { device.Register(driver{}) }

// driver plugs LG webOS into the device registry.
type driver struct{}

func (driver) Brand() string { return brand }

func (driver) Discover(ctx context.Context, timeout time.Duration) ([]device.Found, error) {
	found, err := Discover(ctx, timeout)
	if err != nil {
		return nil, err
	}
	out := make([]device.Found, len(found))
	for i, f := range found {
		out[i] = device.Found{Host: f.Host, Brand: brand, Name: f.Name, Model: f.Model}
	}
	return out, nil
}

func (driver) Open(cfg device.Config) device.Device {
	return &Client{Host: cfg.Host, Name: cfg.Name, ID: cfg.ID, MAC: cfg.MAC, ClientKey: cfg.Token}
}

// buttons maps tvctl's keys onto LG remote button names. Keys that are
// calls rather than buttons (play/pause, power) are handled in Key.
var buttons = map[device.Key]string{
	device.KeyHome:       "HOME",
	device.KeyBack:       "BACK",
	device.KeyMenu:       "MENU",
	device.KeySelect:     "ENTER",
	device.KeyUp:         "UP",
	device.KeyDown:       "DOWN",
	device.KeyLeft:       "LEFT",
	device.KeyRight:      "RIGHT",
	device.KeyFastFwd:    "FASTFORWARD",
	device.KeyRewind:     "REWIND",
	device.KeyVolumeUp:   "VOLUMEUP",
	device.KeyVolumeDown: "VOLUMEDOWN",
	device.KeyMute:       "MUTE",
	device.KeyStop:       "STOP",
}

func (c *Client) Key(ctx context.Context, k device.Key) error {
	switch k {
	case device.KeyPlayPause:
		// LG's remote has separate play and pause; toggle on what the
		// media pipeline says is playing.
		b := "PLAY"
		if c.playing(ctx) {
			b = "PAUSE"
		}
		return c.button(ctx, b)
	case device.KeySleep:
		return c.request(ctx, "system/turnOff", nil, nil)
	case device.KeyWake:
		return c.Wake(ctx)
	}
	b, ok := buttons[k]
	if !ok {
		return device.Failf("%s has no %s key", c.name(), k)
	}
	return c.button(ctx, b)
}

// playing asks only the media pipeline, one request, since a play/pause
// toggle doesn't need to know which app is in front.
func (c *Client) playing(ctx context.Context) bool {
	_, state, err := c.mediaState(ctx)
	return err == nil && state == "playing"
}

// mediaState is the media pipeline's view: the app it is playing for and
// a coarse state. Empty when nothing is loaded.
func (c *Client) mediaState(ctx context.Context) (app, state string, err error) {
	var r struct {
		Apps []struct {
			AppID     string `json:"appId"`
			PlayState string `json:"playState"`
		} `json:"foregroundAppInfo"`
	}
	if err := c.request(ctx, "com.webos.media/getForegroundAppInfo", nil, &r); err != nil {
		return "", "", err
	}
	for _, a := range r.Apps {
		return a.AppID, map[string]string{"playing": "playing", "paused": "paused", "loaded": "paused"}[a.PlayState], nil
	}
	return "", "", nil
}

// Now is the foreground app and, when the media pipeline knows, whether it
// is playing.
func (c *Client) Now(ctx context.Context) (device.Now, error) {
	var fg struct {
		AppID string `json:"appId"`
	}
	if err := c.request(ctx, "com.webos.applicationManager/getForegroundAppInfo", nil, &fg); err != nil {
		return device.Now{}, err
	}
	n := device.Now{App: fg.AppID}
	if app, state, err := c.mediaState(ctx); err == nil && (app == fg.AppID || fg.AppID == "") {
		n.State = state
		if n.App == "" {
			n.App = app
		}
	}
	return n, nil
}

// knownApps are the apps worth opening by name whether or not the
// launch-point list shows them. YouTube is LG's own app.
var knownApps = map[string]string{
	"youtube": youTubeApp,
	"browser": "com.webos.app.browser",
}

// builtinNames covers the system apps that have no launch-point tile.
var builtinNames = map[string]string{
	"com.webos.app.home":        "Home",
	"com.webos.app.livetv":      "Live TV",
	"com.webos.app.browser":     "Web Browser",
	"com.webos.app.screensaver": "Screen Saver",
	"com.webos.app.settings":    "Settings",
	"com.webos.app.discovery":   "App Store",
	"com.webos.app.hdmi1":       "HDMI 1",
	"com.webos.app.hdmi2":       "HDMI 2",
	"com.webos.app.hdmi3":       "HDMI 3",
	"com.webos.app.hdmi4":       "HDMI 4",
}

func (c *Client) AppID(name string) (string, bool) {
	id, ok := knownApps[strings.ToLower(name)]
	return id, ok
}

func (c *Client) AppName(id string) string { return builtinNames[id] }

// Wake sends Wake-on-LAN and waits, as long as ctx allows, for the TV to
// answer a request and call itself on. The API port alone proves nothing:
// it stays open in standby. Needs the MAC and "Turn on via Wi-Fi" (Mobile
// TV On) enabled on the TV; without the MAC there is nothing to send.
func (c *Client) Wake(ctx context.Context) error {
	if c.MAC == "" {
		return device.Failf("can't wake %s without its MAC address", c.name()).Note("pair again while it's on")
	}
	if err := wol.Send(c.MAC); err != nil {
		return err
	}
	for {
		cx, cancel := context.WithTimeout(ctx, 2*time.Second)
		state, err := c.powerState(cx)
		cancel()
		if err == nil && awake(state) {
			return nil
		}
		select {
		case <-ctx.Done():
			return device.Failf("%s didn't wake up", c.name()).Note("is \"Turn on via Wi-Fi\" enabled on the TV?")
		case <-time.After(time.Second):
			_ = wol.Send(c.MAC) // standby NICs sometimes miss the first frame
		}
	}
}

// LearnMAC returns the MAC of the interface the TV is reachable on, so
// Wake-on-LAN hits the right NIC: getStatus says which interface holds
// our address, getinfo carries the addresses. Firmware that withholds
// either falls back to the local ARP cache, then to wired-before-wifi.
// "" when nothing answered.
func (c *Client) LearnMAC(ctx context.Context) string {
	type nic struct {
		State string `json:"state"`
		IP    string `json:"ipAddress"`
	}
	var status struct {
		Wired nic `json:"wired"`
		Wifi  nic `json:"wifi"`
	}
	_ = c.request(ctx, "com.webos.service.connectionmanager/getStatus", nil, &status)
	var info struct {
		Wired struct {
			MAC string `json:"macAddress"`
		} `json:"wiredInfo"`
		Wifi struct {
			MAC string `json:"macAddress"`
		} `json:"wifiInfo"`
	}
	if err := c.request(ctx, "com.webos.service.connectionmanager/getinfo", nil, &info); err != nil {
		info.Wired.MAC, info.Wifi.MAC = "", ""
	}
	wired, wifi := info.Wired.MAC, info.Wifi.MAC
	switch {
	case status.Wifi.IP == c.Host && wifi != "":
		c.MAC = wifi
	case status.Wired.IP == c.Host && wired != "":
		c.MAC = wired
	case status.Wifi.State == "connected" && status.Wired.State != "connected" && wifi != "":
		c.MAC = wifi
	case status.Wired.State == "connected" && status.Wifi.State != "connected" && wired != "":
		c.MAC = wired
	default:
		if m := netx.MACOf(c.Host); m != "" {
			c.MAC = m
		} else if wired != "" {
			c.MAC = wired
		} else if wifi != "" {
			c.MAC = wifi
		}
	}
	return c.MAC
}

// Inputs lists the TV's external inputs with the owner's labels.
func (c *Client) Inputs(ctx context.Context) ([]device.Input, error) {
	var r struct {
		Devices []struct {
			ID        string `json:"id"`
			Label     string `json:"label"`
			Connected bool   `json:"connected"`
		} `json:"devices"`
	}
	if err := c.request(ctx, "tv/getExternalInputList", nil, &r); err != nil {
		return nil, err
	}
	out := make([]device.Input, 0, len(r.Devices))
	for _, d := range r.Devices {
		out = append(out, device.Input{ID: d.ID, Name: inputName(d.ID), Label: d.Label, Connected: d.Connected})
	}
	return out, nil
}

// inputName turns "HDMI_1" into the word people say, "hdmi1".
func inputName(id string) string {
	return strings.ReplaceAll(strings.ToLower(id), "_", "")
}

// SwitchInput switches to an input by the id Inputs reported ("HDMI_1").
func (c *Client) SwitchInput(ctx context.Context, id string) error {
	return c.request(ctx, "tv/switchInput", map[string]any{"inputId": id}, nil)
}

// Toast shows a short notification through the TV's own API.
func (c *Client) Toast(ctx context.Context, text string, _ device.ToastOpts) error {
	return c.request(ctx, "system.notifications/createToast", map[string]any{"message": text}, nil)
}

// Text replaces what the focused field holds, like the Fire TV driver.
func (c *Client) Text(ctx context.Context, s string) error {
	return c.request(ctx, "com.webos.service.ime/insertText", map[string]any{"text": s, "replace": 1}, nil)
}

func (c *Client) Apps(ctx context.Context) ([]device.App, error) {
	var r struct {
		Points []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"launchPoints"`
	}
	if err := c.request(ctx, "com.webos.applicationManager/listLaunchPoints", nil, &r); err != nil {
		return nil, err
	}
	apps := make([]device.App, 0, len(r.Points))
	for _, p := range r.Points {
		apps = append(apps, device.App{ID: p.ID, Name: p.Title, Installed: true})
	}
	return apps, nil
}

func (c *Client) Launch(ctx context.Context, appID string) error {
	return c.request(ctx, "system.launcher/launch", map[string]any{"id": appID}, nil)
}

// OpenURL plays YouTube videos through YouTube's casting flow and hands
// anything else to the TV's browser. Launching LG's YouTube app with the
// link as a parameter only opens the app, in every form tried (watch,
// tv, youtu.be, contentId); the cast path is the one that plays, the
// same one the Fire TV driver uses. A YouTube link with no video in it
// (a channel) opens the app.
func (c *Client) OpenURL(ctx context.Context, link string) error {
	if id, start := youtube.VideoID(link); id != "" {
		apps, err := dial.Find(ctx, c.Host, 3*time.Second)
		if err != nil {
			return err
		}
		launch := func(ctx context.Context, app, body string, hdr map[string]string) error {
			return dial.Launch(ctx, apps, app, body, hdr)
		}
		return youtube.Play(ctx, launch, id, start)
	}
	if youtube.IsLink(link) {
		return c.request(ctx, "system.launcher/launch", map[string]any{
			"id":     youTubeApp,
			"params": map[string]any{"contentTarget": link},
		}, nil)
	}
	return c.request(ctx, "system.launcher/open", map[string]any{"target": link}, nil)
}

const youTubeApp = "youtube.leanback.v4"

// Status is whatever system and software information the TV lets this
// pairing read; newer firmware withholds some (system info needs a signed
// manifest), so each part is optional.
func (c *Client) Status(ctx context.Context) (map[string]any, error) {
	out := map[string]any{}
	var lastErr error
	for key, uri := range map[string]string{
		"system":     "system/getSystemInfo",
		"software":   "com.webos.service.update/getCurrentSWInformation",
		"connection": "com.webos.service.connectionmanager/getinfo",
		"power":      "com.webos.service.tvpower/power/getPowerState",
	} {
		var v map[string]any
		if err := c.request(ctx, uri, nil, &v); err != nil {
			lastErr = err
			continue
		}
		delete(v, "returnValue")
		out[key] = v
	}
	if len(out) == 0 {
		return nil, lastErr
	}
	return out, nil
}
