package firetv

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// OpenURL routes a web link to the right app. Only two apps on Fire TV are
// addressable with a payload without ADB, both through DIAL:
//
//   - YouTube: POST /apps/YouTube with "v=<videoId>" (plus "t=<seconds>")
//   - Silk:    POST /apps/com.amazon.cloud9 with the URL as the payload
//
// Everything else falls through to Silk, which is a browser and will show
// the page; for sites with a TV app (Netflix) the companion app, once
// installed, takes over this route with a proper deep link.
func (c *Client) OpenURL(ctx context.Context, raw string) error {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("not a URL: %q", raw)
	}
	if id, t := youtubeID(u); id != "" {
		body := "v=" + id
		if t != "" {
			body += "&t=" + t
		}
		if err := dialLaunch(ctx, c.Host, "YouTube", body); err == nil {
			return nil
		}
		// Fall through to Silk if the YouTube DIAL endpoint refuses.
	}
	return dialLaunch(ctx, c.Host, "com.amazon.cloud9", u.String())
}

// youtubeID extracts the video id (and optional start time) from the common
// YouTube URL shapes: watch?v=, youtu.be/, /shorts/, /embed/, /live/.
func youtubeID(u *url.URL) (id, t string) {
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	host = strings.TrimPrefix(host, "m.")
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
