package firetv

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/omAthalye/tvctl/internal/youtube"
)

// OpenURL routes a web link to the matching TV app. Without ADB the only
// apps that accept a payload are the ones registered with DIAL:
//
//   - YouTube: a plain DIAL launch only opens the app; the video is queued
//     through YouTube's lounge protocol (see internal/youtube).
//   - Netflix: POST /apps/Netflix opens the app but ignores any payload, so
//     a Netflix link currently just launches Netflix; the real deep link
//     needs the companion app.
//
// Silk also registers with DIAL but a launch only starts its process in the
// background and never shows a page, so there is no generic-URL path on the
// stock TV. The companion app will own that route.
func (c *Client) OpenURL(ctx context.Context, raw string) error {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("not a URL: %q", raw)
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	host = strings.TrimPrefix(host, "m.")

	if id, t := youtubeID(host, u); id != "" {
		start, _ := strconv.Atoi(t)
		launch := func(ctx context.Context, app, body string, hdr map[string]string) error {
			return dialLaunchWith(ctx, c.Host, app, body, hdr)
		}
		return youtube.Play(ctx, launch, id, start)
	}
	if id := netflixID(host, u); id != "" {
		return dialLaunch(ctx, c.Host, "Netflix", "v="+id)
	}
	return fmt.Errorf("no TV app takes %s links without the companion app (YouTube links play; Netflix links open the app)", host)
}

// youtubeID extracts the video id (and optional start seconds) from the
// common YouTube URL shapes: watch?v=, youtu.be/, /shorts/, /embed/, /live/.
func youtubeID(host string, u *url.URL) (id, t string) {
	q := u.Query()
	t = strings.TrimSuffix(q.Get("t"), "s")
	switch host {
	case "youtu.be":
		return strings.Trim(u.Path, "/"), t
	case "youtube.com", "music.youtube.com":
		if v := q.Get("v"); v != "" {
			return v, t
		}
		for _, p := range []string{"/shorts/", "/embed/", "/live/", "/v/"} {
			if strings.HasPrefix(u.Path, p) {
				return strings.Trim(strings.TrimPrefix(u.Path, p), "/"), t
			}
		}
	}
	return "", ""
}

var netflixTitle = regexp.MustCompile(`^/(?:[a-z]{2}(?:-[a-z]{2})?/)?(?:title|watch)/(\d+)`)

// netflixID pulls the numeric title id from netflix.com/title/<id> and
// /watch/<id>, with or without a locale prefix.
func netflixID(host string, u *url.URL) string {
	if host != "netflix.com" {
		return ""
	}
	if m := netflixTitle.FindStringSubmatch(u.Path); m != nil {
		return m[1]
	}
	return ""
}
