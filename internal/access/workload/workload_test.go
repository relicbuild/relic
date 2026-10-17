package workload

import (
	"errors"
	"fmt"
	"testing"

	"relic/internal/access/audit"
)

func TestNewBoundsIssuerRequests(t *testing.T) {
	t.Parallel()

	if timeout := New(nil, nil, nil).client.Timeout; timeout <= 0 {
		t.Errorf("issuer requests have no timeout: %v", timeout)
	}
}

func TestExchangeOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want audit.Outcome
	}{
		{
			name: "exchanged",
			want: audit.Success,
		},
		{
			name: "refused",
			err:  fmt.Errorf("%w: wrong audience", ErrRefused),
			want: audit.Denied,
		},
		{
			name: "the database failed",
			err:  errors.New("connection refused"),
			want: audit.Failure,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := exchangeOutcome(tt.err); got != tt.want {
				t.Errorf("exchangeOutcome(%v) = %s, want %s", tt.err, got, tt.want)
			}
		})
	}
}
