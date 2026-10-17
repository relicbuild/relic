package main

const ri = "[$__rate_interval]"

func dashboards() []dashboard {
	return []dashboard{
		overview(),
		packages(),
		storage(),
		scanning(),
		jobs(),
		database(),
		runtime(),
	}
}

func overview() dashboard {
	b := newDashboard(
		"relic-overview",
		"Relic / Overview",
		"Is Relic healthy? Golden signals per API family, saturation and logs.",
		true,
	)
	b.row("Instances")
	b.add(4, 4, stat("Servers up", "none", q(`count(relic_build_info{S, component="server"})`, "")))
	b.add(4, 4, stat("Workers up", "none", q(`count(relic_build_info{S, component="worker"})`, "")))
	b.add(
		8,
		4,
		stat("Version", "none", q(`count by (version, revision) (relic_build_info{S})`, "{{version}} {{revision}}")),
	)
	b.add(8, 4, stat("Requests per second", "reqps", q(`sum(rate(relic_http_requests_total{S}`+ri+`))`, "")))
	b.row("Traffic, errors and latency by API family")
	b.add(
		12,
		8,
		timeseries(
			"Request rate",
			"reqps",
			q(`sum by (family) (rate(relic_http_requests_total{S}`+ri+`))`, "{{family}}"),
		),
	)
	b.add(12, 8, timeseries(
		"5xx ratio",
		"percentunit",
		q(
			`sum by (family) (rate(relic_http_requests_total{S, code="5xx"}`+ri+`)) / sum by (family) (rate(relic_http_requests_total{S}`+ri+`))`,
			"{{family}}",
		),
	))

	for _, p := range []struct {
		title, quantile string
	}{
		{
			"Latency p50",
			"0.5",
		},
		{
			"Latency p95",
			"0.95",
		},
		{
			"Latency p99",
			"0.99",
		},
	} {
		b.add(8, 8, timeseries(
			p.title,
			"s",
			qx(
				`histogram_quantile(`+p.quantile+`, sum by (le, family) (rate(relic_http_request_duration_seconds_bucket{S}`+ri+`)))`,
				"{{family}}",
			),
		))
	}

	b.add(12, 8, timeseries(
		"Slowest routes (p95)",
		"s",
		qx(
			`topk(10, histogram_quantile(0.95, sum by (le, route, method) (rate(relic_http_request_duration_seconds_bucket{S}`+ri+`))))`,
			"{{method}} {{route}}",
		),
	))
	b.add(12, 8, timeseries(
		"4xx by route",
		"reqps",
		q(
			`topk(10, sum by (route, method) (rate(relic_http_requests_total{S, code="4xx"}`+ri+`)))`,
			"{{method}} {{route}}",
		),
	))
	b.row("Relic API and sign-in")
	b.add(12, 8, timeseries(
		"Relic API errors",
		"reqps",
		q(
			`sum by (service, method, code) (rate(relic_rpc_requests_total{S, code!="ok"}`+ri+`))`,
			"{{service}}/{{method}} {{code}}",
		),
	))
	b.add(12, 8, timeseries("Sign-in attempts", "reqps",
		q(`sum by (method, result) (rate(relic_auth_attempts_total{S}`+ri+`))`, "{{method}} {{result}}")))
	b.row("Saturation")
	b.add(
		8,
		8,
		timeseries("Requests in flight", "none", q(`sum by (family) (relic_http_requests_in_flight{S})`, "{{family}}")),
	)
	b.add(8, 8, timeseries("Database pool in use", "percentunit",
		q(`sum by (instance) (relic_db_pool_acquired{S}) / sum by (instance) (relic_db_pool_max{S})`, "{{instance}}")))
	b.add(
		8,
		8,
		timeseries(
			"Oldest waiting job",
			"s",
			q(`max by (kind) (relic_job_oldest_available_age_seconds{S})`, "{{kind}}"),
		),
	)
	b.row("Logs")
	b.d.Templating.List = append(
		b.d.Templating.List,
		variable{
			Name:  "request_id",
			Label: "Request ID",
			Type:  "textbox",
			Query: "",
			Current: map[string]any{
				"text":  "",
				"value": "",
			},
		},
		variable{
			Name:  "trace_id",
			Label: "Trace ID",
			Type:  "textbox",
			Query: "",
			Current: map[string]any{
				"text":  "",
				"value": "",
			},
		},
	)
	b.add(
		24,
		12,
		description(logs("Relic logs", `{app="relic", namespace=~"$namespace"} |~ "$request_id" |~ "$trace_id" | json`),
			"Filter by request ID or trace ID with the variables above; a line's trace_id opens the trace in Tempo."),
	)
	b.add(24, 8, logs("Warnings and errors", `{app="relic", namespace=~"$namespace"} | json | level=~"WARN|ERROR"`))

	return b.d
}

func packages() dashboard {
	b := newDashboard(
		"relic-packages",
		"Relic / Packages and repositories",
		"Uploads, downloads, remote repositories and their upstreams.",
		false,
	)
	b.row("Uploads and downloads")
	b.add(
		12,
		8,
		timeseries(
			"Uploads by format",
			"ops",
			q(`sum by (format) (rate(relic_artifact_uploads_total{S}`+ri+`))`, "{{format}}"),
		),
	)
	b.add(12, 8, timeseries("Downloads by format and source", "ops",
		q(`sum by (format, source) (rate(relic_artifact_downloads_total{S}`+ri+`))`, "{{format}} {{source}}")))
	b.add(
		12,
		8,
		timeseries(
			"Bytes",
			"Bps",
			q(`sum by (direction) (rate(relic_artifact_bytes_total{S}`+ri+`))`, "{{direction}}"),
		),
	)
	b.add(12, 8, timeseries(
		"Checksum deploys (content already stored)",
		"percentunit",
		q(
			`sum(rate(relic_deploy_checksum_hits_total{S}`+ri+`)) / sum(rate(relic_artifact_uploads_total{S}`+ri+`))`,
			"ratio",
		),
	))
	b.add(12, 8, bargauge("Top formats by downloads", "none",
		q(`topk(10, sum by (format) (increase(relic_artifact_downloads_total{S}[$__range])))`, "{{format}}")))
	b.add(
		12,
		8,
		timeseries("Repositories", "none", q(`sum by (format, class) (relic_repositories{S})`, "{{format}} {{class}}")),
	)
	b.row("Remote repositories")
	b.add(12, 8, timeseries(
		"Cache hit ratio",
		"percentunit",
		q(
			`sum by (format) (rate(relic_remote_cache_total{S, result="hit"}`+ri+`)) / sum by (format) (rate(relic_remote_cache_total{S}`+ri+`))`,
			"{{format}}",
		),
	))
	b.add(
		12,
		8,
		timeseries(
			"Cache lookups",
			"ops",
			q(`sum by (result) (rate(relic_remote_cache_total{S}`+ri+`))`, "{{result}}"),
		),
	)
	b.add(12, 8, timeseries(
		"Upstream latency p95",
		"s",
		qx(
			`histogram_quantile(0.95, sum by (le, format) (rate(relic_upstream_duration_seconds_bucket{S}`+ri+`)))`,
			"{{format}}",
		),
	))
	b.add(12, 8, timeseries(
		"Upstream errors",
		"percentunit",
		q(
			`sum by (format) (rate(relic_upstream_requests_total{S, code=~"5xx|error"}`+ri+`)) / sum by (format) (rate(relic_upstream_requests_total{S}`+ri+`))`,
			"{{format}}",
		),
	))
	b.row("Builds and releases")
	b.add(
		8,
		8,
		timeseries(
			"Build info published",
			"ops",
			q(`sum by (result) (rate(relic_builds_published_total{S}`+ri+`))`, "{{result}}"),
		),
	)
	b.add(
		8,
		8,
		timeseries(
			"Release operations",
			"ops",
			q(`sum by (op, result) (rate(relic_releases_total{S}`+ri+`))`, "{{op}} {{result}}"),
		),
	)
	b.add(
		8,
		8,
		timeseries(
			"Promotions",
			"ops",
			q(`sum by (kind, result) (rate(relic_promotions_total{S}`+ri+`))`, "{{kind}} {{result}}"),
		),
	)
	b.add(
		8,
		8,
		timeseries(
			"Promotion gate decisions",
			"ops",
			q(`sum by (decision) (rate(relic_promotion_gate_decisions_total{S}`+ri+`))`, "{{decision}}"),
		),
	)
	b.add(
		8,
		8,
		timeseries(
			"Webhook deliveries",
			"ops",
			q(`sum by (result) (rate(relic_webhook_deliveries_total{S}`+ri+`))`, "{{result}}"),
		),
	)
	b.add(
		8,
		8,
		timeseries(
			"Webhook receiver p95",
			"s",
			q(
				`histogram_quantile(0.95, sum by (le) (rate(relic_webhook_delivery_duration_seconds_bucket{S}`+ri+`)))`,
				"p95",
			),
		),
	)

	return b.d
}

func storage() dashboard {
	b := newDashboard(
		"relic-storage",
		"Relic / Storage and garbage collection",
		"Stored content, object storage operations, garbage collection and the trash can.",
		false,
	)
	b.row("Content")
	b.add(6, 6, stat("Stored", "bytes", q(`max(relic_storage_bytes{S})`, "")))
	b.add(6, 6, stat("Blobs", "none", q(`max(relic_storage_blobs{S})`, "")))
	b.add(6, 6, stat("Unreferenced", "bytes", q(`max(relic_unreferenced_bytes{S})`, "")))
	b.add(6, 6, stat("Trash can entries", "none", q(`max(relic_trash_items{S})`, "")))
	b.add(12, 8, timeseries("Stored and unreferenced bytes", "bytes",
		q(`max(relic_storage_bytes{S})`, "stored"), q(`max(relic_unreferenced_bytes{S})`, "unreferenced")))
	b.add(
		12,
		8,
		timeseries("Users and builds", "none", q(`max(relic_users{S})`, "users"), q(`max(relic_builds{S})`, "builds")),
	)
	b.row("Object storage (S3)")
	b.add(
		12,
		8,
		timeseries(
			"Operations",
			"ops",
			q(`sum by (op, result) (rate(relic_object_storage_ops_total{S}`+ri+`))`, "{{op}} {{result}}"),
		),
	)
	b.add(12, 8, timeseries(
		"Latency p99",
		"s",
		qx(
			`histogram_quantile(0.99, sum by (le, op) (rate(relic_object_storage_op_duration_seconds_bucket{S}`+ri+`)))`,
			"{{op}}",
		),
	))
	b.add(12, 8, timeseries(
		"Error ratio",
		"percentunit",
		q(
			`sum(rate(relic_object_storage_ops_total{S, result="error"}`+ri+`)) / sum(rate(relic_object_storage_ops_total{S}`+ri+`))`,
			"errors",
		),
	))
	b.add(
		12,
		8,
		timeseries("Throughput", "Bps", q(`sum by (op) (rate(relic_object_storage_bytes_total{S}`+ri+`))`, "{{op}}")),
	)
	b.add(12, 8, timeseries(
		"Content integrity failures",
		"none",
		q(`sum by (problem) (increase(relic_blob_integrity_failures_total{S}[1h]))`, "{{problem}}"),
	))
	b.row("Garbage collection and trash")
	b.add(
		8,
		8,
		timeseries("GC runs", "none", q(`sum by (result) (increase(relic_gc_runs_total{S}[1h]))`, "{{result}}")),
	)
	b.add(8, 8, timeseries("Reclaimed", "Bps", q(`sum(rate(relic_gc_deleted_bytes_total{S}`+ri+`))`, "bytes"),
		q(`sum(rate(relic_gc_deleted_blobs_total{S}`+ri+`))`, "blobs")))
	b.add(8, 8, timeseries("Trash purged", "none", q(`sum(increase(relic_trash_purged_total{S}[1h]))`, "entries")))

	return b.d
}

func scanning() dashboard {
	b := newDashboard(
		"relic-scan",
		"Relic / Relic Scan",
		"Scan backlog and throughput, scanner stages, findings, blocked downloads and CVE feeds.",
		false,
	)
	b.row("Backlog and throughput")
	b.add(6, 6, stat("Backlog", "none", q(`max(relic_scan_backlog{S})`, "")))
	b.add(6, 6, stat("Scanner database age", "s", q(`max(relic_scanner_db_age_seconds{S})`, "")))
	b.add(12, 6, stat("CVE feed age", "s", q(`max by (feed) (relic_cve_feed_age_seconds{S})`, "{{feed}}")))
	b.add(12, 8, timeseries("Backlog", "none", q(`max(relic_scan_backlog{S})`, "queued")))
	b.add(
		12,
		8,
		timeseries("Scans by result", "none", q(`sum by (result) (increase(relic_scans_total{S}[1h]))`, "{{result}}")),
	)
	b.add(12, 8, timeseries(
		"Stage duration p95",
		"s",
		qx(
			`histogram_quantile(0.95, sum by (le, stage) (rate(relic_scan_duration_seconds_bucket{S}[1h])))`,
			"{{stage}}",
		),
	))
	b.add(
		12,
		8,
		timeseries(
			"Scan failures",
			"none",
			q(`sum(increase(relic_scans_total{S, result=~"failed|error"}[1h]))`, "failed"),
		),
	)
	b.row("Findings and blocking")
	b.add(12, 8, timeseries("Findings by severity", "none", q(`max by (severity) (relic_findings{S})`, "{{severity}}")))
	b.add(
		12,
		8,
		timeseries(
			"Blocked downloads",
			"none",
			q(`sum by (reason) (increase(relic_downloads_blocked_total{S}[1h]))`, "{{reason}}"),
		),
	)

	return b.d
}

func jobs() dashboard {
	b := newDashboard(
		"relic-jobs",
		"Relic / Jobs and worker",
		"Background jobs on River: throughput, duration, queues, and push replication.",
		false,
	)
	b.row("Jobs")
	b.add(
		12,
		8,
		timeseries(
			"Jobs by kind and result",
			"ops",
			q(`sum by (kind, result) (rate(relic_jobs_total{S}`+ri+`))`, "{{kind}} {{result}}"),
		),
	)
	b.add(12, 8, timeseries(
		"Duration p95",
		"s",
		qx(
			`histogram_quantile(0.95, sum by (le, kind) (rate(relic_job_duration_seconds_bucket{S}[15m])))`,
			"{{kind}}",
		),
	))
	b.add(8, 8, timeseries("Queued", "none", q(`max by (kind) (relic_jobs_queued{S})`, "{{kind}}")))
	b.add(8, 8, timeseries("Retryable", "none", q(`max by (kind) (relic_jobs_retryable{S})`, "{{kind}}")))
	b.add(
		8,
		8,
		timeseries("Oldest waiting", "s", q(`max by (kind) (relic_job_oldest_available_age_seconds{S})`, "{{kind}}")),
	)
	b.row("Push replication")
	b.add(
		8,
		8,
		timeseries("Runs", "none", q(`sum by (result) (increase(relic_replication_runs_total{S}[1h]))`, "{{result}}")),
	)
	b.add(8, 8, timeseries("Run duration p95", "s",
		qx(`histogram_quantile(0.95, sum by (le) (rate(relic_replication_duration_seconds_bucket{S}[1h])))`, "p95")))
	b.add(
		8,
		8,
		timeseries("Files", "ops", q(`sum by (result) (rate(relic_replication_events_total{S}`+ri+`))`, "{{result}}")),
	)
	b.add(
		12,
		8,
		timeseries(
			"Time since last run",
			"s",
			q(`max by (repository) (relic_replication_lag_seconds{S})`, "{{repository}}"),
		),
	)
	b.add(
		12,
		8,
		timeseries(
			"Failures in a row",
			"none",
			q(`max by (repository) (relic_replication_consecutive_failures{S})`, "{{repository}}"),
		),
	)

	return b.d
}

func database() dashboard {
	b := newDashboard(
		"relic-database",
		"Relic / Database",
		"Relic's side of PostgreSQL: the connection pool and query latency by sqlc query. The server side is CloudNativePG's dashboard.",
		false,
	)
	b.link("CloudNativePG", "/d/cloudnative-pg")
	b.row("Connection pool")
	b.add(12, 8, timeseries("Connections", "none",
		q(`sum by (instance) (relic_db_pool_acquired{S})`, "in use {{instance}}"),
		q(`sum by (instance) (relic_db_pool_idle{S})`, "idle {{instance}}"),
		q(`sum by (instance) (relic_db_pool_max{S})`, "max {{instance}}")))
	b.add(12, 8, timeseries(
		"Acquire wait (mean) and empty acquires",
		"s",
		q(
			`sum by (instance) (rate(relic_db_pool_acquire_duration_seconds_total{S}`+ri+`)) / sum by (instance) (rate(relic_db_pool_acquires_total{S}`+ri+`))`,
			"wait {{instance}}",
		),
		q(`sum by (instance) (rate(relic_db_pool_empty_acquire_total{S}`+ri+`))`, "empty acquires/s {{instance}}"),
	))
	b.row("Queries")
	b.add(12, 10, timeseries(
		"Slowest queries (p99)",
		"s",
		qx(
			`topk(10, histogram_quantile(0.99, sum by (le, query) (rate(relic_db_query_duration_seconds_bucket{S}`+ri+`))))`,
			"{{query}}",
		),
	))
	b.add(12, 10, timeseries("Time spent by query", "s",
		q(`topk(10, sum by (query) (rate(relic_db_query_duration_seconds_sum{S}`+ri+`)))`, "{{query}}")))
	b.add(
		24,
		8,
		timeseries(
			"Query rate",
			"ops",
			q(`topk(10, sum by (query) (rate(relic_db_query_duration_seconds_count{S}`+ri+`)))`, "{{query}}"),
		),
	)

	return b.d
}

func runtime() dashboard {
	b := newDashboard(
		"relic-go-runtime",
		"Relic / Go runtime",
		"The Go runtime and process of each Relic instance.",
		false,
	)
	b.row("Process")
	b.add(12, 8, timeseries("CPU", "percentunit", q(`rate(process_cpu_seconds_total{S}`+ri+`)`, "{{instance}}")))
	b.add(12, 8, timeseries("Resident memory", "bytes", q(`process_resident_memory_bytes{S}`, "{{instance}}")))
	b.add(
		12,
		8,
		timeseries(
			"Open file descriptors",
			"none",
			q(`process_open_fds{S}`, "{{instance}}"),
			q(`process_max_fds{S}`, "max {{instance}}"),
		),
	)
	b.add(12, 8, timeseries("Goroutines", "none", q(`go_goroutines{S}`, "{{instance}}")))
	b.row("Memory and GC")
	b.add(
		12,
		8,
		timeseries(
			"Heap",
			"bytes",
			q(`go_memstats_heap_inuse_bytes{S}`, "in use {{instance}}"),
			q(`go_memstats_heap_alloc_bytes{S}`, "allocated {{instance}}"),
		),
	)
	b.add(
		12,
		8,
		timeseries("Allocation rate", "Bps", q(`rate(go_memstats_alloc_bytes_total{S}`+ri+`)`, "{{instance}}")),
	)
	b.add(
		12,
		8,
		timeseries(
			"GC pause p99",
			"s",
			q(
				`histogram_quantile(0.99, sum by (le, instance) (rate(go_gc_pauses_seconds_bucket{S}`+ri+`)))`,
				"{{instance}}",
			),
		),
	)
	b.add(12, 8, timeseries(
		"Scheduler latency p99",
		"s",
		q(
			`histogram_quantile(0.99, sum by (le, instance) (rate(go_sched_latencies_seconds_bucket{S}`+ri+`)))`,
			"{{instance}}",
		),
	))

	return b.d
}
