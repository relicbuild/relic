package main

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"relic/internal/platform/servertls"
)

func TestTrustedAuthority(t *testing.T) {
	t.Parallel()

	directory, err := servertls.OpenDirectory(t.Context(), t.TempDir(), []string{
		"127.0.0.1",
	}, time.Now)
	if err != nil {
		t.Fatal(err)
	}

	certificate, err := directory.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.TLS = servertls.Config(tls.VersionTLS12, directory.GetCertificate)
	srv.TLS.Certificates = []tls.Certificate{
		*certificate,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	authority := string(directory.Authority().CertificatePEM())

	authorityFile := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(authorityFile, []byte(authority), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name                     string
		serverURL                string
		certificateAuthorityFile string
		canAsk                   bool
		answer                   string
		want                     string
		wantErr                  string
	}{
		{
			name:      "accepted on a terminal",
			serverURL: srv.URL,
			canAsk:    true,
			answer:    "y\n",
			want:      authority,
		},
		{
			name:      "refused on a terminal",
			serverURL: srv.URL,
			canAsk:    true,
			answer:    "n\n",
			wantErr:   "not trusted",
		},
		{
			name:      "no terminal to ask on",
			serverURL: srv.URL,
			wantErr:   directory.Authority().Fingerprint(),
		},
		{
			name:                     "--ca-file",
			serverURL:                srv.URL,
			certificateAuthorityFile: authorityFile,
			want:                     authority,
		},
		{
			name:      "plain HTTP",
			serverURL: "http://127.0.0.1:1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cmd := &cobra.Command{}
			cmd.SetContext(t.Context())
			cmd.SetIn(strings.NewReader(tt.answer))
			cmd.SetErr(io.Discard)

			server, err := url.Parse(tt.serverURL)
			if err != nil {
				t.Fatal(err)
			}

			got, err := trustedAuthority(cmd, server, tt.certificateAuthorityFile, tt.canAsk)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("err = %v, want one containing %q", err, tt.wantErr)
				}

				return
			}

			if err != nil || got != tt.want {
				t.Errorf("trustedAuthority = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
