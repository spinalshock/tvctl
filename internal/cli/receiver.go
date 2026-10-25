package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/omAthalye/tvctl/internal/cast"
	"github.com/omAthalye/tvctl/internal/receiver"
	"github.com/omAthalye/tvctl/internal/setup"
)

// Verbs backed by the companion app. They use runRC, which produces one
// consistent "run tv setup" message when the app isn't paired.

func (e *cmds) setup() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Install the companion app on the TV (now-playing, screenshots, events, overlays)",
		Long: `Installs the tvctl receiver app on the TV and grants it the permissions
Fire OS offers no settings screen for. This is the one step that needs ADB
debugging switched on, temporarily, on the TV; the binary speaks the
protocol itself, nothing to install here. Safe to re-run; it updates the
app in place.`,
		RunE: e.run(func(s *session, _ []string) error {
			tok, err := setup.Run(s.ctx, s.entry, setup.Options{
				Out:    e.env.Out,
				Ask:    e.env.Ask,
				Launch: func(ctx context.Context, pkg string) error { return s.dev.Launch(ctx, pkg) },
			})
			if err != nil {
				return err
			}
			s.entry.ReceiverToken = tok
			if err := s.save(); err != nil {
				return err
			}
			return e.ok("", map[string]any{"receiver": true})
		}),
	}
}

func (e *cmds) now() *cobra.Command {
	return &cobra.Command{
		Use:   "now",
		Short: "What the TV is playing",
		RunE: e.runRC(func(s *session, _ []string) error {
			n, err := s.rc.Now(s.ctx)
			if err != nil {
				return err
			}
			e.emit(n.Describe(), n)
			return nil
		}),
	}
}

func (e *cmds) events() *cobra.Command {
	return &cobra.Command{
		Use:   "events",
		Short: "Stream TV events (now-playing, foreground app, keyboard) until Ctrl-C",
		Long: `Prints one line per event. With --json each line is the receiver's raw
JSON object, which is the form editors and scripts should consume.`,
		RunE: e.runRC(func(s *session, _ []string) error {
			return s.rc.Events(s.ctx, func(ev receiver.Event) error {
				if e.env.JSON {
					fmt.Fprintln(e.env.Out, string(ev.Raw))
					return nil
				}
				switch ev.Type {
				case "now":
					fmt.Fprintln(e.env.Out, "now        "+ev.Now.Describe())
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
	var title string
	c := &cobra.Command{
		Use:   "play <file|dir|url>...",
		Short: "Play local files (or a direct media URL) in the TV's native player",
		Long: `Local files are served to the TV from this machine for as long as the
command runs; several files, or a directory, play in order. Ctrl-C stops
serving (the TV keeps what it has buffered). A URL is handed to the TV
directly and the command returns.`,
		Args: cobra.MinimumNArgs(1),
		RunE: e.runRC(func(s *session, args []string) error {
			if len(args) == 1 && isLink(args[0]) {
				needs, err := s.rc.Play(s.ctx, args[0], title)
				if err != nil {
					return err
				}
				if err := s.bringForward(needs); err != nil {
					return err
				}
				return e.ok("playing "+args[0], map[string]any{"url": args[0]})
			}
			files, err := cast.Expand(args)
			if err != nil {
				return err
			}
			pl, err := cast.Start(s.ctx, s.rc, s.dev, files)
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
			// Poll now-playing; advance when the current item ends, stop
			// when the user leaves the player. A short grace period after
			// each start covers the moment before the TV reports anything.
			grace := 3
			for {
				select {
				case <-s.ctx.Done():
					return nil
				case <-time.After(2 * time.Second):
				}
				n, err := s.rc.Now(s.ctx)
				if err != nil {
					continue
				}
				if grace > 0 {
					grace--
					if n.App == cast.ReceiverPackage && n.State == "playing" {
						grace = 0
					}
					continue
				}
				if !cast.Finished(n) {
					continue
				}
				if n.App != cast.ReceiverPackage {
					return e.ok("stopped", map[string]any{"reason": "left player"})
				}
				more, err := pl.Next(s.ctx)
				if err != nil {
					return err
				}
				if !more {
					return e.ok("done", map[string]any{"played": len(files)})
				}
				grace = 3
				announce()
			}
		}),
	}
	c.Flags().StringVar(&title, "title", "", "title to show for a URL")
	return c
}

func (e *cmds) toast() *cobra.Command {
	var long bool
	var pos string
	c := &cobra.Command{
		Use:   "toast <text>...",
		Short: "Show a short message on the TV for a few seconds",
		Long: `A card over whatever is on screen, video included. Anything can call it:
a script, a cron job, or any program on the network (POST /toast on the
receiver, see tv setup). Pipe text in: echo done | tv toast -`,
		Args: cobra.MinimumNArgs(1),
		RunE: e.runRC(func(s *session, args []string) error {
			text := strings.Join(args, " ")
			if text == "-" {
				b, err := io.ReadAll(os.Stdin)
				if err != nil {
					return err
				}
				text = strings.TrimSpace(string(b))
			}
			if err := s.rc.Toast(s.ctx, text, long, pos); err != nil {
				return err
			}
			return e.ok("ok", map[string]any{"text": text})
		}),
	}
	c.Flags().BoolVar(&long, "long", false, "keep it on screen longer")
	c.Flags().StringVar(&pos, "pos", "top-right", "top-right, top-left, bottom-right, bottom-left, top, bottom")
	return c
}

func (e *cmds) shot() *cobra.Command {
	return &cobra.Command{
		Use:   "shot [file.png]",
		Short: "Screenshot the TV (DRM video shows black; menus and most apps are fine)",
		Args:  cobra.MaximumNArgs(1),
		RunE: e.runRC(func(s *session, args []string) error {
			png, err := s.rc.Screenshot(s.ctx)
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
				return err
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
