// Package cli defines every `tv` subcommand with cobra. Each one does exactly
// one thing, prints a short human line by default, and a JSON document with
// --json, so editors and scripts can consume it without parsing prose.
//
// The TUI runs this same command tree in-process: it builds a Root with its
// own Env (a buffer for output, a modal for questions) and calls Execute.
// So a command exists exactly once, and whatever the CLI can do, the prompt
// can do.
//
// Layout: this file holds the plumbing (Env, Root, session) and the core
// verbs. companion.go has the verbs whose capability usually comes from
// the companion add-on, media.go the transport verbs, search.go search,
// voice.go pairing, inputs and the tool manifest, and home.go / overlay.go
// / dash.go the on-screen ones.
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

	"github.com/spinalshock/tvctl/internal/config"
	"github.com/spinalshock/tvctl/internal/device"
	"github.com/spinalshock/tvctl/internal/netx"
	"github.com/spinalshock/tvctl/internal/trace"
	"github.com/spinalshock/tvctl/internal/youtube"
)

// Nothing in this package names a brand. Drivers register themselves
// (drivers.go) and expose what they can do through device's optional
// interfaces; a verb asserts the capability it needs and otherwise says
// "<TV> can't do <that>".

// Env is what a command needs from its host: where to print, how to ask the
// user a question, and the parsed global flags. The CLI and the TUI build
// different Envs around the same commands.
type Env struct {
	Out    io.Writer
	Ask    func(prompt string) (string, error)
	JSON   bool
	Device string
	// Interactive is true when a person is at the keyboard to answer Ask.
	// When false (a pipe, the TUI, a voice assistant) commands that would
	// prompt instead return what to run next: pairing shows the PIN and
	// stops, and `tv pin` finishes it.
	Interactive bool
	// Notice, when set, also gets each outcome worth announcing ("launched
	// …", "playing …"), for a host that shows it prominently (the TUI's
	// toast). Routine acks ("ok") don't come here.
	Notice func(text string)
}

// Help groups. Order here is the order in `tv --help`.
const (
	groupSetup   = "setup"
	groupMachine = "machine"
	groupRemote  = "remote"
	groupApps    = "apps"
	groupMedia   = "media"
	groupScreen  = "screen"
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
		&cobra.Group{ID: groupMachine, Title: "This machine:"},
	)
	e := &cmds{env: env}
	add := func(group string, cs ...*cobra.Command) {
		for _, c := range cs {
			if c == nil {
				continue // left out of this build
			}
			c.GroupID = group
			root.AddCommand(c)
		}
	}
	add(groupSetup, e.pair(), e.pin(), e.devices(), e.status(), e.companion())
	add(groupRemote, e.key(), e.vol(), e.input(), e.sleep(), e.wake(), e.text(), e.say())
	add(groupApps, e.openCmd(), e.apps(), e.search(), e.yt())
	add(groupMedia, e.play(), e.pause(), e.stop(), e.seek(), e.now(), e.events())
	add(groupScreen, e.home(), e.shot(), e.toast(), e.overlay(), e.watch(), e.dash())
	add(groupMachine, e.initCmd(), e.tools(), e.shell(), e.install())
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

// Choices is each verb's fixed first-argument values (key names, vol
// up|down|mute, input hdmi1…), for completion. Derived from the tree.
func Choices() map[string][]string {
	out := map[string][]string{}
	for _, c := range Root(&Env{Out: io.Discard}).Commands() {
		if len(c.ValidArgs) > 0 {
			out[c.Name()] = c.ValidArgs
		}
	}
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

// ctx is the context every command runs under: cancelled on Ctrl-C so a
// stuck TV never leaves a hung process, and bounded by the command's
// deadline annotation so a TV that went away costs seconds, not a hung
// assistant. Streaming verbs opt out with deadline "none".
func ctx(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	cx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	tcx, tcancel := withDeadline(cx, cmd)
	return tcx, func() { tcancel(); cancel() }
}

// withDeadline cuts a verb's deadline from base; "none" returns base as is.
func withDeadline(base context.Context, cmd *cobra.Command) (context.Context, context.CancelFunc) {
	dl := deadlineFor(cmd)
	if dl == 0 {
		return base, func() {}
	}
	return context.WithTimeout(base, dl)
}

// session is everything a verb needs, opened once per command: a context
// cancelled on Ctrl-C, the device driver and the config entry it came
// from. Whatever the TV can do, including through an add-on, is asked of
// the driver through device's optional interfaces.
type session struct {
	ctx   context.Context // bounded by the verb's deadline
	base  context.Context // Ctrl-C only; what the deadline is cut from
	id    string          // config key; the TV's stable id
	dev   device.Device
	store *config.Store
	entry config.Entry

	appList []device.App // the TV's apps, once fetched (see apps)
}

func (e *cmds) openSession(cmd *cobra.Command) (*session, context.CancelFunc, error) {
	st, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	id, ent, err := st.Resolve(e.env.Device)
	if err != nil {
		return nil, nil, err
	}
	trace.Logf("cli: using %s (%s)", ent.Name, ent.Host)
	d, err := device.Open(DeviceConfig(id, ent))
	if err != nil {
		return nil, nil, err
	}
	base, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	cx, tcancel := withDeadline(base, cmd)
	s := &session{ctx: cx, base: base, id: id, dev: d, store: st, entry: ent}
	return s, func() { tcancel(); stop() }, nil
}

// run wraps a verb body: open the session, run it, clean up. Every verb
// that talks to the TV goes through here, so that boilerplate exists once.
func (e *cmds) run(f func(s *session, args []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		s, cancel, err := e.openSession(cmd)
		if err != nil {
			return err
		}
		defer cancel()
		if wakes(cmd) {
			if err := s.wakeIfAsleep(cmd); err != nil {
				return err
			}
		}
		err = f(s, args)
		trace.Logf("cli: verb finished err=%v", err)
		if errors.Is(err, context.DeadlineExceeded) || (err != nil && s.ctx.Err() == context.DeadlineExceeded) {
			return device.Failf("%s didn't answer in time", tvName(s.dev))
		}
		if err == nil && s.entry.MAC == "" {
			// A TV paired before its address was kept gets it now, while
			// the connection is warm. Best effort, never an error of its own.
			if mac := learnMAC(s.base, s.dev, s.entry.Host); mac != "" {
				s.entry.MAC = mac
				_ = s.save()
			}
		}
		return err
	}
}

// learnMAC is the TV's hardware address, for Wake-on-LAN: what the driver
// already knows, else what it can ask the TV (device.MACLearner), else the
// local ARP cache, which knows the TV because we just talked to it. ""
// when none of them does.
func learnMAC(ctx context.Context, dev device.Device, host string) string {
	if mac := dev.Info().MAC; mac != "" {
		return mac
	}
	cx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if l, ok := dev.(device.MACLearner); ok {
		if mac := l.LearnMAC(cx); mac != "" {
			return mac
		}
	}
	return netx.MACOf(host)
}

// wakeIfAsleep is the first rung of doing what was asked on a TV that is
// off: one probe, and if it is asleep, wake it with its own budget, then
// give the verb its full deadline again. A TV that can't be woken, or
// can't tell, is left to the verb's own error.
func (s *session) wakeIfAsleep(cmd *cobra.Command) error {
	sl, ok := s.dev.(device.Sleeper)
	w, canWake := s.dev.(device.Waker)
	if !ok || !canWake {
		return nil
	}
	asleep := sl.Asleep(s.base)
	trace.Logf("cli: asleep check done, asleep=%v", asleep)
	if !asleep {
		return nil
	}
	wcx, wcancel := context.WithTimeout(s.base, 30*time.Second)
	defer wcancel()
	if err := w.Wake(wcx); err != nil {
		return err
	}
	s.ctx, _ = withDeadline(s.base, cmd) // released with base when the command ends
	return nil
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
	// The plain line is what gets read aloud when an assistant runs tv, so
	// it is one short sentence; the full cause chain is in --json.
	fmt.Fprintln(e.env.Out, spoken(err))
}

// ok is the common "it worked" emit: the plain line plus the fields. The
// plain output of tv is what an assistant reads aloud, so a routine ack (a
// key, the volume) passes "" and prints nothing: the TV's own screen is
// the feedback. --json always gets the document.
func (e *cmds) ok(human string, fields map[string]any) error {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["ok"] = true
	e.emit(human, fields)
	return nil
}

// done is ok for an outcome worth announcing: one short sentence.
func (e *cmds) done(human string, fields map[string]any) error {
	e.notice(human)
	return e.ok(human, fields)
}

func (e *cmds) notice(text string) {
	if e.env.Notice != nil && !e.env.JSON {
		e.env.Notice(text)
	}
}

// need asserts an optional capability and turns a missing one into a
// readable error instead of a panic.
func need[T any](d device.Device, what string) (T, error) {
	if t, ok := d.(T); ok {
		return t, nil
	}
	var zero T
	return zero, device.Failf("%s can't do %s", tvName(d), what)
}

// NeedAddon is need for a capability the companion add-on provides. A TV
// that could take the add-on but doesn't have it yet is told what to run;
// one that can't is told it can't. Exported for the TUI's own shortcuts.
func NeedAddon[T any](d device.Device, what string) (T, error) {
	t, err := need[T](d, what)
	if err != nil {
		if _, can := d.(device.Installer); can {
			return t, device.Failf("%s needs the companion app for %s", tvName(d), what).Note("run tv companion")
		}
	}
	return t, err
}

// tvName is what errors call the TV.
func tvName(d device.Device) string {
	if n := d.Info().Name; n != "" {
		return n
	}
	return d.Info().Brand
}

// ---- apps -----------------------------------------------------------------

// Aliases are the short words people say for apps, and each app's proper
// name. The name is resolved on whatever TV is in front (resolveApp): the
// driver answers for apps it knows by name, else the TV's own list is
// matched by prefix. No app ids here: which package or id an app has is a
// brand's business. Exported so the TUI can offer them in completion.
var Aliases = map[string]string{
	"netflix":    "Netflix",
	"youtube":    "YouTube",
	"yt":         "YouTube",
	"spotify":    "Spotify",
	"prime":      "Prime Video",
	"primevideo": "Prime Video",
	"browser":    "Browser",
	"silk":       "Silk",
	"gfn":        "GeForce NOW",
	"projectivy": "Projectivy",
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

// resolveApp turns what the user said into this TV's app id: an id as is
// (it has a dot), else an alias's proper name or the words themselves,
// asked of the driver first (device.AppNamer) and then matched against the
// TV's app list by unique prefix.
func (s *session) resolveApp(word string) (string, error) {
	if strings.Contains(word, ".") {
		return word, nil
	}
	name := word
	if n, ok := Aliases[strings.ToLower(word)]; ok {
		name = n
	}
	if an, ok := s.dev.(device.AppNamer); ok {
		if id, ok := an.AppID(name); ok {
			return id, nil
		}
	}
	return s.appByName(name)
}

// apps is the TV's app list, fetched once per command.
func (s *session) apps() ([]device.App, error) {
	if s.appList == nil {
		apps, err := s.dev.Apps(s.ctx)
		if err != nil {
			return nil, err
		}
		s.appList = apps
	}
	return s.appList, nil
}

// appTitle is the name to say for an app id: the driver's name for it,
// else the title the TV lists it under, else the id itself.
func (s *session) appTitle(id string) string {
	if an, ok := s.dev.(device.AppNamer); ok {
		if n := an.AppName(id); n != "" {
			return n
		}
	}
	if apps, err := s.apps(); err == nil {
		for _, a := range apps {
			if a.ID == id && a.Name != "" {
				return a.Name
			}
		}
	}
	return id
}

// appByName finds the one app on this TV whose name starts with name.
func (s *session) appByName(name string) (string, error) {
	apps, err := s.apps()
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
		return "", device.Failf("no app called %s on this TV", name)
	}
	names := make([]string, len(hits))
	for i, h := range hits {
		names[i] = h.Name
	}
	return "", device.Failf("%s could mean %s", name, strings.Join(names, " or "))
}

// openApp resolves and launches an app, and returns the name to announce.
func (s *session) openApp(word string) (id, name string, err error) {
	if id, err = s.resolveApp(word); err != nil {
		return "", "", err
	}
	if err = s.dev.Launch(s.ctx, id); err != nil {
		return "", "", err
	}
	return id, s.appTitle(id), nil
}

// openLink hands a link to the TV. Where it lands (YouTube's app, a
// browser, a deep link) is the driver's business.
func (s *session) openLink(link string) error {
	if !strings.Contains(link, "://") {
		link = "https://" + link // "youtube.com/watch?v=..." pasted without a scheme
	}
	o, err := need[device.URLOpener](s.dev, "opening links")
	if err != nil {
		return err
	}
	return o.OpenURL(s.ctx, link)
}

// isLink is the one definition of "this argument is a URL".
func isLink(s string) bool {
	return strings.Contains(s, "://") || strings.HasPrefix(strings.ToLower(s), "www.") || youtube.IsLink(s)
}

// ---- setup verbs ----------------------------------------------------------

// DeviceConfig is a saved entry as its driver sees it. An entry from
// before there was more than one brand has no brand; the registry knows
// whose those are.
func DeviceConfig(id string, ent config.Entry) device.Config {
	return device.Config{ID: id, Brand: ent.Brand, Name: ent.Name, Host: ent.Host, MAC: ent.MAC, Token: ent.Token, CompanionToken: ent.CompanionToken}
}

func (e *cmds) pair() *cobra.Command {
	var token, brand string
	c := &cobra.Command{
		Use:         "pair [host]",
		Short:       "Pair with a TV (a PIN or an Allow prompt on screen)",
		Annotations: meta("", "90s"),
		Long: `Pair with a TV. Without a host, discovers TVs and uses the only one found
(or asks you to pick; a name or IP from ` + "`tv devices --scan`" + ` also works). The TV
then shows a PIN to read back or an Allow prompt to accept with the remote.
At a terminal the PIN is typed here. From a script or an assistant, pair
stops once the PIN is on screen and ` + "`tv pin <digits>`" + ` finishes the
job. Pairing is stored in ` + mustPath() + ` and survives TV reboots.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx(cmd)
			defer cancel()
			tv, err := e.pickTV(cx, args, brand)
			if err != nil {
				return err
			}
			dev, err := device.Open(device.Config{Brand: tv.Brand, Name: tv.Name, Host: tv.Host, Token: token})
			if err != nil {
				return err
			}
			if token == "" {
				if p, ok := dev.(device.PINPairer); ok && !e.env.Interactive {
					if err := p.ShowPIN(cx); err != nil {
						return err
					}
					st, err := config.Load()
					if err != nil {
						return err
					}
					st.Pending = &config.Pending{Brand: tv.Brand, Name: tv.Name, Host: tv.Host, Started: time.Now()}
					if err := st.Save(); err != nil {
						return err
					}
					return e.done("Tell me the PIN on the TV.",
						map[string]any{"pending": true, "next": invokedAs() + " pin <digits>", "brand": tv.Brand, "name": tv.Name, "host": tv.Host})
				}
				if p, ok := dev.(device.Pairer); ok {
					line := "Allow it on the TV."
					if _, pin := dev.(device.PINPairer); pin {
						line = "Read the PIN on the TV."
					}
					e.progress(line)
					if token, err = p.Pair(cx, e.env.Ask); err != nil {
						return err
					}
				}
			}
			return e.finishPair(cx, dev, tv, token)
		},
	}
	c.Flags().StringVar(&token, "token", "", "import an existing client token instead of pairing")
	c.Flags().StringVar(&brand, "brand", "", "the TV's brand ("+strings.Join(device.Brands(), ", ")+") when it can't be discovered")
	return c
}

// pickTV finds the TV to pair: the host given (its brand from discovery, or
// --brand), else the only TV found, else the user's choice.
func (e *cmds) pickTV(cx context.Context, args []string, brand string) (device.Found, error) {
	host := ""
	if len(args) == 1 {
		host = args[0]
		if brand != "" {
			return device.Found{Host: host, Brand: brand, Name: host}, nil
		}
	}
	found, err := device.Discover(cx, 2500*time.Millisecond)
	if err != nil {
		return device.Found{}, err
	}
	if host != "" {
		for _, f := range found {
			if f.Host == host || strings.EqualFold(f.Name, host) {
				return f, nil
			}
		}
		return device.Found{}, device.Failf("no TV answered at %s", host).Notef("if it's on, pass its brand: tv pair %s --brand %s", host, strings.Join(device.Brands(), "|"))
	}
	switch len(found) {
	case 0:
		return device.Found{}, device.Fail("no TVs found").Notef("pass the IP: tv pair 192.168.1.20 --brand %s", strings.Join(device.Brands(), "|"))
	case 1:
		return found[0], nil
	}
	if !e.env.Interactive {
		names := make([]string, len(found))
		for i, f := range found {
			names[i] = fmt.Sprintf("%s (%s)", f.Name, f.Host)
		}
		return device.Found{}, device.Failf("found %d TVs, say which: %s", len(found), strings.Join(names, ", "))
	}
	for i, f := range found {
		fmt.Fprintf(e.env.Out, "%d) %-16s %-8s %s\n", i+1, f.Host, f.Brand, f.Name)
	}
	s, err := e.env.Ask("which one? ")
	if err != nil {
		return device.Found{}, err
	}
	n, _ := strconv.Atoi(s)
	if n < 1 || n > len(found) {
		return device.Found{}, device.Fail("no such choice")
	}
	return found[n-1], nil
}

func (e *cmds) devices() *cobra.Command {
	var scan bool
	c := &cobra.Command{
		Use:         "devices",
		Short:       "List paired TVs (--scan: find TVs on this network instead)",
		Annotations: meta("", "6s"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if scan {
				return e.scan(cmd)
			}
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
	c.Flags().BoolVar(&scan, "scan", false, "find TVs on this network (paired or not)")
	return c
}

// scan is devices --scan: what answers on this network right now.
func (e *cmds) scan(cmd *cobra.Command) error {
	cx, cancel := ctx(cmd)
	defer cancel()
	found, err := device.Discover(cx, 2500*time.Millisecond)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return device.Fail("no TVs found on this network")
	}
	var lines []string
	for _, f := range found {
		line := fmt.Sprintf("%-16s %-8s %s", f.Host, f.Brand, f.Name)
		if f.Model != "" && !strings.Contains(f.Name, f.Model) {
			line += "  (" + f.Model + ")"
		}
		lines = append(lines, line)
	}
	e.emit(strings.Join(lines, "\n"), found)
	return nil
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
			flat := map[string]any{}
			flatten("", m, flat)
			keys := make([]string, 0, len(flat))
			for k := range flat {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			lines := []string{fmt.Sprintf("%s  %s  %s", info.Name, info.Host, info.Brand)}
			for _, k := range keys {
				lines = append(lines, fmt.Sprintf("  %-32s %v", k, flat[k]))
			}
			e.emit(strings.Join(lines, "\n"), map[string]any{"device": info, "properties": m})
			return nil
		}),
	}
}

// flatten turns nested status maps into dotted keys ("connection.wifi.mac")
// so the plain listing is one fact per line instead of Go map syntax.
func flatten(prefix string, v any, out map[string]any) {
	m, ok := v.(map[string]any)
	if !ok {
		out[prefix] = v
		return
	}
	for k, val := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		flatten(key, val, out)
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
		Use:         "key <key>...",
		Short:       "Press remote keys (home, back, select, up, down, …)",
		Long:        "Keys: " + strings.Join(keys, " ") + ". Several can be given at once: key down down select.",
		Args:        cobra.MinimumNArgs(1),
		ValidArgs:   keys,
		Annotations: meta("", "5s", wake),
		RunE: e.run(func(s *session, args []string) error {
			for _, a := range args {
				if err := s.dev.Key(s.ctx, device.Key(a)); err != nil {
					return err
				}
			}
			return e.ok("", map[string]any{"keys": args})
		}),
	}
}

func (e *cmds) vol() *cobra.Command {
	return &cobra.Command{
		Use:         "vol up|down|mute [n]",
		Short:       "Volume up/down n steps, or mute",
		Args:        cobra.RangeArgs(1, 2),
		ValidArgs:   []string{"up", "down", "mute"},
		Annotations: meta("", "8s", wake),
		RunE: e.run(func(s *session, args []string) error {
			n := 1
			if len(args) == 2 {
				var err error
				if n, err = strconv.Atoi(args[1]); err != nil || n < 1 || n > 50 {
					return device.Fail("volume steps go from 1 to 50")
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
				return device.Fail("say vol up, vol down or vol mute")
			}
			for i := 0; i < n; i++ {
				if err := s.dev.Key(s.ctx, k); err != nil {
					return err
				}
			}
			return e.ok("", map[string]any{"action": args[0], "steps": n})
		}),
	}
}

func (e *cmds) sleep() *cobra.Command {
	return &cobra.Command{
		Use: "sleep", Short: "Put the TV to sleep (same as key sleep)", Args: cobra.NoArgs,
		Annotations: meta("", "8s"),
		RunE: e.run(func(s *session, _ []string) error {
			if err := s.dev.Key(s.ctx, device.KeySleep); err != nil {
				return err
			}
			return e.done("TV off.", nil)
		}),
	}
}

func (e *cmds) wake() *cobra.Command {
	return &cobra.Command{
		Use: "wake", Short: "Wake the TV (Wake-on-LAN if a MAC is configured, then protocol wake)", Args: cobra.NoArgs,
		Annotations: meta("", "30s"),
		RunE: e.run(func(s *session, _ []string) error {
			w, err := need[device.Waker](s.dev, "wake")
			if err != nil {
				return err
			}
			if err := w.Wake(s.ctx); err != nil {
				return err
			}
			return e.done("TV on.", nil)
		}),
	}
}

func (e *cmds) text() *cobra.Command {
	return &cobra.Command{
		Use:         "text <string>...",
		Short:       "Type text into the focused field on the TV",
		Args:        cobra.MinimumNArgs(1),
		Annotations: meta("", "", wake),
		RunE: e.run(func(s *session, args []string) error {
			str := strings.Join(args, " ")
			if kw, ok := s.dev.(device.KeyboardWatcher); ok {
				if open, _, err := kw.KeyboardOpen(s.ctx); err == nil && !open {
					return device.Fail("the TV's keyboard isn't open, so there's nothing to type into").
						Note("Netflix and YouTube draw their own keyboards: use search netflix <words>, search youtube <words>, or say ...")
				}
			}
			if err := s.dev.Text(s.ctx, str); err != nil {
				return err
			}
			return e.ok("", map[string]any{"text": str})
		}),
	}
}

func (e *cmds) say() *cobra.Command {
	return &cobra.Command{
		Use:         "say <phrase>...",
		Short:       "Speak to the TV's voice assistant (e.g. say switch to HDMI 1)",
		Args:        cobra.MinimumNArgs(1),
		Annotations: meta("", "25s", wake),
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
			return e.ok("", map[string]any{"phrase": phrase, "seconds": secs})
		}),
	}
}

// ---- app verbs ------------------------------------------------------------

func (e *cmds) openCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "open <app|link>",
		Aliases:     []string{"app", "url"},
		Annotations: meta("", "", wake),
		Short:       "Open an app (by alias, name prefix or package) or a link",
		Long: "Apps: " + strings.Join(AliasNames(), ", ") + "; anything else matches the start of an\n" +
			"app's name (see `tv apps`), or is an app id when it contains a dot.\n" +
			"Links: YouTube links play in the YouTube app; Netflix titles open in\n" +
			"Netflix; other web pages open in the browser.",
		Args: cobra.ExactArgs(1),
		RunE: e.run(func(s *session, args []string) error {
			if isLink(args[0]) {
				if err := s.openLink(args[0]); err != nil {
					return err
				}
				return e.done("Opening it on the TV.", map[string]any{"url": args[0]})
			}
			id, name, err := s.openApp(args[0])
			if err != nil {
				return err
			}
			return e.done("Opening "+name+".", map[string]any{"app": id, "name": name})
		}),
	}
}

func (e *cmds) apps() *cobra.Command {
	return &cobra.Command{
		Use:   "apps",
		Short: "List apps on the TV",
		RunE: e.run(func(s *session, _ []string) error {
			apps, err := s.apps()
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
