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
var spokenName = map[string]string{
	"yt": "YouTube", "youtube": "YouTube", "prime": "prime video", "primevideo": "prime video",
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
			if _, known := Aliases[app]; known && len(args) > 1 {
				words := strings.Join(args[1:], " ")
				name := spokenName[app]
				if name == "" {
					name = app
				}
				phrase := "find " + words + " on " + name
				if name == "YouTube" {
					phrase = "search YouTube for " + words
				}
				return e.speak(s, phrase, app, words)
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

// youtube is different from `search youtube`: it looks the words up here,
// without the TV, and plays the top hit directly through the lounge
// protocol, so there is no results screen to pick from.
func (e *cmds) yt() *cobra.Command {
	var list bool
	c := &cobra.Command{
		Use:     "youtube <words>...",
		Aliases: []string{"yt"},
		Short:   "Play the top YouTube result for these words (--list to see several)",
		Args:    cobra.MinimumNArgs(1),
		RunE: e.run(func(s *session, args []string) error {
			q := strings.Join(args, " ")
			limit := 1
			if list {
				limit = 8
			}
			res, err := youtube.Search(s.ctx, q, limit)
			if err != nil {
				return err
			}
			if list {
				var lines []string
				for i, r := range res {
					lines = append(lines, fmt.Sprintf("%d) %-11s %s", i+1, r.ID, r.Title))
				}
				e.emit(strings.Join(lines, "\n"), res)
				return nil
			}
			if err := s.openLink("https://www.youtube.com/watch?v=" + res[0].ID); err != nil {
				return err
			}
			return e.ok("playing: "+res[0].Title, map[string]any{"id": res[0].ID, "title": res[0].Title})
		}),
	}
	c.Flags().BoolVar(&list, "list", false, "show the top results instead of playing")
	return c
}
