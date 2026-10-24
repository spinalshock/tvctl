package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/omAthalye/tvctl/internal/cast"
	"github.com/omAthalye/tvctl/internal/config"
	"github.com/omAthalye/tvctl/internal/device"
	"github.com/omAthalye/tvctl/internal/receiver"
	"github.com/omAthalye/tvctl/internal/setup"
)

// Verbs backed by the companion app. Each one opens the receiver client
// from the device entry; a missing ReceiverToken produces one consistent
// "run tv setup" message instead of a 403 from the TV.

func (e *cmds) openReceiver() (string, device.Device, *config.Store, *receiver.Client, error) {
	id, d, st, err := e.open()
	if err != nil {
		return "", nil, nil, nil, err
	}
	ent := st.Devices[id]
	if ent.ReceiverToken == "" {
		return "", nil, nil, nil, errors.New("companion app not set up on this TV: run `tv setup`")
	}
	return id, d, st, receiver.New(ent.Host, ent.ReceiverToken), nil
}

// optionalReceiver returns a client for the default device's receiver, or
// nil when none is set up; for verbs that work without one but do better
// with it (app resolution sees built-in apps).
func (e *cmds) optionalReceiver(st *config.Store) *receiver.Client {
	if st == nil {
		return nil
	}
	_, ent, err := st.Resolve(e.env.Device)
	if err != nil || ent.ReceiverToken == "" {
		return nil
	}
	return receiver.New(ent.Host, ent.ReceiverToken)
}

// bringForward is the basic-mode fallback: when the receiver parks a
// request because it can't start activities from the background, launch
// the receiver app through the TV's own API, which may.
func (e *cmds) bringForward(ctx context.Context, d device.Device, needs bool) error {
	if !needs {
		return nil
	}
	return d.Launch(ctx, "dev.tvctl.receiver")
}

func (e *cmds) setup() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Install the companion app on the TV and unlock now-playing, screenshots and events",
		Long: `Installs the tvctl receiver app on the TV and grants it the permissions
Fire OS offers no settings screen for. This is the one step that needs ADB
debugging switched on, temporarily, on the TV; the binary speaks the
protocol itself, nothing to install here. Safe to re-run; it updates the
app in place.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			id, d, st, err := e.open()
			if err != nil {
				return err
			}
			ent := st.Devices[id]
			tok, err := setup.Run(cx, ent, setup.Options{
				Out:    e.env.Out,
				Ask:    e.env.Ask,
				Launch: func(ctx context.Context, pkg string) error { return d.Launch(ctx, pkg) },
			})
			if err != nil {
				return err
			}
			ent.ReceiverToken = tok
			st.Devices[id] = ent
			if err := st.Save(); err != nil {
				return err
			}
			e.emit("", map[string]any{"ok": true, "receiver": true})
			return nil
		},
	}
}

func (e *cmds) now() *cobra.Command {
	return &cobra.Command{
		Use:   "now",
		Short: "What the TV is playing",
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			_, _, _, rc, err := e.openReceiver()
			if err != nil {
				return err
			}
			n, err := rc.Now(cx)
			if err != nil {
				return err
			}
			e.emit(n.Describe(), n)
			return nil
		},
	}
}

func (e *cmds) events() *cobra.Command {
	return &cobra.Command{
		Use:   "events",
		Short: "Stream TV events (now-playing, foreground app, keyboard) until Ctrl-C",
		Long: `Prints one line per event. With --json each line is the receiver's raw
JSON object, which is the form editors and scripts should consume.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			_, _, _, rc, err := e.openReceiver()
			if err != nil {
				return err
			}
			return rc.Events(cx, func(ev receiver.Event) error {
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
		},
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
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			_, d, _, rc, err := e.openReceiver()
			if err != nil {
				return err
			}
			if len(args) == 1 && strings.Contains(args[0], "://") {
				needs, err := rc.Play(cx, args[0], title)
				if err != nil {
					return err
				}
				if err := e.bringForward(cx, d, needs); err != nil {
					return err
				}
				e.emit("playing "+args[0], map[string]any{"ok": true, "url": args[0]})
				return nil
			}
			files, err := cast.Expand(args)
			if err != nil {
				return err
			}
			pl, err := cast.Start(cx, rc, d, files)
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
				case <-cx.Done():
					return nil
				case <-time.After(2 * time.Second):
				}
				n, err := rc.Now(cx)
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
					e.emit("stopped", map[string]any{"ok": true, "reason": "left player"})
					return nil
				}
				more, err := pl.Next(cx)
				if err != nil {
					return err
				}
				if !more {
					e.emit("done", map[string]any{"ok": true, "played": len(files)})
					return nil
				}
				grace = 3
				announce()
			}
		},
	}
	c.Flags().StringVar(&title, "title", "", "title to show for a URL")
	return c
}

func (e *cmds) toast() *cobra.Command {
	var long bool
	c := &cobra.Command{
		Use:   "toast <text>...",
		Short: "Show a short message on the TV",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			_, _, _, rc, err := e.openReceiver()
			if err != nil {
				return err
			}
			text := strings.Join(args, " ")
			if err := rc.Toast(cx, text, long); err != nil {
				return err
			}
			e.emit("ok", map[string]any{"ok": true, "text": text})
			return nil
		},
	}
	c.Flags().BoolVar(&long, "long", false, "keep it on screen longer")
	return c
}

func (e *cmds) shot() *cobra.Command {
	return &cobra.Command{
		Use:   "shot [file.png]",
		Short: "Screenshot the TV (DRM video shows black; menus and most apps are fine)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			_, _, _, rc, err := e.openReceiver()
			if err != nil {
				return err
			}
			png, err := rc.Screenshot(cx)
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
			e.emit(path, map[string]any{"ok": true, "path": path, "bytes": len(png)})
			return nil
		},
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
