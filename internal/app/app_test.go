package app

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"relic/internal/access/auth"
	"relic/internal/app/ui"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/objectstore"
)

func TestRunRejectsUnknownCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			"no args",
			nil,
			"usage: relic",
		},
		{
			"unknown",
			[]string{
				"nope",
			},
			`unknown command "nope"`,
		},
		{
			"--worker is the server's",
			[]string{
				"worker",
				"--worker",
			},
			`unknown argument "--worker"`,
		},
		{
			"migrate needs the database URL only, and that",
			[]string{
				"migrate",
			},
			"RELIC_DATABASE_URL is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := Run(t.Context(), tt.args, func(string) string {
				return ""
			}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Run(%v) = %v, want error containing %q", tt.args, err, tt.want)
			}
		})
	}
}

func TestRunListsEveryConfigurationError(t *testing.T) {
	t.Parallel()

	for _, command := range []string{
		"server",
		"worker",
	} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()

			env := map[string]string{
				"RELIC_LOG_LEVEL":              "verbose",
				"RELIC_MAX_UPLOAD_SIZE":        "10GB",
				"RELIC_DATABASE_URL":           "sqlite:///var/lib/relic/relic.db",
				"RELIC_TRASH_RETENTION":        "bogus",
				"RELIC_METRICS_LISTEN_ADDRESS": "off",
			}

			err := Run(t.Context(), []string{
				command,
			}, func(k string) string {
				return env[k]
			}, io.Discard)
			if err == nil {
				t.Fatal("Run went ahead with wrong settings")
			}

			for _, want := range []string{
				"RELIC_LOG_LEVEL",
				"RELIC_MAX_UPLOAD_SIZE",
				"RELIC_TRASH_RETENTION",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %v, want it to name %s", err, want)
				}
			}
		})
	}
}

func TestRoutes(t *testing.T) {
	t.Parallel()

	uiAnswer := httptest.NewRecorder()
	ui.Handler("/").ServeHTTP(uiAnswer, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	tests := []struct {
		method, path string
		want         int
	}{
		{
			http.MethodGet,
			"/healthz",
			http.StatusOK,
		},
		{
			http.MethodPost,
			"/healthz",
			http.StatusNotFound,
		},
		{
			http.MethodGet,
			"/",
			uiAnswer.Code,
		},
		{
			http.MethodGet,
			"/repositories/libs",
			uiAnswer.Code,
		},
		{
			http.MethodGet,
			"/api/v1/nope",
			http.StatusNotFound,
		},
		{
			http.MethodGet,
			"/readyz",
			http.StatusOK,
		},
	}
	h := routes(healthChecks(fakePinger{}, fakeBlobs{}))

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, nil))

			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

type fakePinger struct{}

func (fakePinger) Ping(context.Context) error { return nil }

type fakeBlobs struct {
	err error
}

func (f fakeBlobs) Head(context.Context, string) (objectstore.Info, error) {
	return objectstore.Info{}, f.err
}

type failingPinger struct{}

func (failingPinger) Ping(context.Context) error { return errors.New("down") }

func TestReadyz(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		db    interface{ Ping(context.Context) error }
		blobs fakeBlobs
		want  int
	}{
		{
			name: "ready (probe key absent)",
			db:   fakePinger{},
			blobs: fakeBlobs{
				err: objectstore.ErrNotFound,
			},
			want: http.StatusOK,
		},
		{
			name:  "database down",
			db:    failingPinger{},
			blobs: fakeBlobs{},
			want:  http.StatusServiceUnavailable,
		},
		{
			name: "storage down",
			db:   fakePinger{},
			blobs: fakeBlobs{
				err: errors.New("connection refused"),
			},
			want: http.StatusServiceUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			routes(
				healthChecks(tt.db, tt.blobs),
			).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))

			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestRouteAccess(t *testing.T) {
	t.Parallel()

	rt := routes(healthChecks(fakePinger{}, fakeBlobs{}))
	public := map[string]bool{
		"GET /healthz": true,
		"GET /readyz":  true,
		"/":            true,
	}

	for _, r := range rt.Routes() {
		switch r.Access {
		case auth.Public:
			if !public[r.Pattern] {
				t.Errorf("route %q is public but not allowlisted", r.Pattern)
			}
		case auth.Authenticated, auth.Admin:
		default:
			t.Errorf("route %q has no valid access level (%v)", r.Pattern, r.Access)
		}
	}
}

func TestAPINotFound(t *testing.T) {
	t.Parallel()

	ui := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "spa")
	})
	h := apiNotFound(ui)
	tests := []struct {
		path string
		want int
	}{
		{
			"/",
			http.StatusOK,
		},
		{
			"/docs/installation/overview",
			http.StatusOK,
		},
		{
			"/api/relic.v1.Nope/Get",
			http.StatusNotFound,
		},
		{
			"/apis",
			http.StatusOK,
		},
		{
			"/v1/repositories/app/tags",
			http.StatusNotFound,
		},
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/docs", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /docs = %d, want 404", rec.Code)
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.path, nil))

			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}

	for _, p := range apiPrefixes {
		for _, path := range []string{
			p + "unknown",
			strings.TrimSuffix(p, "/"),
		} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))

			if rec.Code != http.StatusNotFound {
				t.Errorf("%s = %d, want 404", path, rec.Code)
			}
		}
	}
}

func TestMetricsNotOnAPIPort(t *testing.T) {
	t.Parallel()

	h := NewHandler(nil, nil, nil, nil, Options{}, slog.New(slog.DiscardHandler))

	for _, path := range []string{
		"/metrics",
		"/debug/pprof/",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))

		if body := rec.Body.String(); strings.Contains(body, "relic_build_info") ||
			strings.Contains(body, "go_goroutines") || strings.Contains(body, "/debug/pprof/") {
			t.Errorf("GET %s on the API port = %d %q", path, rec.Code, body)
		}
	}
}

func TestRelicVersionIsTheStampedRelease(t *testing.T) {
	Version = "v1.2.3"

	t.Cleanup(func() {
		Version = ""
	})

	if v, _ := relicVersion(); v != "v1.2.3" {
		t.Errorf("version = %q", v)
	}
}

func TestRelicRevisionComesFromADescribedVersion(t *testing.T) {
	Version = "v0.2.0-beta.3-2-gf84fd3cb"

	t.Cleanup(func() {
		Version = ""
	})

	if _, r := relicVersion(); r != "f84fd3cb" {
		t.Errorf("revision = %q", r)
	}
}

func TestOutboundClientsRefusePrivateAddresses(t *testing.T) {
	t.Parallel()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)

	clients := map[string]func(allowPrivate bool) *http.Client{
		"webhook": webhookClient,
		"replication": func(allowPrivate bool) *http.Client {
			return replicationClient(allowPrivate, nil)
		},
	}

	tests := []struct {
		name         string
		allowPrivate bool
		wantRefused  bool
	}{
		{
			name:        "a loopback target is refused by default",
			wantRefused: true,
		},
		{
			name:         "private targets allowed",
			allowPrivate: true,
		},
	}
	for client, newClient := range clients {
		for _, tt := range tests {
			t.Run(client+": "+tt.name, func(t *testing.T) {
				t.Parallel()

				req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target.URL, nil)
				if err != nil {
					t.Fatal(err)
				}

				resp, err := newClient(tt.allowPrivate).Do(req)
				if err == nil {
					_ = resp.Body.Close()
				}

				if refused := errors.Is(err, repository.ErrEgressDenied); refused != tt.wantRefused {
					t.Errorf("POST %s = %v, want refused %v", target.URL, err, tt.wantRefused)
				}

				if !tt.wantRefused && (err != nil || resp.StatusCode != http.StatusNoContent) {
					t.Errorf("POST %s = %v, %v; want the target's 204", target.URL, resp, err)
				}
			})
		}
	}
}

func TestReplicationClientTrustsTheUpstreamAuthority(t *testing.T) {
	t.Parallel()

	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)

	caFile := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: target.Certificate().Raw,
	}), 0o600); err != nil {
		t.Fatal(err)
	}

	roots, err := trustedRoots("RELIC_UPSTREAM_CA_CERTIFICATE_FILE", caFile)
	if err != nil {
		t.Fatal(err)
	}

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
			name:  "the upstream CA file trusts it",
			roots: roots,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target.URL, nil)
			if err != nil {
				t.Fatal(err)
			}

			resp, err := replicationClient(true, tt.roots).Do(req)
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

func TestTrustedRootsRefusesAFileWithoutCertificates(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(file, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := trustedRoots("RELIC_UPSTREAM_CA_CERTIFICATE_FILE", file); err == nil ||
		!strings.Contains(err.Error(), "RELIC_UPSTREAM_CA_CERTIFICATE_FILE") {
		t.Errorf("trustedRoots = %v, want an error naming the setting", err)
	}
}
