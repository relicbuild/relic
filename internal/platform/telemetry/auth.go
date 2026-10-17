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

// AuthMethods are the values of the method label on auth attempts.
var AuthMethods = []string{
	"basic",
	"bearer",
	"cookie",
	"ldap",
	"oidc",
	"saml",
	"workload",
}

var (
	// PromQL: sum by (method) (rate(relic_auth_attempts_total{result="failure"}[5m]))
	authAttempts = counterVec(
		"auth_attempts_total",
		"Authentication attempts by credential method and result (success, failure, throttled, error).",
		"method",
		"result",
	)

	// PromQL: sum by (kind) (increase(relic_tokens_issued_total[1h]))
	tokensIssued = counterVec(
		"tokens_issued_total",
		"Access tokens issued: new ones (access) and exchanges of a refresh token (refresh).",
		"kind",
	)

	// PromQL: sum by (outcome) (increase(relic_audit_events_total[1h]))
	auditEvents = counterVec(
		"audit_events_total",
		"Audit events by outcome (success, denied, failure), and whether recording them failed (result).",
		"outcome",
		"result",
	)

	// PromQL: histogram_quantile(0.95, sum by (le, directory_op) (rate(relic_ldap_request_duration_seconds_bucket[5m])))
	ldapDuration = histogramVec(
		"ldap_request_duration_seconds",
		"LDAP operations (dial, bind, search) and their duration.",
		requestBuckets,
		"directory_op",
		"result",
	)
)

// AuthAttempt counts one authentication attempt.
func AuthAttempt(method, res string) {
	if !slices.Contains(AuthMethods, method) {
		method = Other
	}

	authAttempts.WithLabelValues(method, result(res)).Inc()
}

// TokenIssued counts an issued token; kind is "access" or "refresh".
func TokenIssued(kind string) {
	if kind != "refresh" {
		kind = "access"
	}

	tokensIssued.WithLabelValues(kind).Inc()
}

// AuditOutcomes are the values of the outcome label on audit events.
var AuditOutcomes = []string{
	"success",
	"denied",
	"failure",
}

// AuditEvent counts one audit event and whether recording it failed.
func AuditEvent(outcome string, err error) {
	if !slices.Contains(AuditOutcomes, outcome) {
		outcome = Other
	}

	auditEvents.WithLabelValues(outcome, Result(err)).Inc()
}

var ldapOps = []string{
	"dial",
	"bind",
	"search",
}

// StartLDAP opens a client span for one directory operation (dial, bind or
// search).
func StartLDAP(ctx context.Context, op string) (end func(result string)) {
	if !slices.Contains(ldapOps, op) {
		op = Other
	}

	ctx, span := otel.Tracer(tracerName).Start(ctx, "ldap "+op,
		trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attribute.String("ldap.operation", op)))
	start := time.Now()

	return func(res string) {
		if res == "error" {
			span.SetStatus(codes.Error, "ldap "+op+" failed")
		}

		span.End()
		observe(ctx, ldapDuration.WithLabelValues(op, result(res)), time.Since(start).Seconds())
	}
}
