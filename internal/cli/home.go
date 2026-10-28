package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/omAthalye/tvctl/internal/receiver"
)

// The home dashboard lives in the companion app on the TV: a minimal row of
// tiles shown instead of Amazon's home screen. Reordering happens on the TV
// itself (long-press OK on a tile); everything else is here.

var reInput = regexp.MustCompile(`^hdmi[1-4]$`)

func (e *cmds) home() *cobra.Command {
	c := &cobra.Command{
		Use:   "home",
		Short: "The TV's home dashboard: tiles, on/off, wallpaper",
		Long: `A minimal home screen from the companion app, shown instead of Amazon's
when on. Amazon's Settings stays reachable (the gear, or Alexa).

  tv home                        show the tiles and whether it's on
  tv home on | off               replace Amazon's home screen, or stop
  tv home add <app|hdmi1..4>     add a tile (--at N for a position)
  tv home remove <app|hdmiN|N>   remove a tile
  tv home wallpaper <image|off>  set or clear the background

On the TV, long-press OK on a tile to move it, then ◀ ▶ and OK.`,
		Args: cobra.NoArgs,
		RunE: e.runRC(func(s *session, _ []string) error {
			st, err := s.rc.Home(s.ctx)
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

func describeHome(st *receiver.HomeState) string {
	state := "off (Amazon's home screen shows)"
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
	short := "Show the dashboard instead of Amazon's home screen"
	if !on {
		short = "Stop replacing Amazon's home screen"
	}
	return &cobra.Command{
		Use: use, Short: short, Args: cobra.NoArgs,
		RunE: e.runRC(func(s *session, _ []string) error {
			st, err := s.rc.SetHome(s.ctx, &on, nil)
			if err != nil {
				return err
			}
			e.emit(describeHome(st), st)
			return nil
		}),
	}
}

func (e *cmds) homeAdd() *cobra.Command {
	var at int
	c := &cobra.Command{
		Use:   "add <app|hdmi1..4>",
		Short: "Add a tile (an app by alias, name or package, or an HDMI input)",
		Args:  cobra.ExactArgs(1),
		RunE: e.runRC(func(s *session, args []string) error {
			t, err := s.homeTile(args[0])
			if err != nil {
				return err
			}
			st, err := s.rc.Home(s.ctx)
			if err != nil {
				return err
			}
			for _, x := range st.Tiles {
				if x.Type == t.Type && x.ID == t.ID {
					return fmt.Errorf("%s is already on the dashboard", x.Label)
				}
			}
			tiles := append([]receiver.HomeTile{}, st.Tiles...)
			pos := len(tiles)
			if at > 0 && at <= len(tiles) {
				pos = at - 1
			}
			tiles = append(tiles[:pos], append([]receiver.HomeTile{t}, tiles[pos:]...)...)
			if st, err = s.rc.SetHome(s.ctx, nil, tiles); err != nil {
				return err
			}
			e.emit(describeHome(st), st)
			return nil
		}),
	}
	c.Flags().IntVar(&at, "at", 0, "position (1 = first); default last")
	return c
}

func (e *cmds) homeRemove() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <app|hdmiN|position>",
		Short: "Remove a tile",
		Args:  cobra.ExactArgs(1),
		RunE: e.runRC(func(s *session, args []string) error {
			st, err := s.rc.Home(s.ctx)
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
				return fmt.Errorf("no tile %q on the dashboard (tv home lists them)", args[0])
			}
			tiles := append(append([]receiver.HomeTile{}, st.Tiles[:idx]...), st.Tiles[idx+1:]...)
			if st, err = s.rc.SetHome(s.ctx, nil, tiles); err != nil {
				return err
			}
			e.emit(describeHome(st), st)
			return nil
		}),
	}
}

func (e *cmds) homeWallpaper() *cobra.Command {
	return &cobra.Command{
		Use:   "wallpaper <image|off>",
		Short: "Set the dashboard's background image (JPEG/PNG/WebP), or off for the default",
		Args:  cobra.ExactArgs(1),
		RunE: e.runRC(func(s *session, args []string) error {
			var st *receiver.HomeState
			var err error
			msg := fmt.Sprintf("✓ %s is now the TV's wallpaper", filepath.Base(args[0]))
			if args[0] == "off" {
				msg = "✓ wallpaper cleared: back to the default"
				st, err = s.rc.ClearWallpaper(s.ctx)
			} else {
				img, rerr := os.ReadFile(args[0])
				if rerr != nil {
					return rerr
				}
				if len(img) > 25<<20 {
					return errors.New("image must be under 25 MB")
				}
				st, err = s.rc.SetWallpaper(s.ctx, img)
			}
			if err != nil {
				return err
			}
			e.emit(msg, st)
			return nil
		}),
	}
}

// homeTile turns "hdmi1" into an input tile and anything else into an app
// tile, resolved like `tv open` (alias, name prefix or package).
func (s *session) homeTile(name string) (receiver.HomeTile, error) {
	if n := strings.ToLower(name); reInput.MatchString(n) {
		return receiver.HomeTile{Type: "input", ID: n}, nil
	}
	id, err := s.resolveApp(name)
	if err != nil {
		return receiver.HomeTile{}, err
	}
	return receiver.HomeTile{Type: "app", ID: id}, nil
}
