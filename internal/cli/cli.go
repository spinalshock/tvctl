// Package cli defines every `tv` subcommand with cobra. Each one does exactly
// one thing, prints a short human line by default, and a JSON document with
// --json, so editors and scripts can consume it without parsing prose.
//
// The TUI runs this same command tree in-process: it builds a Root with its
// own Env (a buffer for output, a modal for questions) and calls Execute.
// So a command exists exactly once, and whatever the CLI can do, the prompt
// can do.
//
// Layout: this file holds the plumbing (Env, Root, session) and the verbs
// that talk to the TV's own API. receiver.go has the verbs that need the
// companion app, media.go the transport verbs, search.go search, and
// overlay.go / dash.go the on-screen ones.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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

// Help groups. Order here is the order in `tv --help`.
const (
	groupSetup  = "setup"
	groupRemote = "remote"
	groupApps   = "apps"
	groupMedia  = "media"
	groupScreen = "screen"
)

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
	// `completion` must exist (tvctl init evals it) but needn't be advertised.
	root.CompletionOptions.HiddenDefaultCmd = true
	root.AddGroup(
		&cobra.Group{ID: groupSetup, Title: "Setup:"},
		&cobra.Group{ID: groupRemote, Title: "Remote:"},
		&cobra.Group{ID: groupApps, Title: "Apps and search:"},
		&cobra.Group{ID: groupMedia, Title: "Media:"},
		&cobra.Group{ID: groupScreen, Title: "Screen:"},
	)
	e := &cmds{env: env}
	add := func(group string, cs ...*cobra.Command) {
		for _, c := range cs {
			c.GroupID = group
			root.AddCommand(c)
		}
	}
	add(groupSetup, e.discover(), e.pair(), e.setup(), e.devices(), e.status(), e.initCmd(), e.shell(), e.install())
	add(groupRemote, e.key(), e.vol(), e.sleep(), e.wake(), e.text(), e.say())
	add(groupApps, e.openCmd(), e.apps(), e.search(), e.yt())
	add(groupMedia, e.play(), e.now(), e.pause(), e.resume(), e.toggle(), e.stop(), e.next(), e.prev(), e.seek(), e.events())
	add(groupScreen, e.shot(), e.toast(), e.overlay(), e.watch(), e.dash())
	return root
}

// Run executes one argv against env. Errors are printed through env (JSON
// or plain) and also returned so the caller can set an exit code.
func Run(args []string, env *Env) error {
	root := Root(env)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		(&cmds{env: env}).emitErr(err)
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

// VerbWords is Verbs plus every command alias (app, url, yt, pp, …): the
// words that, typed first, mean "run this verb".
func VerbWords() []string {
	var out []string
	for _, c := range Root(&Env{Out: io.Discard}).Commands() {
		if c.Name() == "completion" || c.Name() == "help" {
			continue
		}
		out = append(out, c.Name())
		out = append(out, c.Aliases...)
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

// ---- plumbing -------------------------------------------------------------

// ctx is cancelled on Ctrl-C so a stuck TV never leaves a hung process.
func ctx() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

// session is everything a verb needs, opened once per command: a context
// cancelled on Ctrl-C, the device driver, the config entry it came from,
// and the companion-app client when one is paired.
type session struct {
	ctx   context.Context
	id    string // config key; the TV's serial
	dev   device.Device
	store *config.Store
	entry config.Entry
	rc    *receiver.Client // nil until `tv setup` has run on this TV
}

func (e *cmds) openSession() (*session, context.CancelFunc, error) {
	st, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	id, ent, err := st.Resolve(e.env.Device)
	if err != nil {
		return nil, nil, err
	}
	var d device.Device
	switch ent.Brand {
	case "firetv", "":
		c := firetv.New(ent.Host, ent.Name, ent.Token)
		c.MAC = ent.MAC
		c.Serial = id
		d = c
	default:
		return nil, nil, fmt.Errorf("unsupported brand %q", ent.Brand)
	}
	cx, cancel := ctx()
	s := &session{ctx: cx, id: id, dev: d, store: st, entry: ent}
	if ent.ReceiverToken != "" {
		s.rc = receiver.New(ent.Host, ent.ReceiverToken)
	}
	return s, cancel, nil
}

// run wraps a verb body: open the session, run it, clean up. Every verb
// that talks to the TV goes through here, so that boilerplate exists once.
func (e *cmds) run(f func(s *session, args []string) error) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, args []string) error {
		s, cancel, err := e.openSession()
		if err != nil {
			return err
		}
		defer cancel()
		return f(s, args)
	}
}

// runRC is run for verbs that need the companion app, with one consistent
// message when it isn't there instead of a 403 from the TV.
func (e *cmds) runRC(f func(s *session, args []string) error) func(*cobra.Command, []string) error {
	return e.run(func(s *session, args []string) error {
		if s.rc == nil {
			return errors.New("companion app not set up on this TV: run `tv setup`")
		}
		return f(s, args)
	})
}

// bringForward is the basic-mode fallback: when the receiver parks a
// request because it can't start activities from the background, launch
// the receiver app through the TV's own API, which may.
func (s *session) bringForward(needs bool) error {
	if !needs {
		return nil
	}
	return s.dev.Launch(s.ctx, "dev.tvctl.receiver")
}

// save persists a changed entry (a receiver token after setup).
func (s *session) save() error {
	s.store.Devices[s.id] = s.entry
	return s.store.Save()
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

// ok is the common "it worked" emit: the human word plus the fields.
func (e *cmds) ok(human string, fields map[string]any) error {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["ok"] = true
	e.emit(human, fields)
	return nil
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

// ---- apps -----------------------------------------------------------------

// Aliases are the words people type; the values are package names. Anything
// not here is matched against the TV's live app list by name prefix.
// Exported so the TUI can offer them in completion.
var Aliases = map[string]string{
	"netflix":    "com.netflix.ninja",
	"youtube":    YouTubeApp,
	"yt":         YouTubeApp,
	"spotify":    "com.spotify.tv.android",
	"silk":       "com.amazon.cloud9",
	"browser":    "com.amazon.cloud9",
	"prime":      "com.amazon.firebat", // Prime Video's package on Fire TV
	"primevideo": "com.amazon.firebat",
	"gfn":        "com.nvidia.tegrazone3",
	"projectivy": "com.spocky.projengmenu",
}

// AliasNames is Aliases' keys, sorted, for help text and completion.
func AliasNames() []string {
	names := make([]string, 0, len(Aliases))
	for a := range Aliases {
		names = append(names, a)
	}
	sort.Strings(names)
	return names
}

// AllApps merges the TV's own app list with the receiver's, when one is
// paired. The TV's API omits built-in apps (Prime Video, Appstore,
// Gallery); the receiver asks the launcher and sees them all. rc may be
// nil. A receiver failure is not an error: the TV list still stands.
func AllApps(cx context.Context, d device.Device, rc *receiver.Client) ([]device.App, error) {
	apps, err := d.Apps(cx)
	if err != nil {
		return nil, err
	}
	if rc == nil {
		return apps, nil
	}
	more, err := rc.Apps(cx)
	if err != nil {
		return apps, nil
	}
	seen := make(map[string]bool, len(apps))
	for _, a := range apps {
		seen[a.ID] = true
	}
	for _, a := range more {
		if !seen[a.ID] {
			apps = append(apps, device.App{ID: a.ID, Name: a.Name, Installed: true})
		}
	}
	return apps, nil
}

// resolveApp turns user input into a package id: explicit package, alias,
// or unique case-insensitive prefix of an app's name (see AllApps).
func (s *session) resolveApp(name string) (string, error) {
	if strings.Contains(name, ".") {
		return name, nil
	}
	if id, ok := Aliases[strings.ToLower(name)]; ok {
		return id, nil
	}
	apps, err := AllApps(s.ctx, s.dev, s.rc)
	if err != nil {
		return "", err
	}
	var hits []device.App
	for _, a := range apps {
		if strings.HasPrefix(strings.ToLower(a.Name), strings.ToLower(name)) {
			hits = append(hits, a)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0].ID, nil
	case 0:
		return "", fmt.Errorf("no app matches %q (try `tv apps`)", name)
	}
	names := make([]string, len(hits))
	for i, h := range hits {
		names[i] = h.Name
	}
	return "", fmt.Errorf("ambiguous %q: %s", name, strings.Join(names, ", "))
}

// openLink routes a link. YouTube goes through the lounge protocol, which
// needs no app on the TV. Everything else is best handled by Android's own
// URL routing via the companion app (Netflix titles deep-link, web pages
// open in Silk); without the app, fall back to the stock route.
func (s *session) openLink(link string) error {
	if !strings.Contains(link, "://") {
		link = "https://" + link // "youtube.com/watch?v=..." pasted without a scheme
	}
	if s.rc != nil {
		// YouTube links go to the YouTube app tvctl uses (SmartTube); the
		// rest to whatever on the TV handles them.
		pkg := ""
		if isYouTube(link) {
			pkg = YouTubeApp
		}
		needs, err := s.rc.Open(s.ctx, link, pkg)
		if err == nil {
			return s.bringForward(needs)
		}
		if pkg == "" {
			return err
		}
		// SmartTube missing or refusing the link: fall back to the TV's own route.
	}
	o, err := need[device.URLOpener](s.dev, "opening URLs")
	if err != nil {
		return err
	}
	return o.OpenURL(s.ctx, link)
}

// YouTubeApp is the app tvctl uses for YouTube: SmartTube, an ad-free
// client, which handles youtube.com and youtu.be links itself.
const YouTubeApp = "org.smarttube.stable"

func isYouTube(link string) bool {
	l := strings.ToLower(link)
	return strings.Contains(l, "youtube.com/") || strings.Contains(l, "youtu.be/")
}

// isLink is the one definition of "this argument is a URL".
func isLink(s string) bool {
	return strings.Contains(s, "://") || strings.HasPrefix(strings.ToLower(s), "www.") || isYouTube(s)
}

// ---- setup verbs ----------------------------------------------------------

func (e *cmds) discover() *cobra.Command {
	return &cobra.Command{
		Use:   "discover",
		Short: "Find TVs on the local network",
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			found, err := firetv.Discover(cx, 2500*time.Millisecond)
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

func (e *cmds) status() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the TV's own facts (model, software, serial)",
		RunE: e.run(func(s *session, _ []string) error {
			st, err := need[device.Statuser](s.dev, "status")
			if err != nil {
				return err
			}
			m, err := st.Status(s.ctx)
			if err != nil {
				return err
			}
			info := s.dev.Info()
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
		}),
	}
}

func mustPath() string {
	p, _ := config.Path()
	return p
}

// ---- remote verbs ---------------------------------------------------------

func (e *cmds) key() *cobra.Command {
	keys := make([]string, len(device.AllKeys))
	for i, k := range device.AllKeys {
		keys[i] = string(k)
	}
	return &cobra.Command{
		Use:       "key <key>...",
		Short:     "Press remote keys (home, back, select, up, down, …)",
		Long:      "Keys: " + strings.Join(keys, " ") + ". Several can be given at once: key down down select.",
		Args:      cobra.MinimumNArgs(1),
		ValidArgs: keys,
		RunE: e.run(func(s *session, args []string) error {
			for _, a := range args {
				if err := s.dev.Key(s.ctx, device.Key(a)); err != nil {
					return err
				}
			}
			return e.ok("ok", map[string]any{"keys": args})
		}),
	}
}

func (e *cmds) vol() *cobra.Command {
	return &cobra.Command{
		Use:       "vol up|down|mute [n]",
		Short:     "Volume up/down n steps, or mute",
		Args:      cobra.RangeArgs(1, 2),
		ValidArgs: []string{"up", "down", "mute"},
		RunE: e.run(func(s *session, args []string) error {
			n := 1
			if len(args) == 2 {
				var err error
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
				if err := s.dev.Key(s.ctx, k); err != nil {
					return err
				}
			}
			return e.ok("ok", map[string]any{"action": args[0], "steps": n})
		}),
	}
}

func (e *cmds) sleep() *cobra.Command {
	return &cobra.Command{
		Use: "sleep", Short: "Put the TV to sleep (same as key sleep)", Args: cobra.NoArgs,
		RunE: e.run(func(s *session, _ []string) error {
			if err := s.dev.Key(s.ctx, device.KeySleep); err != nil {
				return err
			}
			return e.ok("sleeping", nil)
		}),
	}
}

func (e *cmds) wake() *cobra.Command {
	return &cobra.Command{
		Use: "wake", Short: "Wake the TV (Wake-on-LAN if a MAC is configured, then protocol wake)", Args: cobra.NoArgs,
		RunE: e.run(func(s *session, _ []string) error {
			w, err := need[device.Waker](s.dev, "wake")
			if err != nil {
				return err
			}
			if err := w.Wake(s.ctx); err != nil {
				return err
			}
			return e.ok("awake", nil)
		}),
	}
}

func (e *cmds) text() *cobra.Command {
	return &cobra.Command{
		Use:   "text <string>...",
		Short: "Type text into the focused field on the TV",
		Args:  cobra.MinimumNArgs(1),
		RunE: e.run(func(s *session, args []string) error {
			str := strings.Join(args, " ")
			if kw, ok := s.dev.(device.KeyboardWatcher); ok {
				if open, _, err := kw.KeyboardOpen(s.ctx); err == nil && !open {
					return errors.New("the TV's system keyboard isn't open, so there's nothing to type into. " +
						"Netflix and YouTube draw their own keyboards: use `search netflix <words>`, `search youtube <words>`, or `say ...`")
				}
			}
			if err := s.dev.Text(s.ctx, str); err != nil {
				return err
			}
			return e.ok("ok", map[string]any{"text": str})
		}),
	}
}

func (e *cmds) say() *cobra.Command {
	return &cobra.Command{
		Use:   "say <phrase>...",
		Short: "Speak to the TV's voice assistant (e.g. say switch to HDMI 1)",
		Args:  cobra.MinimumNArgs(1),
		RunE: e.run(func(s *session, args []string) error {
			sp, err := need[device.Speaker](s.dev, "voice commands")
			if err != nil {
				return err
			}
			phrase := strings.Join(args, " ")
			t0 := time.Now()
			if err := sp.Say(s.ctx, phrase); err != nil {
				return err
			}
			secs := time.Since(t0).Seconds()
			return e.ok(fmt.Sprintf("said %q (%.1fs)", phrase, secs), map[string]any{"phrase": phrase, "seconds": secs})
		}),
	}
}

// ---- app verbs ------------------------------------------------------------

func (e *cmds) openCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "open <app|link>",
		Aliases: []string{"app", "url"},
		Short:   "Open an app (by alias, name prefix or package) or a link",
		Long: "Apps: " + strings.Join(AliasNames(), ", ") + "; anything else matches the start of an\n" +
			"app's name (see `tv apps`), or is a package id when it contains a dot.\n" +
			"Links: YouTube opens in SmartTube; Netflix titles open in Netflix; other\n" +
			"web pages open in the browser.",
		Args: cobra.ExactArgs(1),
		RunE: e.run(func(s *session, args []string) error {
			if isLink(args[0]) {
				if err := s.openLink(args[0]); err != nil {
					return err
				}
				return e.ok("opened "+args[0], map[string]any{"url": args[0]})
			}
			id, err := s.resolveApp(args[0])
			if err != nil {
				return err
			}
			if err := s.dev.Launch(s.ctx, id); err != nil {
				return err
			}
			return e.ok("launched "+id, map[string]any{"app": id})
		}),
	}
}

func (e *cmds) apps() *cobra.Command {
	return &cobra.Command{
		Use:   "apps",
		Short: "List apps on the TV",
		RunE: e.run(func(s *session, _ []string) error {
			apps, err := AllApps(s.ctx, s.dev, s.rc)
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
		}),
	}
}
