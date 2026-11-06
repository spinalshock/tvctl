package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/spinalshock/tvctl/internal/cast"
	"github.com/spinalshock/tvctl/internal/device"
	"github.com/spinalshock/tvctl/internal/receiver"
)

// Verbs whose capability usually comes from the companion add-on. They
// assert it with NeedAddon, which tells a TV that could have the add-on
// to install it, and a TV that can't that it can't.

func (e *cmds) companion() *cobra.Command {
	return &cobra.Command{
		Use:         "companion",
		Aliases:     []string{"setup"},
		Short:       "Install the optional companion app on an Android-based TV you own (dashboard, overlays, screenshots, local files)",
		Annotations: meta("", "none"),
		Long: `Installs the tvctl receiver app on the TV and grants it the permissions
Android offers no settings screen for. This is the one step that needs ADB
debugging switched on, temporarily, on the TV; the binary speaks the
protocol itself, nothing to install here. Safe to re-run; it updates the
app in place. Only Android-based TVs (Fire TV) can take it.`,
		RunE: e.run(func(s *session, _ []string) error {
			inst, ok := s.dev.(device.Installer)
			if !ok {
				return device.Failf("%s can't take the companion app", tvName(s.dev)).Note("it runs on Android-based TVs only")
			}
			tok, err := inst.InstallCompanion(s.ctx, e.env.Out, e.env.Ask)
			if err != nil {
				return err
			}
			s.entry.CompanionToken = tok
			if err := s.save(); err != nil {
				return err
			}
			return e.ok("", map[string]any{"companion": true})
		}),
	}
}

func (e *cmds) now() *cobra.Command {
	return &cobra.Command{
		Use:   "now",
		Short: "What the TV is playing",
		Long: `With the companion app: title, artist, position, for any app. Without it,
what the TV itself tells: the foreground app and whether it is playing.`,
		RunE: e.run(func(s *session, _ []string) error {
			if p, ok := s.dev.(device.Player); ok {
				n, err := p.Playing(s.ctx)
				if err != nil {
					return err
				}
				e.emit(nowLine(n, s.appTitle), n)
				return nil
			}
			nw, err := need[device.Nower](s.dev, "now playing")
			if err != nil {
				return err
			}
			n, err := nw.Now(s.ctx)
			if err != nil {
				return err
			}
			name := ""
			line := "Nothing is playing."
			if n.App != "" {
				name = s.appTitle(n.App)
				line = name + " is open."
				if n.State != "" {
					line = name + " is " + n.State + "."
				}
			}
			e.emit(line, map[string]any{"app": n.App, "name": name, "state": n.State})
			return nil
		}),
	}
}

func (e *cmds) events() *cobra.Command {
	return &cobra.Command{
		Use:         "events",
		Short:       "Stream TV events (now-playing, foreground app, keyboard) until Ctrl-C",
		Annotations: meta(needsCompanion, "none"),
		Long: `Prints one line per event. With --json each line is the receiver's raw
JSON object, which is the form editors and scripts should consume.`,
		RunE: e.run(func(s *session, _ []string) error {
			src, err := NeedAddon[device.EventSource](s.dev, "streaming events")
			if err != nil {
				return err
			}
			return src.Events(s.ctx, func(ev device.Event) error {
				if e.env.JSON {
					fmt.Fprintln(e.env.Out, string(ev.Raw))
					return nil
				}
				switch ev.Type {
				case "now":
					fmt.Fprintln(e.env.Out, "now        "+receiver.Describe(ev.Now))
				case "foreground":
					fmt.Fprintln(e.env.Out, "foreground "+deref(ev.Package))
				case "keyboard":
					state := "closed"
					if ev.Open != nil && *ev.Open {
						state = "open"
					}
					fmt.Fprintf(e.env.Out, "keyboard   %s (%s)\n", state, deref(ev.Package))
				default:
					fmt.Fprintln(e.env.Out, string(ev.Raw))
				}
				return nil
			})
		}),
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (e *cmds) play() *cobra.Command {
	c := &cobra.Command{
		Use:         "play [file|dir|link|words]...",
		Short:       "Resume; or play files, a link, or the top YouTube hit for some words",
		Annotations: meta("", "none", wake),
		Long: `Alone, play resumes whatever is paused. A link plays in the matching app
(YouTube links in the YouTube app). Local files are served to the TV from this
machine for as long as the command runs, several files or a directory in
order, Ctrl-C to stop serving; this needs the companion app. Anything else
is searched on YouTube and the top hit plays: play tambdi chambdi.`,
		Args: cobra.ArbitraryArgs,
		RunE: e.run(func(s *session, args []string) error {
			switch {
			case len(args) == 0:
				return e.transport(s, "play", "resuming", 0, device.KeyPlayPause)
			case len(args) == 1 && isLink(args[0]):
				if err := s.openLink(args[0]); err != nil {
					return err
				}
				return e.done("Playing.", map[string]any{"url": args[0]})
			case !allFiles(args):
				return e.playTopHit(s, strings.Join(args, " "))
			}
			mp, err := NeedAddon[device.MediaPlayer](s.dev, "playing local files")
			if err != nil {
				return err
			}
			watch, err := NeedAddon[device.Player](s.dev, "playing local files")
			if err != nil {
				return err
			}
			files, err := cast.Expand(args)
			if err != nil {
				return err
			}
			pl, err := cast.Start(s.ctx, mp, s.entry.Host, files)
			if err != nil {
				return err
			}
			defer pl.Close()
			announce := func() {
				i, n := pl.Index()
				if !e.env.JSON {
					fmt.Fprintf(e.env.Out, "playing %d/%d %s  (serving from %s, Ctrl-C to stop)\n", i+1, n, pl.Current(), pl.Addr())
				}
			}
			announce()
			// Poll now-playing; the playlist says when to advance or stop.
			for {
				select {
				case <-s.ctx.Done():
					return nil
				case <-time.After(2 * time.Second):
				}
				n, err := watch.Playing(s.ctx)
				if err != nil {
					continue
				}
				advance, left := pl.Observe(n)
				if left {
					return e.done("Stopped.", map[string]any{"reason": "left player"})
				}
				if !advance {
					continue
				}
				more, err := pl.Next(s.ctx)
				if err != nil {
					return err
				}
				if !more {
					return e.done("Playlist finished.", map[string]any{"played": len(files)})
				}
				announce()
			}
		}),
	}
	return c
}

// allFiles reports whether every argument names something on disk, which
// is what separates "play these files" from "play this song".
func allFiles(args []string) bool {
	for _, a := range args {
		if _, err := os.Stat(a); err != nil {
			return false
		}
	}
	return true
}

func (e *cmds) toast() *cobra.Command {
	var long bool
	var pos string
	c := &cobra.Command{
		Use:   "toast <text>...",
		Short: "Show a short message on the TV for a few seconds",
		Long: `A card over whatever is on screen, video included. Anything can call it:
a script, a cron job, or any program on the network (POST /toast on the
receiver, see tv companion). Pipe text in: echo done | tv toast -`,
		Args: cobra.MinimumNArgs(1),
		RunE: e.run(func(s *session, args []string) error {
			text := strings.Join(args, " ")
			if text == "-" {
				b, err := io.ReadAll(os.Stdin)
				if err != nil {
					return device.Fail("couldn't read the text to show").Because(err)
				}
				text = strings.TrimSpace(string(b))
			}
			t, err := NeedAddon[device.Toaster](s.dev, "toasts")
			if err != nil {
				return err
			}
			if err := t.Toast(s.ctx, text, device.ToastOpts{Long: long, Pos: pos}); err != nil {
				return err
			}
			return e.ok("", map[string]any{"text": text})
		}),
	}
	c.Flags().BoolVar(&long, "long", false, "keep it on screen longer")
	c.Flags().StringVar(&pos, "pos", "top-right", "top-right, top-left, bottom-right, bottom-left, top, bottom")
	return c
}

func (e *cmds) shot() *cobra.Command {
	return &cobra.Command{
		Use:         "shot [file.png]",
		Short:       "Screenshot the TV (DRM video shows black; menus and most apps are fine)",
		Args:        cobra.MaximumNArgs(1),
		Annotations: meta(needsCompanion, "20s"),
		RunE: e.run(func(s *session, args []string) error {
			sh, err := NeedAddon[device.Screenshotter](s.dev, "screenshots")
			if err != nil {
				return err
			}
			png, err := sh.Screenshot(s.ctx)
			if err != nil {
				return err
			}
			path := ""
			if len(args) == 1 {
				path = args[0]
			} else {
				dir := filepath.Join(userPictures(), "tv")
				_ = os.MkdirAll(dir, 0o755)
				path = filepath.Join(dir, time.Now().Format("2006-01-02_15-04-05")+".png")
			}
			if err := os.WriteFile(path, png, 0o644); err != nil {
				return device.Failf("couldn't save the screenshot").Because(err)
			}
			return e.ok(path, map[string]any{"path": path, "bytes": len(png)})
		}),
	}
}

func userPictures() string {
	h, _ := os.UserHomeDir()
	if p := filepath.Join(h, "Pictures"); dirExists(p) {
		return p
	}
	return h
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
