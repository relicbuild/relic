package audit

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBounded(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 400<<20)

	tests := []struct {
		name  string
		event Event
		want  Event
	}{
		{
			name: "short fields stay",
			event: Event{
				Actor:  "alice",
				Action: "relic.v1.SecurityService/PutUser",
				Target: "bob",
				Client: "10.0.0.1",
				Detail: "sign in first",
			},
			want: Event{
				Actor:  "alice",
				Action: "relic.v1.SecurityService/PutUser",
				Target: "bob",
				Client: "10.0.0.1",
				Detail: "sign in first",
			},
		},
		{
			name: "long fields are cut",
			event: Event{
				Actor:  long,
				Action: long,
				Target: long,
				Client: long,
				Detail: long,
			},
			want: Event{
				Actor:  strings.Repeat("x", maxFieldBytes-len("…")) + "…",
				Action: strings.Repeat("x", maxFieldBytes-len("…")) + "…",
				Target: strings.Repeat("x", maxFieldBytes-len("…")) + "…",
				Client: strings.Repeat("x", maxFieldBytes-len("…")) + "…",
				Detail: strings.Repeat("x", maxFieldBytes-len("…")) + "…",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.event.bounded(); got != tt.want {
				t.Errorf("bounded() field sizes = %v, want %v", fieldSizes(got), fieldSizes(tt.want))
			}
		})
	}
}

func TestBoundedKeepsWholeRunes(t *testing.T) {
	t.Parallel()

	got := Event{
		Target: strings.Repeat("é", maxFieldBytes),
	}.bounded()

	if len(got.Target) > maxFieldBytes || !utf8.ValidString(got.Target) || !strings.HasSuffix(got.Target, "…") {
		t.Errorf("bounded target = %d bytes, valid %v", len(got.Target), utf8.ValidString(got.Target))
	}
}

func fieldSizes(e Event) []int {
	return []int{
		len(e.Actor),
		len(e.Action),
		len(e.Target),
		len(e.Client),
		len(e.Detail),
	}
}
