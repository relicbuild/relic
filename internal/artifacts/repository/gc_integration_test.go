//go:build integration

package repository

import (
	"strings"
	"testing"
	"time"

	"relic/internal/platform/objectstore"
)

func TestCollectGarbage(t *testing.T) {
	t.Parallel()
	s, blobs, db := newTestService(t)
	ctx := t.Context()
	deploy(t, s, "keep.txt", "kept")
	deploy(t, s, "drop.txt", "dropped")

	if err := s.Delete(ctx, "generic-local", "drop.txt"); err != nil {
		t.Fatal(err)
	}

	if n, err := s.CollectGarbage(ctx, time.Hour, 10); err != nil || n != 0 {
		t.Fatalf("CollectGarbage within grace = %d, %v", n, err)
	}

	s.now = func() time.Time {
		return time.Now().UTC().Add(2 * time.Hour)
	}
	if n, err := s.CollectGarbage(ctx, time.Hour, 1); err != nil || n != 1 {
		t.Fatalf("CollectGarbage = %d, %v; want 1", n, err)
	}

	if _, err := db.GetBlob(ctx, sha256Hex("dropped")); err == nil {
		t.Error("collected blob row still exists")
	}

	if keys := blobs.Keys(); len(keys) != 1 || keys[0] != objectstore.Key(sha256Hex("kept")) {
		t.Errorf("objects = %v, want only the referenced blob", keys)
	}

	s.now = func() time.Time {
		return time.Now().UTC()
	}
	item := deploy(t, s, "again.txt", "dropped")

	rc, err := s.Open(ctx, item, nil)
	if err != nil {
		t.Fatalf("redeployed content unreadable: %v", err)
	}

	_ = rc.Close()

	if n := refcount(t, db, "dropped"); n != 1 {
		t.Errorf("refcount = %d, want 1", n)
	}

	if !strings.Contains(strings.Join(blobs.Keys(), ","), sha256Hex("dropped")) {
		t.Error("redeployed object missing")
	}
}
