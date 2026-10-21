// Package tui is a text-first front end: a prompt with fuzzy completion, a
// scrollback of commands and their output, and a status bar. It executes
// the cobra command tree from internal/cli in-process, so every verb the
// CLI has works here with the same output, and nothing is defined twice.
//
// Layout, top to bottom:
//
//	header     device name, host, receiver state
//	scrollback what you ran and what came back (scrolls)
//	suggests   up to six completion candidates while typing
//	prompt     "> " + input
//	status     mode hints, or a running indicator
package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/omAthalye/tvctl/internal/cast"
	"github.com/omAthalye/tvctl/internal/cli"
	"github.com/omAthalye/tvctl/internal/config"
	"github.com/omAthalye/tvctl/internal/device"
	"github.com/omAthalye/tvctl/internal/firetv"
	"github.com/omAthalye/tvctl/internal/receiver"
)

var debugLog io.Writer

// Run starts the program and blocks until the user quits.
func Run() error {
	if p := os.Getenv("TVCTL_DEBUG"); p != "" {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err == nil {
			debugLog = f
		}
	}
	m, err := newModel()
	if err != nil {
		return err
	}
	_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

// ---- model ----------------------------------------------------------------

type mode int

const (
	modePrompt mode = iota // typing commands
	modeKeys               // arrow keys go straight to the TV
)

type model struct {
	input    textinput.Model
	view     viewport.Model
	lines    []string // scrollback, already styled
	history  []string
	histPos  int
	suggests []match
	sugSel   int
	mode     mode
	running  string // command currently executing, "" when idle
	width    int
	height   int

	devName string
	devHost string
	verbs   []string
	apps    []device.App // cached once at start, refreshed by `apps`

	// Companion-app state. events is fed by a goroutine holding the
	// receiver's WebSocket open; nil when the app isn't set up.
	rc       *receiver.Client
	rcState  string // "not set up", "offline", "online"
	now      *receiver.Now
	nowAt    time.Time // when `now` was received, for extrapolation
	keyboard string    // package asking for text, "" when none
	events   chan receiver.Event

	pl       *cast.Playlist // local files being served, nil when idle
	plGrace  int            // now-events to ignore right after starting an item
	lastShot string         // path of the last screenshot, for `view`
}

// Messages from background work.
type (
	resultMsg struct {
		cmd  string
		out  string
		err  error
		took time.Duration
	}
	appsMsg []device.App
	// eventMsg is one receiver event; rcStateMsg a connection change.
	eventMsg receiver.Event
	tickMsg  time.Time
	// chosenMsg carries yazi's selection; execDoneMsg any other external
	// program's exit.
	chosenMsg struct {
		paths []string
		err   error
	}
	execDoneMsg struct{ err error }
)

func tick() tea.Cmd { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }

func newModel() (*model, error) {
	st, err := config.Load()
	if err != nil {
		return nil, err
	}
	_, ent, err := st.Resolve("")
	if err != nil {
		return nil, err
	}
	in := textinput.New()
	in.Prompt = "> "
	in.Placeholder = "netflix · youtube.com/watch?v=… · vol up 3 · say switch to hdmi 1 · help"
	in.Focus()
	in.CharLimit = 512
	m := &model{
		input:   in,
		view:    viewport.New(80, 20),
		devName: ent.Name,
		devHost: ent.Host,
		verbs:   cli.Verbs(),
		rcState: "not set up",
		events:  make(chan receiver.Event, 32),
	}
	if ent.ReceiverToken != "" {
		m.rc = receiver.New(ent.Host, ent.ReceiverToken)
		m.rcState = "offline"
	}
	m.say(styleDim.Render("tvctl · type help for commands, keys for remote mode, quit to exit"))
	return m, nil
}

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink, loadApps()}
	if m.rc != nil {
		cmds = append(cmds, m.connectEvents(), m.waitEvent(), tick())
	}
	return tea.Batch(cmds...)
}

// connectEvents holds the receiver socket open for the TUI's lifetime,
// reconnecting with backoff. Events land on m.events; waitEvent turns
// them into Bubble Tea messages one at a time, which is the idiomatic way
// to bridge a long-lived goroutine into the update loop.
func (m *model) connectEvents() tea.Cmd {
	rc, ch := m.rc, m.events
	return func() tea.Msg {
		go func() {
			delay := time.Second
			for {
				err := rc.Events(context.Background(), func(ev receiver.Event) error {
					ch <- ev
					return nil
				})
				_ = err
				ch <- receiver.Event{Type: "_offline"}
				time.Sleep(delay)
				if delay < 15*time.Second {
					delay *= 2
				}
			}
		}()
		return nil
	}
}

func (m *model) waitEvent() tea.Cmd {
	ch := m.events
	return func() tea.Msg { return eventMsg(<-ch) }
}

// loadApps fetches the app list once so completion can offer app names
// without a round-trip per keystroke.
func loadApps() tea.Cmd {
	return func() tea.Msg {
		st, err := config.Load()
		if err != nil {
			return appsMsg(nil)
		}
		_, ent, err := st.Resolve("")
		if err != nil {
			return appsMsg(nil)
		}
		c := firetv.New(ent.Host, ent.Name, ent.Token)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		apps, err := c.Apps(ctx)
		if err != nil {
			return appsMsg(nil)
		}
		return appsMsg(apps)
	}
}

// ---- update ---------------------------------------------------------------

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case appsMsg:
		m.apps = msg
		return m, nil

	case tickMsg:
		return m, tick()

	case chosenMsg:
		if msg.err != nil {
			m.say(styleErr.Render("  yazi: " + msg.err.Error()))
			return m, nil
		}
		if len(msg.paths) == 0 {
			m.say(styleDim.Render("  nothing chosen"))
			return m, nil
		}
		m.playlistStart(msg.paths)
		return m, nil

	case execDoneMsg:
		if msg.err != nil {
			m.say(styleErr.Render("  " + msg.err.Error()))
		}
		return m, nil

	case eventMsg:
		switch msg.Type {
		case "_offline":
			m.rcState = "offline"
		case "now":
			m.rcState = "online"
			m.now = msg.Now
			m.nowAt = time.Now()
			if m.pl != nil {
				m.playlistObserve(msg.Now)
			}
		case "foreground":
			m.rcState = "online"
		case "keyboard":
			m.rcState = "online"
			if msg.Open != nil && *msg.Open {
				pkg := ""
				if msg.Package != nil {
					pkg = *msg.Package
				}
				m.keyboard = pkg
				m.say(styleDim.Render("⌨ TV wants text (" + pkg + "): type  text <your words>"))
			} else {
				m.keyboard = ""
			}
		}
		return m, m.waitEvent()

	case resultMsg:
		m.running = ""
		if msg.cmd == "shot" && msg.err == nil {
			m.lastShot = strings.SplitN(msg.out, "\n", 2)[0]
		}
		m.say(stylePrompt.Render("> ") + msg.cmd)
		out := strings.TrimRight(msg.out, "\n")
		if msg.err != nil && out == "" {
			out = "tv: " + msg.err.Error()
		}
		for _, l := range strings.Split(out, "\n") {
			if msg.err != nil {
				m.say("  " + styleErr.Render(l))
			} else {
				m.say("  " + l)
			}
		}
		m.say(styleDim.Render(fmt.Sprintf("  %.1fs", msg.took.Seconds())))
		return m, nil

	case tea.KeyMsg:
		if m.mode == modeKeys {
			return m.updateKeys(msg)
		}
		return m.updatePrompt(msg)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// updatePrompt handles typing. Tab cycles suggestions, Enter runs, Up/Down
// walk history when no suggestion is selected.
// Special keys are matched on msg.Type, never msg.String(): Bubble Tea
// batches runes that arrive together into one message whose String() is the
// concatenation, so a fast "up" would otherwise look like the up arrow.
func (m *model) updatePrompt(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyCtrlL:
		m.lines = nil
		m.refresh()
		return m, nil
	case tea.KeyTab:
		if len(m.suggests) > 0 {
			m.accept(m.suggests[m.sugSel].text)
			m.sugSel = 0
			m.complete()
		}
		return m, nil
	case tea.KeyShiftTab:
		if len(m.suggests) > 0 {
			m.sugSel = (m.sugSel + 1) % len(m.suggests)
		}
		return m, nil
	case tea.KeyUp:
		if m.histPos > 0 {
			m.histPos--
			m.input.SetValue(m.history[m.histPos])
			m.input.CursorEnd()
		}
		return m, nil
	case tea.KeyDown:
		if m.histPos < len(m.history)-1 {
			m.histPos++
			m.input.SetValue(m.history[m.histPos])
			m.input.CursorEnd()
		} else {
			m.histPos = len(m.history)
			m.input.SetValue("")
		}
		return m, nil
	case tea.KeyEnter:
		line := strings.TrimSpace(m.input.Value())
		m.input.SetValue("")
		m.suggests = nil
		m.layout()
		if line == "" {
			return m, nil
		}
		m.history = append(m.history, line)
		m.histPos = len(m.history)
		return m, m.dispatch(line)
	case tea.KeyEsc:
		m.input.SetValue("")
		m.suggests = nil
		return m, nil
	}
	var cmd tea.Cmd
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 && !msg.Paste {
		// Runes that arrived in one read (fast typing, some pastes) come as
		// one message. The text input matches its own bindings by string,
		// so a batch spelling "up" or "down" would hit the suggestion
		// bindings and vanish. Feed them one rune at a time instead.
		for _, r := range msg.Runes {
			m.input, cmd = m.input.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
	} else {
		m.input, cmd = m.input.Update(msg)
	}
	if debugLog != nil {
		fmt.Fprintf(debugLog, "key=%q type=%d paste=%v value=%q\n", msg.String(), msg.Type, msg.Paste, m.input.Value())
	}
	m.complete()
	m.layout()
	return m, cmd
}

// Keys mode mappings. Special keys by type; printable ones by rune. Space
// is play/pause because that's the universal media convention.
var (
	keyByType = map[tea.KeyType]device.Key{
		tea.KeyUp: device.KeyUp, tea.KeyDown: device.KeyDown, tea.KeyLeft: device.KeyLeft, tea.KeyRight: device.KeyRight,
		tea.KeyEnter: device.KeySelect, tea.KeyBackspace: device.KeyBack, tea.KeySpace: device.KeyPlayPause,
	}
	keyByRune = map[rune]device.Key{
		'h': device.KeyHome, 'm': device.KeyMenu, ' ': device.KeyPlayPause,
		'+': device.KeyVolumeUp, '-': device.KeyVolumeDown, '0': device.KeyMute,
		'>': device.KeyFastFwd, '<': device.KeyRewind,
	}
)

func (m *model) updateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyEsc || msg.Type == tea.KeyCtrlC || (msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == 'q') {
		m.mode = modePrompt
		m.say(styleDim.Render("keys mode off"))
		return m, nil
	}
	k, ok := keyByType[msg.Type]
	if !ok && msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
		k, ok = keyByRune[msg.Runes[0]]
	}
	if ok {
		// Fire and forget: a remote should feel instant, so we don't wait
		// for the result before accepting the next key, and we don't log
		// each press into the scrollback.
		return m, func() tea.Msg {
			env := &cli.Env{Out: &bytes.Buffer{}, Ask: noAsk}
			_ = cli.Run([]string{"key", string(k)}, env)
			return nil
		}
	}
	return m, nil
}

// dispatch turns a prompt line into argv and runs it in the background.
func (m *model) dispatch(line string) tea.Cmd {
	argv, local := m.resolve(line)
	if local != nil {
		return local()
	}
	m.running = line
	return func() tea.Msg {
		var buf bytes.Buffer
		t0 := time.Now()
		err := cli.Run(argv, &cli.Env{Out: &buf, Ask: noAsk})
		return resultMsg{cmd: line, out: buf.String(), err: err, took: time.Since(t0)}
	}
}

// noAsk is the TUI's answer to a command that wants to prompt (pairing):
// not here. Pairing is a one-time terminal job.
func noAsk(prompt string) (string, error) {
	return "", errors.New(prompt + ": run this one as `tv pair` in a terminal")
}

var urlish = regexp.MustCompile(`^(https?://|www\.|[a-z0-9-]+(\.[a-z0-9-]+)+(/|$))`)

// resolve implements the agreed precedence: built-in word, verb, URL, app.
// local non-nil means the line is handled in-process (help, keys, quit) and
// returns the tea.Cmd to run, if any.
func (m *model) resolve(line string) (argv []string, local func() tea.Cmd) {
	fields := strings.Fields(line)
	head := strings.ToLower(fields[0])
	switch head {
	case "quit", "exit":
		return nil, func() tea.Cmd { return tea.Quit }
	case "clear":
		return nil, func() tea.Cmd { m.lines = nil; m.refresh(); return nil }
	case "keys":
		return nil, func() tea.Cmd {
			m.mode = modeKeys
			m.say(styleDim.Render("keys mode: arrows · enter=select · backspace=back · h=home · m=menu · space=play · +/-/0 volume · esc to leave"))
			return nil
		}
	case "help":
		return nil, func() tea.Cmd { m.help(fields[1:]); return nil }
	case "files":
		return nil, func() tea.Cmd { return m.chooseFiles() }
	case "next":
		return nil, func() tea.Cmd { m.playlistNext(); return nil }
	case "stop":
		return nil, func() tea.Cmd { m.playlistStop("stopped"); return nil }
	case "shot":
		return nil, func() tea.Cmd { return m.screenshot() }
	case "view":
		return nil, func() tea.Cmd { return m.viewShot() }
	}
	for _, v := range m.verbs {
		if head == v {
			return fields, nil
		}
	}
	if urlish.MatchString(strings.ToLower(fields[0])) {
		return []string{"url", fields[0]}, nil
	}
	// Anything else is an app name; ResolveApp handles aliases and
	// prefixes and produces the "did you mean" error.
	return []string{"app", line}, nil
}

// help renders cobra's own help for the root or one verb into the
// scrollback, plus the TUI-only words.
func (m *model) help(args []string) {
	var buf bytes.Buffer
	root := cli.Root(&cli.Env{Out: &buf, Ask: noAsk})
	root.SetArgs(append([]string{"help"}, args...))
	_ = root.Execute()
	for _, l := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		m.say("  " + l)
	}
	if len(args) == 0 {
		m.say("")
		m.say("  " + styleBold.Render("Prompt shortcuts"))
		m.say("  <app name>        launch it (netflix, yt, or any prefix of an installed app)")
		m.say("  <url>             open it (YouTube links play directly)")
		m.say("  keys              remote mode: arrow keys go to the TV until esc")
		m.say("  files             pick files or a folder in yazi, play them on the TV in order")
		m.say("  next · stop       skip to the next file · end the playlist")
		m.say("  shot · view       screenshot the TV · open the last one in yazi")
		m.say("  help <command>    details for one command")
		m.say("  clear · quit      ctrl+l also clears, ctrl+c quits")
	}
}

// ---- files, playlist, screenshots ----------------------------------------

// chooseFiles hands the terminal to yazi in chooser mode. tea.ExecProcess
// suspends the TUI, runs the program, and restores the screen; the chosen
// paths come back through the temp file yazi writes on confirm.
func (m *model) chooseFiles() tea.Cmd {
	if m.rc == nil {
		m.say(styleErr.Render("  files needs the companion app: run tv setup"))
		return nil
	}
	if _, err := exec.LookPath("yazi"); err != nil {
		m.say(styleErr.Render("  yazi not found on PATH"))
		return nil
	}
	f, err := os.CreateTemp("", "tvctl-chooser-")
	if err != nil {
		m.say(styleErr.Render("  " + err.Error()))
		return nil
	}
	tmp := f.Name()
	f.Close()
	c := exec.Command("yazi", "--chooser-file="+tmp)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		defer os.Remove(tmp)
		if err != nil {
			return chosenMsg{err: err}
		}
		b, err := os.ReadFile(tmp)
		if err != nil {
			return chosenMsg{err: err}
		}
		var paths []string
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				paths = append(paths, l)
			}
		}
		return chosenMsg{paths: paths}
	})
}

func (m *model) playlistStart(paths []string) {
	files, err := cast.Expand(paths)
	if err != nil {
		m.say(styleErr.Render("  " + err.Error()))
		return
	}
	m.playlistStop("")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Device for the basic-mode bring-forward path.
	st, err := config.Load()
	if err != nil {
		m.say(styleErr.Render("  " + err.Error()))
		return
	}
	_, ent, _ := st.Resolve("")
	dev := firetv.New(ent.Host, ent.Name, ent.Token)
	pl, err := cast.Start(ctx, m.rc, dev, files)
	if err != nil {
		m.say(styleErr.Render("  " + err.Error()))
		return
	}
	m.pl = pl
	m.plGrace = 3
	m.sayPlaylist()
}

func (m *model) sayPlaylist() {
	i, n := m.pl.Index()
	m.say(fmt.Sprintf("  ▶ %d/%d %s  %s", i+1, n, m.pl.Current(), styleDim.Render("(serving from "+m.pl.Addr()+")")))
}

// playlistObserve is called on every now event while a playlist runs:
// advance when the item ends, stop when the user leaves the player.
func (m *model) playlistObserve(n *receiver.Now) {
	if m.plGrace > 0 {
		m.plGrace--
		if n.App == cast.ReceiverPackage && n.State == "playing" {
			m.plGrace = 0
		}
		return
	}
	if !cast.Finished(n) {
		return
	}
	if n.App != cast.ReceiverPackage {
		m.playlistStop("left the player")
		return
	}
	m.playlistNext()
}

func (m *model) playlistNext() {
	if m.pl == nil {
		m.say(styleDim.Render("  no playlist running"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	more, err := m.pl.Next(ctx)
	if err != nil {
		m.say(styleErr.Render("  " + err.Error()))
		return
	}
	if !more {
		m.playlistStop("finished")
		return
	}
	m.plGrace = 3
	m.sayPlaylist()
}

func (m *model) playlistStop(why string) {
	if m.pl == nil {
		return
	}
	m.pl.Close()
	m.pl = nil
	if why != "" {
		m.say(styleDim.Render("  playlist " + why))
	}
}

// screenshot saves a PNG under ~/Pictures/tv and remembers it for `view`.
func (m *model) screenshot() tea.Cmd {
	if m.rc == nil {
		m.say(styleErr.Render("  shot needs the companion app: run tv setup"))
		return nil
	}
	m.running = "shot"
	rc := m.rc
	return func() tea.Msg {
		t0 := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		png, err := rc.Screenshot(ctx)
		if err != nil {
			return resultMsg{cmd: "shot", err: err, took: time.Since(t0)}
		}
		home, _ := os.UserHomeDir()
		dir := home + "/Pictures/tv"
		_ = os.MkdirAll(dir, 0o755)
		path := dir + "/" + time.Now().Format("2006-01-02_15-04-05") + ".png"
		if err := os.WriteFile(path, png, 0o644); err != nil {
			return resultMsg{cmd: "shot", err: err, took: time.Since(t0)}
		}
		return resultMsg{cmd: "shot", out: path + "\n(type view to open it in yazi)", took: time.Since(t0)}
	}
}

// viewShot opens the last screenshot in yazi, which previews images
// inline using whatever graphics protocol the terminal supports.
func (m *model) viewShot() tea.Cmd {
	if m.lastShot == "" {
		m.say(styleDim.Render("  no screenshot yet: type shot"))
		return nil
	}
	c := exec.Command("yazi", m.lastShot)
	return tea.ExecProcess(c, func(err error) tea.Msg { return execDoneMsg{err: err} })
}

// ---- completion -----------------------------------------------------------

// complete recomputes suggestions for the current input. The first word
// completes over verbs, app names and shortcuts; later words complete over
// the verb's own choices (vol up|down|mute, key names, app names).
func (m *model) complete() {
	val := m.input.Value()
	if strings.TrimSpace(val) == "" {
		m.suggests = nil
		return
	}
	fields := strings.Fields(val)
	typingFirst := len(fields) == 1 && !strings.HasSuffix(val, " ")
	var cands []string
	switch {
	case typingFirst:
		cands = append(cands, m.verbs...)
		cands = append(cands, "help", "keys", "clear", "quit", "files", "next", "stop", "shot", "view")
		for a := range cli.Aliases {
			cands = append(cands, a)
		}
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
		case "app":
			for a := range cli.Aliases {
				cands = append(cands, a)
			}
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
	m.suggests = fuzzyRank(last, cands, 6)
	if m.sugSel >= len(m.suggests) {
		m.sugSel = 0
	}
}

// accept replaces the word being typed with the chosen suggestion.
func (m *model) accept(s string) {
	val := m.input.Value()
	if strings.HasSuffix(val, " ") {
		m.input.SetValue(val + s + " ")
	} else {
		fields := strings.Fields(val)
		fields[len(fields)-1] = s
		m.input.SetValue(strings.Join(fields, " ") + " ")
	}
	m.input.CursorEnd()
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

// ---- view -----------------------------------------------------------------

var (
	styleHeader = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("24")).Padding(0, 1)
	styleStatus = lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Background(lipgloss.Color("236")).Padding(0, 1)
	styleDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	styleErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	styleBold   = lipgloss.NewStyle().Bold(true)
	stylePrompt = lipgloss.NewStyle().Foreground(lipgloss.Color("81")).Bold(true)
	styleSug    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleSugSel = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("238"))
	styleHit    = lipgloss.NewStyle().Foreground(lipgloss.Color("81")).Bold(true)
)

func (m *model) View() string {
	if m.width == 0 {
		return ""
	}
	header := styleHeader.Width(m.width).Render(
		fmt.Sprintf("%s  %s  ·  receiver: %s", m.devName, m.devHost, m.rcState))
	nowLine := styleDim.Render("  now: " + m.describeNow())

	var sug strings.Builder
	for i, s := range m.suggests {
		line := "  " + highlight(s)
		if i == m.sugSel {
			line = styleSugSel.Render(line)
		} else {
			line = styleSug.Render(line)
		}
		sug.WriteString(line + "\n")
	}

	var status string
	switch {
	case m.running != "":
		status = "running: " + m.running
	case m.pl != nil:
		i, n := m.pl.Index()
		status = fmt.Sprintf("playlist %d/%d %s  ·  next · stop", i+1, n, m.pl.Current())
	case m.mode == modeKeys:
		status = "KEYS  arrows · enter · backspace · h m space + - 0 < >  ·  esc to leave"
	default:
		status = "tab complete · ↑↓ history · help · keys · quit"
	}
	statusBar := styleStatus.Width(m.width).Render(status)

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		nowLine,
		m.view.View(),
		strings.TrimRight(sug.String(), "\n"),
		m.input.View(),
		statusBar,
	)
}

// describeNow renders the now line, extrapolating the position from the
// last snapshot so the clock ticks between events.
func (m *model) describeNow() string {
	if m.rc == nil {
		return "run tv setup for now-playing, screenshots and local files"
	}
	if m.now == nil {
		return "–"
	}
	n := *m.now
	if n.State == "playing" && n.Speed > 0 && n.UpdatedMs > 0 {
		// updated_ms is the TV's uptime clock for system sessions and wall
		// time for our player; either way the delta since we received the
		// snapshot is what matters, so we track receipt time instead.
		n.PositionMs += int64(time.Since(m.nowAt).Seconds() * 1000 * n.Speed)
		if n.DurationMs > 0 && n.PositionMs > n.DurationMs {
			n.PositionMs = n.DurationMs
		}
	}
	return n.Describe()
}

// highlight bolds the matched characters of a suggestion.
func highlight(s match) string {
	var b strings.Builder
	pi := 0
	for i, r := range s.text {
		if pi < len(s.pos) && s.pos[pi] == i {
			b.WriteString(styleHit.Render(string(r)))
			pi++
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// layout sizes the viewport so header, suggestions, prompt and status
// always fit; the scrollback takes whatever is left.
func (m *model) layout() {
	sugLines := len(m.suggests)
	if sugLines == 0 {
		sugLines = 1 // JoinVertical renders the empty block as one line
	}
	h := m.height - 1 - 1 - sugLines - 1 - 1 // header, now line, suggestions, prompt, status
	if h < 3 {
		h = 3
	}
	m.view.Width = m.width
	m.view.Height = h
	m.input.Width = m.width - 4
	m.refresh()
}

// say appends one line to the scrollback and keeps the view pinned to the
// bottom, like a terminal.
func (m *model) say(line string) {
	m.lines = append(m.lines, line)
	if len(m.lines) > 2000 {
		m.lines = m.lines[len(m.lines)-2000:]
	}
	m.refresh()
}

func (m *model) refresh() {
	m.view.SetContent(strings.Join(m.lines, "\n"))
	m.view.GotoBottom()
}
