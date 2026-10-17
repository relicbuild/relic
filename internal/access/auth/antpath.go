package auth

import (
	"strings"
)

// MatchesAntPattern reports whether path p matches an Ant pattern (*, ** and
// ? in segments; every other character is literal).
func MatchesAntPattern(pattern, p string) bool {
	return matchSegments(split(pattern), split(p))
}

// split drops empty segments, as repo.CleanPath does before storage: a
// check must see the path that will be stored.
func split(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == '/'
	})
}

func dotSegment(p string) bool {
	for _, seg := range split(p) {
		if seg == "." || seg == ".." {
			return true
		}
	}

	return false
}

func matchSegments(pat, segs []string) bool {
	reached := make([]bool, len(pat)+1)
	reached[0] = true
	skipDoubleStars(pat, reached)

	for _, seg := range segs {
		next := make([]bool, len(pat)+1)

		for i, p := range pat {
			switch {
			case !reached[i]:
			case p == "**":
				next[i] = true
			case matchSegment(p, seg):
				next[i+1] = true
			}
		}

		skipDoubleStars(pat, next)
		reached = next
	}

	return reached[len(pat)]
}

func skipDoubleStars(pat []string, reached []bool) {
	for i, p := range pat {
		if reached[i] && p == "**" {
			reached[i+1] = true
		}
	}
}

func matchSegment(pattern, name string) bool {
	patternRunes, nameRunes := []rune(pattern), []rune(name)
	patternAt, nameAt := 0, 0
	starAt, resumeAt := -1, 0

	for nameAt < len(nameRunes) {
		switch {
		case patternAt < len(patternRunes) && patternRunes[patternAt] == '*':
			starAt, resumeAt = patternAt, nameAt
			patternAt++
		case patternAt < len(patternRunes) &&
			(patternRunes[patternAt] == '?' || patternRunes[patternAt] == nameRunes[nameAt]):
			patternAt++
			nameAt++
		case starAt >= 0:
			resumeAt++
			patternAt, nameAt = starAt+1, resumeAt
		default:
			return false
		}
	}

	for patternAt < len(patternRunes) && patternRunes[patternAt] == '*' {
		patternAt++
	}

	return patternAt == len(patternRunes)
}
