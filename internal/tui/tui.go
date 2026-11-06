// Package tui is a text-first front end: a prompt with fuzzy completion, a
// scrollback of commands and their output, and a status bar. It executes
// the cobra command tree from internal/cli in-process, so every verb the
// CLI has works here with the same output, and nothing is defined twice.
//
// It is built the Bubble Tea way, as a root model composed of sub-models
// that each own one concern and know nothing about the others:
//
//	console  prompt, history, completion, scrollback, spinner  (console.go)
//	nowBar   now-playing block with a progress bar              (nowbar.go)
//	player   yazi chooser, playlist, screenshots                (player.go)
//	keys     key bindings with their help text                  (keys.go)
//
// The root (this file) wires messages between them and draws the frame:
//
//	header     device name, host, receiver state
//	now        what's playing, with position
//	console    scrollback, suggestions, prompt
//	footer     spinner while running, playlist, or the mode's key help
//
// Background work (HTTP calls, the event socket, timers) never blocks the
// update loop: each runs in a tea.Cmd and comes back as a message. The
// event socket is a long-lived goroutine bridged through a channel.
package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/spinalshock/tvctl/internal/cli"
	"github.com/spinalshock/tvctl/internal/config"
	"github.com/spinalshock/tvctl/internal/device"
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
	console console
	now     nowBar
	player  player
	footer  help.Model

	mode   mode
	width  int
	height int

	devName     string
	devHost     string
	verbs       []string
	verbWords   map[string]bool     // verb names and aliases, built on first use
	verbChoices map[string][]string // each verb's fixed argument values, built on first use
	apps        []device.App        // cached once at start, for completion

	dev    device.Device // the TV, with whatever it can do (companion app included)
	events chan device.Event
	ticks  int

	typing bool        // the TV wants text: the prompt mirrors its field (typing.go)
	asleep bool        // last keyboard probe found the TV in standby
	t      typingState // typing mode's state (typing.go)
	toast  toastState  // the success card over the middle (toast.go)
}

// Messages from background work.
type (
	resultMsg struct {
		cmd    string
		out    string
		err    error
		took   time.Duration
		notice string // the command's announced outcome, if any (toast)
	}
	appsMsg  []device.App
	eventMsg device.Event
	tickMsg  time.Time
	// resyncMsg is a periodic GET /now, a safety net for missed events.
	resyncMsg struct{ now *device.Playing }
	// kbMsg is the result of one keyboard-state poll.
	kbMsg struct {
		open   bool
		text   string
		asleep bool // remote service not listening: TV in standby
		err    bool // the probe failed; not evidence the field closed
	}
)

func tick() tea.Cmd { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }

func newModel() (*model, error) {
	st, err := config.Load()
	if err != nil {
		return nil, err
	}
	id, ent, err := st.Resolve("")
	if err != nil {
		return nil, err
	}
	dev, err := device.Open(cli.DeviceConfig(id, ent))
	if err != nil {
		return nil, err
	}
	m := &model{
		console: newConsole(),
		footer:  newHelp(),
		devName: ent.Name,
		devHost: ent.Host,
		verbs:   cli.Verbs(),
		events:  make(chan device.Event, 32),
		dev:     dev,
	}
	m.now = newNowBar(nowFeed(m.dev))
	m.now.name = m.appTitle
	m.player = player{dev: m.dev, log: m.console.say}
	m.console.complete = m.complete
	m.console.say(styleDim.Render("tvctl · type help for commands, keys for remote mode, quit to exit"))
	return m, nil
}

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink, loadApps(m.dev), tick(), m.pollKeyboard()}
	if _, ok := m.dev.(device.EventSource); ok {
		cmds = append(cmds, m.connectEvents(), m.waitEvent(), m.fetchInput())
	}
	return tea.Batch(cmds...)
}

// ---- background commands --------------------------------------------------

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
		if errors.Is(err, device.ErrAsleep) {
			return kbMsg{asleep: true}
		}
		if err != nil {
			return kbMsg{err: true}
		}
		return kbMsg{open: open, text: cur}
	})
}

// connectEvents holds the receiver socket open for the TUI's lifetime,
// reconnecting with backoff. Events land on m.events; waitEvent turns
// them into Bubble Tea messages one at a time, which is the idiomatic way
// to bridge a long-lived goroutine into the update loop.
func (m *model) connectEvents() tea.Cmd {
	src, ok := m.dev.(device.EventSource)
	if !ok {
		return nil
	}
	ch := m.events
	return func() tea.Msg {
		go func() {
			delay := time.Second
			for {
				_ = src.Events(context.Background(), func(ev device.Event) error {
					ch <- ev
					return nil
				})
				ch <- device.Event{Type: "_offline"}
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

func (m *model) resyncNow() tea.Cmd {
	p, ok := m.dev.(device.Player)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		n, err := p.Playing(ctx)
		if err != nil {
			return resyncMsg{}
		}
		return resyncMsg{now: n}
	}
}

// loadApps fetches the app list once so completion can offer app names
// without a round-trip per keystroke.
func loadApps(dev device.Device) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		apps, err := dev.Apps(ctx)
		if err != nil {
			return appsMsg(nil)
		}
		return appsMsg(apps)
	}
}

// dispatch turns a prompt line into argv and runs it in the background.
func (m *model) dispatch(line string) tea.Cmd {
	argv, local := m.resolve(line)
	if local != nil {
		return local()
	}
	return m.runArgv(line, argv)
}

// runArgv runs a CLI command from argv directly, for callers whose
// arguments (file paths with spaces) mustn't go through word splitting.
func (m *model) runArgv(line string, argv []string) tea.Cmd {
	run := func() tea.Msg {
		var buf bytes.Buffer
		var notice string
		t0 := time.Now()
		err := cli.Run(argv, &cli.Env{Out: &buf, Ask: noAsk, Notice: func(s string) { notice = s }})
		return resultMsg{cmd: line, out: buf.String(), err: err, took: time.Since(t0), notice: notice}
	}
	return tea.Batch(m.console.begin(line), run)
}

// sendKey fires a remote key without waiting: a remote should feel
// instant, and each press isn't worth a line of scrollback.
func sendKey(k device.Key) tea.Cmd {
	return func() tea.Msg {
		_ = cli.Run([]string{"key", string(k)}, &cli.Env{Out: io.Discard, Ask: noAsk})
		return nil
	}
}

// ---- update ---------------------------------------------------------------

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
		// A TV text field takes over from any mode (keys mode included) and
		// hands control back when it closes.
		if m.typing {
			return m, m.typingKey(msg)
		}
		if m.mode == modeKeys {
			k, leave := remoteMap.match(msg)
			if leave {
				m.mode = modePrompt
				return m, nil
			}
			if k != "" {
				return m, sendKey(k)
			}
			return m, nil
		}
		cmd := m.console.Update(msg)
		m.layout()
		return m, cmd

	case submitMsg:
		return m, m.dispatch(msg.line)

	case inputMsg, textSentMsg, leaveCheckMsg, typingErrMsg:
		return m, m.updateTyping(msg)

	case resultMsg:
		if msg.cmd == "shot" && msg.err == nil {
			m.player.lastShot = strings.SplitN(msg.out, "\n", 2)[0]
		}
		m.console.done(msg.cmd, msg.out, msg.err, msg.took)
		if msg.err == nil && msg.notice != "" {
			return m, m.showToast(msg.notice)
		}
		return m, nil

	case toastDoneMsg:
		m.toastDone(msg)
		return m, nil

	case appsMsg:
		m.apps = msg
		return m, nil

	case tickMsg:
		m.ticks++
		if _, rich := m.dev.(device.Player); rich && m.ticks%10 == 0 {
			return m, tea.Batch(tick(), m.resyncNow())
		}
		return m, tick()

	case resyncMsg:
		if msg.now != nil {
			m.now.set(msg.now)
		}
		return m, nil

	case kbMsg:
		m.asleep = msg.asleep
		m.onPoll(msg)
		// While the TV has a keyboard up, also read what the field holds
		// (the companion app answers only when asked).
		var read tea.Cmd
		if msg.open || m.typing {
			read = m.fetchInput()
		}
		return m, tea.Batch(m.pollKeyboard(), read)

	case eventMsg:
		switch msg.Type {
		case "_offline":
			m.now.offline()
			m.t.live = false // fall back to the keyboard poll until the app is back
		case "now":
			m.now.set(msg.Now)
			m.player.observe(msg.Now)
		default:
			m.now.online()
		}
		return m, m.waitEvent()

	case chosenMsg:
		if msg.purpose == "wallpaper" {
			return m, m.setWallpaper(msg)
		}
		m.player.chosen(msg)
		return m, nil

	case execDoneMsg:
		if msg.err != nil {
			m.console.say(styleErr.Render("  " + msg.err.Error()))
		}
		return m, nil
	}
	// Anything else (spinner frames, cursor blinks) belongs to the console.
	return m, m.console.Update(msg)
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
	styleBarOff = lipgloss.NewStyle().Foreground(rule)
)

// Rows the frame uses outside the console: header, a rule on each side of the
// context block, a rule above the footer, and the footer — five fixed rows,
// plus the context block itself (now-bar = 2, typing panel = typingPanelRows).
const fixedFrameRows = 5

func (m *model) contextRows() int {
	if m.typing {
		return typingPanelRows
	}
	return 2
}

func (m *model) frameRows() int { return fixedFrameRows + m.contextRows() }

func (m *model) View() string {
	if m.width == 0 {
		return ""
	}
	w := m.width
	hr := styleRule.Render(strings.Repeat("─", w))

	// Header: centred. A coloured dot carries the receiver state.
	dot, state := m.now.dot()
	title := styleBold.Render(m.devName) + styleDim.Render("  "+m.devHost+"  ·  "+state+" ")
	header := lipgloss.PlaceHorizontal(w, lipgloss.Center, dot+" "+title)

	if m.typing {
		m.console.input.Prompt = "⌨ "
	} else {
		m.console.input.Prompt = "❯ "
	}

	// Footer: whichever is most urgent, otherwise the mode's key help.
	var footer string
	switch {
	case m.console.running != "":
		footer = m.console.status()
	case m.player.active():
		footer = m.player.status()
	case m.typing:
		footer = m.typingFooter()
	case m.mode == modeKeys:
		m.footer.Width = m.width - 5
		footer = styleAccent.Render("KEYS ") + m.footer.View(remoteMap)
		m.footer.Width = m.width
	default:
		footer = m.footer.View(promptMap)
	}

	// While a TV field is focused, a highlighted panel takes the now-bar's
	// place to make the prompt unmistakable.
	context := m.now.View(w)
	if m.typing {
		context = m.typingPanel(w)
	}
	return m.withToast(lipgloss.JoinVertical(lipgloss.Left,
		header, hr, context, hr, m.console.View(), hr, footer))
}

// layout hands the console whatever height the frame leaves.
func (m *model) layout() {
	m.footer.Width = m.width
	m.console.layout(m.width, m.height-m.frameRows())
}

// sayf is a convenience for formatted scrollback lines from the root.
func (m *model) sayf(format string, a ...any) { m.console.say(fmt.Sprintf(format, a...)) }
