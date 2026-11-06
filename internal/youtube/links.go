package youtube

import (
	"net/url"
	"strconv"
	"strings"
)

// IsLink reports whether raw points at YouTube, with or without a scheme.
func IsLink(raw string) bool {
	u, err := url.Parse(withScheme(raw))
	return err == nil && IsHost(u.Host)
}

// IsHost reports whether host is one of YouTube's, ignoring www. and m.
func IsHost(host string) bool {
	h := strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(host), "www."), "m.")
	return h == "youtube.com" || h == "youtu.be" || h == "music.youtube.com"
}

// VideoID pulls the video id and start offset (seconds) out of the common
// link shapes: watch?v=, youtu.be/, /shorts/, /embed/, /live/, /v/. "" when
// raw isn't a YouTube video link.
func VideoID(raw string) (id string, start int) {
	u, err := url.Parse(withScheme(raw))
	if err != nil || !IsHost(u.Host) {
		return "", 0
	}
	q := u.Query()
	start, _ = strconv.Atoi(strings.TrimSuffix(q.Get("t"), "s"))
	host := strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(u.Host), "www."), "m.")
	if host == "youtu.be" {
		return strings.Trim(u.Path, "/"), start
	}
	if v := q.Get("v"); v != "" {
		return v, start
	}
	for _, p := range []string{"/shorts/", "/embed/", "/live/", "/v/"} {
		if strings.HasPrefix(u.Path, p) {
			return strings.Trim(strings.TrimPrefix(u.Path, p), "/"), start
		}
	}
	return "", 0
}

func withScheme(raw string) string {
	if strings.Contains(raw, "://") {
		return raw
	}
	return "https://" + raw
}
