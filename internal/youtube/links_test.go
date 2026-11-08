package youtube

import "testing"

func TestLinks(t *testing.T) {
	for raw, want := range map[string]string{
		"https://www.youtube.com/watch?v=abc123&t=90s": "abc123",
		"youtu.be/xyz":                            "xyz",
		"https://m.youtube.com/shorts/sh0rt":      "sh0rt",
		"https://music.youtube.com/watch?v=mus1c": "mus1c",
		"https://youtube.com/":                    "",
		"https://netflix.com/title/1":             "",
	} {
		if id, _ := VideoID(raw); id != want {
			t.Errorf("VideoID(%q) = %q, want %q", raw, id, want)
		}
	}
	if _, start := VideoID("https://www.youtube.com/watch?v=abc123&t=90s"); start != 90 {
		t.Errorf("start = %d", start)
	}
	if !IsLink("youtube.com/watch?v=x") || IsLink("https://example.com/youtube.com/") {
		t.Error("IsLink")
	}
}

func TestParseResultsTellsEmptyFromBroken(t *testing.T) {
	page := `var ytInitialData = {"contents":[{"videoRenderer":{"videoId":"kmjeMrjOjFA","title":{"runs":[{"text":"Taambdi \"Chaamdi\""}]}}},{"videoRenderer":{"videoId":"kmjeMrjOjFA"}},{"videoRenderer":{"videoId":"dQw4w9WgXcQ","title":{"runs":[{"text":"Second"}]}}}]}`
	res, hasData := parseResults(page, 5)
	if !hasData || len(res) != 2 || res[0].ID != "kmjeMrjOjFA" || res[0].Title != `Taambdi "Chaamdi"` || res[1].Title != "Second" {
		t.Errorf("got %+v hasData=%v", res, hasData)
	}
	if res, hasData := parseResults("<html>consent</html>", 5); hasData || len(res) != 0 {
		t.Errorf("a page without data must not look like an empty result: %+v %v", res, hasData)
	}
	if res, hasData := parseResults(`var ytInitialData = {"contents":[]}`, 5); !hasData || len(res) != 0 {
		t.Errorf("an empty result set has data: %+v %v", res, hasData)
	}
}
