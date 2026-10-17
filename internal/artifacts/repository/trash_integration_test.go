//go:build integration

package repository

import (
	"errors"
	"testing"
	"time"
)

func TestTrashingAFolderKeepsAnEarlierEntryBelowIt(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, _, _ := newTestService(t)
	deploy(t, s, "t2/x/f1.txt", "first")
	deploy(t, s, "t2/x/g.txt", "second")

	if err := s.DeleteToTrash(ctx, "generic-local", "t2/x/f1.txt", "alice"); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteToTrash(ctx, "generic-local", "t2/x", "alice"); err != nil {
		t.Fatal(err)
	}

	if err := s.RestoreFromTrash(ctx, "generic-local/t2/x", "", "", "alice"); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"t2/x/f1.txt",
		"t2/x/g.txt",
	} {
		if _, err := s.Stat(ctx, "generic-local", path); err != nil {
			t.Errorf("%s after restoring its folder: %v", path, err)
		}
	}
}

func TestTrashingAFileOverAFolderEntryReplacesIt(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, _, _ := newTestService(t)
	deploy(t, s, "k/inner.txt", "inside a folder")

	if err := s.DeleteToTrash(ctx, "generic-local", "k", "alice"); err != nil {
		t.Fatal(err)
	}

	deploy(t, s, "k", "now a file")

	if err := s.DeleteToTrash(ctx, "generic-local", "k", "alice"); err != nil {
		t.Fatal(err)
	}

	it, err := s.Stat(ctx, s.TrashRepository(), "generic-local/k")
	if err != nil || it.IsFolder() {
		t.Errorf("trash entry = %+v, %v; want the file", it, err)
	}
}

func TestPurgeTrashKeepsNewerEntriesBelowAnExpiredOne(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, _, _ := newTestService(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := start
	s.now = func() time.Time {
		return clock
	}

	deploy(t, s, "p/x/old.txt", "old")

	if err := s.DeleteToTrash(ctx, "generic-local", "p/x", "alice"); err != nil {
		t.Fatal(err)
	}

	clock = start.Add(48 * time.Hour)

	deploy(t, s, "p/x/new.txt", "new")

	if err := s.DeleteToTrash(ctx, "generic-local", "p/x/new.txt", "alice"); err != nil {
		t.Fatal(err)
	}

	n, err := s.PurgeTrash(ctx, start.Add(24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PurgeTrash = %d, %v; want 1 entry purged", n, err)
	}

	if _, err := s.Stat(ctx, s.TrashRepository(), "generic-local/p/x/new.txt"); err != nil {
		t.Errorf("newer entry after the purge: %v", err)
	}

	if _, err := s.Stat(ctx, s.TrashRepository(), "generic-local/p/x/old.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired entry's file after the purge = %v, want ErrNotFound", err)
	}

	entries, _, err := s.TrashEntries(
		ctx,
		TrashQuery{
			Max: 10,
		},
	)
	if err != nil || len(entries) != 1 || entries[0].Item.Path() != "generic-local/p/x/new.txt" {
		t.Errorf("trash after the purge = %+v, %v; want the newer entry alone", entries, err)
	}
}
