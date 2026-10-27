package usage

import (
	"testing"
	"time"
	"uuid"
)

func TestRecorderAggregates(t *testing.T) {
	t.Parallel()

	r := NewItemDownloads(nil, nil)
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

	var none *ItemDownloads
	none.Download(a, "libs", "x")

	if err := none.Flush(t.Context()); err != nil {
		t.Errorf("nil Flush = %v", err)
	}
}

func TestRecorderKeepsAtMostMaxPendingItems(t *testing.T) {
	t.Parallel()

	r := NewItemDownloads(nil, nil)
	r.maxPending = 2

	a, b, c := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	r.Download(a, "libs", "alice")
	r.Download(b, "libs", "alice")
	r.Download(c, "libs", "alice")
	r.Download(a, "libs", "bob")

	if len(r.pending) != 2 || r.pending[a].count != 2 || r.pending[b].count != 1 {
		t.Errorf("pending = %+v; want a twice and b once", r.pending)
	}

	r.keep(map[uuid.UUID]pending{
		a: {
			count: 1,
		},
		c: {
			count: 1,
		},
	})

	if len(r.pending) != 2 || r.pending[a].count != 3 {
		t.Errorf("pending after a failed flush = %+v; want a three times and b once", r.pending)
	}
}
