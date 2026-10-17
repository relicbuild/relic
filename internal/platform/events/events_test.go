package events

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

type recorder struct {
	got []Event
}

func (r *recorder) Publish(_ context.Context, e Event) {
	r.got = append(r.got, e)
}

func TestPublishTruncatesTheSubject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		subject string
		wantLen int
	}{
		{
			name:    "short subject",
			subject: "libs/a.jar",
			wantLen: len("libs/a.jar"),
		},
		{
			name:    "long ASCII subject",
			subject: strings.Repeat("a", maxSubject+10),
			wantLen: maxSubject,
		},
		{
			name:    "multi-byte rune across the limit",
			subject: strings.Repeat("a", maxSubject-1) + "é" + "b",
			wantLen: maxSubject - 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &recorder{}
			Publish(t.Context(), r, Event{
				Type:    ArtifactDeployed,
				Subject: tt.subject,
			})

			got := r.got[0].Subject
			if len(got) != tt.wantLen || !utf8.ValidString(got) {
				t.Errorf(
					"subject of %d bytes, valid UTF-8 %v; want %d bytes",
					len(got),
					utf8.ValidString(got),
					tt.wantLen,
				)
			}
		})
	}
}
