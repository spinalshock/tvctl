package tui

import (
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/spinalshock/tvctl/internal/device"
)

// Key bindings are declared once with their help text, so the footer and
// the matching can't disagree. Two maps: what the prompt offers, and what
// remote (keys) mode sends to the TV.

type promptKeys struct {
	Complete, History, Help, Keys, Quit key.Binding
}

func (k promptKeys) ShortHelp() []key.Binding {
	return []key.Binding{k.Complete, k.History, k.Help, k.Keys, k.Quit}
}
func (k promptKeys) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

// "help" and "keys" are words, not keys; they carry a key only because the
// help renderer hides bindings without one.
var promptMap = promptKeys{
	Complete: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "complete")),
	History:  key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "history")),
	Help:     key.NewBinding(key.WithKeys("help"), key.WithHelp("help", "commands")),
	Keys:     key.NewBinding(key.WithKeys("keys"), key.WithHelp("keys", "remote mode")),
	Quit:     key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
}

// remoteKey pairs a binding with the TV key it sends.
type remoteKey struct {
	key.Binding
	send device.Key
}

type remoteKeys struct {
	Dpad, Select, Back, Home, Menu, Play, Vol, Mute, Seek []remoteKey
	Leave                                                 key.Binding
}

func rk(send device.Key, keys []string, h, desc string) remoteKey {
	return remoteKey{key.NewBinding(key.WithKeys(keys...), key.WithHelp(h, desc)), send}
}

var remoteMap = remoteKeys{
	Dpad: []remoteKey{
		rk(device.KeyUp, []string{"up"}, "↑↓←→", "move"), rk(device.KeyDown, []string{"down"}, "", ""),
		rk(device.KeyLeft, []string{"left"}, "", ""), rk(device.KeyRight, []string{"right"}, "", ""),
	},
	Select: []remoteKey{rk(device.KeySelect, []string{"enter"}, "enter", "select")},
	Back:   []remoteKey{rk(device.KeyBack, []string{"backspace"}, "⌫", "back")},
	Home:   []remoteKey{rk(device.KeyHome, []string{"h"}, "h", "home")},
	Menu:   []remoteKey{rk(device.KeyMenu, []string{"m"}, "m", "menu")},
	Play:   []remoteKey{rk(device.KeyPlayPause, []string{" "}, "space", "play")},
	Vol:    []remoteKey{rk(device.KeyVolumeUp, []string{"+"}, "+/-", "vol"), rk(device.KeyVolumeDown, []string{"-"}, "", "")},
	Mute:   []remoteKey{rk(device.KeyMute, []string{"0"}, "0", "mute")},
	Seek:   []remoteKey{rk(device.KeyFastFwd, []string{">"}, "<>", "seek"), rk(device.KeyRewind, []string{"<"}, "", "")},
	Leave:  key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("esc", "leave")),
}

func (k remoteKeys) all() []remoteKey {
	var out []remoteKey
	for _, g := range [][]remoteKey{k.Dpad, k.Select, k.Back, k.Home, k.Menu, k.Play, k.Vol, k.Mute, k.Seek} {
		out = append(out, g...)
	}
	return out
}

// ShortHelp shows the first binding of each group (the one with help text).
func (k remoteKeys) ShortHelp() []key.Binding {
	var out []key.Binding
	for _, g := range [][]remoteKey{k.Dpad, k.Select, k.Back, k.Home, k.Menu, k.Play, k.Vol, k.Mute, k.Seek} {
		out = append(out, g[0].Binding)
	}
	return append(out, k.Leave)
}
func (k remoteKeys) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

// match returns the TV key for a keypress in remote mode, or leave.
func (k remoteKeys) match(msg tea.KeyMsg) (send device.Key, leave bool) {
	if key.Matches(msg, k.Leave) || msg.Type == tea.KeyCtrlC {
		return "", true
	}
	for _, b := range k.all() {
		if key.Matches(msg, b.Binding) {
			return b.send, false
		}
	}
	return "", false
}

// newHelp is the footer renderer, shared by both modes.
func newHelp() help.Model {
	h := help.New()
	h.ShortSeparator = " · "
	h.Styles.ShortKey = styleAccent
	h.Styles.ShortDesc = styleDim
	h.Styles.ShortSeparator = styleRule
	return h
}
