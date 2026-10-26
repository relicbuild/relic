//go:build integration

package npm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"relic/internal/artifacts/formats/npm"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

type upstreamRegistry struct {
	server   *httptest.Server
	requests atomic.Int64
	body     []byte
}

func newUpstreamRegistry(t *testing.T, name string, packument func(base string) []byte) *upstreamRegistry {
	t.Helper()

	u := &upstreamRegistry{}

	var body []byte

	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+name {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		u.requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(u.server.Close)

	body = packument(u.server.URL)
	u.body = body

	return u
}

func remoteService(t *testing.T, upstreamURL string, cooldownDays int32) (*npm.Service, *repository.Service) {
	t.Helper()

	db := databasetest.NewDB(t)
	repos := repository.NewService(db, objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))
	repos.AllowPrivateUpstreams(true)
	svc := npm.NewService(db, repos, slog.New(slog.DiscardHandler))

	if err := repos.CreateRepository(t.Context(), repository.Repository{
		Name:   "npmjs",
		Class:  repository.ClassRemote,
		Format: npm.Format,
		Config: repository.Config{
			Remote: &repository.RemoteConfig{
				URL:          upstreamURL,
				CooldownDays: cooldownDays,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	return svc, repos
}

func largePackument(base string) []byte {
	const versions = 2000

	readme := strings.Repeat("x", 10<<10)

	var b strings.Builder

	b.WriteString(`{"_id":"big","name":"big","dist-tags":{"latest":"1.0.1999"},"time":{`)

	for i := range versions {
		fmt.Fprintf(&b, `"1.0.%d":"2020-01-01T00:00:00.000Z",`, i)
	}

	b.WriteString(`"modified":"2020-01-01T00:00:00.000Z"},"versions":{`)

	for i := range versions {
		if i > 0 {
			b.WriteString(",")
		}

		fmt.Fprintf(&b, `"1.0.%d":{"name":"big","version":"1.0.%d","description":"%s",`+
			`"dist":{"tarball":"%s/big/-/big-1.0.%d.tgz","shasum":"%040d"}}`, i, i, readme, base, i, i)
	}

	b.WriteString(`}}`)

	return []byte(b.String())
}

//nolint:paralleltest // measures the process's allocations, which parallel tests would add to
func TestLargeRemotePackumentIsRenderedOnceForConcurrentClients(t *testing.T) {
	upstream := newUpstreamRegistry(t, "big", largePackument)
	svc, _ := remoteService(t, upstream.server.URL, 0)
	size := len(upstream.body)

	const clients = 20

	var (
		before, after runtime.MemStats
		wg            sync.WaitGroup
		bodies        [clients][]byte
		errs          [clients]error
	)

	runtime.GC()
	runtime.ReadMemStats(&before)

	for i := range clients {
		wg.Go(func() {
			bodies[i], errs[i] = svc.Document(t.Context(), "npmjs", "big", "http://relic/npm/npmjs")
		})
	}

	wg.Wait()
	runtime.ReadMemStats(&after)

	for i, err := range errs {
		if err != nil {
			t.Fatalf("client %d: %v", i, err)
		}
	}

	if n := upstream.requests.Load(); n != 1 {
		t.Errorf("upstream requests = %d, want 1", n)
	}

	if reads, renders := svc.IndexReads(), svc.Renders(); reads != 1 || renders != 1 {
		t.Errorf("index reads = %d, renders = %d; want the cached packument read and rendered once", reads, renders)
	}

	for i, body := range bodies {
		if &body[0] != &bodies[0][0] {
			t.Fatalf("client %d got its own copy of the document", i)
		}
	}

	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > uint64(12*size) {
		t.Errorf("%d clients allocated %d MiB for a %d MiB packument, want at most %d MiB",
			clients, allocated>>20, size>>20, 12*size>>20)
	}

	var doc struct {
		Versions map[string]struct {
			Dist struct {
				Tarball string `json:"tarball"`
			} `json:"dist"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(bodies[0], &doc); err != nil {
		t.Fatal(err)
	}

	if len(doc.Versions) != 2000 || doc.Versions["1.0.7"].Dist.Tarball != "http://relic/npm/npmjs/big/-/big-1.0.7.tgz" {
		t.Errorf("versions = %d, 1.0.7 tarball = %q", len(doc.Versions), doc.Versions["1.0.7"].Dist.Tarball)
	}

	again, err := svc.Document(t.Context(), "npmjs", "big", "http://relic/npm/npmjs")
	if err != nil || &again[0] != &bodies[0][0] || svc.Renders() != 1 {
		t.Errorf("a later request = %v, renders = %d; want the cached document", err, svc.Renders())
	}
}

type refusedVersions map[string]bool

func (r refusedVersions) gate(
	_ context.Context,
	_ repository.Repository,
	_, _ string,
	versions []repository.Version,
) (map[string]bool, error) {
	refused := map[string]bool{}
	for _, v := range versions {
		refused[v.Version] = r[v.Version]
	}

	return refused, nil
}

func TestRemotePackumentFollowsEachCaller(t *testing.T) {
	t.Parallel()

	older := time.Now().Add(-40 * 24 * time.Hour).UTC().Format(time.RFC3339)
	old := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	fresh := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)

	upstream := newUpstreamRegistry(t, "@acme/pad", func(base string) []byte {
		return []byte(`{"_id":"@acme/pad","name":"@acme/pad","readme":"hi",
		  "dist-tags":{"latest":"1.2.0","beta":"1.1.0","legacy":"1.0.0"},
		  "time":{"created":"` + old + `","modified":"` + fresh + `","0.9.0":"` + old + `",
		    "1.0.0":"` + older + `","1.1.0":"` + old + `","1.2.0":"` + fresh + `"},
		  "versions":{
		    "1.0.0":{"version":"1.0.0","dist":{"tarball":"` + base + `/@acme/pad/-/pad-1.0.0.tgz",
		      "attestations":{"url":"https://registry.example/attestations","provenance":{"predicateType":"p"}}}},
		    "1.1.0":{"version":"1.1.0","dist":{"tarball":"` + base + `/@acme/pad/-/pad-1.1.0.tgz"}},
		    "1.2.0":{"version":"1.2.0","dist":{"tarball":"` + base + `/@acme/pad/-/pad-1.2.0.tgz"}}}}`)
	})
	svc, repos := remoteService(t, upstream.server.URL, 7)
	repos.SetVersionGate(refusedVersions{
		"1.1.0": true,
	}.gate)

	type packument struct {
		DistTags map[string]string `json:"dist-tags"`
		Time     map[string]string `json:"time"`
		Readme   string            `json:"readme"`
		Versions map[string]struct {
			Dist struct {
				Tarball      string         `json:"tarball"`
				Attestations map[string]any `json:"attestations"`
			} `json:"dist"`
		} `json:"versions"`
	}

	read := func(ctx context.Context) packument {
		t.Helper()

		body, err := svc.Document(ctx, "npmjs", "@acme/pad", "http://relic/npm/npmjs")
		if err != nil {
			t.Fatal(err)
		}

		var doc packument
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatal(err)
		}

		return doc
	}

	everyone := read(t.Context())

	if len(everyone.Versions) != 1 || everyone.DistTags["latest"] != "1.0.0" || len(everyone.DistTags) != 2 ||
		everyone.DistTags["legacy"] != "1.0.0" || everyone.Readme != "hi" || len(everyone.Time) != 6 {
		t.Errorf("unrestricted packument = %+v; want 1.0.0 alone, tagged latest and legacy, the rest kept", everyone)
	}

	dist := everyone.Versions["1.0.0"].Dist
	if dist.Tarball != "http://relic/npm/npmjs/@acme/pad/-/pad-1.0.0.tgz" ||
		dist.Attestations["url"] != "http://relic/npm/npmjs/-/npm/v1/attestations/@acme%2Fpad@1.0.0" ||
		dist.Attestations["provenance"] == nil {
		t.Errorf("dist = %+v; want the tarball and attestations through Relic", dist)
	}

	restricted := repository.WithVisibility(t.Context(), hiddenPaths{
		"@acme/pad/-/pad-1.0.0.tgz": true,
	})

	if _, err := svc.Document(restricted, "npmjs", "@acme/pad", "http://relic/npm/npmjs"); err == nil {
		t.Error("a caller who sees no version got a packument")
	}

	partial := repository.WithVisibility(t.Context(), hiddenPaths{
		"@acme/pad/-/pad-1.2.0.tgz": true,
	})

	repos.SetVersionGate(refusedVersions{}.gate)

	limited := read(partial)
	if len(limited.Versions) != 2 || limited.DistTags["latest"] != "1.1.0" || limited.DistTags["beta"] != "1.1.0" ||
		len(limited.Time) != 4 || limited.Time["0.9.0"] != "" {
		t.Errorf("restricted packument = %+v; want 1.0.0 and 1.1.0, their times, created and modified", limited)
	}

	if after := read(t.Context()); len(after.Versions) != 2 || after.DistTags["beta"] != "1.1.0" {
		t.Errorf("unrestricted packument once 1.1.0 is no longer refused = %+v", after)
	}

	if ok, err := svc.Offers(t.Context(), "npmjs", "@acme/pad", "1.2.0"); ok || err != nil {
		t.Errorf("offers a cooling-down version = %v, %v", ok, err)
	}

	tags, err := svc.DistTags(t.Context(), "npmjs", "@acme/pad")
	if err != nil || tags["latest"] != "1.1.0" {
		t.Errorf("dist-tags = %v, %v; want latest at 1.1.0", tags, err)
	}
}
