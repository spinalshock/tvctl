package youtube

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spinalshock/tvctl/internal/device"
	"github.com/spinalshock/tvctl/internal/trace"
)

// Result is one search hit.
type Result struct {
	ID    string
	Title string
}

var (
	reVideoID = regexp.MustCompile(`"videoRenderer":\{"videoId":"([A-Za-z0-9_-]{11})"`)
	reTitle   = regexp.MustCompile(`"title":\{"runs":\[\{"text":"((?:[^"\\]|\\.)*)"`)
)

// Search returns the top results for q by reading the embedded data on
// YouTube's results page. It needs no API key; the page carries its
// initial result set as JSON, and the videoRenderer entries are the
// ordinary (non-ad, non-shelf) results in display order. YouTube now and
// then answers with a page that has no data at all; one retry clears it.
func Search(ctx context.Context, q string, limit int) ([]Result, error) {
	var (
		out     []Result
		hasData bool
		err     error
	)
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(400 * time.Millisecond):
			}
		}
		var body string
		body, err = resultsPage(ctx, q)
		if err != nil {
			continue
		}
		out, hasData = parseResults(body, limit)
		trace.Logf("search: page %d fetched, %d results, data=%v", attempt+1, len(out), hasData)
		if len(out) > 0 || hasData {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		if hasData {
			return nil, device.Failf("nothing on YouTube for %s", q)
		}
		return nil, device.Fail("YouTube didn't answer the search").Note("the results page came back without data twice")
	}
	return out, nil
}

func resultsPage(ctx context.Context, q string) (string, error) {
	u := "https://www.youtube.com/results?search_query=" + url.QueryEscape(q) + "&hl=en"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	// A desktop UA and a consent cookie get the full page rather than a
	// consent interstitial in regions that show one.
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en")
	req.Header.Set("Cookie", "CONSENT=YES+1; SOCS=CAI")
	resp, err := httpc.Do(req)
	if err != nil {
		return "", device.Fail("couldn't reach YouTube").Because(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 6<<20))
	if resp.StatusCode != 200 {
		return "", device.Fail("YouTube refused the search").Notef("HTTP %d", resp.StatusCode)
	}
	return string(body), nil
}

// parseResults pulls the ordinary video results out of a results page.
// hasData says whether the page carried its result data at all, which
// tells an empty result set ("nothing matches") from a broken page.
func parseResults(s string, limit int) (out []Result, hasData bool) {
	hasData = strings.Contains(s, "ytInitialData")
	seen := map[string]bool{}
	for _, m := range reVideoID.FindAllStringSubmatchIndex(s, -1) {
		id := s[m[2]:m[3]]
		if seen[id] {
			continue
		}
		seen[id] = true
		title := ""
		// The title follows the id within the same renderer object.
		if t := reTitle.FindStringSubmatch(s[m[1]:min(len(s), m[1]+4000)]); t != nil {
			title = unescapeJSON(t[1])
		}
		out = append(out, Result{ID: id, Title: title})
		if len(out) >= limit {
			break
		}
	}
	return out, hasData
}

func unescapeJSON(s string) string {
	// The Go string unquoter understands the same escapes as JSON.
	if u, err := strconv.Unquote(`"` + s + `"`); err == nil {
		return u
	}
	return s
}
