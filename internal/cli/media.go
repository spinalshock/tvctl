package cli

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/omAthalye/tvctl/internal/device"
	"github.com/omAthalye/tvctl/internal/receiver"
)

// Transport verbs. With the companion app they act on the active media
// session of any app (Netflix, Spotify, YouTube, our player) and are
// deterministic. Without it they fall back to the TV's media keys, which
// every app honours but which can only toggle or skip.

func (e *cmds) transport(action string, positionMs int64, fallback device.Key) error {
	cx, cancel := ctx()
	defer cancel()
	id, d, st, err := e.open()
	if err != nil {
		return err
	}
	ent := st.Devices[id]
	if ent.ReceiverToken != "" {
		rc := receiver.New(ent.Host, ent.ReceiverToken)
		if err := rc.Control(cx, action, positionMs); err == nil {
			e.emit(action, map[string]any{"ok": true, "action": action, "via": "session"})
			return nil
		} else if !strings.Contains(err.Error(), "nothing is playing") {
			return err
		}
	}
	if fallback == "" {
		return errors.New("needs the companion app (tv setup) or something playing")
	}
	if err := d.Key(cx, fallback); err != nil {
		return err
	}
	e.emit(action+" (media key)", map[string]any{"ok": true, "action": action, "via": "key"})
	return nil
}

func (e *cmds) pause() *cobra.Command {
	return &cobra.Command{
		Use: "pause", Short: "Pause whatever is playing", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return e.transport("pause", 0, device.KeyPlayPause) },
	}
}

func (e *cmds) resume() *cobra.Command {
	return &cobra.Command{
		Use: "resume", Aliases: []string{"unpause"}, Short: "Resume playback", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return e.transport("play", 0, device.KeyPlayPause) },
	}
}

func (e *cmds) toggle() *cobra.Command {
	return &cobra.Command{
		Use: "toggle", Aliases: []string{"pp"}, Short: "Play/pause toggle", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return e.transport("toggle", 0, device.KeyPlayPause) },
	}
}

func (e *cmds) stop() *cobra.Command {
	return &cobra.Command{
		Use: "stop", Short: "Stop playback", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return e.transport("stop", 0, "") },
	}
}

func (e *cmds) next() *cobra.Command {
	return &cobra.Command{
		Use: "next", Short: "Next track / episode (fast-forward on the TV without the app)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return e.transport("next", 0, device.KeyFastFwd) },
	}
}

func (e *cmds) prev() *cobra.Command {
	return &cobra.Command{
		Use: "prev", Aliases: []string{"previous"}, Short: "Previous track (rewind on the TV without the app)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return e.transport("previous", 0, device.KeyRewind) },
	}
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
			if rel {
				return e.transport("seek_by", ms, "")
			}
			return e.transport("seek", ms, "")
		},
	}
}
