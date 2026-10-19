package telemetry

import (
	"slices"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Other is the value an unknown label input becomes.
const Other = "other"

// Families are the API families of the HTTP series.
var Families = []string{
	"relic",
	"packages",
	"container-registry",
	"scanning",
	"access",
	"releases",
	"evidence",
	"ui",
	"health",
}

var familyPrefixes = struct {
	sync.RWMutex
	m map[string]string
}{
	m: map[string]string{
		"/api/": "relic",
	},
}

// RegisterFamily counts requests under prefix toward family, one of
// Families; an unknown family counts as "other".
func RegisterFamily(prefix, family string) {
	if !slices.Contains(Families, family) {
		family = Other
	}

	familyPrefixes.Lock()
	defer familyPrefixes.Unlock()

	familyPrefixes.m[prefix] = family
}

// Family is the API family of a request path.
func Family(path string) string {
	switch path {
	case "/healthz", "/readyz":
		return "health"
	}

	familyPrefixes.RLock()
	defer familyPrefixes.RUnlock()

	best, family := "", "ui"
	for prefix, f := range familyPrefixes.m {
		if (strings.HasPrefix(path, prefix) || path == strings.TrimSuffix(prefix, "/")) && len(prefix) > len(best) {
			best, family = prefix, f
		}
	}

	return family
}

// Methods are the HTTP methods kept as label values; others become OTHER.
var Methods = []string{
	"GET",
	"HEAD",
	"POST",
	"PUT",
	"PATCH",
	"DELETE",
	"OPTIONS",
}

// Method bounds an HTTP method.
func Method(m string) string {
	if slices.Contains(Methods, m) {
		return m
	}

	return "OTHER"
}

// StatusClasses are the values of the code label on HTTP series.
var StatusClasses = []string{
	"1xx",
	"2xx",
	"3xx",
	"4xx",
	"5xx",
}

// StatusClass is "2xx" for 200 and so on; out-of-range codes count as 5xx.
func StatusClass(code int) string {
	if code < 100 || code > 599 {
		return "5xx"
	}

	return StatusClasses[code/100-1]
}

// Route is the path of a router pattern without its method
// ("GET /api/v1/files/{repo}/{path...}" becomes
// "/api/v1/files/{repo}/{path...}").
func Route(pattern string) string {
	if pattern == "" {
		return Other
	}

	if m, rest, ok := strings.Cut(pattern, " "); ok && slices.Contains(Methods, m) {
		return rest
	}

	return pattern
}

// Formats are the repository formats (package types) kept as label values.
var Formats = []string{
	"alpine",
	"ansible",
	"bower",
	"cargo",
	"chef",
	"cocoapods",
	"composer",
	"conan",
	"conda",
	"cran",
	"debian",
	"docker",
	"gems",
	"generic",
	"gitlfs",
	"go",
	"gradle",
	"helm",
	"helmoci",
	"huggingfaceml",
	"ivy",
	"maven",
	"nix",
	"npm",
	"nuget",
	"oci",
	"opkg",
	"pub",
	"puppet",
	"pypi",
	"rpm",
	"sbt",
	"swift",
	"terraform",
	"terraformbackend",
	"vagrant",
}

// Format bounds a repository format.
func Format(f string) string {
	f = strings.ToLower(f)
	if slices.Contains(Formats, f) {
		return f
	}

	return Other
}

// Classes are the repository classes kept as label values.
var Classes = []string{
	"local",
	"remote",
	"virtual",
	"federated",
}

// Class bounds a repository class.
func Class(c string) string {
	c = strings.ToLower(c)
	if slices.Contains(Classes, c) {
		return c
	}

	return Other
}

// Results are the outcomes on result labels.
var Results = []string{
	"success",
	"failure",
	"error",
	"throttled",
	"hit",
	"miss",
	"stale",
	"absent",
	"skipped",
	"uncached",
}

func result(r string) string {
	if slices.Contains(Results, r) {
		return r
	}

	return Other
}

// Result is "success" for a nil error and "error" otherwise.
func Result(err error) string {
	if err == nil {
		return "success"
	}

	return "error"
}

type repoFamily[V prometheus.Collector] struct {
	build  func(labels []string) V
	labels []string

	mu       sync.RWMutex
	vec      V
	withRepo bool
}

const repoLabelName = "repository"

func newRepoFamily[V prometheus.Collector](
	reg prometheus.Registerer,
	labels []string,
	build func([]string) V,
) *repoFamily[V] {
	f := &repoFamily[V]{
		build:  build,
		labels: labels,
	}
	f.vec = build(labels)
	reg.MustRegister(f)

	return f
}

func (f *repoFamily[V]) Describe(chan<- *prometheus.Desc) {}

func (f *repoFamily[V]) Collect(ch chan<- prometheus.Metric) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	f.vec.Collect(ch)
}

func (f *repoFamily[V]) set(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.withRepo == on {
		return
	}

	labels := f.labels
	if on {
		labels = append(slices.Clone(labels), repoLabelName)
	}

	f.vec = f.build(labels)
	f.withRepo = on
}

func (f *repoFamily[V]) current(repo string, lvs []string) (V, []string) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if f.withRepo {
		lvs = append(slices.Clone(lvs), repo)
	}

	return f.vec, lvs
}

var (
	repoFamiliesMu sync.Mutex
	repoFamilies   []interface{ set(bool) }
)

func repoCounterVec(name, help string, labels ...string) *repoFamily[*prometheus.CounterVec] {
	describe(Metric{
		Name:            namespace + "_" + name,
		Type:            "counter",
		Help:            help,
		Labels:          labels,
		RepositoryLabel: true,
	})
	f := newRepoFamily(registry, labels, func(l []string) *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      name,
			Help:      help,
		}, l)
	})

	repoFamiliesMu.Lock()
	defer repoFamiliesMu.Unlock()

	repoFamilies = append(repoFamilies, f)

	return f
}

// SetRepositoryLabel switches the repository label on or off for every
// series that supports it.
func SetRepositoryLabel(on bool) {
	repoFamiliesMu.Lock()
	defer repoFamiliesMu.Unlock()

	for _, f := range repoFamilies {
		f.set(on)
	}
}
