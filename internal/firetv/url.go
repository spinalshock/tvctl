package firetv

import (
	"context"
	"net/url"
	"regexp"
	"strings"

	"github.com/omAthalye/tvctl/internal/device"
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
	if id, start := youtube.VideoID(raw); id != "" {
		launch := func(ctx context.Context, app, body string, hdr map[string]string) error {
			return dialLaunchWith(ctx, c.Host, app, body, hdr)
		}
		return youtube.Play(ctx, launch, id, start)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return device.Failf("that isn't a link: %s", raw)
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	if id := netflixID(host, u); id != "" {
		return dialLaunch(ctx, c.Host, "Netflix", "v="+id)
	}
	return device.Failf("no app on %s takes %s links", c.name(), host).Note("YouTube links play; Netflix links open the app; the companion app routes the rest")
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
