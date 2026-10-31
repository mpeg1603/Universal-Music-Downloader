package main

import (
	"sort"
	"strings"
)

// Fuzzy string similarity on a 0–100 scale, following rapidfuzz's definitions.

// ratio is 100 * 2*LCS / (len(a)+len(b)), counted in runes.
func ratio(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	if len(ra)+len(rb) == 0 {
		return 100
	}
	return 200 * float64(lcs(ra, rb)) / float64(len(ra)+len(rb))
}

func lcs(a, b []rune) int {
	if len(a) < len(b) {
		a, b = b, a
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			switch {
			case a[i-1] == b[j-1]:
				cur[j] = prev[j-1] + 1
			case prev[j] > cur[j-1]:
				cur[j] = prev[j]
			default:
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func sortedTokens(s string) string {
	f := strings.Fields(s)
	sort.Strings(f)
	return strings.Join(f, " ")
}

func tokenSortRatio(a, b string) float64 {
	return ratio(sortedTokens(a), sortedTokens(b))
}

// tokenSetRatio scores 100 when all words of one string appear in the other.
func tokenSetRatio(a, b string) float64 {
	setA, setB := map[string]bool{}, map[string]bool{}
	for _, t := range strings.Fields(a) {
		setA[t] = true
	}
	for _, t := range strings.Fields(b) {
		setB[t] = true
	}
	var inter, onlyA, onlyB []string
	for t := range setA {
		if setB[t] {
			inter = append(inter, t)
		} else {
			onlyA = append(onlyA, t)
		}
	}
	for t := range setB {
		if !setA[t] {
			onlyB = append(onlyB, t)
		}
	}
	if len(inter) > 0 && (len(onlyA) == 0 || len(onlyB) == 0) {
		return 100
	}
	sort.Strings(inter)
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	sect := strings.Join(inter, " ")
	ab := strings.TrimSpace(sect + " " + strings.Join(onlyA, " "))
	ba := strings.TrimSpace(sect + " " + strings.Join(onlyB, " "))
	best := ratio(ab, ba)
	if sect != "" {
		best = max(best, ratio(sect, ab), ratio(sect, ba))
	}
	return best
}

// partialRatio is the best ratio of the shorter string against any equally long window of the longer one.
func partialRatio(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	if len(ra) > len(rb) {
		ra, rb = rb, ra
	}
	if len(ra) == 0 {
		if len(rb) == 0 {
			return 100
		}
		return 0
	}
	short := string(ra)
	best := 0.0
	for i := 0; i+len(ra) <= len(rb); i++ {
		best = max(best, ratio(short, string(rb[i:i+len(ra)])))
		if best == 100 {
			break
		}
	}
	return best
}
