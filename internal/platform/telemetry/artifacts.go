package telemetry

import (
	"context"
	"slices"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Sources of a download: a local repository, a remote's cache, or the
// remote's upstream (fetched for this request).
var Sources = []string{
	"local",
	"cache",
	"upstream",
}

var (
	// PromQL: sum by (format) (rate(relic_artifact_uploads_total[5m]))
	artifactUploads = repoCounterVec(
		"artifact_uploads_total",
		"Files deployed into local repositories, by format and repository class.",
		"format",
		"class",
	)

	// PromQL: sum by (format, source) (rate(relic_artifact_downloads_total[5m]))
	// PromQL: sum(rate(relic_artifact_downloads_total{source="cache"}[5m]))
	//   / sum(rate(relic_artifact_downloads_total{class="remote"}[5m]))   (remote cache hit ratio)
	artifactDownloads = repoCounterVec(
		"artifact_downloads_total",
		"Full file downloads by format, class of the repository that served them, and source (local, cache, upstream).",
		"format",
		"class",
		"source",
	)

	// PromQL: sum by (direction, format) (rate(relic_artifact_bytes_total[5m]))
	artifactBytes = repoCounterVec(
		"artifact_bytes_total",
		"File bytes deployed (direction up) and downloaded (down), by format.",
		"direction",
		"format",
	)

	// PromQL: sum(rate(relic_deploy_checksum_hits_total[5m])) / sum(rate(relic_artifact_uploads_total[5m]))
	deployChecksumHits = counterVec(
		"deploy_checksum_hits_total",
		"Deploys satisfied by content Relic already stored (checksum deploys), by format.",
		"format",
	)

	// PromQL: histogram_quantile(0.95, sum by (le) (rate(relic_move_batch_duration_seconds_bucket[5m])))
	moveBatchDuration = histogramVec(
		"move_batch_duration_seconds",
		"Duration of each transaction that moves part of a large folder (a delete into the trash can).",
		requestBuckets,
	)

	// PromQL: sum(increase(relic_item_downloads_dropped_total[1h]))
	itemDownloadsDropped = counterVec(
		"item_downloads_dropped_total",
		"Downloads left out of the per-item download counts because too many items were waiting for a database write.",
	)

	// PromQL: sum(rate(relic_move_batch_items_total[5m]))
	moveBatchItems = counterVec(
		"move_batch_items_total",
		"Items moved by the transactions that move part of a large folder.",
	)
)

// MoveBatch records one transaction of a large folder's move: the items it
// moved and how long it took.
func MoveBatch(ctx context.Context, items int, elapsed time.Duration) {
	moveBatchItems.WithLabelValues().Add(float64(items))
	observe(ctx, moveBatchDuration.WithLabelValues(), elapsed.Seconds())
}

// ItemDownloadsDropped counts downloads left out of the per-item download
// counts.
func ItemDownloadsDropped(downloads int64) {
	itemDownloadsDropped.WithLabelValues().Add(float64(downloads))
}

// Tracer is the tracer of Relic's own spans.
func Tracer() trace.Tracer { return otel.Tracer(tracerName) }

// Span attribute keys Relic's own spans share.
var (
	AttrRepository = attribute.Key("relic.repository")
	AttrFormat     = attribute.Key("relic.format")
	AttrClass      = attribute.Key("relic.class")
	AttrSource     = attribute.Key("relic.source")
)

// End ends span, marking it failed when err is not nil.
func End(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "failed")
	}

	span.End()
}

// ArtifactUploaded counts a deploy of size bytes; checksum is true when the
// content was already stored.
func ArtifactUploaded(repo, format, class string, size int64, checksum bool) {
	format, class = Format(format), Class(class)
	v, lvs := artifactUploads.current(repo, []string{
		format,
		class,
	})
	v.WithLabelValues(lvs...).Inc()
	b, lvs := artifactBytes.current(repo, []string{
		"up",
		format,
	})
	b.WithLabelValues(lvs...).Add(float64(size))

	if checksum {
		deployChecksumHits.WithLabelValues(format).Inc()
	}
}

// ArtifactDownloaded counts a full download and notes it on the request's
// span.
func ArtifactDownloaded(ctx context.Context, repo, format, class, source string, size int64) {
	format, class = Format(format), Class(class)

	if !slices.Contains(Sources, source) {
		source = "local"
	}

	v, lvs := artifactDownloads.current(repo, []string{
		format,
		class,
		source,
	})
	v.WithLabelValues(lvs...).Inc()
	b, lvs := artifactBytes.current(repo, []string{
		"down",
		format,
	})
	b.WithLabelValues(lvs...).Add(float64(size))
	trace.SpanFromContext(ctx).
		SetAttributes(AttrRepository.String(repo), AttrFormat.String(format), AttrSource.String(source))
}
