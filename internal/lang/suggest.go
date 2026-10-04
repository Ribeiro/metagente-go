package lang

// distance is the Levenshtein distance between two words, counted in runes.
func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// ClosestName finds the option nearest to word, for "did you mean" hints. It
// returns "" when nothing is close enough: the distance must be at most 2 and
// smaller than the length of the word. Among equally close options the first
// one wins.
func ClosestName(word string, options []string) string {
	limit := len([]rune(word))
	best, bestDistance := "", -1
	for _, option := range options {
		d := distance(word, option)
		if d <= 2 && d < limit && (bestDistance < 0 || d < bestDistance) {
			best, bestDistance = option, d
		}
	}
	return best
}
