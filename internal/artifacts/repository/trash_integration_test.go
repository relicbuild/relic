//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"slices"
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

func TestTrashingAFolderAgainReplacesItsEarlierEntry(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, _, _ := newTestService(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := start
	s.now = func() time.Time {
		return clock
	}

	deploy(t, s, "r/x/old.txt", "old")
	deploy(t, s, "r/x/kept.txt", "trashed on its own")

	if err := s.DeleteToTrash(ctx, "generic-local", "r/x/kept.txt", "alice"); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteToTrash(ctx, "generic-local", "r/x", "alice"); err != nil {
		t.Fatal(err)
	}

	clock = start.Add(48 * time.Hour)

	deploy(t, s, "r/x/new.txt", "new")

	if err := s.DeleteToTrash(ctx, "generic-local", "r/x", "alice"); err != nil {
		t.Fatal(err)
	}

	entries, _, err := s.TrashEntries(
		ctx,
		TrashQuery{
			Max: 10,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	deleted := map[string]time.Time{}
	for _, e := range entries {
		deleted[e.Item.Path()] = e.Deleted
	}

	if len(deleted) != 2 ||
		!deleted["generic-local/r/x"].Equal(clock) ||
		!deleted["generic-local/r/x/kept.txt"].Equal(start) {
		t.Errorf("trash entries = %v; want the folder deleted again and the file trashed on its own", deleted)
	}

	if _, err := s.Stat(ctx, s.TrashRepository(), "generic-local/r/x/old.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("file of the earlier folder entry in the trash = %v, want ErrNotFound", err)
	}

	if err := s.RestoreFromTrash(ctx, "generic-local/r/x", "", "", "alice"); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"r/x/new.txt",
		"r/x/kept.txt",
	} {
		if _, err := s.Stat(ctx, "generic-local", path); err != nil {
			t.Errorf("%s after restoring its folder: %v", path, err)
		}
	}

	if _, err := s.Stat(ctx, "generic-local", "r/x/old.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("file of the earlier folder entry after the restore = %v, want ErrNotFound", err)
	}
}

func TestMoveAndRestoreKeepWhenAndByWhomAFileWasCreated(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, _, _ := newTestService(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := start
	s.now = func() time.Time {
		return clock
	}

	original := deploy(t, s, "dk/keep/k.txt", "keep")

	clock = start.Add(time.Hour)

	src, err := s.Stat(ctx, "generic-local", "dk")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Copy(ctx, CopyInput{
		Source:                src,
		DestinationRepository: "generic-local",
		DestinationPath:       "moved",
		Move:                  true,
		User:                  "mover",
	}); err != nil {
		t.Fatal(err)
	}

	check := func(when, path string) {
		t.Helper()

		it, err := s.Stat(ctx, "generic-local", path)
		if err != nil {
			t.Fatalf("%s: %v", when, err)
		}

		if !it.Created.Equal(original.Created) || it.CreatedBy != original.CreatedBy {
			t.Errorf(
				"%s: created %v by %q, want %v by %q",
				when,
				it.Created,
				it.CreatedBy,
				original.Created,
				original.CreatedBy,
			)
		}
	}

	check("after the move", "moved/keep/k.txt")

	clock = start.Add(2 * time.Hour)

	if err := s.DeleteToTrash(ctx, "generic-local", "moved/keep/k.txt", "mover"); err != nil {
		t.Fatal(err)
	}

	clock = start.Add(3 * time.Hour)

	if err := s.RestoreFromTrash(ctx, "generic-local/moved/keep/k.txt", "", "", "mover"); err != nil {
		t.Fatal(err)
	}

	check("after a trip through the trash", "moved/keep/k.txt")
}

func TestRestoreDropsTrashPropertiesOfNestedEntries(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, _, _ := newTestService(t)
	deploy(t, s, "t/x/f2.txt", "folder entry")

	if err := s.DeleteToTrash(ctx, "generic-local", "t/x", "alice"); err != nil {
		t.Fatal(err)
	}

	deploy(t, s, "t/x/f3.txt", "nested entry")

	if err := s.DeleteToTrash(ctx, "generic-local", "t/x/f3.txt", "alice"); err != nil {
		t.Fatal(err)
	}

	if err := s.RestoreFromTrash(ctx, "generic-local/t/x", "", "", "alice"); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"t/x",
		"t/x/f2.txt",
		"t/x/f3.txt",
	} {
		it, err := s.Stat(ctx, "generic-local", path)
		if err != nil {
			t.Fatalf("%s after the restore: %v", path, err)
		}

		props, err := s.Properties(ctx, it)
		if err != nil {
			t.Fatal(err)
		}

		for _, key := range s.trashLayout.keys() {
			if values, ok := props[key]; ok {
				t.Errorf("%s keeps %s=%v after the restore", path, key, values)
			}
		}
	}
}

func TestTrashingALargeFolderCommitsItInBatches(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, _, _ := newTestService(t)
	files := largeFolder(t, s, "big")
	last := files[len(files)-1]

	var committedBeforeTheEnd bool

	if err := s.DeleteToTrashChecked(ctx, "generic-local", "big", "alice", func(ctx context.Context, file Item) error {
		if file.Path() == last && firstFileTrashed(ctx, s, files) {
			committedBeforeTheEnd = true
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if !committedBeforeTheEnd {
		t.Error(
			"the first files reached the trash only when the whole folder had moved; want them committed in batches",
		)
	}

	assertLargeFolderTrashed(t, s, files)
}

func TestTrashingALargeFolderAgainAfterAFailureKeepsWhatMoved(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, _, _ := newTestService(t)
	deploy(t, s, "big/old.txt", "an earlier entry")

	if err := s.DeleteToTrash(ctx, "generic-local", "big", "alice"); err != nil {
		t.Fatal(err)
	}

	files := largeFolder(t, s, "big")
	last := files[len(files)-1]
	refusal := errors.New("refused")

	err := s.DeleteToTrashChecked(ctx, "generic-local", "big", "alice", func(ctx context.Context, file Item) error {
		if file.Path() == last && firstFileTrashed(ctx, s, files) {
			return refusal
		}

		return nil
	})
	if !errors.Is(err, refusal) {
		t.Fatalf("delete refused in a later batch = %v, want the refusal", err)
	}

	if _, err := s.Stat(ctx, "generic-local", last); err != nil {
		t.Fatalf("refused file after the failed delete: %v", err)
	}

	if err := s.DeleteToTrash(ctx, "generic-local", "big", "alice"); err != nil {
		t.Fatal(err)
	}

	assertLargeFolderTrashed(t, s, files)
}

func largeFolder(t *testing.T, s *Service, folder string) []string {
	t.Helper()

	files := make([]string, 0, 2*moveBatchItems+10)
	for i := range cap(files) {
		path := fmt.Sprintf("%s/d%d/f-%03d.txt", folder, i%3, i)
		deploy(t, s, path, path)
		files = append(files, path)
	}

	slices.Sort(files)

	return files
}

func firstFileTrashed(ctx context.Context, s *Service, files []string) bool {
	_, err := s.Stat(ctx, s.TrashRepository(), "generic-local/"+files[0])

	return err == nil
}

func assertLargeFolderTrashed(t *testing.T, s *Service, files []string) {
	t.Helper()

	ctx := t.Context()

	if _, err := s.Stat(ctx, "generic-local", "big"); !errors.Is(err, ErrNotFound) {
		t.Errorf("folder after its delete = %v, want ErrNotFound", err)
	}

	for _, f := range files {
		it, err := s.Stat(ctx, s.TrashRepository(), "generic-local/"+f)
		if err != nil || it.Checksums.SHA256 != sha256Hex(f) {
			t.Errorf("%s in the trash = %+v, %v", f, it, err)
		}
	}

	entries, _, err := s.TrashEntries(ctx, TrashQuery{
		Max: 10,
	})
	if err != nil || len(entries) != 1 || entries[0].Item.Path() != "generic-local/big" ||
		entries[0].OriginalPath != "big" || entries[0].DeletedBy != "alice" {
		t.Errorf("trash entries = %+v, %v; want the folder", entries, err)
	}
}
