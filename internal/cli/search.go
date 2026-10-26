package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/omAthalye/tvctl/internal/device"
	"github.com/omAthalye/tvctl/internal/youtube"
)

// Netflix, YouTube and Prime draw their own on-screen keyboards, so no
// local route can type into their search fields; the official phone app
// can't either. Alexa can, with system privileges, and the voice channel
// is already ours. search speaks the phrase the TV understands.

// spokenName is how Alexa knows each alias ("prime" is "prime video").
// YouTube isn't here: Alexa would open the official app, so YouTube
// searches play the top hit in SmartTube instead (see yt).
var spokenName = map[string]string{
	"prime": "prime video", "primevideo": "prime video",
	"silk": "the browser", "browser": "the browser", "gfn": "GeForce Now",
}

func (e *cmds) search() *cobra.Command {
	return &cobra.Command{
		Use:   "search [app] <words>...",
		Short: "Search the TV (all apps) or one app, via Alexa",
		Long: `Without an app name this is Fire TV's universal search, which shows
results across every installed app. With one (` + strings.Join(AliasNames(), ", ") + `)
it searches inside that app. In the TUI, "<app> <words>" is a shortcut for this.`,
		Args: cobra.MinimumNArgs(1),
		RunE: e.run(func(s *session, args []string) error {
			app := strings.ToLower(args[0])
			if pkg, known := Aliases[app]; known && len(args) > 1 {
				words := strings.Join(args[1:], " ")
				if pkg == YouTubeApp {
					return e.playTopHit(s, words)
				}
				name := spokenName[app]
				if name == "" {
					name = app
				}
				return e.speak(s, "find "+words+" on "+name, app, words)
			}
			words := strings.Join(args, " ")
			return e.speak(s, "search for "+words, "", words)
		}),
	}
}

func (e *cmds) speak(s *session, phrase, app, words string) error {
	sp, err := need[device.Speaker](s.dev, "voice commands")
	if err != nil {
		return err
	}
	if err := sp.Say(s.ctx, phrase); err != nil {
		return err
	}
	where := "the TV"
	if app != "" {
		where = app
	}
	return e.ok(fmt.Sprintf("searching %s for %q", where, words), map[string]any{"app": app, "query": words, "said": phrase})
}

// yt opens SmartTube, tvctl's YouTube app. With words it looks them up
// here, without the TV, and plays the top hit straight away, so there is
// no results screen to pick from.
func (e *cmds) yt() *cobra.Command {
	var list bool
	c := &cobra.Command{
		Use:     "youtube [words]...",
		Aliases: []string{"yt"},
		Short:   "Open SmartTube, or play the top YouTube result for these words (--list to see several)",
		RunE: e.run(func(s *session, args []string) error {
			if len(args) == 0 {
				if err := s.dev.Launch(s.ctx, YouTubeApp); err != nil {
					return err
				}
				return e.ok("launched SmartTube", map[string]any{"app": YouTubeApp})
			}
			q := strings.Join(args, " ")
			if !list {
				return e.playTopHit(s, q)
			}
			res, err := youtube.Search(s.ctx, q, 8)
			if err != nil {
				return err
			}
			var lines []string
			for i, r := range res {
				lines = append(lines, fmt.Sprintf("%d) %-11s %s", i+1, r.ID, r.Title))
			}
			e.emit(strings.Join(lines, "\n"), res)
			return nil
		}),
	}
	c.Flags().BoolVar(&list, "list", false, "show the top results instead of playing")
	return c
}

// playTopHit plays the best YouTube match for q in SmartTube.
func (e *cmds) playTopHit(s *session, q string) error {
	res, err := youtube.Search(s.ctx, q, 1)
	if err != nil {
		return err
	}
	if len(res) == 0 {
		return fmt.Errorf("nothing on YouTube for %q", q)
	}
	if err := s.openLink("https://www.youtube.com/watch?v=" + res[0].ID); err != nil {
		return err
	}
	return e.ok("playing: "+res[0].Title, map[string]any{"id": res[0].ID, "title": res[0].Title})
}
