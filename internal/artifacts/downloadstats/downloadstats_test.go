package downloadstats

import (
	"testing"
	"time"
	"uuid"
)

func TestRecorderAggregates(t *testing.T) {
	t.Parallel()

	r := NewRecorder(nil, nil)
	clock := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	r.now = func() time.Time {
		return clock
	}
	a, b := uuid.NewV7(), uuid.NewV7()
	r.Download(a, "libs", "alice")

	clock = clock.Add(time.Second)

	r.Download(a, "libs", "bob")
	r.Download(b, "libs", "carol")

	if got := r.pending[a]; got.count != 2 || got.by != "bob" || !got.last.Equal(clock) {
		t.Errorf("a = %+v", got)
	}

	if got := r.pending[b]; got.count != 1 || got.by != "carol" {
		t.Errorf("b = %+v", got)
	}

	var none *Recorder
	none.Download(a, "libs", "x")

	if err := none.Flush(t.Context()); err != nil {
		t.Errorf("nil Flush = %v", err)
	}
}
