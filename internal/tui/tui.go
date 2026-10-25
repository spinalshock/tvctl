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
	"github.com/charmbracelet/x/ansi"

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
	ticks    int
	keyboard string // package asking for text, "" when none
	events   chan receiver.Event

	dev       device.Device   // stock-API driver, for keyboard polling and typing
	typing    bool            // TV keyboard is open: Enter sends the line as text
	asleep    bool            // last keyboard probe found the TV in standby
	verbWords map[string]bool // verb names and aliases, built on first use
	tvText    string          // what the TV's field currently holds
	pl        *cast.Playlist  // local files being served, nil when idle
	plGrace   int             // now-events to ignore right after starting an item
	lastShot  string          // path of the last screenshot, for `view`
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
	// resyncMsg is a periodic GET /now, a safety net for missed events.
	resyncMsg struct{ now *receiver.Now }
	// kbMsg is the result of one keyboard-state poll.
	kbMsg struct {
		open   bool
		text   string
		asleep bool // remote service not listening: TV in standby
	}
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
	in.Prompt = "❯ "
	in.PromptStyle = stylePrompt
	in.Placeholder = "command, app, search or link · help"
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
	m.dev = firetv.New(ent.Host, ent.Name, ent.Token)
	m.say(styleDim.Render("tvctl · type help for commands, keys for remote mode, quit to exit"))
	return m, nil
}

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink, loadApps(), tick(), m.pollKeyboard()}
	if m.rc != nil {
		cmds = append(cmds, m.connectEvents(), m.waitEvent())
	}
	return tea.Batch(cmds...)
}

// pollKeyboard asks the TV every 1.5 s whether its keyboard is up. It's a
// tiny local HTTP call, and it's the only way to know in basic mode. The
// probe is passive (it never wakes the TV), and while the TV is asleep the
// poll slows to every 10 s so a standby TV isn't pestered.
func (m *model) pollKeyboard() tea.Cmd {
	kw, ok := m.dev.(device.KeyboardWatcher)
	if !ok {
		return nil
	}
	every := 1500 * time.Millisecond
	if m.asleep {
		every = 10 * time.Second
	}
	return tea.Tick(every, func(time.Time) tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		open, cur, err := kw.KeyboardOpen(ctx)
		if errors.Is(err, firetv.ErrAsleep) {
			return kbMsg{asleep: true}
		}
		if err != nil {
			return kbMsg{}
		}
		return kbMsg{open: open, text: cur}
	})
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

func (m *model) resyncNow() tea.Cmd {
	rc := m.rc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		n, err := rc.Now(ctx)
		if err != nil {
			return resyncMsg{}
		}
		return resyncMsg{now: n}
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
		var rc *receiver.Client
		if ent.ReceiverToken != "" {
			rc = receiver.New(ent.Host, ent.ReceiverToken)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		apps, err := cli.AllApps(ctx, c, rc)
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
		m.ticks++
		if m.rc != nil && m.ticks%10 == 0 {
			return m, tea.Batch(tick(), m.resyncNow())
		}
		return m, tick()

	case resyncMsg:
		if msg.now != nil {
			m.rcState = "online"
			m.now = msg.now
			m.nowAt = time.Now()
		}
		return m, nil

	case kbMsg:
		m.asleep = msg.asleep
		if msg.open && !m.typing {
			m.typing = true
			m.say(styleAccent.Render("⌨ ") + styleText.Render("the TV is asking for text — type it and press Enter (Esc to cancel)"))
		} else if !msg.open && m.typing {
			m.typing = false
		}
		m.tvText = msg.text
		return m, m.pollKeyboard()

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
			m.now = nil // never show frozen data from a dead connection
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
		m.say(stylePrompt.Render("› ") + styleBold.Render(msg.cmd) + styleDim.Render(fmt.Sprintf("   %.1fs", msg.took.Seconds())))
		out := strings.TrimRight(msg.out, "\n")
		if msg.err != nil && out == "" {
			out = msg.err.Error()
		}
		for _, l := range strings.Split(out, "\n") {
			l = strings.TrimPrefix(l, "tv: ")
			if msg.err != nil {
				m.say("  " + styleErr.Render(l))
			} else {
				m.say("  " + styleText.Render(l))
			}
		}
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
		if m.typing && !m.isCommand(line) {
			// The TV has a text field open: send the line as typed.
			return m, m.dispatch("text " + line)
		}
		m.history = append(m.history, line)
		m.histPos = len(m.history)
		return m, m.dispatch(line)
	case tea.KeyEsc:
		m.input.SetValue("")
		m.suggests = nil
		if m.typing {
			m.typing = false
			m.say(styleDim.Render("  typing mode off (the TV's keyboard is still open)"))
		}
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

// builtins are the prompt-only words the TUI handles itself; everything
// else is a CLI verb. Resolver, command check and completion all read this.
var builtins = []string{"help", "keys", "files", "view", "clear", "quit", "exit"}

func isBuiltin(word string) bool {
	for _, b := range builtins {
		if word == b {
			return true
		}
	}
	return false
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
		if m.pl != nil {
			return nil, func() tea.Cmd { m.playlistNext(); return nil }
		}
	case "stop":
		if m.pl != nil {
			return nil, func() tea.Cmd { m.playlistStop("stopped"); return nil }
		}
	case "play":
		if len(fields) == 1 {
			return []string{"resume"}, nil
		}
	case "shot":
		return nil, func() tea.Cmd { return m.screenshot() }
	case "view":
		return nil, func() tea.Cmd { return m.viewShot() }
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

// isCommand reports whether the first word is a verb or built-in, so that
// `vol up` still works while the TV's keyboard is open.
func (m *model) isCommand(line string) bool {
	head := strings.ToLower(strings.Fields(line)[0])
	return isBuiltin(head) || m.isVerb(head)
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
		m.say("  <app>             open it: " + strings.Join(cli.AliasNames(), " · ") + ", or any prefix of an app's name")
		m.say("  <app> <words>     search inside that app (netflix dark); youtube <words> plays the top result")
		m.say("  <link>            open it (YouTube links play directly)")
		m.say("  keys              remote mode: arrow keys go to the TV until esc")
		m.say("  files             pick files or a folder in yazi, play them on the TV in order")
		m.say("  pause · resume · toggle · stop · next · prev · seek +30   media controls for any app")
		m.say("  next · stop       with a playlist running: skip to the next file · end the playlist")
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
	accent  = lipgloss.Color("81")  // light blue: prompt, playing, matches
	fgText  = lipgloss.Color("252") // output text
	fgDim   = lipgloss.Color("243") // metadata, hints
	fgErr   = lipgloss.Color("203")
	okGreen = lipgloss.Color("78")
	warn    = lipgloss.Color("221")
	rule    = lipgloss.Color("238")

	styleText   = lipgloss.NewStyle().Foreground(fgText)
	styleDim    = lipgloss.NewStyle().Foreground(fgDim)
	styleErr    = lipgloss.NewStyle().Foreground(fgErr)
	styleBold   = lipgloss.NewStyle().Bold(true).Foreground(fgText)
	stylePrompt = lipgloss.NewStyle().Foreground(accent).Bold(true)
	styleAccent = lipgloss.NewStyle().Foreground(accent)
	styleTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
	styleSug    = lipgloss.NewStyle().Foreground(fgDim)
	styleSugSel = lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Background(rule)
	styleHit    = lipgloss.NewStyle().Foreground(accent).Bold(true)
	styleRule   = lipgloss.NewStyle().Foreground(rule)
	styleBarOn  = lipgloss.NewStyle().Foreground(accent)
	styleBarOff = lipgloss.NewStyle().Foreground(rule)
)

func (m *model) View() string {
	if m.width == 0 {
		return ""
	}
	w := m.width
	hr := styleRule.Render(strings.Repeat("─", w))

	// Header: centred, no fill. A coloured dot carries the receiver state.
	dot, state := styleDim.Render("○"), m.rcState
	switch m.rcState {
	case "online":
		dot = lipgloss.NewStyle().Foreground(okGreen).Render("●")
	case "offline":
		dot = styleErr.Render("●")
	case "not set up":
		dot = lipgloss.NewStyle().Foreground(warn).Render("●")
		state = "receiver not set up · tv setup"
	}
	title := styleBold.Render(m.devName) + styleDim.Render("  "+m.devHost+"  ·  "+state+" ")
	header := lipgloss.PlaceHorizontal(w, lipgloss.Center, dot+" "+title)

	var sug strings.Builder
	for i, sg := range m.suggests {
		line := "  " + highlight(sg)
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
		status = styleAccent.Render("… ") + styleDim.Render(m.running)
	case m.pl != nil:
		i, n := m.pl.Index()
		status = styleDim.Render(fmt.Sprintf("playlist %d/%d %s  ·  next · stop", i+1, n, m.pl.Current()))
	case m.mode == modeKeys:
		status = styleAccent.Render("KEYS ") + styleDim.Render("arrows · enter · backspace · h m space + - 0 < >  ·  esc to leave")
	default:
		status = styleDim.Render("tab complete · ↑↓ history · help · keys · quit")
	}

	parts := []string{header, hr, m.renderNow(w), hr, m.view.View()}
	if sug.Len() > 0 {
		parts = append(parts, strings.TrimRight(sug.String(), "\n"))
	}
	if m.typing {
		m.input.Prompt = "⌨ "
		status = styleAccent.Render("TYPING ") + styleDim.Render("Enter sends to the TV · Esc leaves · commands still work")
	} else {
		m.input.Prompt = "❯ "
	}
	parts = append(parts, hr, m.input.View(), status)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// renderNow draws the two-line now-playing block: state, app and title on
// the first line; a progress bar with the clock on the second.
func (m *model) renderNow(w int) string {
	empty := styleBarOff.Render("  " + strings.Repeat("╌", max(0, w-4)))
	if m.rc == nil {
		return styleDim.Render("  now-playing, screenshots and local files need the companion app: tv setup") + "\n" + empty
	}
	n := m.now
	if n == nil || n.Source == nil {
		fg := ""
		if n != nil && n.Foreground != nil {
			fg = "  ·  " + shortApp(*n.Foreground)
		}
		return styleDim.Render("  nothing playing"+fg) + "\n" + empty
	}
	// Extrapolate the position while playing so the bar moves between events.
	pos := n.PositionMs
	if n.State == "playing" && n.Speed > 0 {
		pos += int64(time.Since(m.nowAt).Seconds() * 1000 * n.Speed)
		if n.DurationMs > 0 && pos > n.DurationMs {
			pos = n.DurationMs
		}
	}
	sym := map[string]string{"playing": "▶", "paused": "⏸", "buffering": "◌", "stopped": "■"}[n.State]
	if sym == "" {
		sym = "·"
	}
	symStyle := styleAccent
	if n.State != "playing" {
		symStyle = styleDim
	}
	title := ""
	if n.Title != nil {
		title = *n.Title
	}
	if n.Artist != nil && *n.Artist != "" {
		title = *n.Artist + "  —  " + title
	}
	left := "  " + symStyle.Render(sym) + " " + styleAccent.Render(shortApp(n.App)) + "   "
	avail := w - lipgloss.Width(left) - 2
	var line1 string
	if strings.TrimSpace(title) == "" {
		// Some apps (Netflix) publish no metadata at all; say so instead of
		// leaving a gap that looks like a bug.
		line1 = left + styleDim.Render("no title published by this app")
	} else {
		line1 = left + styleTitle.Render(ansi.Truncate(title, max(0, avail), "…"))
	}

	clock := fmtClock(pos)
	if n.DurationMs > 0 {
		clock += " / " + fmtClock(n.DurationMs)
	}
	barW := w - 4 - lipgloss.Width(clock) - 2
	if barW < 10 {
		barW = 10
	}
	var bar string
	if n.DurationMs > 0 {
		filled := min(barW, int(float64(barW)*float64(pos)/float64(n.DurationMs)))
		bar = styleBarOn.Render(strings.Repeat("━", filled)) + styleBarOff.Render(strings.Repeat("─", barW-filled))
	} else {
		// Unknown length: a dashed line reads as "no end known", not "0%".
		bar = styleBarOff.Render(strings.Repeat("╌", barW))
		clock += "  elapsed"
	}
	return line1 + "\n" + "  " + bar + "  " + styleDim.Render(clock)
}

func fmtClock(ms int64) string {
	s := ms / 1000
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func shortApp(pkg string) string {
	known := map[string]string{
		"com.netflix.ninja":                 "Netflix",
		"com.amazon.firetv.youtube":         "YouTube",
		"com.spotify.tv.android":            "Spotify",
		"com.amazon.avod":                   "Prime Video",
		"com.amazon.cloud9":                 "Silk",
		"com.amazon.venezia":                "Appstore",
		"com.spocky.projengmenu":            "Projectivy",
		"com.amazon.tv.launcher":            "Fire TV Home",
		"dev.tvctl.receiver":                "tvctl player",
		"com.onemainstream.sonyliv.android": "Sony LIV",
	}
	if n, ok := known[pkg]; ok {
		return n
	}
	return pkg
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
	h := m.height - 1 - 1 - 2 - 1 - sugLines - 1 - 1 - 1 // header, rule, now, rule, suggestions, rule, prompt, status
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

// refresh pins the newest output just above the prompt, like a terminal:
// when there's less content than space, blank lines are added on top.
func (m *model) refresh() {
	lines := m.lines
	if pad := m.view.Height - len(lines); pad > 0 {
		lines = append(make([]string, pad), lines...)
	}
	m.view.SetContent(strings.Join(lines, "\n"))
	m.view.GotoBottom()
}
