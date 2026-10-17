package replication

import (
	"testing"
	"time"
)

func TestDue(t *testing.T) {
	t.Parallel()

	noon := time.Date(2026, 9, 23, 12, 0, 30, 0, time.UTC)
	before := noon.Add(-2 * time.Hour)
	justAfter := noon.Add(-20 * time.Second)

	tests := []struct {
		name string
		expr string
		last *time.Time
		now  time.Time
		want bool
	}{
		{
			name: "fired since last run",
			expr: "0 12 * * *",
			last: &before,
			now:  noon,
			want: true,
		},
		{
			name: "already ran after firing",
			expr: "0 12 * * *",
			last: &justAfter,
			now:  noon,
			want: false,
		},
		{
			name: "never ran, fired in window",
			expr: "0 12 * * *",
			now:  noon,
			want: true,
		},
		{
			name: "never ran, not in window",
			expr: "0 12 * * *",
			now:  noon.Add(2 * time.Hour),
			want: false,
		},
		{
			name: "descriptor",
			expr: "@hourly",
			last: &before,
			now:  noon,
			want: true,
		},
		{
			name: "every minute",
			expr: "* * * * *",
			last: &justAfter,
			now:  noon.Add(time.Minute),
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Due(tt.expr, tt.last, tt.now, time.Minute)
			if err != nil || got != tt.want {
				t.Errorf("Due = %v, %v; want %v", got, err, tt.want)
			}
		})
	}

	for _, bad := range []string{
		"not a cron",
		"0 0 12 * * ?",
	} {
		if _, err := Due(bad, nil, noon, time.Minute); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
