//go:build integration

package terraform_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"relic/internal/artifacts/formats/terraform"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func TestRemoteProviderArchives(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	log := slog.New(slog.DiscardHandler)
	repos := repository.NewService(databasetest.NewDB(t), objectstoretest.NewMemory(), log)
	repos.AllowPrivateUpstreams(true)

	svc := terraform.NewService(repos, log)

	var queries atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			queries.Add(1)
		}

		switch r.URL.Path {
		case "/v1/providers/hashicorp/random/versions":
			_, _ = io.WriteString(w, `{"versions":[{"version":"1.0.0","platforms":[`+
				`{"os":"linux","arch":"amd64"},{"os":"darwin","arch":"arm64"}]}]}`)
		case "/v1/providers/hashicorp/random/1.0.0/download/linux/amd64":
			_, _ = io.WriteString(w, `{"filename":"terraform-provider-random_1.0.0_linux_amd64.zip",`+
				`"download_url":"https://releases.example/random.zip","shasum":"`+strings.Repeat("a", 64)+`"}`)
		case "/v1/providers/hashicorp/random/1.0.0/download/darwin/arm64":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	upstream, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	r := repository.Repository{
		Name:   "terraform-remote",
		Class:  repository.ClassRemote,
		Format: repository.FormatTerraform,
		Config: repository.Config{
			Remote: &repository.RemoteConfig{
				URL: srv.URL,
			},
		},
	}
	if err := repos.CreateRepository(ctx, r); err != nil {
		t.Fatal(err)
	}

	a := terraform.Address{
		Hostname:  upstream.Hostname(),
		Namespace: "hashicorp",
		Type:      "random",
	}

	if archives, err := svc.Archives(ctx, r, a, "1.0.0"); !errors.Is(err, repository.ErrUpstream) {
		t.Errorf("Archives with one platform's download failing = %v, %v; want ErrUpstream", archives, err)
	}

	_, err = svc.Package(ctx, r, a, "terraform-provider-random_1.0.0_linux?x=y_amd64.zip")
	if !errors.Is(err, terraform.ErrNotFound) {
		t.Errorf("Package with a query in its platform = %v, want ErrNotFound", err)
	}

	if n := queries.Load(); n != 0 {
		t.Errorf("the upstream got %d requests with a query string", n)
	}
}
