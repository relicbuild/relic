package telemetry

import (
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

var junk = []string{
	"",
	" ",
	"../../etc/passwd",
	"sha256:0123456789abcdef",
	"alice@example.com",
	"10.0.0.1",
	"GET /x",
	strings.Repeat("a", 300),
	"NPM",
	"Local",
	"\x00",
	"{repo}",
	"other",
}

func TestLabelFunctionsAreBounded(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		f       func(string) string
		allowed []string
	}{
		{
			"Family",
			Family,
			Families,
		},
		{
			"Method",
			Method,
			append(slices.Clone(Methods), "OTHER"),
		},
		{
			"Format",
			Format,
			append(slices.Clone(Formats), Other),
		},
		{
			"Class",
			Class,
			append(slices.Clone(Classes), Other),
		},
		{
			"result",
			result,
			append(slices.Clone(Results), Other),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for _, in := range append(slices.Clone(junk), tt.allowed...) {
				if got := tt.f(in); !slices.Contains(tt.allowed, got) {
					t.Errorf("%s(%q) = %q, outside the allowed values", tt.name, in, got)
				}
			}
		})
	}
}

func TestFamily(t *testing.T) {
	t.Parallel()
	RegisterFamily("/pkg/", "packages")
	RegisterFamily("/pkg/api/registry/", "container-registry")
	RegisterFamily("/registry/", "container-registry")
	RegisterFamily("/odd/", "not-a-family")

	for path, want := range map[string]string{
		"/api/relic.v1.RepositoryService/ListRepositories": "relic",
		"/api/v1/files/libs/a.txt":                         "relic",
		"/pkg/libs/a.txt":                                  "packages",
		"/pkg/api/registry/v2/":                            "container-registry",
		"/registry/":                                       "container-registry",
		"/registry":                                        "container-registry",
		"/odd/x":                                           Other,
		"/healthz":                                         "health",
		"/readyz":                                          "health",
		"/":                                                "ui",
		"/repositories/libs":                               "ui",
		"/wp-login.php":                                    "ui",
	} {
		if got := Family(path); got != want {
			t.Errorf("Family(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestRouteAndStatusClass(t *testing.T) {
	t.Parallel()

	for pattern, want := range map[string]string{
		"GET /api/v1/files/{repo}/{path...}": "/api/v1/files/{repo}/{path...}",
		"/api/":                              "/api/",
		"":                                   Other,
		"PUT /packages/{repo}/{path...}":     "/packages/{repo}/{path...}",
	} {
		if got := Route(pattern); got != want {
			t.Errorf("Route(%q) = %q, want %q", pattern, got, want)
		}
	}

	for code, want := range map[int]string{
		200: "2xx",
		204: "2xx",
		302: "3xx",
		404: "4xx",
		503: "5xx",
		0:   "5xx",
		999: "5xx",
		101: "1xx",
	} {
		if got := StatusClass(code); got != want {
			t.Errorf("StatusClass(%d) = %q, want %q", code, got, want)
		}
	}
}

func labelNames(t *testing.T, reg *prometheus.Registry, name string) [][]string {
	t.Helper()

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}

	var out [][]string

	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}

		for _, m := range mf.GetMetric() {
			out = append(out, names(m))
		}
	}

	return out
}

func names(m *dto.Metric) []string {
	out := make([]string, 0, len(m.GetLabel()))
	for _, l := range m.GetLabel() {
		out = append(out, l.GetName())
	}

	return out
}

func TestRepositoryLabel(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	f := newRepoFamily(reg, []string{
		"format",
	}, func(l []string) *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "t_total",
			Help: "t",
		}, l)
	})
	inc := func() {
		v, lvs := f.current("libs-local", []string{
			"npm",
		})
		v.WithLabelValues(lvs...).Inc()
	}
	inc()

	if got := labelNames(t, reg, "t_total"); len(got) != 1 || slices.Contains(got[0], "repository") {
		t.Fatalf("off: labels %v", got)
	}

	f.set(true)
	inc()

	if got := labelNames(t, reg, "t_total"); len(got) != 1 || !slices.Equal(got[0], []string{
		"format",
		"repository",
	}) {
		t.Fatalf("on: labels %v", got)
	}

	f.set(false)
	inc()

	if got := labelNames(t, reg, "t_total"); len(got) != 1 || slices.Contains(got[0], "repository") {
		t.Fatalf("off again: labels %v", got)
	}
}

func TestCatalogNamesAreRelic(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for _, m := range Catalog() {
		if !strings.HasPrefix(m.Name, "relic_") || seen[m.Name] || m.Help == "" {
			t.Errorf("catalog entry %+v: not relic_*, duplicated or without help", m)
		}

		if m.Type == "counter" && !strings.HasSuffix(m.Name, "_total") {
			t.Errorf("counter %s does not end in _total", m.Name)
		}

		seen[m.Name] = true
		for _, l := range m.Labels {
			if slices.Contains([]string{
				"user",
				"path",
				"digest",
				"token",
				"ip",
				"sha256",
			}, l) {
				t.Errorf("%s has the unbounded label %q", m.Name, l)
			}
		}
	}
}
