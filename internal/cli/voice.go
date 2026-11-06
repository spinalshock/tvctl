package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/omAthalye/tvctl/internal/config"
	"github.com/omAthalye/tvctl/internal/device"
)

// tvctl is driven by people, scripts and voice assistants alike. An
// assistant runs `tv …` and reads the plain output aloud, so that output
// is one short sentence, or nothing for a routine ack the TV shows on its
// own screen. Every command also has a deadline, so a TV that went away
// costs seconds, not a hung conversation. Deadlines and the companion-app
// requirement live in cobra Annotations so `tv tools` can publish them.

const (
	annNeeds    = "needs"    // "companion": the capability comes from the companion add-on (Android TVs)
	annDeadline = "deadline" // Go duration, "none" for streaming verbs; "" = default
	annWake     = "wake"     // "yes": wake a sleeping TV before running

	needsCompanion  = "companion"
	wake            = "wake" // flag for meta: the verb changes something, so wake the TV first
	defaultDeadline = 15 * time.Second
)

// meta builds a command's annotations. needs is "" or needsCompanion;
// deadline is a duration string, "none", or "" for the default; flags
// may include wake.
func meta(needs, deadline string, flags ...string) map[string]string {
	m := map[string]string{}
	if needs != "" {
		m[annNeeds] = needs
	}
	if deadline != "" {
		m[annDeadline] = deadline
	}
	for _, f := range flags {
		if f == wake {
			m[annWake] = "yes"
		}
	}
	return m
}

// wakes reports whether a verb wakes a sleeping TV before running.
func wakes(cmd *cobra.Command) bool { return cmd != nil && cmd.Annotations[annWake] == "yes" }

// deadlineFor reads a command's deadline annotation; 0 means none.
func deadlineFor(cmd *cobra.Command) time.Duration {
	if cmd == nil {
		return defaultDeadline
	}
	switch v := cmd.Annotations[annDeadline]; v {
	case "":
		return defaultDeadline
	case "none":
		return 0
	default:
		d, err := time.ParseDuration(v)
		if err != nil {
			return defaultDeadline
		}
		return d
	}
}

// sentence capitalises a clause and ends it with a full stop.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Capitalise the first word unless it is a proper noun already carrying
	// a capital ("webOS", "iPhone"), which TTS would otherwise mangle.
	first := strings.Fields(s)[0]
	if strings.ToLower(first) == first {
		r := []rune(s)
		r[0] = unicode.ToUpper(r[0])
		s = string(r)
	}
	if !strings.ContainsAny(s[len(s)-1:], ".!?") {
		s += "."
	}
	return s
}

// spoken is the one sentence an assistant reads aloud for an error. Our
// own errors are device.Problems and carry it. Anything else came from a
// library or the OS (a refused connection, a missing file): its first
// line, cut before any cause chain, is the best that can be done.
func spoken(err error) string {
	var p *device.Problem
	if errors.As(err, &p) {
		return sentence(p.Said())
	}
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if i := strings.Index(msg, ": "); i > 0 {
		msg = msg[:i]
	}
	return sentence(strings.TrimSuffix(strings.TrimSpace(msg), "."))
}

// matchInput finds the input a person meant: its short name ("hdmi1"),
// the TV's id ("HDMI_1"), or part of the owner's label ("reliance" for
// "Reliance Set-Top Box"). want is lowercase.
func matchInput(inputs []device.Input, want string) *device.Input {
	for i := range inputs {
		in := &inputs[i]
		if in.Name == want || strings.EqualFold(in.ID, want) || strings.Contains(strings.ToLower(in.Label), want) {
			return in
		}
	}
	return nil
}

// spokenInput is what people call an input: "hdmi1" -> "HDMI 1".
func spokenInput(name string) string {
	n := strings.ToLower(strings.ReplaceAll(name, "_", ""))
	for _, p := range []string{"hdmi", "av", "component", "usb"} {
		if rest := strings.TrimPrefix(n, p); rest != n {
			if rest == "" {
				return strings.ToUpper(p)
			}
			return strings.ToUpper(p) + " " + rest
		}
	}
	return name
}

// progress prints an instruction before a wait (pairing's "Allow it on
// the TV."), as its own JSON line in --json mode, so whoever is listening
// hears it while there is still time to act; the final document follows.
func (e *cmds) progress(line string) {
	if e.env.JSON {
		_ = json.NewEncoder(e.env.Out).Encode(map[string]any{"status": "waiting", "message": line})
		return
	}
	fmt.Fprintln(e.env.Out, line)
}

// finishPair stores a completed pairing: the TV's stable id and facts from
// its status call, its MAC for Wake-on-LAN, and the token. It is shared by
// `pair` and `pin`.
func (e *cmds) finishPair(cx context.Context, dev device.Device, tv device.Found, token string) error {
	// Status also fills in the TV's stable id (Fire TV serial, LG device
	// UUID): the IP may change with DHCP, the id never does. A status
	// failure is reported but never costs the pairing.
	var props map[string]any
	var err error
	if s, ok := dev.(device.Statuser); ok {
		if props, err = s.Status(cx); err != nil {
			e.progress("Couldn't read the TV's details; paired anyway.")
		}
	}
	st, err := config.Load()
	if err != nil {
		return err
	}
	info := dev.Info()
	ent := config.Entry{Name: tv.Name, Brand: tv.Brand, Host: tv.Host, MAC: learnMAC(cx, dev, tv.Host), Token: token}
	if old, ok := st.Devices[info.ID]; ok {
		if ent.MAC == "" {
			ent.MAC = old.MAC
		}
		ent.CompanionToken = old.CompanionToken // re-pairing keeps the companion app
	}
	st.Put(info.ID, ent)
	st.Pending = nil
	if err := st.Save(); err != nil {
		return err
	}
	return e.done("Connected to "+tv.Name+".",
		map[string]any{"id": info.ID, "brand": tv.Brand, "name": tv.Name, "host": tv.Host, "mac": ent.MAC, "properties": props})
}

// pin finishes a pairing that `tv pair` left waiting for its PIN. The two
// halves are independent requests on every brand that uses a PIN, so no
// process has to stay alive in between.
func (e *cmds) pin() *cobra.Command {
	return &cobra.Command{
		Use:         "pin <digits>",
		Short:       "Finish pairing with the PIN the TV is showing (after tv pair)",
		Args:        cobra.ExactArgs(1),
		Annotations: meta("", "20s"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx(cmd)
			defer cancel()
			st, err := config.Load()
			if err != nil {
				return err
			}
			p := st.Pending
			if p == nil {
				return device.Fail("no pairing is waiting for a PIN").Note("run tv pair first")
			}
			if time.Since(p.Started) > 10*time.Minute {
				st.Pending = nil
				_ = st.Save()
				return device.Fail("that PIN has expired").Note("run tv pair again")
			}
			dev, err := device.Open(device.Config{Brand: p.Brand, Name: p.Name, Host: p.Host})
			if err != nil {
				return err
			}
			pp, ok := dev.(device.PINPairer)
			if !ok {
				return device.Failf("%s doesn't pair with a PIN", p.Name)
			}
			digits := strings.Map(func(r rune) rune {
				if unicode.IsDigit(r) {
					return r
				}
				return -1
			}, args[0])
			token, err := pp.VerifyPIN(cx, digits)
			if errors.Is(err, context.DeadlineExceeded) {
				return device.Failf("%s didn't answer in time", p.Name).Because(err)
			}
			if err != nil {
				return device.Failf("%s didn't accept that PIN", p.Name).Because(err)
			}
			return e.finishPair(cx, dev, device.Found{Host: p.Host, Brand: p.Brand, Name: p.Name}, token)
		},
	}
}

// input switches external inputs. Brands with an input API switch
// directly; the rest ask their voice assistant, which is what people do
// with the remote's microphone anyway.
func (e *cmds) input() *cobra.Command {
	return &cobra.Command{
		Use:   "input <hdmi1|hdmi2|…|name|list>",
		Short: "Switch to an HDMI input (or list them)",
		Long: `Switch the TV to an input by its short name (hdmi1 … hdmi4) or the label
the owner gave it on the TV ("PS5"). "list" shows what the TV has. A TV
with an input API switches at once; one without asks its voice assistant,
which takes a few seconds.`,
		Args:        cobra.ExactArgs(1),
		ValidArgs:   []string{"hdmi1", "hdmi2", "hdmi3", "hdmi4", "list"},
		Annotations: meta("", "25s", wake),
		RunE: e.run(func(s *session, args []string) error {
			want := strings.ToLower(args[0])
			if sw, ok := s.dev.(device.InputSwitcher); ok {
				if want == "list" {
					inputs, err := sw.Inputs(s.ctx)
					if err != nil {
						return err
					}
					var lines []string
					for _, in := range inputs {
						mark := " "
						if in.Connected {
							mark = "*"
						}
						lines = append(lines, fmt.Sprintf("%s %-8s %s", mark, in.Name, in.Label))
					}
					e.emit(strings.Join(lines, "\n"), inputs)
					return nil
				}
				inputs, err := sw.Inputs(s.ctx)
				if err != nil {
					return err
				}
				hit := matchInput(inputs, want)
				if hit == nil {
					return device.Failf("%s has no input called %s", tvName(s.dev), args[0])
				}
				if err := sw.SwitchInput(s.ctx, hit.ID); err != nil {
					return err
				}
				said := hit.Label
				if said == "" {
					said = spokenInput(hit.Name)
				}
				return e.done("Switched to "+said+".", map[string]any{"input": hit.Name, "id": hit.ID, "label": hit.Label, "via": "tv"})
			}
			if want == "list" {
				return device.Failf("%s can't list its inputs", tvName(s.dev))
			}
			sp, err := need[device.Speaker](s.dev, "input switching")
			if err != nil {
				return err
			}
			if err := sp.Say(s.ctx, "switch to "+spokenInput(want)); err != nil {
				return err
			}
			return e.done("Switching to "+spokenInput(want)+".", map[string]any{"input": want, "via": "voice"})
		}),
	}
}

// Tool is one command as an assistant sees it. Generated from the command
// tree, so the list of tools is the list of verbs by construction.
type Tool struct {
	Name     string   `json:"name"`               // "key", "home add"
	Usage    string   `json:"usage"`              // cobra's Use line
	Short    string   `json:"description"`        // one line
	Aliases  []string `json:"aliases,omitempty"`  // other words for it
	Choices  []string `json:"choices,omitempty"`  // the first argument's fixed values
	Needs    string   `json:"needs,omitempty"`    // "companion" when the companion app is required
	Wakes    bool     `json:"wakes,omitempty"`    // wakes a sleeping TV first
	Blocking bool     `json:"blocking,omitempty"` // runs until stopped (events, play of files)
	Deadline string   `json:"deadline,omitempty"` // how long it may take
	use      string   // cobra's Use without the name, for the schema
}

// Tools lists the command tree as tools.
func Tools() []Tool {
	var out []Tool
	var walk func(prefix string, cs []*cobra.Command)
	walk = func(prefix string, cs []*cobra.Command) {
		for _, c := range cs {
			if c.Hidden || c.Name() == "completion" || c.Name() == "help" || c.Name() == "tools" || c.Name() == "init" {
				continue
			}
			t := Tool{Name: strings.TrimSpace(prefix + " " + c.Name()), Usage: strings.TrimSpace(prefix + " " + c.Use),
				Short: c.Short, Aliases: c.Aliases, Choices: c.ValidArgs, Needs: c.Annotations[annNeeds], Wakes: wakes(c),
				use: strings.TrimSpace(strings.TrimPrefix(c.Use, c.Name()))}
			if d := deadlineFor(c); d == 0 {
				t.Blocking = true
			} else {
				t.Deadline = d.String()
			}
			out = append(out, t)
			walk(t.Name, c.Commands())
		}
	}
	walk("", Root(&Env{Out: io.Discard}).Commands())
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// tools prints the manifest an assistant loads: every verb with its
// arguments, what it needs, how long it may take, plus the paired TVs by
// name so "on the bedroom TV" can become --device.
func (e *cmds) tools() *cobra.Command {
	var schema, voice bool
	c := &cobra.Command{
		Use:   "tools",
		Short: "Describe every command as a tool, in JSON, for assistants and scripts",
		Long: `Prints JSON: each command with its usage, fixed choices, whether it needs
the companion app and how long it may take, then the paired TVs. Each
command's plain output is one short sentence fit to be read aloud, or
nothing for a routine ack; --json gives the full document instead.

--schema prints function declarations in the shape tool-calling models
take (OpenAI style: name, description, parameters). Each tool's parameters
are its positional arguments in order, so argv is the parameter values in
that order after the tool's words: tv_home_add {"app": "netflix"} runs
  tv home add netflix
--voice keeps only the verbs a person would say to an assistant.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if schema {
				enc := json.NewEncoder(e.env.Out)
				enc.SetIndent("", "  ")
				return enc.Encode(Schema(voice))
			}
			type dev struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Brand   string `json:"brand"`
				Default bool   `json:"default"`
			}
			var devs []dev
			if st, err := config.Load(); err == nil {
				for id, d := range st.Devices {
					devs = append(devs, dev{id, d.Name, d.Brand, id == st.Default})
				}
				sort.Slice(devs, func(i, j int) bool { return devs[i].Name < devs[j].Name })
			}
			enc := json.NewEncoder(e.env.Out)
			enc.SetIndent("", "  ")
			tools := Tools()
			if voice {
				tools = voiceTools(tools)
			}
			return enc.Encode(map[string]any{
				"command": invokedAs(),
				"flags":   map[string]string{"--device": "TV id or name; default: the default TV", "--json": "machine-readable output"},
				"tools":   tools,
				"devices": devs,
			})
		},
	}
	c.Flags().BoolVar(&schema, "schema", false, "function declarations for a tool-calling model")
	c.Flags().BoolVar(&voice, "voice", false, "only the verbs a person says (no setup, no streaming, no companion-app extras)")
	return c
}

// voiceVerbs are what a person says to an assistant. Setup, streaming and
// terminal-only verbs are left out so a small model has less to choose
// from. Companion-app verbs stay out too: the TVs an assistant drives
// mostly won't have it.
var voiceVerbs = map[string]bool{
	"key": true, "vol": true, "input": true, "sleep": true, "wake": true, "open": true, "youtube": true,
	"search": true, "play": true, "pause": true, "stop": true, "seek": true, "now": true, "toast": true, "say": true,
}

func voiceTools(all []Tool) []Tool {
	var out []Tool
	for _, t := range all {
		if voiceVerbs[t.Name] {
			out = append(out, t)
		}
	}
	return out
}

// Schema renders the tools as OpenAI-style function declarations: the
// shape FunctionGemma, llama.cpp and most tool-calling runtimes accept.
// Parameters are the verb's positional arguments in order, parsed from
// its Use line: <name> required, [name] optional, a|b|c an enum, and a
// trailing ... a free string of words.
func Schema(voice bool) []map[string]any {
	tools := Tools()
	if voice {
		tools = voiceTools(tools)
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		props, required := schemaParams(t)
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "tv_" + strings.ReplaceAll(t.Name, " ", "_"),
				"description": t.Short,
				"parameters": map[string]any{
					"type":       "object",
					"properties": props,
					"required":   required,
				},
			},
		})
	}
	return out
}

// param is one positional argument as a JSON-schema property.
type param struct {
	name string
	prop map[string]any
}

// orderedProps marshals as a JSON object in slice order, since a tool's
// parameters are positional and JSON parsers keep key order.
type orderedProps []param

func (o orderedProps) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, p := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(p.name)
		v, err := json.Marshal(p.prop)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

// schemaParams turns a Use line ("key <key>...", "vol up|down|mute [n]",
// "input <hdmi1|hdmi2|…|name|list>") into properties in argument order.
// The first argument takes the command's ValidArgs as its enum when it
// has them; other a|b|c tokens become enums of their own; a trailing ...
// is a free string of words.
func schemaParams(t Tool) (orderedProps, []string) {
	var props orderedProps
	required := []string{}
	for i, tok := range strings.Fields(t.use) {
		if strings.HasPrefix(tok, "-") || tok == "|" {
			continue // flags are not spoken
		}
		variadic := strings.HasSuffix(tok, "...")
		tok = strings.TrimSuffix(tok, "...")
		req := true
		if strings.HasPrefix(tok, "[") && strings.HasSuffix(tok, "]") {
			tok, req = tok[1:len(tok)-1], false
		}
		tok = strings.Trim(tok, "<>")
		alts := strings.Split(tok, "|")
		name := alts[0]
		p := map[string]any{"type": "string"}
		switch {
		case i == 0 && len(t.Choices) > 0:
			// Only declared ValidArgs become an enum; "a|b" in a Use line
			// may be placeholders ("app|link"), not literal values.
			p["enum"] = t.Choices
			if len(alts) > 1 {
				name = t.Name // "vol up|down|mute": the choice is the verb's own argument
			}
			if variadic {
				p["description"] = "one or more, space separated"
			}
		case variadic:
			p["description"] = "the words, space separated"
		case len(alts) > 1:
			p["description"] = strings.Join(alts, " or ")
		}
		name = strings.NewReplacer("…", "", ".", "_", "-", "_", "+", "", ":", "").Replace(name)
		if name == "" {
			name = "value"
		}
		props = append(props, param{name, p})
		if req {
			required = append(required, name)
		}
	}
	return props, required
}

// nowLine is the rich now-playing document as one sentence; name turns
// an app id into what people call it (session.appTitle).
func nowLine(n *device.Playing, name func(string) string) string {
	if n == nil || n.Source == nil {
		if n != nil && n.Foreground != nil && *n.Foreground != "" {
			return name(*n.Foreground) + " is open."
		}
		return "Nothing is playing."
	}
	what := ""
	if n.Title != nil && *n.Title != "" {
		what = *n.Title
		if n.Artist != nil && *n.Artist != "" {
			what += " by " + *n.Artist
		}
	}
	state := n.State
	if state == "" {
		state = "playing"
	}
	if what == "" {
		return name(n.App) + " is " + state + "."
	}
	return fmt.Sprintf("%s, %s on %s.", what, state, name(n.App))
}
