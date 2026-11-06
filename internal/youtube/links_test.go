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
