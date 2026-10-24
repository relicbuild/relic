package artifactory

import (
	"errors"
	"testing"
)

func TestCronFromQuartz(t *testing.T) {
	t.Parallel()

	tests := []struct {
		quartz, standard string
	}{
		{
			"0 0 12 * * ?",
			"0 12 * * *",
		},
		{
			"0 0 12 * * ? 2027",
			"0 12 * * *",
		},
		{
			"0 30 2 ? * 2-6",
			"30 2 * * 1-5",
		},
		{
			"0 0 0 ? * 1,7",
			"0 0 * * 0,6",
		},
		{
			"0 0 0 ? * MON",
			"0 0 * * MON",
		},
		{
			"0 */15 * * * ?",
			"*/15 * * * *",
		},
		{
			"0 0 0 1 * ?",
			"0 0 1 * *",
		},
		{
			"0 0 0 ? * 1/2",
			"0 0 * * 0/2",
		},
		{
			"0 0 0 ? * WED",
			"0 0 * * WED",
		},
		{
			"0 0 0 1 JUL ?",
			"0 0 1 JUL *",
		},
	}
	for _, tt := range tests {
		t.Run(tt.quartz, func(t *testing.T) {
			t.Parallel()

			got, err := cronFromQuartz(tt.quartz)
			if err != nil || got != tt.standard {
				t.Errorf("cronFromQuartz(%q) = %q, %v; want %q", tt.quartz, got, err, tt.standard)
			}
		})
	}
}

func TestCronFromQuartzRefuses(t *testing.T) {
	t.Parallel()

	for _, q := range []string{
		"",
		"0 0 12 * *",
		"*/30 * * * * ?",
		"0 0 12 L * ?",
		"0 0 12 ? * 6#3",
		"0 0 12 15W * ?",
		"0 0 12 ? * 5L",
	} {
		if _, err := cronFromQuartz(q); !errors.Is(err, errUnsupportedQuartz) {
			t.Errorf("cronFromQuartz(%q) = %v, want errUnsupportedQuartz", q, err)
		}
	}
}

func TestQuartzFromCron(t *testing.T) {
	t.Parallel()

	tests := []struct {
		standard, quartz string
	}{
		{
			"0 12 * * *",
			"0 0 12 * * ?",
		},
		{
			"30 2 * * 1-5",
			"0 30 2 ? * 2-6",
		},
		{
			"0 0 1 * *",
			"0 0 0 1 * ?",
		},
		{
			"0 0 * * 0,7",
			"0 0 0 ? * 1,1",
		},
		{
			"0 12 1 * 3",
			"0 0 12 1 * ?",
		},
		{
			"@daily",
			"0 0 0 * * ?",
		},
		{
			"@hourly",
			"0 0 * * * ?",
		},
	}
	for _, tt := range tests {
		t.Run(tt.standard, func(t *testing.T) {
			t.Parallel()

			if got := quartzFromCron(tt.standard); got != tt.quartz {
				t.Errorf("quartzFromCron(%q) = %q, want %q", tt.standard, got, tt.quartz)
			}
		})
	}
}

func TestQuartzRoundTrip(t *testing.T) {
	t.Parallel()

	for _, q := range []string{
		"0 0 12 * * ?",
		"0 30 2 ? * 2-6",
		"0 */15 * * * ?",
		"0 0 0 1 * ?",
	} {
		s, err := cronFromQuartz(q)
		if err != nil || quartzFromCron(s) != q {
			t.Errorf("%q -> %q (%v) -> %q", q, s, err, quartzFromCron(s))
		}
	}
}
