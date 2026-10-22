package cli

import (
	_ "embed"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

//go:embed dash/clock.html
var clockHTML string

// dash puts a full-screen page on the TV:
//
//	tv dash                      built-in clock (with now-playing)
//	tv dash page.html            your own HTML; --watch re-pushes on change
//	tv dash http://mac:8787/     a page the TV loads itself
//	tv dash js "document.title"  run JavaScript in the open page
//	tv dash off
//
// HTML is sent as-is. A page that needs live data either fetches it from a
// server on this machine (give --base so relative URLs resolve there) or
// is updated in place with `tv dash js`.
func (e *cmds) dash() *cobra.Command {
	var base string
	var watch bool
	c := &cobra.Command{
		Use:   "dash [clock|<file.html>|<url>|js <code>|off]",
		Short: "Show a full-screen page on the TV (clock by default)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			_, d, _, rc, err := e.openReceiver()
			if err != nil {
				return err
			}
			if len(args) >= 1 {
				switch args[0] {
				case "off", "close":
					if err := rc.DashClose(cx); err != nil {
						return err
					}
					e.emit("dashboard closed", map[string]any{"open": false})
					return nil
				case "js":
					if len(args) < 2 {
						return errors.New("dash js: give the code to run")
					}
					if err := rc.DashJS(cx, strings.Join(args[1:], " ")); err != nil {
						return err
					}
					e.emit("ok", map[string]any{"ok": true})
					return nil
				}
			}

			// What to show: a URL, a file, or the built-in clock.
			var file string
			html, url := clockHTML, ""
			if len(args) >= 1 && args[0] != "clock" {
				if strings.Contains(args[0], "://") {
					html, url = "", args[0]
				} else {
					file = args[0]
					b, err := os.ReadFile(file)
					if err != nil {
						return err
					}
					html = string(b)
				}
			}
			push := func() error {
				needs, err := rc.Dash(cx, html, url, base)
				if err != nil {
					return err
				}
				return e.bringForward(cx, d, needs)
			}
			if err := push(); err != nil {
				return err
			}
			what := "clock"
			if url != "" {
				what = url
			} else if file != "" {
				what = file
			}
			e.emit("showing "+what, map[string]any{"open": true, "source": what})

			if !watch || file == "" {
				return nil
			}
			// Re-push whenever the file's mtime moves. Polling once a second
			// is plenty for hand edits and needs no platform watcher.
			last, _ := mtime(file)
			for {
				select {
				case <-cx.Done():
					return nil
				case <-time.After(time.Second):
				}
				m, err := mtime(file)
				if err != nil || m.Equal(last) {
					continue
				}
				last = m
				b, err := os.ReadFile(file)
				if err != nil {
					continue
				}
				html = string(b)
				if err := push(); err != nil {
					return err
				}
			}
		},
	}
	c.Flags().StringVar(&base, "base", "", "base URL for relative links in pushed HTML (e.g. http://<this machine>:8787/)")
	c.Flags().BoolVar(&watch, "watch", false, "keep running and re-push the file when it changes")
	return c
}

func mtime(path string) (time.Time, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return fi.ModTime(), nil
}
