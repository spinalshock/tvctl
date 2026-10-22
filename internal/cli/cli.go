// Package cli defines every `tv` subcommand with cobra. Each one does exactly
// one thing, prints a short human line by default, and a JSON document with
// --json, so editors and scripts can consume it without parsing prose.
//
// The TUI runs this same command tree in-process: it builds a Root with its
// own Env (a buffer for output, a modal for questions) and calls Execute.
// So a command exists exactly once, and whatever the CLI can do, the prompt
// can do.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/omAthalye/tvctl/internal/config"
	"github.com/omAthalye/tvctl/internal/device"
	"github.com/omAthalye/tvctl/internal/firetv"
	"github.com/omAthalye/tvctl/internal/receiver"
)

// Env is what a command needs from its host: where to print, how to ask the
// user a question, and the parsed global flags. The CLI and the TUI build
// different Envs around the same commands.
type Env struct {
	Out    io.Writer
	Ask    func(prompt string) (string, error)
	JSON   bool
	Device string
}

// Root builds a fresh command tree bound to env. Fresh per call on purpose:
// cobra stores flag values on the command objects, so reusing a tree across
// executions would leak --json from one prompt line into the next.
func Root(env *Env) *cobra.Command {
	root := &cobra.Command{
		Use:           invokedAs(),
		Short:         "Control your TV from the terminal",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(env.Out)
	root.SetErr(env.Out)
	root.PersistentFlags().BoolVar(&env.JSON, "json", false, "machine-readable output")
	root.PersistentFlags().StringVarP(&env.Device, "device", "d", "", "device id, name or IP (default: the default device)")
	e := &cmds{env: env}
	root.AddCommand(
		e.discover(), e.pair(), e.key(), e.vol(), e.apps(), e.app(),
		e.text(), e.url(), e.say(), e.sleep(), e.wake(), e.status(), e.devices(),
		e.setup(), e.now(), e.events(), e.play(), e.toast(), e.shot(), e.watch(), e.overlay(), e.dash(), e.initCmd(),
		e.yt(), e.netflixSearch(), e.search(),
		e.pause(), e.resume(), e.toggle(), e.stop(), e.next(), e.prev(), e.seek(),
	)
	return root
}

// Run executes one argv against env. Errors are printed through env (JSON
// or plain) and also returned so the caller can set an exit code.
func Run(args []string, env *Env) error {
	root := Root(env)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		e := &cmds{env: env}
		e.emitErr(err)
		return err
	}
	return nil
}

// Verbs lists the top-level command names, for the TUI's resolver and
// completion. Derived from the tree so it can't drift.
func Verbs() []string {
	var out []string
	for _, c := range Root(&Env{Out: io.Discard}).Commands() {
		if c.Name() != "completion" && c.Name() != "help" {
			out = append(out, c.Name())
		}
	}
	sort.Strings(out)
	return out
}

// cmds carries env into the command constructors.
type cmds struct{ env *Env }

// invokedAs is the name help text uses: "tvctl" when run directly, but
// usually "tv" because the shell function from `tvctl init` execs the
// binary under that name via the TVCTL_ALIAS it exports.
func invokedAs() string {
	if a := os.Getenv("TVCTL_ALIAS"); a != "" {
		return a
	}
	return "tvctl"
}

var execLookPath = exec.LookPath

// ---- plumbing -------------------------------------------------------------

// ctx is cancelled on Ctrl-C so a stuck TV never leaves a hung process.
func ctx() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

// open loads the config and builds the driver for the selected device. The
// returned id is the config key, needed when a command learns something
// worth persisting (the serial after `status`).
func (e *cmds) open() (string, device.Device, *config.Store, error) {
	st, err := config.Load()
	if err != nil {
		return "", nil, nil, err
	}
	id, ent, err := st.Resolve(e.env.Device)
	if err != nil {
		return "", nil, nil, err
	}
	switch ent.Brand {
	case "firetv", "":
		c := firetv.New(ent.Host, ent.Name, ent.Token)
		c.MAC = ent.MAC
		c.Serial = id
		return id, c, st, nil
	}
	return "", nil, nil, fmt.Errorf("unsupported brand %q", ent.Brand)
}

// emit prints either the JSON document or the human line. Keeping both
// paths in one helper is what makes --json consistent across commands.
func (e *cmds) emit(human string, doc any) {
	if e.env.JSON {
		enc := json.NewEncoder(e.env.Out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(doc)
		return
	}
	if human != "" {
		fmt.Fprintln(e.env.Out, human)
	}
}

func (e *cmds) emitErr(err error) {
	if e.env.JSON {
		_ = json.NewEncoder(e.env.Out).Encode(map[string]string{"error": err.Error()})
		return
	}
	fmt.Fprintln(e.env.Out, "tv:", err)
}

// need asserts an optional capability and turns a missing one into a
// readable error instead of a panic.
func need[T any](d device.Device, what string) (T, error) {
	if t, ok := d.(T); ok {
		return t, nil
	}
	var zero T
	return zero, fmt.Errorf("%s does not support %s", d.Info().Brand, what)
}

// ---- commands -------------------------------------------------------------

func (e *cmds) discover() *cobra.Command {
	return &cobra.Command{
		Use:   "discover",
		Short: "Find TVs on the local network",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, cancel := ctx()
			defer cancel()
			found, err := firetv.Discover(c, 2500*time.Millisecond)
			if err != nil {
				return err
			}
			if len(found) == 0 {
				return errors.New("no Fire TVs found (is the TV on and on this network?)")
			}
			var lines []string
			for _, f := range found {
				lines = append(lines, fmt.Sprintf("%-16s %s", f.Host, f.Name))
			}
			e.emit(strings.Join(lines, "\n"), found)
			return nil
		},
	}
}

func (e *cmds) pair() *cobra.Command {
	var token string
	c := &cobra.Command{
		Use:   "pair [host]",
		Short: "Pair with a TV (shows a PIN on screen)",
		Long: `Pair with a TV. Without a host, discovers Fire TVs and uses the only one
found (or asks you to pick). The TV shows a PIN; type it here. Pairing is
stored in ` + mustPath() + ` and survives TV reboots.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			host, name := "", ""
			if len(args) == 1 {
				host = args[0]
			} else {
				found, err := firetv.Discover(cx, 2500*time.Millisecond)
				if err != nil {
					return err
				}
				switch len(found) {
				case 0:
					return errors.New("no Fire TVs found; pass the IP: tv pair 192.168.1.20")
				case 1:
					host, name = found[0].Host, found[0].Name
				default:
					for i, f := range found {
						fmt.Fprintf(e.env.Out, "%d) %-16s %s\n", i+1, f.Host, f.Name)
					}
					s, err := e.env.Ask("which one? ")
					if err != nil {
						return err
					}
					n, _ := strconv.Atoi(s)
					if n < 1 || n > len(found) {
						return errors.New("no such choice")
					}
					host, name = found[n-1].Host, found[n-1].Name
				}
			}
			cl := firetv.New(host, name, token)
			if token == "" {
				if _, err := cl.Pair(cx, e.env.Ask); err != nil {
					return err
				}
			}
			// Properties gives the serial, which is the stable id; the IP
			// may change with DHCP but the serial never does.
			props, err := cl.Status(cx)
			if err != nil {
				return fmt.Errorf("paired but status failed: %w", err)
			}
			// With an explicit host we still want the TV's own name; a quick
			// discovery usually has it. Fall back to a generic label.
			if name == "" {
				if found, _ := firetv.Discover(cx, 1500*time.Millisecond); found != nil {
					for _, f := range found {
						if f.Host == host {
							name = f.Name
						}
					}
				}
			}
			if name == "" {
				name = "Fire TV"
			}
			st, err := config.Load()
			if err != nil {
				return err
			}
			id := cl.Info().ID
			st.Put(id, config.Entry{Name: name, Brand: "firetv", Host: host, Token: cl.Token})
			if err := st.Save(); err != nil {
				return err
			}
			e.emit(fmt.Sprintf("paired with %s (%s)", name, host),
				map[string]any{"id": id, "name": name, "host": host, "properties": props})
			return nil
		},
	}
	c.Flags().StringVar(&token, "token", "", "import an existing client token instead of pairing")
	return c
}

func (e *cmds) key() *cobra.Command {
	keys := make([]string, len(device.AllKeys))
	for i, k := range device.AllKeys {
		keys[i] = string(k)
	}
	return &cobra.Command{
		Use:       "key <key>...",
		Short:     "Press remote keys: " + strings.Join(keys, " "),
		Args:      cobra.MinimumNArgs(1),
		ValidArgs: keys,
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			_, d, _, err := e.open()
			if err != nil {
				return err
			}
			for _, a := range args {
				if err := d.Key(cx, device.Key(a)); err != nil {
					return err
				}
			}
			e.emit("ok", map[string]any{"ok": true, "keys": args})
			return nil
		},
	}
}

func (e *cmds) vol() *cobra.Command {
	return &cobra.Command{
		Use:       "vol up|down|mute [n]",
		Short:     "Volume up/down n steps, or mute",
		Args:      cobra.RangeArgs(1, 2),
		ValidArgs: []string{"up", "down", "mute"},
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			_, d, _, err := e.open()
			if err != nil {
				return err
			}
			n := 1
			if len(args) == 2 {
				if n, err = strconv.Atoi(args[1]); err != nil || n < 1 || n > 50 {
					return errors.New("n must be 1..50")
				}
			}
			var k device.Key
			switch args[0] {
			case "up":
				k = device.KeyVolumeUp
			case "down":
				k = device.KeyVolumeDown
			case "mute":
				k, n = device.KeyMute, 1
			default:
				return errors.New("vol up|down|mute [n]")
			}
			for i := 0; i < n; i++ {
				if err := d.Key(cx, k); err != nil {
					return err
				}
			}
			e.emit("ok", map[string]any{"ok": true, "action": args[0], "steps": n})
			return nil
		},
	}
}

func (e *cmds) apps() *cobra.Command {
	return &cobra.Command{
		Use:   "apps",
		Short: "List installed apps",
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			_, d, _, err := e.open()
			if err != nil {
				return err
			}
			apps, err := d.Apps(cx)
			if err != nil {
				return err
			}
			sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
			var lines []string
			for _, a := range apps {
				lines = append(lines, fmt.Sprintf("%-40s %s", a.ID, a.Name))
			}
			e.emit(strings.Join(lines, "\n"), apps)
			return nil
		},
	}
}

// aliases are the words people type; the values are package names. Anything
// not here is matched against the TV's live app list by name prefix.
// Aliases is exported so the TUI can offer them in completion.
var Aliases = map[string]string{
	"netflix":    "com.netflix.ninja",
	"youtube":    "com.amazon.firetv.youtube",
	"yt":         "com.amazon.firetv.youtube",
	"spotify":    "com.spotify.tv.android",
	"silk":       "com.amazon.cloud9",
	"browser":    "com.amazon.cloud9",
	"prime":      "com.amazon.avod",
	"gfn":        "com.nvidia.tegrazone3",
	"projectivy": "com.spocky.projengmenu",
}

// resolveApp turns user input into a package id: explicit package, alias,
// or unique case-insensitive prefix of an installed app's name.
func ResolveApp(cx context.Context, d device.Device, s string) (string, error) {
	if strings.Contains(s, ".") {
		return s, nil
	}
	if id, ok := Aliases[strings.ToLower(s)]; ok {
		return id, nil
	}
	apps, err := d.Apps(cx)
	if err != nil {
		return "", err
	}
	var hits []device.App
	for _, a := range apps {
		if strings.HasPrefix(strings.ToLower(a.Name), strings.ToLower(s)) {
			hits = append(hits, a)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0].ID, nil
	case 0:
		return "", fmt.Errorf("no app matches %q (try `tv apps`)", s)
	}
	names := make([]string, len(hits))
	for i, h := range hits {
		names[i] = h.Name
	}
	return "", fmt.Errorf("ambiguous %q: %s", s, strings.Join(names, ", "))
}

func (e *cmds) app() *cobra.Command {
	return &cobra.Command{
		Use:   "app <name|package>",
		Short: "Launch an app (netflix, youtube, spotify, or a package id)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			_, d, _, err := e.open()
			if err != nil {
				return err
			}
			id, err := ResolveApp(cx, d, args[0])
			if err != nil {
				return err
			}
			if err := d.Launch(cx, id); err != nil {
				return err
			}
			e.emit("launched "+id, map[string]any{"ok": true, "app": id})
			return nil
		},
	}
}

func (e *cmds) text() *cobra.Command {
	return &cobra.Command{
		Use:   "text <string>...",
		Short: "Type text into the focused field on the TV",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			_, d, _, err := e.open()
			if err != nil {
				return err
			}
			s := strings.Join(args, " ")
			if kw, ok := d.(device.KeyboardWatcher); ok {
				if open, _, err := kw.KeyboardOpen(cx); err == nil && !open {
					return errors.New("the TV's system keyboard isn't open, so there's nothing to type into. " +
						"Netflix and YouTube draw their own keyboards: use `search netflix <words>`, `search youtube <words>`, or `say ...`")
				}
			}
			if err := d.Text(cx, s); err != nil {
				return err
			}
			e.emit("ok", map[string]any{"ok": true, "text": s})
			return nil
		},
	}
}

func (e *cmds) url() *cobra.Command {
	return &cobra.Command{
		Use:   "url <link>",
		Short: "Open a link on the TV (YouTube links open in the YouTube app)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			_, d, _, err := e.open()
			if err != nil {
				return err
			}
			if err := e.openURL(cx, d, args[0]); err != nil {
				return err
			}
			e.emit("ok", map[string]any{"ok": true, "url": args[0]})
			return nil
		},
	}
}

func (e *cmds) say() *cobra.Command {
	return &cobra.Command{
		Use:   "say <phrase>...",
		Short: "Speak to the TV's voice assistant (e.g. say switch to HDMI 1)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			_, d, _, err := e.open()
			if err != nil {
				return err
			}
			sp, err := need[device.Speaker](d, "voice commands")
			if err != nil {
				return err
			}
			phrase := strings.Join(args, " ")
			t0 := time.Now()
			if err := sp.Say(cx, phrase); err != nil {
				return err
			}
			e.emit(fmt.Sprintf("said %q (%.1fs)", phrase, time.Since(t0).Seconds()),
				map[string]any{"ok": true, "phrase": phrase, "seconds": time.Since(t0).Seconds()})
			return nil
		},
	}
}

func (e *cmds) sleep() *cobra.Command {
	return &cobra.Command{
		Use:   "sleep",
		Short: "Put the TV to sleep",
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			_, d, _, err := e.open()
			if err != nil {
				return err
			}
			if err := d.Key(cx, device.KeySleep); err != nil {
				return err
			}
			e.emit("sleeping", map[string]any{"ok": true})
			return nil
		},
	}
}

func (e *cmds) wake() *cobra.Command {
	return &cobra.Command{
		Use:   "wake",
		Short: "Wake the TV (Wake-on-LAN if a MAC is configured, then protocol wake)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			_, d, _, err := e.open()
			if err != nil {
				return err
			}
			w, err := need[device.Waker](d, "wake")
			if err != nil {
				return err
			}
			if err := w.Wake(cx); err != nil {
				return err
			}
			e.emit("awake", map[string]any{"ok": true})
			return nil
		},
	}
}

func (e *cmds) status() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show device facts",
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			_, d, _, err := e.open()
			if err != nil {
				return err
			}
			s, err := need[device.Statuser](d, "status")
			if err != nil {
				return err
			}
			m, err := s.Status(cx)
			if err != nil {
				return err
			}
			info := d.Info()
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			lines := []string{fmt.Sprintf("%s  %s  %s", info.Name, info.Host, info.Brand)}
			for _, k := range keys {
				lines = append(lines, fmt.Sprintf("  %-28s %v", k, m[k]))
			}
			e.emit(strings.Join(lines, "\n"), map[string]any{"device": info, "properties": m})
			return nil
		},
	}
}

func (e *cmds) devices() *cobra.Command {
	return &cobra.Command{
		Use:   "devices",
		Short: "List paired devices",
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := config.Load()
			if err != nil {
				return err
			}
			type row struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Host    string `json:"host"`
				Brand   string `json:"brand"`
				Default bool   `json:"default"`
			}
			var rows []row
			var lines []string
			for id, e := range st.Devices {
				def := id == st.Default
				rows = append(rows, row{id, e.Name, e.Host, e.Brand, def})
				mark := " "
				if def {
					mark = "*"
				}
				lines = append(lines, fmt.Sprintf("%s %-20s %-16s %s", mark, e.Name, e.Host, id))
			}
			e.emit(strings.Join(lines, "\n"), rows)
			return nil
		},
	}
}

func mustPath() string {
	p, _ := config.Path()
	return p
}

// openURL routes a link. YouTube goes through the lounge protocol, which
// needs no app on the TV. Everything else is best handled by Android's own
// URL routing via the companion app (Netflix titles deep-link, web pages
// open in Silk); without the app, fall back to the stock route.
func (e *cmds) openURL(cx context.Context, d device.Device, link string) error {
	id, _, st, _ := e.open()
	ent := st.Devices[id]
	if ent.ReceiverToken != "" && !strings.Contains(strings.ToLower(link), "youtu") {
		rc := receiver.New(ent.Host, ent.ReceiverToken)
		needs, err := rc.Open(cx, link, "")
		if err != nil {
			return err
		}
		return e.bringForward(cx, d, needs)
	}
	o, err := need[device.URLOpener](d, "opening URLs")
	if err != nil {
		return err
	}
	return o.OpenURL(cx, link)
}
