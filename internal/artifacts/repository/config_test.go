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

func TestValidateRefusesCredentialsInRemoteURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		remote  RemoteConfig
		wantErr bool
	}{
		{
			name: "credentials in the username and password",
			remote: RemoteConfig{
				URL:      "https://upstream.example.com/",
				Username: "u",
				Password: "p",
			},
		},
		//nolint:gosec // G101: a made-up URL whose user info is refused
		{
			name: "a user and password in the URL",
			remote: RemoteConfig{
				URL: "https://u:p@upstream.example.com/",
			},
			wantErr: true,
		},
		{
			name: "a user in the URL",
			remote: RemoteConfig{
				URL: "https://u@upstream.example.com/",
			},
			wantErr: true,
		},
		//nolint:gosec // G101: a made-up URL whose user info is refused
		{
			name: "a user and password in the index URL",
			remote: RemoteConfig{
				URL:      "https://upstream.example.com/",
				IndexURL: "https://u:p@index.example.com/",
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := Repository{
				Name:  "r",
				Class: ClassRemote,
				Config: Config{
					Remote: &tt.remote,
				},
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

func TestValidateSigningKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		format  Format
		primary string
		wantErr bool
	}{
		{
			name:    "a plain name",
			format:  FormatDebian,
			primary: "debian-signing.2026_1",
		},
		{
			name:    "a name with a path",
			format:  FormatAlpine,
			primary: "../../../tmp/relic-traversal",
			wantErr: true,
		},
		{
			name:    "a name with a space",
			format:  FormatRPM,
			primary: "../x y",
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
				Config: Config{
					SigningKey: tt.primary,
				},
			}

			err := r.Validate()
			if tt.wantErr != errors.Is(err, ErrInvalidRepository) || !tt.wantErr && err != nil {
				t.Errorf("Validate() = %v, want an error: %v", err, tt.wantErr)
			}
		})
	}
}
