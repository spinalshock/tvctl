package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

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
		Use:   "play <file|url>",
		Short: "Play a local video file or a direct media URL in the TV's native player",
		Long: `A local file is served to the TV from this machine over HTTP for as long
as the command runs (Ctrl-C stops serving; the TV keeps whatever it has
buffered). A URL is handed to the TV directly and the command returns.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			_, d, _, rc, err := e.openReceiver()
			if err != nil {
				return err
			}
			src := args[0]
			if strings.Contains(src, "://") {
				needs, err := rc.Play(cx, src, title)
				if err != nil {
					return err
				}
				if err := e.bringForward(cx, d, needs); err != nil {
					return err
				}
				e.emit("playing "+src, map[string]any{"ok": true, "url": src})
				return nil
			}
			// Local file: serve just that one file on an ephemeral port on
			// the interface that reaches the TV.
			abs, err := filepath.Abs(src)
			if err != nil {
				return err
			}
			if _, err := os.Stat(abs); err != nil {
				return err
			}
			ip, err := localIPFor(rc.Host)
			if err != nil {
				return err
			}
			ln, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
			if err != nil {
				return err
			}
			name := filepath.Base(abs)
			mux := http.NewServeMux()
			mux.HandleFunc("/v/", func(w http.ResponseWriter, r *http.Request) {
				// ServeFile handles Range requests, which is what lets the
				// player seek.
				http.ServeFile(w, r, abs)
			})
			srv := &http.Server{Handler: mux}
			go srv.Serve(ln)
			defer srv.Close()
			url := fmt.Sprintf("http://%s/v/%s", ln.Addr().String(), urlSafe(name))
			if title == "" {
				title = strings.TrimSuffix(name, filepath.Ext(name))
			}
			needs, err := rc.Play(cx, url, title)
			if err != nil {
				return err
			}
			if err := e.bringForward(cx, d, needs); err != nil {
				return err
			}
			if !e.env.JSON {
				fmt.Fprintf(e.env.Out, "playing %s  (serving from %s, Ctrl-C to stop)\n", name, ln.Addr())
			}
			// Stay alive while the TV is playing or paused on our item;
			// exit a little after it stops or moves on.
			idle := 0
			for {
				select {
				case <-cx.Done():
					return nil
				case <-time.After(3 * time.Second):
				}
				n, err := rc.Now(cx)
				if err != nil {
					continue
				}
				if n.App == "dev.tvctl.receiver" && (n.State == "playing" || n.State == "paused" || n.State == "buffering") {
					idle = 0
					continue
				}
				if idle++; idle >= 4 {
					e.emit("done", map[string]any{"ok": true, "file": name})
					return nil
				}
			}
		},
	}
	c.Flags().StringVar(&title, "title", "", "title to show in the player and now-playing")
	return c
}

// localIPFor finds the local address the OS would use to reach host, by
// "connecting" a UDP socket (no packets are sent).
func localIPFor(host string) (string, error) {
	c, err := net.Dial("udp", net.JoinHostPort(host, "9"))
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

func urlSafe(name string) string {
	r := strings.NewReplacer(" ", "%20", "#", "%23", "?", "%3F")
	return r.Replace(name)
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
