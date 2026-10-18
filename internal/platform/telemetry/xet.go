package telemetry

import (
	"context"
	"time"
)

// XetObjects are the values of the object label of the Xet metrics.
var XetObjects = []string{
	"xorb",
	"shard",
}

var xetUploads = counterVec(
	"huggingface_xet_uploads_total",
	"Xet objects Hugging Face clients uploaded, by object (xorb, shard) and result (success, error).",
	"object",
	"result",
)

var xetUploadBytes = counterVec(
	"huggingface_xet_upload_bytes_total",
	"Bytes of the Xet objects Hugging Face clients uploaded and Relic stored, by object (xorb, shard).",
	"object",
)

// PromQL: histogram_quantile(0.95, sum by (le, result) (rate(relic_huggingface_xet_shard_duration_seconds_bucket[5m])))
var xetShardDuration = histogramVec(
	"huggingface_xet_shard_duration_seconds",
	"Time to register a Xet shard, rebuilding the files it lists, by result (success, error).",
	requestBuckets,
	"result",
)

var xetRebuiltBytes = counterVec(
	"huggingface_xet_rebuilt_bytes_total",
	"Bytes of the files Relic rebuilt from Xet xorbs into repository items.",
)

func xetObject(object string) string {
	for _, o := range XetObjects {
		if o == object {
			return o
		}
	}

	return Other
}

// XetUploaded counts a Xet object a client uploaded, with the bytes stored.
func XetUploaded(object string, size int64, err error) {
	xetUploads.WithLabelValues(xetObject(object), Result(err)).Inc()

	if err == nil && size > 0 {
		xetUploadBytes.WithLabelValues(xetObject(object)).Add(float64(size))
	}
}

// XetShardRegistered observes how long registering a shard took.
func XetShardRegistered(ctx context.Context, err error, took time.Duration) {
	observe(ctx, xetShardDuration.WithLabelValues(Result(err)), took.Seconds())
}

// XetFileRebuilt counts the bytes of a file rebuilt from xorbs.
func XetFileRebuilt(size int64) {
	xetRebuiltBytes.WithLabelValues().Add(float64(size))
}
