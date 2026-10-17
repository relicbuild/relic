package repository

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidateRefusesTextTheDatabasesCannotStore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name: "plain text",
			config: Config{
				Description: "Bibliothèques",
				Extensions: map[string]json.RawMessage{
					"custom": json.RawMessage(`{"s":"x y"}`),
				},
			},
		},
		{
			name: "NUL in a description",
			config: Config{
				Description: "a\x00b",
			},
			wantErr: true,
		},
		{
			name: "invalid UTF-8 in a description",
			config: Config{
				Description: "a\xffb",
			},
			wantErr: true,
		},
		{
			name: "escaped NUL in an extension",
			config: Config{
				Extensions: map[string]json.RawMessage{
					"custom": json.RawMessage(`{"s":"x\u0000y"}`),
				},
			},
			wantErr: true,
		},
		{
			name: "invalid UTF-8 in an extension",
			config: Config{
				Extensions: map[string]json.RawMessage{
					"custom": json.RawMessage("{\"s\":\"x\xffy\"}"),
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := Repository{
				Name:   "r",
				Class:  ClassLocal,
				Config: tt.config,
			}

			err := r.Validate()
			if tt.wantErr != errors.Is(err, ErrInvalidRepository) || !tt.wantErr && err != nil {
				t.Errorf("Validate() = %v, want an error: %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		format  Format
		want    Format
		wantErr bool
	}{
		{
			name:   "known",
			format: FormatRubyGems,
			want:   FormatRubyGems,
		},
		{
			name:   "empty is generic",
			format: "",
			want:   FormatGeneric,
		},
		{
			name:    "a package type Relic renamed",
			format:  "gems",
			wantErr: true,
		},
		{
			name:    "unknown",
			format:  "yum",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := Repository{
				Name:   "r",
				Class:  ClassLocal,
				Format: tt.format,
			}

			err := r.Validate()
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidRepository) {
					t.Errorf("Validate() = %v, want ErrInvalidRepo", err)
				}

				return
			}

			if err != nil || r.Format != tt.want {
				t.Errorf("Validate() = %v, format %q; want %q", err, r.Format, tt.want)
			}
		})
	}
}
