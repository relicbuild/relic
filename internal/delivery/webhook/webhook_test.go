package webhook

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestValidateRefusesCredentialsInTheURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		url     string
		wantErr error
	}{
		{
			name: "a plain URL",
			url:  "https://hooks.example.com/x",
		},
		//nolint:gosec // G101: a made-up URL whose user info is refused
		{
			name:    "a user and password",
			url:     "https://u:p@hooks.example.com/x",
			wantErr: ErrInvalid,
		},
		{
			name:    "a user",
			url:     "https://u@hooks.example.com/x",
			wantErr: ErrInvalid,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validate(&Webhook{
				Name: "hook",
				URL:  tt.url,
				Events: []string{
					AllEvents,
				},
			})
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("validate(%s) = %v, want %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestTruncateKeepsValidUTF8(t *testing.T) {
	t.Parallel()

	text := strings.Repeat("a", maxErrorLength-1) + "é and more"

	got := truncate(text)
	if !utf8.ValidString(got) || len(got) > maxErrorLength || got != strings.Repeat("a", maxErrorLength-1) {
		t.Errorf(
			"truncate of an error cut inside é = %q (%d bytes), want the text before it",
			got[len(got)-3:],
			len(got),
		)
	}
}
