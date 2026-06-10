package tui

import "unicode"

// fzf-style fuzzy scoring. We do a single greedy forward subsequence match
// (fzf's FuzzyMatchV1 shape — good enough for the short names/paths here) and
// score it so the BEST match ranks highest. The returned positions are the
// rune indices in the text that matched, used to highlight them in the view.
//
// Smart-case: the match is case-insensitive unless the pattern contains an
// uppercase rune, in which case it's case-sensitive (the fzf convention).
//
// Scoring (higher = better): each matched rune scores a base; a rune at the
// start of the text or right after a boundary (non-alphanumeric, e.g. /, -, _,
// space, .) or at a camelCase hump earns a boundary bonus; runes matched
// consecutively earn a consecutive bonus; gaps between matched runes and a
// late first match are penalized. The net effect mirrors fzf: prefix and
// word-boundary hits float to the top, scattered mid-word hits sink.
const (
	scoreMatch        = 16
	bonusBoundary     = 8
	bonusCamel        = 7
	bonusConsecutive  = 8
	scoreGapStart     = -3
	scoreGapExtension = -1
)

// matchResult is the outcome of scoring one pattern against one text.
type matchResult struct {
	ok    bool
	score int
	pos   []int // rune indices into the text that matched (for highlighting)
}

// fuzzyScore matches pattern against text and scores it. An empty pattern
// matches everything with score 0 and no highlight positions.
func fuzzyScore(pattern, text string) matchResult {
	if pattern == "" {
		return matchResult{ok: true}
	}
	caseSensitive := hasUpper(pattern)
	tr := []rune(text)
	pr := []rune(pattern)

	// Comparison runes (case-folded unless the pattern forces case-sensitivity);
	// tr stays original so camelCase detection sees the real case.
	ct := tr
	cp := pr
	if !caseSensitive {
		ct = foldRunes(tr)
		cp = foldRunes(pr)
	}

	pos := make([]int, 0, len(cp))
	score := 0
	pi := 0
	prevMatch := -2
	inGap := false
	for ti := 0; ti < len(ct) && pi < len(cp); ti++ {
		if ct[ti] != cp[pi] {
			// Penalize gaps only once a match has started (leading distance is
			// charged once, below, when the first rune matches).
			if len(pos) > 0 {
				if inGap {
					score += scoreGapExtension
				} else {
					score += scoreGapStart
				}
				inGap = true
			}
			continue
		}
		bonus := 0
		switch {
		case ti == 0 || isBoundary(tr[ti-1]):
			bonus = bonusBoundary
		case unicode.IsLower(tr[ti-1]) && unicode.IsUpper(tr[ti]):
			bonus = bonusCamel
		}
		if ti == prevMatch+1 {
			score += scoreMatch + bonusConsecutive + bonus
		} else {
			score += scoreMatch + bonus
		}
		if len(pos) == 0 {
			// Mild penalty for starting the match later in the text, so an
			// earlier (more prefix-like) match wins all else equal.
			score += ti * scoreGapExtension
		}
		pos = append(pos, ti)
		prevMatch = ti
		pi++
		inGap = false
	}
	if pi < len(cp) {
		return matchResult{ok: false}
	}
	return matchResult{ok: true, score: score, pos: pos}
}

// isBoundary reports whether r is a word-boundary character (anything that
// isn't a letter or number — /, -, _, space, ., etc.).
func isBoundary(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsNumber(r)
}

func hasUpper(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func foldRunes(rs []rune) []rune {
	out := make([]rune, len(rs))
	for i, r := range rs {
		out[i] = unicode.ToLower(r)
	}
	return out
}
