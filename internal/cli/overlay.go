package cli

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/spinalshock/tvctl/internal/device"
)

// overlayFlags are shared by `watch` and `overlay`: how the panel looks.
type overlayFlags struct {
	pos     string
	lines   int
	title   string
	opacity float64
	size    float64
	width   float64
}

func (f *overlayFlags) bind(c *cobra.Command) {
	c.Flags().StringVar(&f.pos, "pos", "bottom", "panel position: bottom, top, left, right, full")
	c.Flags().IntVarP(&f.lines, "lines", "n", 12, "rows to keep on screen")
	c.Flags().StringVar(&f.title, "title", "", "header line above the text")
	c.Flags().Float64Var(&f.opacity, "opacity", 0.85, "panel darkness, 0 (clear) to 1 (solid)")
	c.Flags().Float64Var(&f.size, "size", 18, "text size")
	c.Flags().Float64Var(&f.width, "width", 0.45, "fraction of the screen for left/right panels")
}

func (f *overlayFlags) opts(titleSet bool) device.Panel {
	return device.Panel{
		Title: f.title, HasTitle: titleSet || f.title != "",
		Pos: f.pos, MaxLines: f.lines, Opacity: f.opacity, Size: f.size, Width: f.width,
	}
}

// watch mirrors a command's output (or stdin) onto the TV as it happens:
//
//	tv watch -- npm run dev
//	tail -f app.log | tv watch --pos right
//
// The terminal still sees everything; the TV gets a copy. Lines are
// batched for ~150 ms so a chatty process is one request per tick rather
// than one per line. ANSI colour is stripped because the panel is plain
// text. The panel is removed when the stream ends unless --keep is set.
func (e *cmds) watch() *cobra.Command {
	var f overlayFlags
	var keep bool
	c := &cobra.Command{
		Use:         "watch [flags] [-- command [args...]]",
		Short:       "Mirror a command's output (or stdin) onto the TV",
		Annotations: meta(needsCompanion, "none"),
		RunE: func(cmd *cobra.Command, args []string) error {
			titleSet := cmd.Flags().Changed("title")
			return e.run(func(s *session, args []string) error {
				return e.watchRun(s, args, &f, titleSet, keep)
			})(cmd, args)
		},
	}
	f.bind(c)
	c.Flags().BoolVar(&keep, "keep", false, "leave the panel on screen after the stream ends")
	// Flags stop at the first positional, so `tv watch go test -v` needs no --.
	c.Flags().SetInterspersed(false)
	return c
}

func (e *cmds) watchRun(s *session, args []string, f *overlayFlags, titleSet, keep bool) error {
	rc, err := NeedAddon[device.Overlayer](s.dev, "overlays")
	if err != nil {
		return err
	}
	cx := s.ctx
	{
		{
			// Source: the child's combined output, or our stdin.
			var src io.Reader
			var child *exec.Cmd
			if len(args) > 0 {
				child = exec.CommandContext(cx, args[0], args[1:]...)
				child.Stdin = os.Stdin
				pr, pw := io.Pipe()
				child.Stdout, child.Stderr = pw, pw
				if err := child.Start(); err != nil {
					return err
				}
				go func() { _ = child.Wait(); pw.Close() }()
				src = pr
			} else {
				if fi, _ := os.Stdin.Stat(); fi != nil && fi.Mode()&os.ModeCharDevice != 0 {
					return device.Fail("nothing to watch").Note("pipe something in, or give a command after --")
				}
				src = os.Stdin
			}

			// Clear the panel and set its look once; everything after is
			// append-only so the TV holds the tail.
			title := f.title
			if title == "" && child != nil {
				title = strings.Join(args, " ")
			}
			o := f.opts(titleSet)
			o.Title, o.HasTitle = title, true
			if err := rc.Overlay(cx, nil, false, o); err != nil {
				return err
			}
			if !keep {
				defer func() {
					hc, hcancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer hcancel()
					_ = rc.OverlayHide(hc)
				}()
			}

			lines := make(chan string, 256)
			go func() {
				sc := bufio.NewScanner(src)
				sc.Buffer(make([]byte, 64<<10), 1<<20)
				for sc.Scan() {
					l := sc.Text()
					_, _ = io.WriteString(e.env.Out, l+"\n")
					lines <- ansi.Strip(l)
				}
				close(lines)
			}()

			// Batch and ship. A tick with nothing pending sends nothing.
			var batch []string
			flush := func() error {
				if len(batch) == 0 {
					return nil
				}
				// Only the tail can be visible; don't ship more than fits.
				if len(batch) > f.lines {
					batch = batch[len(batch)-f.lines:]
				}
				err := rc.Overlay(cx, batch, true, device.Panel{})
				batch = batch[:0]
				return err
			}
			tick := time.NewTicker(150 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-cx.Done():
					return nil
				case l, ok := <-lines:
					if !ok {
						if err := flush(); err != nil {
							return err
						}
						if child != nil && child.ProcessState != nil && !child.ProcessState.Success() {
							return &exitError{child.ProcessState.ExitCode()}
						}
						return nil
					}
					batch = append(batch, l)
					if len(batch) >= 200 {
						if err := flush(); err != nil {
							return err
						}
					}
				case <-tick.C:
					if err := flush(); err != nil {
						return err
					}
				}
			}
		}
	}
}

// exitError carries a child's exit status through cobra to main.
type exitError struct{ code int }

func (e *exitError) Error() string { return "command exited with status " + strconv.Itoa(e.code) }

// overlay puts fixed text on the TV until told otherwise:
//
//	tv overlay "deploy in progress"
//	tv overlay --pos top --lines 1 "$(date)"
//	tv overlay off
func (e *cmds) overlay() *cobra.Command {
	var f overlayFlags
	c := &cobra.Command{
		Use:         "overlay <text>... | off",
		Short:       "Pin text on the TV screen (overlay off removes it)",
		Annotations: meta(needsCompanion, ""),
		Args:        cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			titleSet, linesSet := cmd.Flags().Changed("title"), cmd.Flags().Changed("lines")
			return e.run(func(s *session, args []string) error {
				ov, err := NeedAddon[device.Overlayer](s.dev, "overlays")
				if err != nil {
					return err
				}
				if len(args) == 1 && (args[0] == "off" || args[0] == "hide") {
					if err := ov.OverlayHide(s.ctx); err != nil {
						return err
					}
					return e.ok("", map[string]any{"visible": false})
				}
				lines := strings.Split(strings.Join(args, " "), "\n")
				if !linesSet {
					f.lines = len(lines)
				}
				if err := ov.Overlay(s.ctx, lines, false, f.opts(titleSet)); err != nil {
					return err
				}
				return e.ok("", map[string]any{"visible": true, "lines": len(lines)})
			})(cmd, args)
		},
	}
	f.bind(c)
	return c
}
