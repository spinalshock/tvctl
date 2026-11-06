package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/spinalshock/tvctl/internal/cast"
	"github.com/spinalshock/tvctl/internal/cli"
	"github.com/spinalshock/tvctl/internal/device"
)

// player owns what the TUI does with local files and screenshots: the yazi
// chooser, the playlist being served, and the last screenshot for `view`.
// It reports through log, which the root points at the console.
type player struct {
	dev      device.Device
	pl       *cast.Playlist // local files being served, nil when idle
	lastShot string
	log      func(string)
}

// chosenMsg carries yazi's selection and what it was for ("files" to
// play, "wallpaper" for the home dashboard); execDoneMsg any other external
// program's exit; shotMsg a finished screenshot.
type (
	chosenMsg struct {
		purpose string
		paths   []string
		err     error
	}
	execDoneMsg struct{ err error }
)

func (p *player) active() bool { return p.pl != nil }

// chooseFiles hands the terminal to yazi in chooser mode. tea.ExecProcess
// suspends the TUI, runs the program, and restores the screen; the chosen
// paths come back through the temp file yazi writes on confirm.
func (p *player) chooseFiles(purpose string) tea.Cmd {
	if _, err := cli.NeedAddon[device.MediaPlayer](p.dev, purpose); err != nil {
		p.log(styleErr.Render("  " + err.Error()))
		return nil
	}
	if _, err := exec.LookPath("yazi"); err != nil {
		p.log(styleErr.Render("  yazi not found on PATH"))
		return nil
	}
	f, err := os.CreateTemp("", "tvctl-chooser-")
	if err != nil {
		p.log(styleErr.Render("  " + err.Error()))
		return nil
	}
	tmp := f.Name()
	f.Close()
	c := exec.Command("yazi", "--chooser-file="+tmp)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		defer os.Remove(tmp)
		if err != nil {
			return chosenMsg{purpose: purpose, err: err}
		}
		b, err := os.ReadFile(tmp)
		if err != nil {
			return chosenMsg{purpose: purpose, err: err}
		}
		var paths []string
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				paths = append(paths, l)
			}
		}
		return chosenMsg{purpose: purpose, paths: paths}
	})
}

func (p *player) chosen(msg chosenMsg) {
	if msg.err != nil {
		p.log(styleErr.Render("  yazi: " + msg.err.Error()))
		return
	}
	if len(msg.paths) == 0 {
		p.log(styleDim.Render("  nothing chosen"))
		return
	}
	p.start(msg.paths)
}

func (p *player) start(paths []string) {
	files, err := cast.Expand(paths)
	if err != nil {
		p.log(styleErr.Render("  " + err.Error()))
		return
	}
	p.stop("")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	mp, err := cli.NeedAddon[device.MediaPlayer](p.dev, "playing files")
	if err != nil {
		p.log(styleErr.Render("  " + err.Error()))
		return
	}
	pl, err := cast.Start(ctx, mp, p.dev.Info().Host, files)
	if err != nil {
		p.log(styleErr.Render("  " + err.Error()))
		return
	}
	p.pl = pl
	p.announce()
}

func (p *player) announce() {
	i, n := p.pl.Index()
	p.log(fmt.Sprintf("  ▶ %d/%d %s  %s", i+1, n, p.pl.Current(), styleDim.Render("(serving from "+p.pl.Addr()+")")))
}

// observe is called on every now event while a playlist runs: advance
// when the item ends, stop when the user leaves the player.
func (p *player) observe(n *device.Playing) {
	if p.pl == nil {
		return
	}
	advance, left := p.pl.Observe(n)
	if left {
		p.stop("left the player")
	} else if advance {
		p.next()
	}
}

func (p *player) next() {
	if p.pl == nil {
		p.log(styleDim.Render("  no playlist running"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	more, err := p.pl.Next(ctx)
	if err != nil {
		p.log(styleErr.Render("  " + err.Error()))
		return
	}
	if !more {
		p.stop("finished")
		return
	}
	p.announce()
}

func (p *player) stop(why string) {
	if p.pl == nil {
		return
	}
	p.pl.Close()
	p.pl = nil
	if why != "" {
		p.log(styleDim.Render("  playlist " + why))
	}
}

// status is the one-line playlist summary for the footer.
func (p *player) status() string {
	if p.pl == nil {
		return ""
	}
	i, n := p.pl.Index()
	return styleDim.Render(fmt.Sprintf("playlist %d/%d %s  ·  next · stop", i+1, n, p.pl.Current()))
}

// screenshot saves a PNG under ~/Pictures/tv and remembers it for `view`.
func (p *player) screenshot() tea.Cmd {
	sh, err := cli.NeedAddon[device.Screenshotter](p.dev, "screenshots")
	if err != nil {
		p.log(styleErr.Render("  " + err.Error()))
		return nil
	}
	return func() tea.Msg {
		t0 := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		png, err := sh.Screenshot(ctx)
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
func (p *player) viewShot() tea.Cmd {
	if p.lastShot == "" {
		p.log(styleDim.Render("  no screenshot yet: type shot"))
		return nil
	}
	c := exec.Command("yazi", p.lastShot)
	return tea.ExecProcess(c, func(err error) tea.Msg { return execDoneMsg{err: err} })
}

// setWallpaper uploads yazi's pick as the home dashboard's background. The
// TV keeps only the current wallpaper: each upload replaces it.
func (m *model) setWallpaper(msg chosenMsg) tea.Cmd {
	switch {
	case msg.err != nil:
		m.console.say(styleErr.Render("  yazi: " + msg.err.Error()))
		return nil
	case len(msg.paths) == 0:
		m.console.say(styleDim.Render("  no image chosen"))
		return nil
	}
	path := msg.paths[0]
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".webp":
	default:
		m.console.say(styleErr.Render("  not an image (jpg, png, webp): " + filepath.Base(path)))
		return nil
	}
	return m.runArgv("wallpaper "+filepath.Base(path), []string{"home", "wallpaper", path})
}
