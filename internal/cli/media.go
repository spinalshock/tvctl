package cli

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/spinalshock/tvctl/internal/device"
)

// Transport verbs. With the companion app they act on the active media
// session of any app (Netflix, Spotify, YouTube, our player) and are
// deterministic. Without it they fall back to the TV's media keys, which
// every app honours but which can only toggle or skip.

// transport does one playback action: through the TV's media session when
// it has one (device.Transport), else with the remote key that means the
// same thing. what names the action for the error when neither works.
func (e *cmds) transport(s *session, action, what string, positionMs int64, fallback device.Key) error {
	if t, ok := s.dev.(device.Transport); ok {
		err := t.Control(s.ctx, action, positionMs)
		if err == nil {
			return e.ok("", map[string]any{"action": action, "via": "session"})
		}
		if !errors.Is(err, device.ErrNothingPlaying) {
			return err
		}
	}
	var err error
	if fallback != "" {
		err = s.dev.Key(s.ctx, fallback)
		if err == nil {
			return e.ok("", map[string]any{"action": action, "via": "key"})
		}
	}
	// No session and no key for it (a time seek anywhere; stop on a TV
	// whose remote has no Stop): say what would add it, or that it can't.
	if _, aerr := NeedAddon[device.Transport](s.dev, what); aerr != nil {
		return aerr
	}
	if err != nil {
		return err
	}
	return device.ErrNothingPlaying
}

// transportCmd is the one shape all the simple transport verbs share.
func (e *cmds) transportCmd(use, short, action, what string, fallback device.Key) *cobra.Command {
	return &cobra.Command{
		Use: use, Short: short, Args: cobra.NoArgs,
		RunE: e.run(func(s *session, _ []string) error { return e.transport(s, action, what, 0, fallback) }),
	}
}

func (e *cmds) pause() *cobra.Command {
	return e.transportCmd("pause", "Pause whatever is playing (play resumes)", "pause", "pausing", device.KeyPlayPause)
}

func (e *cmds) stop() *cobra.Command {
	return e.transportCmd("stop", "Stop playback", "stop", "stopping playback", device.KeyStop)
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
			return false, 0, device.Failf("%s is not a number of seconds", s)
		}
		return true, int64(n) * 1000, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return false, 0, device.Fail("seek takes a time like +30, -10, 90, or 1:23")
	}
	return false, int64(n) * 1000, nil
}

func (e *cmds) seek() *cobra.Command {
	return &cobra.Command{
		Use:   "seek <next|prev|+secs|-secs|m:ss>",
		Short: "Next or previous track, or a position within what's playing",
		Long: `seek next and seek prev skip tracks or episodes (fast-forward and rewind
keys without the companion app). A time seeks within the current item and
needs the companion app: +30, -10, 90 (seconds), 1:23 or 1:02:03.`,
		ValidArgs: []string{"next", "prev"},
		// "-30" must reach us as a value, not a flag; so parse by hand and
		// honour the global flags (--json, --device) ourselves.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var vals []string
			for i := 0; i < len(args); i++ {
				a := args[i]
				switch {
				case a == "--json":
					e.env.JSON = true
				case a == "--help" || a == "-h":
					return cmd.Help()
				case (a == "-d" || a == "--device") && i+1 < len(args):
					i++
					e.env.Device = args[i]
				case strings.HasPrefix(a, "--device="):
					e.env.Device = strings.TrimPrefix(a, "--device=")
				default:
					vals = append(vals, a)
				}
			}
			if len(vals) != 1 {
				return device.Fail("seek takes one value: next, prev, +30, -10, 90, or 1:23")
			}
			switch vals[0] {
			case "next":
				return e.run(func(s *session, _ []string) error { return e.transport(s, "next", "skipping", 0, device.KeyFastFwd) })(cmd, nil)
			case "prev", "previous", "back":
				return e.run(func(s *session, _ []string) error {
					return e.transport(s, "previous", "skipping back", 0, device.KeyRewind)
				})(cmd, nil)
			}
			rel, ms, err := parseSeek(vals[0])
			if err != nil {
				return err
			}
			action := "seek"
			if rel {
				action = "seek_by"
			}
			return e.run(func(s *session, _ []string) error {
				return e.transport(s, action, "seeking within what's playing", ms, "")
			})(cmd, nil)
		},
	}
}
