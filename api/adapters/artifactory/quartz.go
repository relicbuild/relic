package artifactory

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// errUnsupportedQuartz is a Quartz expression with no standard equivalent: finer
// than a minute, or using L, W or #.
var errUnsupportedQuartz = errors.New("unsupported Quartz cron expression")

var lastDay = regexp.MustCompile(`(^|[^A-Z])L`)

// cronFromQuartz converts a Quartz expression.
func cronFromQuartz(q string) (string, error) {
	f := strings.Fields(q)
	if len(f) == 7 {
		f = f[:6]
	}

	if len(f) != 6 {
		return "", fmt.Errorf("%w: %q needs 6 or 7 fields", errUnsupportedQuartz, q)
	}

	if _, err := strconv.Atoi(f[0]); err != nil {
		return "", fmt.Errorf("%w: %q runs more than once a minute", errUnsupportedQuartz, q)
	}

	if strings.ContainsAny(f[3], "LW") || strings.Contains(f[5], "#") || lastDay.MatchString(f[5]) {
		return "", fmt.Errorf("%w: %q uses L, W or #", errUnsupportedQuartz, q)
	}

	dom, dow := unused(f[3]), unused(f[5])

	return strings.Join([]string{
		f[1],
		f[2],
		dom,
		f[4],
		shiftDays(dow, -1),
	}, " "), nil
}

// quartzFromCron converts a standard expression (or a descriptor such as
// @daily) to Quartz, dropping the day of the week when both day fields are
// set, which Quartz cannot express.
func quartzFromCron(s string) string {
	switch s {
	case "@yearly", "@annually":
		s = "0 0 1 1 *"
	case "@monthly":
		s = "0 0 1 * *"
	case "@weekly":
		s = "0 0 * * 0"
	case "@daily", "@midnight":
		s = "0 0 * * *"
	case "@hourly":
		s = "0 * * * *"
	}

	f := strings.Fields(s)
	if len(f) != 5 {
		return s
	}

	dom, dow := f[2], shiftDays(f[4], 1)
	switch {
	case dow == "*":
		dow = "?"
	case dom == "*":
		dom = "?"
	default:
		dow = "?"
	}

	return strings.Join([]string{
		"0",
		f[0],
		f[1],
		dom,
		f[3],
		dow,
	}, " ")
}

func unused(field string) string {
	if field == "?" {
		return "*"
	}

	return field
}

func shiftDays(field string, delta int) string {
	values, step, hasStep := strings.Cut(field, "/")

	var b strings.Builder

	for i := 0; i < len(values); {
		j := i
		for j < len(values) && values[j] >= '0' && values[j] <= '9' {
			j++
		}

		if j == i {
			b.WriteByte(values[i])
			i++

			continue
		}

		n, _ := strconv.Atoi(values[i:j])

		n += delta
		if delta > 0 && n == 8 {
			n = 1
		}

		b.WriteString(strconv.Itoa(n))

		i = j
	}

	if hasStep {
		return b.String() + "/" + step
	}

	return b.String()
}
