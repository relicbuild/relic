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

func TestParseBuildRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		ref        string
		wantName   string
		wantNumber string
		wantErr    bool
	}{
		{
			name:       "plain name",
			ref:        "app/42",
			wantName:   "app",
			wantNumber: "42",
		},
		{
			name:       "name with a slash",
			ref:        "team/app/7",
			wantName:   "team/app",
			wantNumber: "7",
		},
		{
			name:    "no number",
			ref:     "app",
			wantErr: true,
		},
		{
			name:    "empty number",
			ref:     "app/",
			wantErr: true,
		},
		{
			name:    "empty name",
			ref:     "/7",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			name, number, err := parseBuildRef(tt.ref)
			if (err != nil) != tt.wantErr || name != tt.wantName || number != tt.wantNumber {
				t.Errorf("parseBuildRef(%q) = %q, %q, %v", tt.ref, name, number, err)
			}
		})
	}
}
