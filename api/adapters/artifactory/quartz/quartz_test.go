package quartz

import (
	"errors"
	"testing"
)

func TestToStandard(t *testing.T) {
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

			got, err := ToStandard(tt.quartz)
			if err != nil || got != tt.standard {
				t.Errorf("ToStandard(%q) = %q, %v; want %q", tt.quartz, got, err, tt.standard)
			}
		})
	}
}

func TestToStandardRefuses(t *testing.T) {
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
		if _, err := ToStandard(q); !errors.Is(err, ErrUnsupported) {
			t.Errorf("ToStandard(%q) = %v, want ErrUnsupported", q, err)
		}
	}
}

func TestFromStandard(t *testing.T) {
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

			if got := FromStandard(tt.standard); got != tt.quartz {
				t.Errorf("FromStandard(%q) = %q, want %q", tt.standard, got, tt.quartz)
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	for _, q := range []string{
		"0 0 12 * * ?",
		"0 30 2 ? * 2-6",
		"0 */15 * * * ?",
		"0 0 0 1 * ?",
	} {
		s, err := ToStandard(q)
		if err != nil || FromStandard(s) != q {
			t.Errorf("%q -> %q (%v) -> %q", q, s, err, FromStandard(s))
		}
	}
}
