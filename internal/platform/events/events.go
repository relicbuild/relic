// Package events carries what happened in Relic to the subscribers of its
// event types, such as webhooks.
package events

import (
	"context"
	"time"
	"unicode/utf8"
)

// Event types.
const (
	ArtifactDeployed   = "artifact.deployed"
	ArtifactDeleted    = "artifact.deleted"
	ScanFinished       = "scan.finished"
	DownloadBlocked    = "download.blocked"
	ReleasePromoted    = "release.promoted"
	QuarantineReleased = "quarantine.released"
	QuarantineRejected = "quarantine.rejected"
	WebhookPing        = "webhook.ping"
)

const maxSubject = 512

// Types are the event types a subscriber can name.
var Types = []string{
	ArtifactDeployed,
	ArtifactDeleted,
	ScanFinished,
	DownloadBlocked,
	ReleasePromoted,
	QuarantineReleased,
	QuarantineRejected,
}

// Event is one thing that happened.
type Event struct {
	Type string `json:"type"`
	// Subject is what it happened to: <repository>/<path> or
	// <release>/<version>.
	Subject string         `json:"subject"`
	Actor   string         `json:"actor,omitempty"`
	Time    time.Time      `json:"time"`
	Data    map[string]any `json:"data,omitempty"`
}

// Publisher takes events; it must not block or fail the operation that
// published them.
type Publisher interface {
	Publish(ctx context.Context, e Event)
}

// Publish sends e to p, stamping its time; a nil p drops it.
func Publish(ctx context.Context, p Publisher, e Event) {
	if p == nil {
		return
	}

	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}

	if len(e.Subject) > maxSubject {
		cut := maxSubject
		for cut > 0 && !utf8.RuneStart(e.Subject[cut]) {
			cut--
		}

		e.Subject = e.Subject[:cut]
	}

	p.Publish(context.WithoutCancel(ctx), e)
}

// Fanout publishes each event to every publisher in it.
type Fanout []Publisher

// Publish implements Publisher.
func (f Fanout) Publish(ctx context.Context, e Event) {
	for _, p := range f {
		if p != nil {
			p.Publish(ctx, e)
		}
	}
}
