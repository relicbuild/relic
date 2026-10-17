package telemetry

import (
	"context"
	"time"
)

var (
	// PromQL: sum by (result) (increase(relic_webhook_deliveries_total[1h]))
	webhookDeliveries = counterVec(
		"webhook_deliveries_total",
		"Webhook delivery attempts by result: delivered, retry (failed, to be retried) or failed (given up).",
		"result",
	)

	// PromQL: histogram_quantile(0.95, sum by (le) (rate(relic_webhook_delivery_duration_seconds_bucket[5m])))
	webhookDuration = histogramVec(
		"webhook_delivery_duration_seconds",
		"Time a webhook's receiver took to answer.",
		requestBuckets,
	)
)

// WebhookDelivered counts a delivery attempt and how long the receiver
// took; result is delivered, retry or failed.
func WebhookDelivered(ctx context.Context, result string, took time.Duration) {
	switch result {
	case "delivered", "retry", "failed":
	default:
		result = Other
	}

	webhookDeliveries.WithLabelValues(result).Inc()
	observe(ctx, webhookDuration.WithLabelValues(), took.Seconds())
}
