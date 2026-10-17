package telemetry

import (
	"context"
	"database/sql"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// PromQL: histogram_quantile(0.99, sum by (le, query) (rate(relic_db_query_duration_seconds_bucket[5m])))
// PromQL: topk(10, sum by (query) (rate(relic_db_query_duration_seconds_sum[5m])))   (where the database time goes)
var dbQueryDuration = histogramVec("db_query_duration_seconds",
	"Database query duration by sqlc query name (other for dynamic and migration SQL).", requestBuckets, "query")

// PromQL: sum by (reason) (rate(relic_db_transaction_retries_total[5m]))   (lock contention)
var dbTransactionRetries = counterVec("db_transaction_retries_total",
	"Transactions run again after a deadlock, a serialization failure, a busy SQLite database or a conflict.",
	"reason")

// The reasons a transaction is retried (relic_db_transaction_retries_total).
const (
	RetryDeadlock      = "deadlock"
	RetrySerialization = "serialization"
	RetryBusy          = "busy"
	RetryConflict      = "conflict"
)

// DBTransactionRetried counts one retried transaction.
func DBTransactionRetried(reason string) {
	switch reason {
	case RetryDeadlock, RetrySerialization, RetryBusy, RetryConflict:
	default:
		reason = Other
	}

	dbTransactionRetries.WithLabelValues(reason).Inc()
}

var queryName = regexp.MustCompile(`^\s*-- name: ([A-Za-z0-9_]{1,64}) :`)

// QueryName is the sqlc name of a statement, or "other".
func QueryName(sql string) string {
	if m := queryName.FindStringSubmatch(sql); m != nil {
		return m[1]
	}

	return Other
}

// DBTracer measures every query (relic_db_query_duration_seconds) and gives
// it an otelpgx span when it runs inside a trace.
func DBTracer() pgx.QueryTracer {
	return dbTracer{
		spans: otelpgx.NewTracer(
			otelpgx.WithSpanNameFunc(func(stmt string) string {
				return "db " + QueryName(stmt)
			}),
		),
	}
}

type dbTracer struct {
	spans *otelpgx.Tracer
}

type dbStartKey struct{}

type dbStart struct {
	at     time.Time
	name   string
	traced bool
}

func (t dbTracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	s := dbStart{
		at:   time.Now(),
		name: QueryName(data.SQL),
	}
	if trace.SpanContextFromContext(ctx).IsValid() {
		ctx = t.spans.TraceQueryStart(ctx, conn, data)
		s.traced = true
	}

	return context.WithValue(ctx, dbStartKey{}, s)
}

func (t dbTracer) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	s, ok := ctx.Value(dbStartKey{}).(dbStart)
	if !ok {
		return
	}

	if s.traced {
		t.spans.TraceQueryEnd(ctx, conn, data)
	}

	observe(ctx, dbQueryDuration.WithLabelValues(s.name), time.Since(s.at).Seconds())
}

// ObserveQuery measures one statement run outside pgx (SQLite, through
// database/sql) like DBTracer measures pgx's.
func ObserveQuery(ctx context.Context, system, sql string) (context.Context, func(error)) {
	name := QueryName(sql)
	start := time.Now()

	var span trace.Span
	if trace.SpanContextFromContext(ctx).IsValid() {
		ctx, span = Tracer().Start(
			ctx,
			"db "+name,
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(attribute.String("db.system.name", system)),
		)
	}

	return ctx, func(err error) {
		if span != nil {
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, "query failed")
			}

			span.End()
		}

		observe(ctx, dbQueryDuration.WithLabelValues(name), time.Since(start).Seconds())
	}
}

type poolStat struct {
	acquired, idle, max, acquires, emptyAcquires float64
	acquireDuration                              time.Duration
}

func pgxPoolStat(p *pgxpool.Pool) poolStat {
	s := p.Stat()

	return poolStat{
		acquired:        float64(s.AcquiredConns()),
		idle:            float64(s.IdleConns()),
		max:             float64(s.MaxConns()),
		acquires:        float64(s.AcquireCount()),
		emptyAcquires:   float64(s.EmptyAcquireCount()),
		acquireDuration: s.AcquireDuration(),
	}
}

func sqlDBStat(db *sql.DB, acquires *atomicCounter) poolStat {
	s := db.Stats()

	return poolStat{
		acquired:        float64(s.InUse),
		idle:            float64(s.Idle),
		max:             float64(s.MaxOpenConnections),
		acquires:        float64(acquires.load()),
		emptyAcquires:   float64(s.WaitCount),
		acquireDuration: s.WaitDuration,
	}
}

var (
	poolsMu sync.Mutex
	pools   = map[any]func() poolStat{}

	poolDescs = []struct {
		desc  *prometheus.Desc
		kind  prometheus.ValueType
		value func(poolStat) float64
	}{
		// PromQL: sum(relic_db_pool_acquired) / sum(relic_db_pool_max)   (saturation)
		{
			poolDesc("db_pool_acquired", "gauge", "Connections in use."),
			prometheus.GaugeValue,
			func(s poolStat) float64 {
				return s.acquired
			},
		},
		{
			poolDesc("db_pool_idle", "gauge", "Idle connections."),
			prometheus.GaugeValue,
			func(s poolStat) float64 {
				return s.idle
			},
		},
		{
			poolDesc("db_pool_max", "gauge", "The pool's maximum size."),
			prometheus.GaugeValue,
			func(s poolStat) float64 {
				return s.max
			},
		},
		{
			poolDesc("db_pool_acquires_total", "counter", "Connections acquired from the pool."),
			prometheus.CounterValue,
			func(s poolStat) float64 {
				return s.acquires
			},
		},
		// PromQL: rate(relic_db_pool_acquire_duration_seconds_total[5m]) / rate(relic_db_pool_acquires_total[5m])   (mean wait)
		{
			poolDesc("db_pool_acquire_duration_seconds_total", "counter", "Time spent acquiring connections."),
			prometheus.CounterValue,
			func(s poolStat) float64 {
				return s.acquireDuration.Seconds()
			},
		},
		// PromQL: rate(relic_db_pool_empty_acquire_total[5m]) > 0 and sum(relic_db_pool_acquired) >= sum(relic_db_pool_max)
		//   (alert RelicDBPoolExhausted)
		{
			poolDesc(
				"db_pool_empty_acquire_total",
				"counter",
				"Acquires that had to wait because no connection was idle.",
			),
			prometheus.CounterValue,
			func(s poolStat) float64 {
				return s.emptyAcquires
			},
		},
	}
)

func poolDesc(name, kind, help string) *prometheus.Desc {
	describe(Metric{
		Name: namespace + "_" + name,
		Type: kind,
		Help: help,
	})

	return prometheus.NewDesc(namespace+"_"+name, help, nil, nil)
}

type poolCollector struct{}

func (poolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range poolDescs {
		ch <- d.desc
	}
}

func (poolCollector) Collect(ch chan<- prometheus.Metric) {
	poolsMu.Lock()

	stats := make([]poolStat, 0, len(pools))
	for _, stat := range pools {
		stats = append(stats, stat())
	}
	poolsMu.Unlock()

	for _, d := range poolDescs {
		var v float64
		for _, s := range stats {
			v += d.value(s)
		}

		ch <- prometheus.MustNewConstMetric(d.desc, d.kind, v)
	}
}

func init() { registry.MustRegister(poolCollector{}) }

// RegisterPool adds p to the pool series until the returned function runs.
func RegisterPool(p *pgxpool.Pool) (unregister func()) {
	return register(p, func() poolStat {
		return pgxPoolStat(p)
	})
}

// RegisterSQLDB adds a database/sql pool to the pool series until the
// returned unregister function runs.
func RegisterSQLDB(db *sql.DB) (acquired func(), unregister func()) {
	var n atomicCounter

	return n.add, register(db, func() poolStat {
		return sqlDBStat(db, &n)
	})
}

func register(key any, stat func() poolStat) func() {
	poolsMu.Lock()
	defer poolsMu.Unlock()

	pools[key] = stat

	return func() {
		poolsMu.Lock()
		defer poolsMu.Unlock()

		delete(pools, key)
	}
}

type atomicCounter struct {
	n atomic.Int64
}

func (c *atomicCounter) add() { c.n.Add(1) }

func (c *atomicCounter) load() int64 { return c.n.Load() }
