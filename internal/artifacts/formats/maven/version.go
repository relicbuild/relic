package maven

import (
	"strings"
	"unicode"
)

// Compare orders Maven versions the way Maven's ComparableVersion does for
// common cases: numeric components compare numerically, and qualifiers rank
// alpha < beta < milestone < rc < snapshot < (release) < sp.
func Compare(a, b string) int {
	ta, tb := tokenize(a), tokenize(b)
	for i := 0; i < max(len(ta), len(tb)); i++ {
		x, y := at(ta, i), at(tb, i)
		if c := x.compare(y); c != 0 {
			return c
		}
	}

	return 0
}

type token struct {
	digits  string
	qual    string
	numeric bool
}

func at(ts []token, i int) token {
	if i < len(ts) {
		return ts[i]
	}

	return token{
		digits:  "0",
		numeric: true,
	}
}

var qualifierRank = map[string]int{
	"alpha":     1,
	"a":         1,
	"beta":      2,
	"b":         2,
	"milestone": 3,
	"m":         3,
	"rc":        4,
	"cr":        4,
	"snapshot":  5,
	"":          6,
	"ga":        6,
	"final":     6,
	"release":   6,
	"sp":        7,
}

func (t token) compare(o token) int {
	switch {
	case t.numeric && o.numeric:
		return compareDigits(t.digits, o.digits)
	case t.numeric:
		if o.qual == "sp" {
			return -1
		}

		if t.digits == "0" && qualifierRank[o.qual] == 0 {
			return -1
		}

		return 1
	case o.numeric:
		return -o.compare(t)
	}

	ra, okA := qualifierRank[t.qual]

	rb, okB := qualifierRank[o.qual]
	switch {
	case okA && okB:
		return cmp(int64(ra), int64(rb))
	case okA:
		return -1
	case okB:
		return 1
	}

	return strings.Compare(t.qual, o.qual)
}

func compareDigits(a, b string) int {
	if c := cmp(int64(len(a)), int64(len(b))); c != 0 {
		return c
	}

	return strings.Compare(a, b)
}

func cmp(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}

	return 0
}

func tokenize(v string) []token {
	var (
		out []token
		cur strings.Builder
	)

	flush := func() {
		if cur.Len() == 0 {
			return
		}

		s := cur.String()
		cur.Reset()

		if strings.Trim(s, "0123456789") == "" {
			digits := strings.TrimLeft(s, "0")
			if digits == "" {
				digits = "0"
			}

			out = append(out, token{
				digits:  digits,
				numeric: true,
			})

			return
		}

		out = append(out, token{
			qual: strings.ToLower(s),
		})
	}
	prevDigit := false

	for i, r := range v {
		switch r {
		case '.', '-', '_':
			flush()
		default:
			if i > 0 && cur.Len() > 0 && unicode.IsDigit(r) != prevDigit {
				flush()
			}

			cur.WriteRune(r)
			prevDigit = unicode.IsDigit(r)
		}
	}

	flush()

	for len(out) > 0 && out[len(out)-1].numeric && out[len(out)-1].digits == "0" {
		out = out[:len(out)-1]
	}

	return out
}

// IsSnapshot reports whether version is a SNAPSHOT version.
func IsSnapshot(version string) bool { return strings.HasSuffix(version, "-SNAPSHOT") }
