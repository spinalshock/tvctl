package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/omAthalye/tvctl/internal/device"
	"github.com/omAthalye/tvctl/internal/youtube"
)

// Streaming apps draw their own on-screen keyboards, so no local route can
// type into their search fields. A TV's voice assistant can, with system
// privileges, so search speaks the phrase to it (device.Speaker). YouTube
// is the exception: its top hit is looked up here and played directly
// (see yt), which needs no assistant and works on every brand.

// spokenName is what the voice assistant calls an app when that differs
// from its proper name.
var spokenName = map[string]string{"Browser": "the browser", "Silk": "the browser"}

// assistantName is how the voice assistant hears an app: its spoken
// alias, else its proper name, else the word itself.
func assistantName(word string) string {
	name := word
	if n, ok := Aliases[strings.ToLower(word)]; ok {
		name = n
	}
	if n, ok := spokenName[name]; ok {
		return n
	}
	return name
}

func (e *cmds) search() *cobra.Command {
	return &cobra.Command{
		Use:         "search [app] <words>...",
		Short:       "Search the TV (all apps) or one app, via its voice assistant",
		Annotations: meta("", "25s", wake),
		Long: `Without an app name this is the TV's universal search, which shows
results across every installed app. With one (` + strings.Join(AliasNames(), ", ") + `)
it searches inside that app. In the TUI, "<app> <words>" is a shortcut for this.`,
		Args: cobra.MinimumNArgs(1),
		RunE: e.run(func(s *session, args []string) error {
			app := strings.ToLower(args[0])
			if name, known := Aliases[app]; known && len(args) > 1 {
				words := strings.Join(args[1:], " ")
				if name == "YouTube" {
					return e.playTopHit(s, words)
				}
				return e.speak(s, "find "+words+" on "+assistantName(app), app, words)
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
	spokenWhere := "the TV"
	if app != "" {
		spokenWhere = assistantName(app)
	}
	return e.done(fmt.Sprintf("Searching %s for %s.", spokenWhere, words),
		map[string]any{"app": app, "query": words, "said": phrase})
}

// yt opens the TV's YouTube app. With words it looks them up here, without
// the TV, and plays the top hit straight away, so there is no results
// screen to pick from.
func (e *cmds) yt() *cobra.Command {
	var list bool
	c := &cobra.Command{
		Use:         "youtube [words]...",
		Aliases:     []string{"yt"},
		Annotations: meta("", "25s", wake),
		Short:       "Open YouTube, or play the top YouTube result for these words (--list to see several)",
		RunE: e.run(func(s *session, args []string) error {
			if len(args) == 0 {
				id, _, err := s.openApp("youtube")
				if err != nil {
					return err
				}
				return e.done("Opening YouTube.", map[string]any{"app": id})
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

// playTopHit plays the best YouTube match for q on the TV.
func (e *cmds) playTopHit(s *session, q string) error {
	res, err := youtube.Search(s.ctx, q, 1)
	if err != nil {
		return err
	}
	if len(res) == 0 {
		return device.Failf("nothing on YouTube for %s", q)
	}
	if err := s.openLink("https://www.youtube.com/watch?v=" + res[0].ID); err != nil {
		return err
	}
	return e.done("Playing "+res[0].Title+".", map[string]any{"id": res[0].ID, "title": res[0].Title})
}
