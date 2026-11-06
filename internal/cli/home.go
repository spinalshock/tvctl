package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/omAthalye/tvctl/internal/device"
)

// The home dashboard lives in the companion app on the TV: a minimal row of
// tiles shown instead of the TV's own home screen. Reordering happens on the TV
// itself (long-press OK on a tile); everything else is here.

var reInput = regexp.MustCompile(`^hdmi[1-4]$`)

func (e *cmds) home() *cobra.Command {
	c := &cobra.Command{
		Use:         "home",
		Short:       "The TV's home dashboard: tiles, on/off, wallpaper",
		Annotations: meta(needsCompanion, ""),
		Long: `A minimal home screen from the companion app, shown instead of the TV's own
when on. The TV's settings stay reachable (the gear, or its assistant).

  tv home                        show the tiles and whether it's on
  tv home on | off               replace the TV's home screen, or stop
  tv home add <app|hdmi1..4>     add a tile (--at N for a position)
  tv home remove <app|hdmiN|N>   remove a tile
  tv home wallpaper <image|off>  set or clear the background

On the TV, long-press OK on a tile to move it, then ◀ ▶ and OK.`,
		Args: cobra.NoArgs,
		RunE: e.runHome(func(s *session, hd device.HomeDashboard, _ []string) error {
			st, err := hd.Home(s.ctx)
			if err != nil {
				return err
			}
			e.emit(describeHome(st), st)
			return nil
		}),
	}
	c.AddCommand(e.homeToggle("on", true), e.homeToggle("off", false), e.homeAdd(), e.homeRemove(), e.homeWallpaper())
	return c
}

// runHome is run for the dashboard verbs: one capability check for all.
func (e *cmds) runHome(f func(s *session, hd device.HomeDashboard, args []string) error) func(*cobra.Command, []string) error {
	return e.run(func(s *session, args []string) error {
		hd, err := NeedAddon[device.HomeDashboard](s.dev, "the home dashboard")
		if err != nil {
			return err
		}
		return f(s, hd, args)
	})
}

func describeHome(st *device.Dashboard) string {
	state := "off (the TV's own home screen shows)"
	if st.Enabled {
		state = "on"
	}
	lines := []string{"home dashboard: " + state}
	for i, t := range st.Tiles {
		note := ""
		if t.Type == "app" && !t.Installed {
			note = "  (not installed, hidden)"
		}
		lines = append(lines, fmt.Sprintf("  %d. %-16s %s%s", i+1, t.Label, t.ID, note))
	}
	if st.Wallpaper {
		lines = append(lines, "wallpaper: custom")
	} else {
		lines = append(lines, "wallpaper: default")
	}
	return strings.Join(lines, "\n")
}

func (e *cmds) homeToggle(use string, on bool) *cobra.Command {
	short := "Show the dashboard instead of the TV's own home screen"
	if !on {
		short = "Stop replacing the TV's home screen"
	}
	return &cobra.Command{
		Use: use, Short: short, Args: cobra.NoArgs, Annotations: meta(needsCompanion, ""),
		RunE: e.runHome(func(s *session, hd device.HomeDashboard, _ []string) error {
			st, err := hd.SetHome(s.ctx, &on, nil)
			if err != nil {
				return err
			}
			line := "Dashboard off."
			if on {
				line = "Dashboard on."
			}
			e.notice(line)
			e.emit(line, st)
			return nil
		}),
	}
}

func (e *cmds) homeAdd() *cobra.Command {
	var at int
	c := &cobra.Command{
		Annotations: meta(needsCompanion, ""),
		Use:         "add <app|hdmi1..4>",
		Short:       "Add a tile (an app by alias, name or package, or an HDMI input)",
		Args:        cobra.ExactArgs(1),
		RunE: e.runHome(func(s *session, hd device.HomeDashboard, args []string) error {
			t, err := s.homeTile(args[0])
			if err != nil {
				return err
			}
			st, err := hd.Home(s.ctx)
			if err != nil {
				return err
			}
			for _, x := range st.Tiles {
				if x.Type == t.Type && x.ID == t.ID {
					return device.Failf("%s is already on the dashboard", x.Label)
				}
			}
			tiles := append([]device.Tile{}, st.Tiles...)
			pos := len(tiles)
			if at > 0 && at <= len(tiles) {
				pos = at - 1
			}
			tiles = append(tiles[:pos], append([]device.Tile{t}, tiles[pos:]...)...)
			if st, err = hd.SetHome(s.ctx, nil, tiles); err != nil {
				return err
			}
			e.notice("Tile added.")
			e.emit("Tile added.", st)
			return nil
		}),
	}
	c.Flags().IntVar(&at, "at", 0, "position (1 = first); default last")
	return c
}

func (e *cmds) homeRemove() *cobra.Command {
	return &cobra.Command{
		Annotations: meta(needsCompanion, ""),
		Use:         "remove <app|hdmiN|position>",
		Short:       "Remove a tile",
		Args:        cobra.ExactArgs(1),
		RunE: e.runHome(func(s *session, hd device.HomeDashboard, args []string) error {
			st, err := hd.Home(s.ctx)
			if err != nil {
				return err
			}
			idx := -1
			if n, err := strconv.Atoi(args[0]); err == nil {
				idx = n - 1
			} else if t, err := s.homeTile(args[0]); err == nil {
				for i, x := range st.Tiles {
					if x.Type == t.Type && x.ID == t.ID {
						idx = i
					}
				}
			}
			if idx < 0 || idx >= len(st.Tiles) {
				return device.Failf("no tile %s on the dashboard", args[0]).Note("tv home lists them")
			}
			tiles := append(append([]device.Tile{}, st.Tiles[:idx]...), st.Tiles[idx+1:]...)
			if st, err = hd.SetHome(s.ctx, nil, tiles); err != nil {
				return err
			}
			e.notice("Tile removed.")
			e.emit("Tile removed.", st)
			return nil
		}),
	}
}

func (e *cmds) homeWallpaper() *cobra.Command {
	return &cobra.Command{
		Annotations: meta(needsCompanion, ""),
		Use:         "wallpaper <image|off>",
		Short:       "Set the dashboard's background image (JPEG/PNG/WebP), or off for the default",
		Args:        cobra.ExactArgs(1),
		RunE: e.runHome(func(s *session, hd device.HomeDashboard, args []string) error {
			var st *device.Dashboard
			var err error
			msg := "Wallpaper set."
			if args[0] == "off" {
				msg = "Wallpaper cleared."
				st, err = hd.ClearWallpaper(s.ctx)
			} else {
				img, rerr := os.ReadFile(args[0])
				if rerr != nil {
					return device.Failf("couldn't read %s", filepath.Base(args[0])).Because(rerr)
				}
				if len(img) > 25<<20 {
					return device.Fail("the image must be under 25 MB")
				}
				st, err = hd.SetWallpaper(s.ctx, img)
			}
			if err != nil {
				return err
			}
			e.notice(msg)
			e.emit(msg, st)
			return nil
		}),
	}
}

// homeTile turns "hdmi1" into an input tile and anything else into an app
// tile, resolved like `tv open` (alias, name prefix or package).
func (s *session) homeTile(name string) (device.Tile, error) {
	if n := strings.ToLower(name); reInput.MatchString(n) {
		return device.Tile{Type: "input", ID: n}, nil
	}
	id, err := s.resolveApp(name)
	if err != nil {
		return device.Tile{}, err
	}
	return device.Tile{Type: "app", ID: id}, nil
}
