package tui

import (
	"bytes"
	"errors"
	"regexp"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/omAthalye/tvctl/internal/cli"
	"github.com/omAthalye/tvctl/internal/device"
)

// builtins are the prompt-only words the TUI handles itself; everything
// else is a CLI verb. Resolver, command check and completion all read this.
var builtins = []string{"help", "keys", "files", "wallpaper", "view", "clear", "quit", "exit"}

func isBuiltin(word string) bool {
	for _, b := range builtins {
		if word == b {
			return true
		}
	}
	return false
}

var urlish = regexp.MustCompile(`^(https?://|www\.|[a-z0-9-]+(\.[a-z0-9-]+)+(/|$))`)

// resolve implements the precedence: built-in word, alias (bare opens,
// with words searches unless a verb owns the word), verb, link, app.
// local non-nil means the line is handled in-process (help, keys, quit)
// and returns the tea.Cmd to run, if any.
func (m *model) resolve(line string) (argv []string, local func() tea.Cmd) {
	fields := strings.Fields(line)
	head := strings.ToLower(fields[0])
	switch head {
	case "quit", "exit":
		return nil, func() tea.Cmd { return tea.Quit }
	case "clear":
		return nil, func() tea.Cmd { m.console.clear(); return nil }
	case "keys":
		return nil, func() tea.Cmd {
			m.mode = modeKeys
			m.console.say(styleDim.Render("  keys mode: your arrow keys drive the TV · esc or q to leave"))
			return nil
		}
	case "help":
		return nil, func() tea.Cmd { m.help(fields[1:]); return nil }
	case "files":
		return nil, func() tea.Cmd { return m.player.chooseFiles("files") }
	case "wallpaper":
		if len(fields) == 1 {
			return nil, func() tea.Cmd { return m.player.chooseFiles("wallpaper") }
		}
		return append([]string{"home", "wallpaper"}, fields[1:]...), nil // wallpaper off
	case "next":
		if m.player.active() {
			return nil, func() tea.Cmd { m.player.next(); return nil }
		}
	case "stop":
		if m.player.active() {
			return nil, func() tea.Cmd { m.player.stop("stopped"); return nil }
		}
	case "play":
		if len(fields) == 1 {
			return []string{"resume"}, nil
		}
	case "shot":
		return nil, func() tea.Cmd { return tea.Batch(m.console.begin("shot"), m.player.screenshot()) }
	case "view":
		return nil, func() tea.Cmd { return m.player.viewShot() }
	}
	// An app alias alone launches the app; with words after it, it is a
	// search inside that app ("netflix" opens Netflix, "netflix dark"
	// searches it). A verb of the same name wins over the search shortcut,
	// which is how "youtube <words>" plays the top result instead.
	if _, isAlias := cli.Aliases[head]; isAlias {
		if len(fields) == 1 {
			return []string{"open", head}, nil
		}
		if !m.isVerb(head) {
			return append([]string{"search", head}, fields[1:]...), nil
		}
	}
	if m.isVerb(head) {
		fields[0] = head // verbs are lowercase; accept "YouTube foo"
		return fields, nil
	}
	if urlish.MatchString(strings.ToLower(fields[0])) {
		return []string{"open", fields[0]}, nil
	}
	// Anything else is an app name; open resolves prefixes and produces
	// the "did you mean" error.
	return []string{"open", line}, nil
}

// isVerb accepts verb names and their aliases ("app", "yt", "pp"), while
// completion (m.verbs) offers only the canonical names.
func (m *model) isVerb(word string) bool {
	if m.verbWords == nil {
		m.verbWords = map[string]bool{}
		for _, v := range cli.VerbWords() {
			m.verbWords[v] = true
		}
	}
	return m.verbWords[word]
}

// noAsk is the TUI's answer to a command that wants to prompt (pairing):
// not here. Pairing is a one-time terminal job.
func noAsk(prompt string) (string, error) {
	return "", errors.New(prompt + ": run this one as `tv pair` in a terminal")
}

// help renders cobra's own help for the root or one verb into the
// scrollback, plus the TUI-only words.
func (m *model) help(args []string) {
	var buf bytes.Buffer
	root := cli.Root(&cli.Env{Out: &buf, Ask: noAsk})
	root.SetArgs(append([]string{"help"}, args...))
	_ = root.Execute()
	say := m.console.say
	for _, l := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		say("  " + l)
	}
	if len(args) == 0 {
		say("")
		say("  " + styleBold.Render("Prompt shortcuts"))
		say("  <app>             open it: " + strings.Join(cli.AliasNames(), " · ") + ", or any prefix of an app's name")
		say("  <app> <words>     search inside that app (netflix dark); youtube <words> plays the top result")
		say("  <link>            open it (YouTube links play directly)")
		say("  keys              remote mode: arrow keys go to the TV until esc")
		say("  files             pick files or a folder in yazi, play them on the TV in order")
		say("  wallpaper         pick an image in yazi, set it as the home dashboard's background")
		say("  pause · resume · toggle · stop · next · prev · seek +30   media controls for any app")
		say("  next · stop       with a playlist running: skip to the next file · end the playlist")
		say("  shot · view       screenshot the TV · open the last one in yazi")
		say("  help <command>    details for one command")
		say("  clear · quit      ctrl+l also clears, ctrl+c quits")
	}
}

// complete computes suggestions for the current input. The first word
// completes over verbs, app names and shortcuts; later words complete over
// the verb's own choices (vol up|down|mute, key names, app names).
func (m *model) complete(val string) []match {
	if strings.TrimSpace(val) == "" {
		return nil
	}
	fields := strings.Fields(val)
	typingFirst := len(fields) == 1 && !strings.HasSuffix(val, " ")
	var cands []string
	switch {
	case typingFirst:
		cands = append(cands, m.verbs...)
		cands = append(cands, builtins...)
		cands = append(cands, cli.AliasNames()...)
		for _, a := range m.apps {
			cands = append(cands, a.Name)
		}
	default:
		switch strings.ToLower(fields[0]) {
		case "vol":
			cands = []string{"up", "down", "mute"}
		case "key":
			for _, k := range device.AllKeys {
				cands = append(cands, string(k))
			}
		case "open", "app":
			cands = append(cands, cli.AliasNames()...)
			for _, a := range m.apps {
				cands = append(cands, a.Name)
			}
		case "help":
			cands = m.verbs
		}
	}
	sort.Strings(cands)
	cands = dedupe(cands)
	last := ""
	if !strings.HasSuffix(val, " ") {
		last = fields[len(fields)-1]
	}
	return fuzzyRank(last, cands, 6)
}

// dedupe drops case-insensitive duplicates ("Netflix" the app vs "netflix"
// the alias), keeping the first, which after sorting is the capitalised
// app name.
func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		k := strings.ToLower(s)
		if !seen[k] {
			seen[k] = true
			out = append(out, s)
		}
	}
	return out
}
