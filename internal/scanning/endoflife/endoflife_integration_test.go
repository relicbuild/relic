//go:build integration

package endoflife

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"relic/internal/platform/database/databasetest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

const distroDump = `{"result":[
 {"name":"alpine-linux","label":"Alpine Linux","identifiers":[],
  "releases":[{"name":"3.17","eolFrom":"2024-11-22","isEol":true,"isMaintained":false,"latest":{"name":"3.17.10"}},
              {"name":"3.22","eolFrom":"2027-05-01","isEol":false,"isMaintained":true,"latest":{"name":"3.22.2"}}]},
 {"name":"django","label":"Django","identifiers":[{"type":"purl","id":"pkg:pypi/django"}],
  "releases":[{"name":"5.2","eolFrom":"2028-04-30","isEol":false,"isMaintained":true,"latest":{"name":"5.2.7"}},
              {"name":"4.2","eolFrom":"2026-04-30","isEol":true,"isMaintained":false,"latest":{"name":"4.2.25"}}]}]}`

func fixedNow() time.Time {
	return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
}

func TestRefreshAndAssess(t *testing.T) {
	t.Parallel()

	db := databasetest.NewDB(t)

	var served atomic.Int32

	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)

		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)

			return
		}

		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(distroDump))
	}))
	t.Cleanup(feed.Close)

	ref := NewRefresher(db, feed.URL, nil, slog.New(slog.DiscardHandler))

	changed, err := ref.Refresh(t.Context())
	if err != nil || !changed {
		t.Fatalf("first refresh = %v, %v; want changed", changed, err)
	}

	if changed, err := ref.Refresh(t.Context()); err != nil || changed {
		t.Fatalf("second refresh = %v, %v; want unchanged (ETag)", changed, err)
	}

	if n := served.Load(); n != 2 {
		t.Errorf("the source answered %d requests, want 2", n)
	}

	cat := NewCatalog(db, nil)
	cat.now = fixedNow

	st, err := cat.Status(t.Context())
	if err != nil || st.Products != 2 || st.Source != SourceFeed {
		t.Fatalf("status = %+v, %v", st, err)
	}

	risks, err := cat.Assess(t.Context(), []Package{
		{
			PURL:    "pkg:pypi/Django@4.2.11",
			Name:    "Django",
			Version: "4.2.11",
		},
		{
			PURL:    "pkg:pypi/django@5.2.1",
			Name:    "django",
			Version: "5.2.1",
		},
		{
			PURL:    "pkg:npm/left-pad@1.3.0",
			Name:    "left-pad",
			Version: "1.3.0",
		},
	}, Distro{
		ID:      "alpine",
		Version: "3.17.4",
	})
	if err != nil {
		t.Fatal(err)
	}

	slices.SortFunc(risks, func(a, b Risk) int {
		return strings.Compare(a.Product, b.Product)
	})

	if len(risks) != 2 {
		t.Fatalf("risks = %+v, want Alpine 3.17 and Django 4.2", risks)
	}

	alpine, django := risks[0], risks[1]
	if alpine.Product != "alpine-linux" || alpine.Cycle != "3.17" || alpine.Severity != SeverityHigh ||
		alpine.PURL != "pkg:generic/alpine@3.17.4" {
		t.Errorf("alpine = %+v", alpine)
	}

	if django.Product != "django" || django.Cycle != "4.2" || django.EOLFrom != "2026-04-30" ||
		django.Version != "4.2.11" || !strings.Contains(django.Detail, "reached end of life on 2026-04-30") {
		t.Errorf("django = %+v", django)
	}

	n, err := ref.Import(t.Context(), strings.NewReader(sampleDump))
	if err != nil || n != 2 {
		t.Fatalf("import = %d, %v", n, err)
	}

	if risks, err := cat.Assess(t.Context(), nil, Distro{
		ID:      "alpine",
		Version: "3.17.4",
	}); err != nil || len(risks) != 0 {
		t.Errorf("after an import without Alpine: %+v, %v", risks, err)
	}
}

func TestDepsDev(t *testing.T) {
	t.Parallel()

	db := databasetest.NewDB(t)

	var calls atomic.Int32

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)

		switch r.URL.EscapedPath() {
		case "/v3/systems/NPM/packages/request/versions/2.88.2":
			_, _ = w.Write([]byte(`{"isDeprecated":true,"deprecatedReason":"request has been deprecated",
				"relatedProjects":[{"projectKey":{"id":"github.com/request/request"},"relationType":"SOURCE_REPO"}]}`))
		case "/v3/projects/github.com%2Frequest%2Frequest":
			_, _ = w.Write([]byte(`{"scorecard":{"checks":[{"name":"Maintained","score":0}]}}`))
		case "/v3/systems/NPM/packages/core-js/versions/2.6.12":
			_, _ = w.Write([]byte(`{"isDeprecated":true,"deprecatedReason":"core-js@<3 is no longer maintained",
				"relatedProjects":[{"projectKey":{"id":"github.com/zloirock/core-js"},"relationType":"SOURCE_REPO"}]}`))
		case "/v3/projects/github.com%2Fzloirock%2Fcore-js":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)

	deps := NewDepsDev(db, api.URL, nil, slog.New(slog.DiscardHandler))
	pkgs := []Package{
		{
			PURL:    "pkg:npm/request@2.88.2",
			Name:    "request",
			Version: "2.88.2",
		},
		{
			PURL:    "pkg:npm/left-pad@1.3.0",
			Name:    "left-pad",
			Version: "1.3.0",
		},
		{
			PURL:    "pkg:npm/core-js@2.6.12",
			Name:    "core-js",
			Version: "2.6.12",
		},
		{
			PURL:    "pkg:deb/debian/openssl@3.0.11",
			Name:    "openssl",
			Version: "3.0.11",
		},
	}

	risks := deps.Risks(t.Context(), pkgs)
	slices.SortFunc(risks, func(a, b Risk) int {
		return strings.Compare(a.Kind+a.PURL, b.Kind+b.PURL)
	})

	if len(risks) != 3 || risks[0].Kind != KindDeprecated || risks[0].PURL != "pkg:npm/core-js@2.6.12" ||
		risks[1].Kind != KindDeprecated ||
		!strings.Contains(risks[1].Detail, "request has been deprecated") ||
		risks[2].Kind != KindUnmaintained {
		t.Fatalf("risks = %+v", risks)
	}

	first := calls.Load()
	if first != 5 {
		t.Errorf("deps.dev calls = %d, want 5 (three versions, two projects; Debian is not asked)", first)
	}

	if again := deps.Risks(t.Context(), pkgs); len(again) != 3 || calls.Load()-first != 2 {
		t.Errorf(
			"second pass = %d risks, %d calls; want the cache to answer all but the unfinished core-js",
			len(again),
			calls.Load()-first,
		)
	}
}

func TestDepsDevKeepsAnAnswerItCannotCache(t *testing.T) {
	t.Parallel()

	if databasetest.SQLite() {
		t.Skip("SQLite stores a NUL in text, so the cache write does not fail")
	}

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() == "/v3/systems/NPM/packages/old-lib/versions/1.0.0" {
			_, _ = w.Write([]byte(`{"isDeprecated":true,"deprecatedReason":"use new-lib\u0000 instead"}`))

			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(api.Close)

	deps := NewDepsDev(databasetest.NewDB(t), api.URL, nil, slog.New(slog.DiscardHandler))

	risks := deps.Risks(t.Context(), []Package{
		{
			PURL:    "pkg:npm/old-lib@1.0.0",
			Name:    "old-lib",
			Version: "1.0.0",
		},
	})
	if len(risks) != 1 || risks[0].Kind != KindDeprecated {
		t.Errorf("risks of a deprecation the cache refused = %+v, want the deprecation", risks)
	}
}

func TestImportWaitsForAnotherStoreOfTheData(t *testing.T) {
	t.Parallel()

	db := databasetest.NewDB(t)
	ref := NewRefresher(db, "http://127.0.0.1:1", nil, slog.New(slog.DiscardHandler))

	held := make(chan struct{})
	unlock := make(chan struct{})
	lockErr := make(chan error, 1)

	go func() {
		lockErr <- db.WithLock(t.Context(), endOfLifeDataLock, func() error {
			close(held)
			<-unlock

			return nil
		})
	}()

	<-held

	imported := make(chan error, 1)

	go func() {
		_, err := ref.Import(t.Context(), strings.NewReader(sampleDump))
		imported <- err
	}()

	select {
	case err := <-imported:
		t.Errorf("Import went ahead while another store of the end-of-life data held its lock (err %v)", err)
		close(unlock)
	case <-time.After(2 * time.Second):
		close(unlock)

		if err := <-imported; err != nil {
			t.Fatal(err)
		}
	}

	if err := <-lockErr; err != nil {
		t.Fatal(err)
	}
}
