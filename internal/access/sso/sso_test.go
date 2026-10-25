package sso

import (
	"errors"
	"testing"
)

func TestNewBoundsProviderRequests(t *testing.T) {
	t.Parallel()

	if timeout := New(nil, nil, nil).client.Timeout; timeout <= 0 {
		t.Errorf("identity provider requests have no timeout: %v", timeout)
	}
}

func TestProviderValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider Provider
		want     error
	}{
		{
			name: "an https issuer",
			provider: Provider{
				Name: "corp",
				OIDC: &OIDCSettings{
					Issuer:   "https://login.example.com",
					ClientID: "relic",
				},
			},
		},
		{
			name: "a plain http issuer on this machine",
			provider: Provider{
				Name: "corp",
				OIDC: &OIDCSettings{
					Issuer:   "http://127.0.0.1:8081",
					ClientID: "relic",
				},
			},
		},
		{
			name: "a plain http issuer on localhost",
			provider: Provider{
				Name: "corp",
				OIDC: &OIDCSettings{
					Issuer:   "http://localhost:8081",
					ClientID: "relic",
				},
			},
		},
		{
			name: "a plain http issuer on the network",
			provider: Provider{
				Name: "corp",
				OIDC: &OIDCSettings{
					Issuer:   "http://login.example.com",
					ClientID: "relic",
				},
			},
			want: ErrInvalid,
		},
		{
			name: "SAML metadata over plain http on the network",
			provider: Provider{
				Name: "corp",
				SAML: &SAMLSettings{
					MetadataURL: "http://login.example.com/metadata",
				},
			},
			want: ErrInvalid,
		},
		{
			name: "SAML metadata over https",
			provider: Provider{
				Name: "corp",
				SAML: &SAMLSettings{
					MetadataURL: "https://login.example.com/metadata",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if err := tt.provider.Validate(); !errors.Is(err, tt.want) {
				t.Errorf("Validate = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestReturnPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "a path on this server",
			path: "/browse/libs-local?tab=files",
			want: "/browse/libs-local?tab=files",
		},
		{
			name: "an absolute URL",
			path: "https://evil.example.com/",
			want: "/",
		},
		{
			name: "a host-relative URL",
			path: "//evil.example.com/x",
			want: "/",
		},
		{
			name: "a backslash after the slash",
			path: "/\\evil.example.com/x",
			want: "/",
		},
		{
			name: "a tab between the slashes",
			path: "/\t/evil.example.com/x",
			want: "/",
		},
		{
			name: "a newline between the slashes",
			path: "/\n/evil.example.com/x",
			want: "/",
		},
		{
			name: "a carriage return between the slashes",
			path: "/\r/evil.example.com/x",
			want: "/",
		},
		{
			name: "a backslash later in the path",
			path: "/a\\b",
			want: "/",
		},
		{
			name: "a delete character",
			path: "/a\x7f",
			want: "/",
		},
		{
			name: "empty",
			path: "",
			want: "/",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := ReturnPath(tt.path); got != tt.want {
				t.Errorf("ReturnPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
