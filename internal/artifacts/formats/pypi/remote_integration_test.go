//go:build integration

package pypi_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync"
	"testing"

	"relic/internal/artifacts/formats/pypi"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

type redirectingTransport struct {
	target    *url.URL
	mu        sync.Mutex
	requested []string
}

func (t *redirectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.requested = append(t.requested, req.URL.String())
	t.mu.Unlock()

	redirected := req.Clone(req.Context())
	redirected.Host = req.URL.Host
	redirected.URL.Scheme = t.target.Scheme
	redirected.URL.Host = t.target.Host

	return http.DefaultTransport.RoundTrip(redirected)
}

func (t *redirectingTransport) requestedURLs() []string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return slices.Clone(t.requested)
}

func TestRegistryRemoteFetchesFilesFromTheFileHost(t *testing.T) {
	t.Parallel()

	const (
		wheel     = "packages/d9/5a/six-1.16.0-py2.py3-none-any.whl"
		wheelBody = "wheel"
	)

	sum := sha256.Sum256([]byte(wheelBody))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Host + r.URL.Path {
		case "pypi.org/simple/six/":
			_, _ = io.WriteString(w, `<html><body>
			<a href="https://files.pythonhosted.org/`+wheel+`#sha256=`+hex.EncodeToString(sum[:])+`">six</a>
			</body></html>`)
		case "files.pythonhosted.org/" + wheel:
			_, _ = io.WriteString(w, wheelBody)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	transport := &redirectingTransport{
		target: target,
	}

	repos := repository.NewService(databasetest.NewDB(t), objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))
	repos.SetHTTPClient(&http.Client{
		Transport: transport,
	})
	svc := pypi.NewService(repos, slog.New(slog.DiscardHandler))

	r := repository.Repository{
		Name:   "pypi-registry-remote",
		Class:  repository.ClassRemote,
		Format: pypi.Format,
		Config: repository.Config{
			Remote: &repository.RemoteConfig{
				URL: "https://pypi.org",
			},
		},
	}
	if err := repos.CreateRepository(t.Context(), r); err != nil {
		t.Fatal(err)
	}

	links, err := svc.Links(t.Context(), r, "six")
	if err != nil {
		t.Fatal(err)
	}

	if len(links) != 1 || links[0].Href != "packages/"+wheel {
		t.Fatalf("links = %+v, want one link through Relic", links)
	}

	item, err := svc.Package(t.Context(), r, wheel)
	if err != nil {
		t.Fatalf("Package(%s) = %v", wheel, err)
	}

	if item.Path() != wheel {
		t.Errorf("cached at %q, want %q", item.Path(), wheel)
	}

	if requested := transport.requestedURLs(); !slices.Contains(requested, "https://files.pythonhosted.org/"+wheel) {
		t.Errorf("upstream requests = %v, want the file from https://files.pythonhosted.org", requested)
	}

	if _, err := repos.Stat(t.Context(), r.Name, wheel); err != nil {
		t.Errorf("Stat(%s) after the download = %v, want the cached file", wheel, err)
	}
}
