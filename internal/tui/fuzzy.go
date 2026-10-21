package tui

import (
	"sort"
	"strings"
)

// match is one completion candidate with its score and the positions of
// the matched characters, which the view highlights.
type match struct {
	text  string
	score int
	pos   []int
}

// fuzzyRank scores every candidate against the query and returns the ones
// that match, best first. The scoring is deliberately simple: every query
// character must appear in order; consecutive matches and matches at the
// start score higher. That's enough for "n" -> netflix, "vu" -> volume_up,
// and it has no dependency.
func fuzzyRank(query string, candidates []string, limit int) []match {
	q := strings.ToLower(query)
	var out []match
	for _, c := range candidates {
		if m, ok := fuzzyOne(q, c); ok {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].text < out[j].text
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func fuzzyOne(q, c string) (match, bool) {
	lc := strings.ToLower(c)
	if q == "" {
		return match{text: c}, true
	}
	var pos []int
	score, last := 0, -2
	ci := 0
	for _, r := range q {
		i := strings.IndexRune(lc[ci:], r)
		if i < 0 {
			return match{}, false
		}
		at := ci + i
		pos = append(pos, at)
		switch {
		case at == 0:
			score += 10 // first character of the word
		case at == last+1:
			score += 5 // continues a run
		case lc[at-1] == '_' || lc[at-1] == ' ' || lc[at-1] == '-':
			score += 4 // starts a sub-word
		default:
			score++
		}
		last = at
		ci = at + 1
	}
	// Shorter candidates win ties: "yt" over "youtube music".
	score -= len(lc) / 8
	return match{text: c, score: score, pos: pos}, true
}
