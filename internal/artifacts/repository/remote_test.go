package repository

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestRetryable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "reset connection",
			err:  errors.New("connection reset by peer"),
			want: true,
		},
		{
			name: "unknown host",
			err: fmt.Errorf("dial: %w", &net.DNSError{
				Err:        "no such host",
				Name:       "nope.invalid",
				IsNotFound: true,
			}),
		},
		{
			name: "untrusted certificate",
			err:  fmt.Errorf("tls: %w", x509.UnknownAuthorityError{}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := retryable(tt.err); got != tt.want {
				t.Errorf("retryable = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{
			name:   "no header",
			header: "",
			want:   0,
		},
		{
			name:   "seconds",
			header: "2",
			want:   2 * time.Second,
		},
		{
			name:   "capped",
			header: "3600",
			want:   maxRetryAfter,
		},
		{
			name:   "an HTTP date",
			header: now.Add(5 * time.Second).Format(http.TimeFormat),
			want:   5 * time.Second,
		},
		{
			name:   "unparseable",
			header: "soon",
			want:   0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := http.Header{}
			if tt.header != "" {
				h.Set("Retry-After", tt.header)
			}

			if got := retryAfter(h, now); got != tt.want {
				t.Errorf("retryAfter(%q) = %s, want %s", tt.header, got, tt.want)
			}
		})
	}
}
