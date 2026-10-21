package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/omAthalye/tvctl/internal/device"
	"github.com/omAthalye/tvctl/internal/youtube"
)

// Search verbs: a word or two after an app name is almost always a search.

func (e *cmds) yt() *cobra.Command {
	var list bool
	c := &cobra.Command{
		Use:     "youtube <words>...",
		Aliases: []string{"yt"},
		Short:   "Search YouTube and play the first result on the TV",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			q := strings.Join(args, " ")
			limit := 1
			if list {
				limit = 8
			}
			res, err := youtube.Search(cx, q, limit)
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
			_, d, _, err := e.open()
			if err != nil {
				return err
			}
			if err := e.openURL(cx, d, "https://www.youtube.com/watch?v="+res[0].ID); err != nil {
				return err
			}
			e.emit("playing: "+res[0].Title, map[string]any{"ok": true, "id": res[0].ID, "title": res[0].Title})
			return nil
		},
	}
	c.Flags().BoolVar(&list, "list", false, "show the top results instead of playing")
	return c
}

// Netflix and YouTube draw their own on-screen keyboards, so no local
// route can type into their search fields; the official phone app can't
// either. Alexa can, with system privileges, and the voice channel is
// already ours. These verbs speak the phrase the TV understands.

func (e *cmds) netflixSearch() *cobra.Command {
	return &cobra.Command{
		Use:   "netflix <words>...",
		Short: "Search Netflix for these words (via Alexa)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return e.speak(fmt.Sprintf("find %s on netflix", strings.Join(args, " ")), "netflix", strings.Join(args, " "))
		},
	}
}

func (e *cmds) search() *cobra.Command {
	return &cobra.Command{
		Use:   "search [youtube|netflix|prime] <words>...",
		Short: "Search the TV (all apps), or one app, via Alexa",
		Long: `Without an app name this is Fire TV's universal search, which shows
results across every installed app. With one, it searches inside that app.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := strings.ToLower(args[0])
			words := strings.Join(args[1:], " ")
			switch app {
			case "youtube", "yt":
				return e.speak("search YouTube for "+words, app, words)
			case "netflix":
				return e.speak("find "+words+" on netflix", app, words)
			case "prime", "primevideo":
				return e.speak("find "+words+" on prime video", app, words)
			}
			words = strings.Join(args, " ")
			return e.speak("search for "+words, "", words)
		},
	}
}

func (e *cmds) speak(phrase, app, words string) error {
	cx, cancel := ctx()
	defer cancel()
	_, d, _, err := e.open()
	if err != nil {
		return err
	}
	sp, err := need[device.Speaker](d, "voice commands")
	if err != nil {
		return err
	}
	if err := sp.Say(cx, phrase); err != nil {
		return err
	}
	where := "the TV"
	if app != "" {
		where = app
	}
	e.emit(fmt.Sprintf("searching %s for %q", where, words), map[string]any{"ok": true, "app": app, "query": words, "said": phrase})
	return nil
}
