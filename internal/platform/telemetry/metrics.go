package telemetry

import (
	"cmp"
	"context"
	"slices"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var registry = func() *prometheus.Registry {
	r := prometheus.NewRegistry()
	r.MustRegister(
		collectors.NewGoCollector(
			collectors.WithGoCollectorRuntimeMetrics(collectors.MetricsScheduler, collectors.MetricsGC),
		),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	return r
}()

// Registry is the registry /metrics serves.
func Registry() *prometheus.Registry { return registry }

// Metric describes one relic_* series family.
type Metric struct {
	Name            string
	Type            string
	Help            string
	Labels          []string
	RepositoryLabel bool
}

var (
	catalogMu sync.Mutex
	catalog   []Metric
)

// Catalog returns every relic_* series family, sorted by name.
func Catalog() []Metric {
	catalogMu.Lock()
	defer catalogMu.Unlock()

	out := slices.Clone(catalog)
	slices.SortFunc(out, func(a, b Metric) int {
		return cmp.Compare(a.Name, b.Name)
	})

	return out
}

func describe(m Metric) {
	catalogMu.Lock()
	defer catalogMu.Unlock()

	catalog = append(catalog, m)
}

const namespace = "relic"

func counterVec(name, help string, labels ...string) *prometheus.CounterVec {
	describe(Metric{
		Name:   namespace + "_" + name,
		Type:   "counter",
		Help:   help,
		Labels: labels,
	})
	v := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      name,
		Help:      help,
	}, labels)
	registry.MustRegister(v)

	return v
}

func gaugeVec(name, help string, labels ...string) *prometheus.GaugeVec {
	describe(Metric{
		Name:   namespace + "_" + name,
		Type:   "gauge",
		Help:   help,
		Labels: labels,
	})
	v := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      name,
		Help:      help,
	}, labels)
	registry.MustRegister(v)

	return v
}

func histogramVec(name, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
	describe(Metric{
		Name:   namespace + "_" + name,
		Type:   "histogram",
		Help:   help,
		Labels: labels,
	})
	v := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      name,
			Help:      help,
			Buckets:   buckets,
		},
		labels,
	)
	registry.MustRegister(v)

	return v
}

var (
	// 2 s is the download latency objective (RelicDownloadLatencySLO).
	requestBuckets = []float64{
		.005,
		.01,
		.025,
		.05,
		.1,
		.25,
		.5,
		1,
		2,
		5,
		10,
		30,
		60,
		300,
	}
	sizeBuckets = prometheus.ExponentialBuckets(256, 8, 9)
)

func observe(ctx context.Context, h prometheus.Observer, v float64) {
	if sc := trace.SpanContextFromContext(ctx); sc.IsSampled() {
		if eo, ok := h.(prometheus.ExemplarObserver); ok {
			eo.ObserveWithExemplar(v, prometheus.Labels{
				"trace_id": sc.TraceID().String(),
			})

			return
		}
	}

	h.Observe(v)
}

var buildInfo = gaugeVec(
	"build_info",
	"Relic's version and source revision; always 1.",
	"version",
	"revision",
	"component",
)

var attrComponent = attribute.Key("relic.component")
