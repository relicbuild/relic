package telemetry

import (
	"context"
	"slices"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// ObjectStorageOps are the object storage operations measured.
var ObjectStorageOps = []string{
	"put",
	"get",
	"head",
	"copy",
	"delete",
	"presign",
	"list",
	"sweep",
}

var (
	// PromQL: sum(rate(relic_object_storage_ops_total{result="error"}[10m])) / sum(rate(relic_object_storage_ops_total[10m]))
	//   (alert RelicStorageErrors > 0.01)
	objectStorageOps = counterVec(
		"object_storage_ops_total",
		"Object storage operations by operation and result (success, miss for a key that does not exist, error).",
		"op",
		"result",
	)

	// PromQL: histogram_quantile(0.99, sum by (le, op) (rate(relic_object_storage_op_duration_seconds_bucket[5m])))
	objectStorageDuration = histogramVec(
		"object_storage_op_duration_seconds",
		"Object storage operation duration (for get, to the start of the body).",
		requestBuckets,
		"op",
	)

	// PromQL: sum by (op) (rate(relic_object_storage_bytes_total[5m]))
	objectStorageBytes = counterVec(
		"object_storage_bytes_total",
		"Bytes written to (put) and read from (get) object storage.",
		"op",
	)
)

// The problems a stored blob can have (relic_blob_integrity_failures_total).
const (
	BlobMissing = "missing"
	BlobCorrupt = "corrupt"
)

// PromQL: sum by (problem) (increase(relic_blob_integrity_failures_total[15m]))   (alert RelicContentIntegrity > 0)
var blobIntegrityFailures = counterVec(
	"blob_integrity_failures_total",
	"Reads that found a blob missing from object storage, or stored content that does not match its digest or size.",
	"problem",
)

// BlobIntegrityFailure counts a missing or corrupt blob.
func BlobIntegrityFailure(problem string) {
	if problem != BlobMissing && problem != BlobCorrupt {
		problem = Other
	}

	blobIntegrityFailures.WithLabelValues(problem).Inc()
}

// What a storage check finds (relic_storage_check_findings_total).
const (
	FindingAdopted   = "adopted"
	FindingMissing   = "missing"
	FindingWrongSize = "wrong_size"
	FindingCorrupt   = "corrupt"
)

var (
	// PromQL: sum by (finding) (increase(relic_storage_check_findings_total[1d]))
	storageCheckFindings = counterVec(
		"storage_check_findings_total",
		"What storage checks found: objects no blob row named (adopted for garbage collection), and blobs missing, of the wrong size or corrupt.",
		"finding",
	)
	// PromQL: sum(increase(relic_storage_check_verified_bytes_total[1d]))
	storageCheckVerifiedBytes = counterVec(
		"storage_check_verified_bytes_total",
		"Bytes of stored content storage checks re-hashed.",
	)
	// PromQL: sum by (result) (increase(relic_storage_check_runs_total[1d]))
	storageCheckRuns = counterVec(
		"storage_check_runs_total",
		"Storage checks by result.",
		"result",
	)
)

// StorageCheckFinding counts one finding of a storage check.
func StorageCheckFinding(finding string) {
	switch finding {
	case FindingAdopted, FindingMissing, FindingWrongSize, FindingCorrupt:
	default:
		finding = Other
	}

	storageCheckFindings.WithLabelValues(finding).Inc()
}

// StorageCheckVerified counts re-hashed bytes.
func StorageCheckVerified(n int64) { storageCheckVerifiedBytes.WithLabelValues().Add(float64(n)) }

// StorageCheckRun counts a finished storage check.
func StorageCheckRun(err error) { storageCheckRuns.WithLabelValues(Result(err)).Inc() }

var (
	// PromQL: sum by (result) (increase(relic_cleanup_runs_total[1d]))
	cleanupRuns = counterVec("cleanup_runs_total", "Cleanup policy runs by result.", "result")
	// PromQL: sum(increase(relic_cleanup_moved_total[1d]))
	cleanupMoved = counterVec("cleanup_moved_total", "Items cleanup policies moved to the trash.")
)

// CleanupRun counts a finished cleanup policy run.
func CleanupRun(err error) { cleanupRuns.WithLabelValues(Result(err)).Inc() }

// CleanupMoved counts items a cleanup policy moved to the trash.
func CleanupMoved(n int) { cleanupMoved.WithLabelValues().Add(float64(n)) }

// StartObjectStorage opens a client span for an object storage operation.
func StartObjectStorage(ctx context.Context, op, area string) (context.Context, func(res string)) {
	if !slices.Contains(ObjectStorageOps, op) {
		op = Other
	}

	ctx, span := Tracer().Start(ctx, "object storage "+op, trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("object_storage.operation", op), attribute.String("object_storage.area", area)),
	)
	start := time.Now()

	return ctx, func(res string) {
		if res == "error" {
			span.SetStatus(codes.Error, "object storage "+op+" failed")
		}

		span.End()
		objectStorageOps.WithLabelValues(op, result(res)).Inc()
		observe(ctx, objectStorageDuration.WithLabelValues(op), time.Since(start).Seconds())
	}
}

// ObjectStorageBytes counts bytes moved by a put or get.
func ObjectStorageBytes(op string, n int64) {
	if op != "put" && op != "get" {
		return
	}

	objectStorageBytes.WithLabelValues(op).Add(float64(n))
}
