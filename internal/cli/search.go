package cli

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/omAthalye/tvctl/internal/receiver"
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

func (e *cmds) netflixSearch() *cobra.Command {
	return &cobra.Command{
		Use:   "netflix <words>...",
		Short: "Open Netflix's search for these words (needs the companion app)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cx, cancel := ctx()
			defer cancel()
			id, d, st, err := e.open()
			if err != nil {
				return err
			}
			ent := st.Devices[id]
			if ent.ReceiverToken == "" {
				return fmt.Errorf("netflix search needs the companion app: run `tv setup`")
			}
			q := strings.Join(args, " ")
			rc := receiver.New(ent.Host, ent.ReceiverToken)
			// Netflix routes its own scheme; the https search URL goes to Silk.
			needs, err := rc.Open(cx, "netflix://search?q="+url.QueryEscape(q), "com.netflix.ninja")
			if err != nil {
				return err
			}
			if err := e.bringForward(cx, d, needs); err != nil {
				return err
			}
			e.emit("searching Netflix for "+q, map[string]any{"ok": true, "query": q})
			return nil
		},
	}
}
