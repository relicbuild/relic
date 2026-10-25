package repository

import (
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestPublicAddr(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		"8.8.8.8":          true,
		"151.101.1.1":      true,
		"2606:4700::1111":  true,
		"127.0.0.1":        false,
		"10.1.2.3":         false,
		"172.20.0.1":       false,
		"192.168.1.1":      false,
		"169.254.169.254":  false,
		"100.64.0.1":       false,
		"0.0.0.0":          false,
		"::1":              false,
		"fe80::1":          false,
		"fd00::1":          false,
		"::ffff:127.0.0.1": false,
	}
	for addr, want := range tests {
		t.Run(addr, func(t *testing.T) {
			t.Parallel()

			if got := PublicAddr(netip.MustParseAddr(addr)); got != want {
				t.Errorf("PublicAddr(%s) = %v, want %v", addr, got, want)
			}
		})
	}
}

func TestSameSite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		upstream, target string
		want             bool
	}{
		{
			"https://registry-1.docker.io",
			"https://auth.docker.io/token",
			true,
		},
		{
			"https://ghcr.io",
			"https://ghcr.io/token",
			true,
		},
		{
			"https://index.crates.io",
			"https://static.crates.io/crates/x",
			true,
		},
		{
			"https://registry-1.docker.io",
			"https://evil.example/token",
			false,
		},
		{
			"https://registry-1.docker.io",
			"http://auth.docker.io/token",
			false,
		},
		{
			"http://nexus.local:8081/repo",
			"http://nexus.local:8081/other",
			true,
		},
		{
			"https://example.com",
			"https://169.254.169.254/latest",
			false,
		},
		{
			"https://localhost",
			"https://other.localhost",
			false,
		},
		{
			"https://a.co.uk",
			"https://b.co.uk/token",
			false,
		},
		{
			"https://reg.a.co.uk",
			"https://auth.a.co.uk/token",
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.upstream+" "+tt.target, func(t *testing.T) {
			t.Parallel()

			if got := SameSite(tt.upstream, tt.target); got != tt.want {
				t.Errorf("SameSite = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUpstreamClientRefusesPrivateAddresses(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("internal"))
		}),
	)
	defer srv.Close()

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)

	resp, err := newUpstreamClient(time.Second, false, nil).Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}

	if !errors.Is(err, ErrEgressDenied) {
		t.Errorf("loopback upstream: err = %v, want ErrEgressDenied", err)
	}

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)

	resp, err = newUpstreamClient(time.Second, true, nil).Do(req)
	if err != nil {
		t.Fatalf("allowed private upstream: %v", err)
	}

	_ = resp.Body.Close()
}

func TestUpstreamClientTrustsTheUpstreamRoots(t *testing.T) {
	t.Parallel()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())

	tests := []struct {
		name        string
		roots       *x509.CertPool
		wantRefused bool
	}{
		{
			name:        "the system roots refuse a private authority",
			wantRefused: true,
		},
		{
			name:  "the upstream roots trust it",
			roots: roots,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)

			resp, err := newUpstreamClient(time.Second, true, tt.roots).Do(req)
			if err == nil {
				_ = resp.Body.Close()
			}

			var unknown x509.UnknownAuthorityError
			if refused := errors.As(err, &unknown); refused != tt.wantRefused {
				t.Errorf("request = %v, want refused %v", err, tt.wantRefused)
			}
		})
	}
}

func TestNoDowngrade(t *testing.T) {
	t.Parallel()

	https, _ := http.NewRequest(http.MethodGet, "https://a.example/x", nil) //nolint:noctx // redirect policy only

	plain, _ := http.NewRequest(http.MethodGet, "http://a.example/y", nil) //nolint:noctx // redirect policy only
	if err := noDowngrade(plain, []*http.Request{
		https,
	}); !errors.Is(err, ErrEgressDenied) {
		t.Errorf("https→http redirect allowed: %v", err)
	}

	if err := noDowngrade(https, []*http.Request{
		plain,
	}); err != nil {
		t.Errorf("http→https redirect refused: %v", err)
	}

	if err := noDowngrade(plain, []*http.Request{
		plain,
	}); err != nil {
		t.Errorf("http→http redirect refused: %v", err)
	}
}

func TestUpstreamClientWaitsEachRepositorysTimeout(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(300 * time.Millisecond)

			_, _ = w.Write([]byte("slow"))
		}),
	)
	defer srv.Close()

	s := &Service{
		allowPrivate: true,
	}

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)

	resp, err := s.httpClient(50 * time.Millisecond).Do(req)
	if err == nil {
		_ = resp.Body.Close()

		t.Fatal("a 50ms header timeout waited 300ms")
	}

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)

	resp, err = s.httpClient(5 * time.Second).Do(req)
	if err != nil {
		t.Fatalf("a 5s header timeout after a 50ms one: %v", err)
	}

	_ = resp.Body.Close()
}
