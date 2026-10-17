package telemetry

import (
	"context"
	"slices"

	"go.opentelemetry.io/otel/trace"
)

var (
	// PromQL: sum by (result) (increase(relic_builds_published_total[1h]))
	buildsPublished = counterVec("builds_published_total", "Build info documents published, by result.", "result")

	// PromQL: sum by (op, result) (increase(relic_releases_total[1h]))
	releases = counterVec(
		"releases_total",
		"Release (release bundle) operations by operation (create, update, finalize, delete, export, import) and result.",
		"op",
		"result",
	)

	// PromQL: sum by (kind, result) (increase(relic_promotions_total[1h]))
	promotions = counterVec(
		"promotions_total",
		"Build and release promotions by kind (build, release) and result.",
		"kind",
		"result",
	)

	// PromQL: sum by (decision) (increase(relic_promotion_gate_decisions_total[1h]))
	gateDecisions = counterVec(
		"promotion_gate_decisions_total",
		"Promotion gate decisions by decision (allowed, refused).",
		"decision",
	)
)

var releaseOps = []string{
	"create",
	"update",
	"finalize",
	"delete",
	"export",
	"import",
}

// Operation opens a span named name for a lifecycle operation.
func Operation(ctx context.Context, name string, count func(error)) (context.Context, func(error)) {
	ctx, span := Tracer().Start(ctx, name, trace.WithSpanKind(trace.SpanKindInternal))

	return ctx, func(err error) {
		count(err)
		End(span, err)
	}
}

// BuildPublished counts a build info publish.
func BuildPublished(err error) { buildsPublished.WithLabelValues(Result(err)).Inc() }

// ReleaseOp counts a release operation.
func ReleaseOp(op string, err error) {
	if !slices.Contains(releaseOps, op) {
		op = Other
	}

	releases.WithLabelValues(op, Result(err)).Inc()
}

// Promoted counts a promotion; kind is "build" or "release".
func Promoted(kind string, err error) {
	if kind != "build" {
		kind = "release"
	}

	promotions.WithLabelValues(kind, Result(err)).Inc()
}

// GateDecided counts a promotion gate's decision.
func GateDecided(allowed bool) {
	decision := "refused"
	if allowed {
		decision = "allowed"
	}

	gateDecisions.WithLabelValues(decision).Inc()
}
