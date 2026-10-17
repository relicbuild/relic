package main

import (
	"errors"
	"path"
	"testing"
)

func TestCheckPatterns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		patterns []string
		wantErr  bool
	}{
		{
			name: "valid patterns",
			patterns: []string{
				"CI_*",
				"*_TOKEN",
			},
		},
		{
			name: "an unclosed bracket",
			patterns: []string{
				"MY_[PRIV",
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkPatterns("--exclude", tt.patterns)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkPatterns(%q) = %v, want error %v", tt.patterns, err, tt.wantErr)
			}

			if tt.wantErr && !errors.Is(err, path.ErrBadPattern) {
				t.Errorf("checkPatterns error = %v, want path.ErrBadPattern", err)
			}
		})
	}
}
