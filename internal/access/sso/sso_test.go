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
