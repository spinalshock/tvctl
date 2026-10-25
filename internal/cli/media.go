package cli

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/omAthalye/tvctl/internal/device"
)

// Transport verbs. With the companion app they act on the active media
// session of any app (Netflix, Spotify, YouTube, our player) and are
// deterministic. Without it they fall back to the TV's media keys, which
// every app honours but which can only toggle or skip.

func (e *cmds) transport(s *session, action string, positionMs int64, fallback device.Key) error {
	if s.rc != nil {
		err := s.rc.Control(s.ctx, action, positionMs)
		if err == nil {
			return e.ok(action, map[string]any{"action": action, "via": "session"})
		}
		if !strings.Contains(err.Error(), "nothing is playing") {
			return err
		}
	}
	if fallback == "" {
		return errors.New("needs the companion app (tv setup) or something playing")
	}
	if err := s.dev.Key(s.ctx, fallback); err != nil {
		return err
	}
	return e.ok(action+" (media key)", map[string]any{"action": action, "via": "key"})
}

// transportCmd is the one shape all the simple transport verbs share.
func (e *cmds) transportCmd(use, short string, aliases []string, action string, fallback device.Key) *cobra.Command {
	return &cobra.Command{
		Use: use, Aliases: aliases, Short: short, Args: cobra.NoArgs,
		RunE: e.run(func(s *session, _ []string) error { return e.transport(s, action, 0, fallback) }),
	}
}

func (e *cmds) pause() *cobra.Command {
	return e.transportCmd("pause", "Pause whatever is playing", nil, "pause", device.KeyPlayPause)
}

func (e *cmds) resume() *cobra.Command {
	return e.transportCmd("resume", "Resume playback", []string{"unpause"}, "play", device.KeyPlayPause)
}

func (e *cmds) toggle() *cobra.Command {
	return e.transportCmd("toggle", "Play/pause toggle", []string{"pp"}, "toggle", device.KeyPlayPause)
}

func (e *cmds) stop() *cobra.Command {
	return e.transportCmd("stop", "Stop playback", nil, "stop", "")
}

func (e *cmds) next() *cobra.Command {
	return e.transportCmd("next", "Next track / episode (fast-forward without the companion app)", nil, "next", device.KeyFastFwd)
}

func (e *cmds) prev() *cobra.Command {
	return e.transportCmd("prev", "Previous track (rewind without the companion app)", []string{"previous"}, "previous", device.KeyRewind)
}

var reClock = regexp.MustCompile(`^(?:(\d+):)?(\d{1,2}):(\d{2})$`)

// parseSeek accepts "+30", "-10", "90" (seconds) or "1:23" / "1:02:03".
func parseSeek(s string) (relative bool, ms int64, err error) {
	if m := reClock.FindStringSubmatch(s); m != nil {
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		se, _ := strconv.Atoi(m[3])
		return false, int64(h*3600+mi*60+se) * 1000, nil
	}
	if strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") {
		n, err := strconv.Atoi(s)
		if err != nil {
			return false, 0, fmt.Errorf("seek: %q is not a number of seconds", s)
		}
		return true, int64(n) * 1000, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return false, 0, fmt.Errorf("seek: use +30, -10, 90, or 1:23")
	}
	return false, int64(n) * 1000, nil
}

func (e *cmds) seek() *cobra.Command {
	return &cobra.Command{
		Use:   "seek <+secs|-secs|secs|m:ss|h:mm:ss>",
		Short: "Seek within what's playing (needs the companion app)",
		// "-30" must reach us as a value, not a flag; so parse by hand and
		// honour --json ourselves.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var vals []string
			for _, a := range args {
				switch a {
				case "--json":
					e.env.JSON = true
				case "--help", "-h":
					return cmd.Help()
				default:
					vals = append(vals, a)
				}
			}
			if len(vals) != 1 {
				return errors.New("seek takes one value: +30, -10, 90, or 1:23")
			}
			rel, ms, err := parseSeek(vals[0])
			if err != nil {
				return err
			}
			action := "seek"
			if rel {
				action = "seek_by"
			}
			return e.run(func(s *session, _ []string) error { return e.transport(s, action, ms, "") })(cmd, nil)
		},
	}
}
