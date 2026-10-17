//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func newTestService(t *testing.T) (*Service, *objectstoretest.Memory, *database.DB) {
	t.Helper()
	db := databasetest.NewDB(t)
	blobs := objectstoretest.NewMemory()
	s := NewService(db, blobs, slog.New(slog.DiscardHandler))
	s.AllowPrivateUpstreams(true)

	if err := s.CreateRepository(
		t.Context(),
		Repository{
			Name:   "generic-local",
			Class:  ClassLocal,
			Format: "generic",
		},
	); err != nil {
		t.Fatal(err)
	}

	return s, blobs, db
}

func deploy(t *testing.T, s *Service, path, content string) Item {
	t.Helper()

	item, err := s.Deploy(
		t.Context(),
		DeployInput{
			Repository: "generic-local",
			Path:       path,
			Body:       strings.NewReader(content),
			User:       "admin",
		},
	)
	if err != nil {
		t.Fatalf("Deploy(%s): %v", path, err)
	}

	return item
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func refcount(t *testing.T, db *database.DB, content string) int64 {
	t.Helper()

	b, err := db.GetBlob(t.Context(), sha256Hex(content))
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}

	return b.Refcount
}

func TestDeployCreatesFileFoldersAndBlob(t *testing.T) {
	t.Parallel()
	s, blobs, db := newTestService(t)
	item := deploy(t, s, "/a/b/hello.txt", "hello")

	if item.Path() != "a/b/hello.txt" || item.Size != 5 || item.Checksums.SHA256 != sha256Hex("hello") ||
		item.Checksums.SHA1 != "aaf4c61ddcc5e8a2dabede0f3b482cd9aea9434d" || item.Checksums.MD5 != "5d41402abc4b2a76b9719d911017c592" {
		t.Errorf("unexpected item %+v", item)
	}

	for _, p := range []string{
		"a",
		"a/b",
	} {
		f, err := s.Stat(t.Context(), "generic-local", p)
		if err != nil || !f.IsFolder() {
			t.Errorf("Stat(%s) = %+v, %v; want folder", p, f, err)
		}
	}

	if got := blobs.Keys(); len(got) != 1 || got[0] != objectstore.Key(sha256Hex("hello")) {
		t.Errorf("blob keys = %v, want only the CAS key (staging removed)", got)
	}

	rc, err := s.Open(t.Context(), item, &objectstore.Range{
		Start: 1,
		End:   3,
	})
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = rc.Close()
	}()

	if b, _ := io.ReadAll(rc); string(b) != "ell" {
		t.Errorf("range read = %q, want %q", b, "ell")
	}

	if n := refcount(t, db, "hello"); n != 1 {
		t.Errorf("refcount = %d, want 1", n)
	}

	if got, err := s.SHA256OfSHA1(t.Context(), strings.ToUpper(item.Checksums.SHA1)); err != nil ||
		got != sha256Hex("hello") {
		t.Errorf("SHA256OfSHA1 = %q, %v; want %q", got, err, sha256Hex("hello"))
	}

	if got, err := s.SHA256OfSHA1(t.Context(), strings.Repeat("0", 40)); err != nil || got != "" {
		t.Errorf("SHA256OfSHA1 of unknown content = %q, %v; want none", got, err)
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := s.SHA256OfSHA1(canceled, item.Checksums.SHA1); err == nil {
		t.Error("SHA256OfSHA1 with a failing database succeeded")
	}
}

func TestDeployDeduplicatesAndOverwriteReleases(t *testing.T) {
	t.Parallel()
	s, blobs, db := newTestService(t)
	deploy(t, s, "x/one.txt", "same")
	deploy(t, s, "y/two.txt", "same")

	if n := refcount(t, db, "same"); n != 2 {
		t.Fatalf("refcount after two deploys = %d, want 2", n)
	}

	if got := len(blobs.Keys()); got != 1 {
		t.Fatalf("stored objects = %d, want 1", got)
	}

	deploy(t, s, "x/one.txt", "different")

	if n := refcount(t, db, "same"); n != 1 {
		t.Errorf("refcount after overwrite = %d, want 1", n)
	}

	if err := s.Delete(t.Context(), "generic-local", "y"); err != nil {
		t.Fatal(err)
	}

	if n := refcount(t, db, "same"); n != 0 {
		t.Errorf("refcount after deleting the folder = %d, want 0", n)
	}

	if _, err := s.Stat(t.Context(), "generic-local", "y/two.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Stat after delete = %v, want ErrNotFound", err)
	}
}

func TestDeployErrors(t *testing.T) {
	t.Parallel()
	s, blobs, _ := newTestService(t)
	deploy(t, s, "dir/file.txt", "content")

	errOverwriteRefused := errors.New("overwrite refused")

	tests := []struct {
		name string
		in   DeployInput
		want error
	}{
		{
			name: "overwrite the caller may not make",
			in: DeployInput{
				Repository: "generic-local",
				Path:       "dir/file.txt",
				Authorize: func(_ string, overwrite bool) error {
					if overwrite {
						return errOverwriteRefused
					}

					return nil
				},
			},
			want: errOverwriteRefused,
		},
		{
			name: "checksum mismatch",
			in: DeployInput{
				Repository: "generic-local",
				Path:       "bad.txt",
				Expected: Checksums{
					SHA1: strings.Repeat("0", 40),
				},
			},
			want: ErrChecksumMismatch,
		},
		{
			name: "file over folder",
			in: DeployInput{
				Repository: "generic-local",
				Path:       "dir",
			},
			want: ErrConflict,
		},
		{
			name: "folder over file",
			in: DeployInput{
				Repository: "generic-local",
				Path:       "dir/file.txt/inner",
			},
			want: ErrConflict,
		},
		{
			name: "unknown repo",
			in: DeployInput{
				Repository: "nope",
				Path:       "a",
			},
			want: ErrRepositoryNotFound,
		},
		{
			name: "traversal",
			in: DeployInput{
				Repository: "generic-local",
				Path:       "../a",
			},
			want: ErrInvalidPath,
		},
		{
			name: "root",
			in: DeployInput{
				Repository: "generic-local",
				Path:       "/",
			},
			want: ErrInvalidPath,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.in.Body = strings.NewReader("x")
			if _, err := s.Deploy(t.Context(), tt.in); !errors.Is(err, tt.want) {
				t.Errorf("Deploy() error = %v, want %v", err, tt.want)
			}
		})
	}

	if got := len(blobs.Keys()); got != 1 {
		t.Errorf("failed deploys left objects behind: %v", blobs.Keys())
	}
}

func TestDeployByChecksum(t *testing.T) {
	t.Parallel()
	s, _, db := newTestService(t)
	first := deploy(t, s, "orig.bin", "payload")

	item, err := s.DeployByChecksum(
		t.Context(),
		DeployInput{
			Repository: "generic-local",
			Path:       "copy.bin",
			Expected: Checksums{
				SHA1: first.Checksums.SHA1,
			},
			User: "admin",
		},
	)
	if err != nil || item.Checksums != first.Checksums {
		t.Fatalf("DeployByChecksum = %+v, %v", item, err)
	}

	if n := refcount(t, db, "payload"); n != 2 {
		t.Errorf("refcount = %d, want 2", n)
	}

	_, err = s.DeployByChecksum(
		t.Context(),
		DeployInput{
			Repository: "generic-local",
			Path:       "x.bin",
			Expected: Checksums{
				SHA256: sha256Hex("unknown"),
			},
		},
	)
	if !errors.Is(err, ErrChecksumUnknown) {
		t.Errorf("unknown checksum error = %v, want ErrChecksumUnknown", err)
	}
}

func TestProperties(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestService(t)
	ctx := t.Context()

	_, err := s.Deploy(
		ctx,
		DeployInput{
			Repository: "generic-local",
			Path:       "p/a.txt",
			Body:       strings.NewReader("a"),
			Props: Properties{
				"stage": {
					"dev",
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	deploy(t, s, "p/q/b.txt", "b")

	folder, err := s.Stat(ctx, "generic-local", "p")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetProperties(ctx, folder, Properties{
		"team": {
			"x",
			"y",
		},
	}, true); err != nil {
		t.Fatal(err)
	}

	props := func(path string) Properties {
		t.Helper()

		it, err := s.Stat(ctx, "generic-local", path)
		if err != nil {
			t.Fatal(err)
		}

		p, err := s.Properties(ctx, it)
		if err != nil {
			t.Fatal(err)
		}

		return p
	}
	if got := props("p/q/b.txt"); strings.Join(got["team"], ",") != "x,y" {
		t.Errorf("recursive set: %v", got)
	}

	if got := props("p/a.txt"); got["stage"][0] != "dev" || len(got["team"]) != 2 {
		t.Errorf("deploy props + set: %v", got)
	}

	if err := s.DeleteProperties(ctx, folder, []string{
		"team",
	}, true); err != nil {
		t.Fatal(err)
	}

	if got := props("p/q/b.txt"); len(got) != 0 {
		t.Errorf("after delete: %v", got)
	}
}

func TestRepositoryLifecycle(t *testing.T) {
	t.Parallel()
	s, _, db := newTestService(t)

	ctx := t.Context()
	if err := s.CreateRepository(ctx, Repository{
		Name:  "generic-local",
		Class: ClassLocal,
	}); !errors.Is(
		err,
		ErrRepositoryExists,
	) {
		t.Errorf("duplicate create = %v, want ErrRepoExists", err)
	}

	deploy(t, s, "f.txt", "repo-content")

	if err := s.DeleteRepository(ctx, "generic-local"); err != nil {
		t.Fatal(err)
	}

	if n := refcount(t, db, "repo-content"); n != 0 {
		t.Errorf("refcount after repo delete = %d", n)
	}

	if err := s.DeleteRepository(ctx, "generic-local"); !errors.Is(err, ErrRepositoryNotFound) {
		t.Errorf("second delete = %v, want ErrRepoNotFound", err)
	}
}

func TestConcurrentDeploysToOnePath(t *testing.T) {
	t.Parallel()
	s, _, db := newTestService(t)

	const n = 8

	var wg sync.WaitGroup

	errs := make(chan error, n)
	for i := range n {
		wg.Go(func() {
			_, err := s.Deploy(
				context.WithoutCancel(t.Context()),
				DeployInput{
					Repository: "generic-local",
					Path:       "race/f.txt",
					Body:       strings.NewReader(strings.Repeat("r", i+1)),
				},
			)
			errs <- err
		})
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent deploy: %v", err)
		}
	}

	var total int64
	for i := range n {
		total += refcount(t, db, strings.Repeat("r", i+1))
	}

	if total != 1 {
		t.Errorf("sum of refcounts = %d, want 1 (one surviving item)", total)
	}
}

func TestRepositoryValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		r       Repository
		wantErr bool
	}{
		{
			name: "minimal",
			r: Repository{
				Name:  "libs",
				Class: ClassLocal,
			},
		},
		{
			name: "bad class",
			r: Repository{
				Name:  "libs",
				Class: "weird",
			},
			wantErr: true,
		},
		{
			name: "reserved",
			r: Repository{
				Name:  "api",
				Class: ClassLocal,
			},
			wantErr: true,
		},
		{
			name: "bad key",
			r: Repository{
				Name:  "a/b",
				Class: ClassLocal,
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.r.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}

			if err == nil && tt.r.Format != "generic" {
				t.Errorf("format = %q, want generic", tt.r.Format)
			}
		})
	}

	v := Repository{
		Name:  "v",
		Class: ClassVirtual,
		Config: Config{
			Remote: &RemoteConfig{
				URL: "x",
			},
		},
	}
	if err := v.Validate(); err != nil || v.Config.Remote != nil || v.Config.Virtual == nil ||
		v.Config.Virtual.Members == nil {
		t.Errorf("virtual = %+v %v, want only a virtual block", v.Config, err)
	}
}

func TestDeployRefusesAStagedUploadTheStoreTruncated(t *testing.T) {
	t.Parallel()

	db := databasetest.NewDB(t)
	blobs := objectstoretest.NewFaulty(objectstoretest.NewMemory())
	s := NewService(db, blobs, slog.New(slog.DiscardHandler))

	if err := s.CreateRepository(
		t.Context(),
		Repository{
			Name:   "generic-local",
			Class:  ClassLocal,
			Format: "generic",
		},
	); err != nil {
		t.Fatal(err)
	}

	blobs.Inject(objectstoretest.Fault{
		Op:         objectstoretest.OpPut,
		KeyPrefix:  objectstore.TemporaryKeyPrefix,
		Times:      1,
		ShortWrite: 3,
	})

	if _, err := s.Deploy(
		t.Context(),
		DeployInput{
			Repository: "generic-local",
			Path:       "short.txt",
			Body:       strings.NewReader("complete content"),
		},
	); err == nil {
		t.Fatal("Deploy accepted an upload the store kept 3 bytes of")
	}

	if _, err := s.Stat(t.Context(), "generic-local", "short.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Stat after the failed deploy = %v, want ErrNotFound", err)
	}

	if _, err := db.GetBlob(t.Context(), sha256Hex("complete content")); err == nil {
		t.Error("the failed deploy left a blobs row")
	}
}

func TestCollectGarbageSkipsABlobItCannotDelete(t *testing.T) {
	t.Parallel()

	s, blobs, memory := newFaultyService(t)
	s.now = func() time.Time {
		return time.Now().Add(2 * time.Hour)
	}

	for _, content := range []string{
		"first",
		"second",
		"third",
	} {
		deploy(t, s, content+".txt", content)

		if err := s.Delete(t.Context(), "generic-local", content+".txt"); err != nil {
			t.Fatal(err)
		}
	}

	stuck := objectstore.Key(sha256Hex("second"))
	blobs.Inject(objectstoretest.Fault{
		Op:        objectstoretest.OpDelete,
		KeyPrefix: stuck,
		Err:       objectstore.ErrTransient,
	})

	n, err := s.CollectGarbage(t.Context(), time.Hour, 500)
	if n != 2 || !errors.Is(err, objectstore.ErrTransient) {
		t.Fatalf("CollectGarbage = %d, %v; want 2 collected and the transient error", n, err)
	}

	if _, err := memory.Head(t.Context(), stuck); err != nil {
		t.Fatalf("the blob GC could not delete is gone from the store: %v", err)
	}

	deploy(t, s, "fourth.txt", "fourth")

	if err := s.Delete(t.Context(), "generic-local", "fourth.txt"); err != nil {
		t.Fatal(err)
	}

	if n, err := s.CollectGarbage(t.Context(), time.Hour, 1); n != 1 || !errors.Is(err, objectstore.ErrTransient) {
		t.Errorf("a run of batches of 1 behind the stuck blob = %d, %v; want the newer blob collected", n, err)
	}

	blobs.Clear()

	if n, err := s.CollectGarbage(t.Context(), time.Hour, 500); n != 1 || err != nil {
		t.Errorf("the next run = %d, %v; want the skipped blob collected", n, err)
	}
}

func TestAMoveRollsBackWhenItsTransactionFails(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	item := deploy(t, s, "keep/me.txt", "kept")

	err := s.Copy(t.Context(), CopyInput{
		Source:                item,
		DestinationRepository: "generic-local",
		DestinationPath:       "moved/me.txt",
		Move:                  true,
		then: func(database.Querier) error {
			return errors.New("properties failed")
		},
	})
	if err == nil {
		t.Fatal("Copy succeeded although its last step failed")
	}

	if _, err := s.Stat(t.Context(), "generic-local", "keep/me.txt"); err != nil {
		t.Errorf("the source is gone after a failed move: %v", err)
	}

	if _, err := s.Stat(t.Context(), "generic-local", "moved/me.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the destination exists after a failed move: %v", err)
	}

	other := deploy(t, s, "keep/other.txt", "kept too")

	err = s.CopyAll(t.Context(), []CopyInput{
		{
			Source:                item,
			DestinationRepository: "generic-local",
			DestinationPath:       "moved/me.txt",
			Move:                  true,
		},
		{
			Source:                other,
			DestinationRepository: "generic-local",
			DestinationPath:       "moved/other.txt",
			Move:                  true,
			then: func(database.Querier) error {
				return errors.New("properties failed")
			},
		},
	})
	if err == nil {
		t.Fatal("CopyAll succeeded although its last move failed")
	}

	if _, err := s.Stat(t.Context(), "generic-local", "keep/me.txt"); err != nil {
		t.Errorf("the first source is gone after a failed CopyAll: %v", err)
	}

	if _, err := s.Stat(t.Context(), "generic-local", "moved/me.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the first destination exists after a failed CopyAll: %v", err)
	}
}

func TestDeleteToTrashCheckedRefusesAFileAddedDuringTheDelete(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, _, _ := newTestService(t)

	deploy(t, s, "folder/allowed.txt", "allowed")

	var addedLate sync.Once

	err := s.DeleteToTrashChecked(ctx, "generic-local", "folder", "alice", func(ctx context.Context, file Item) error {
		addedLate.Do(func() {
			if _, err := s.Deploy(ctx, DeployInput{
				Repository: "generic-local",
				Path:       "folder/protected.txt",
				Body:       strings.NewReader("protected"),
				User:       "admin",
			}); err != nil {
				t.Error(err)
			}
		})

		if file.Name == "protected.txt" {
			return ErrForbidden
		}

		return nil
	})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("DeleteToTrashChecked = %v, want the refusal of the file added during the delete", err)
	}

	for _, p := range []string{
		"folder/allowed.txt",
		"folder/protected.txt",
	} {
		if _, err := s.Stat(ctx, "generic-local", p); err != nil {
			t.Errorf("%s after a refused delete: %v", p, err)
		}
	}
}

func TestMoveRefusesAFolderThatChangedDuringTheMove(t *testing.T) {
	t.Parallel()

	if databasetest.SQLite() {
		t.Skip("SQLite runs one writer at a time: nothing commits during a move")
	}

	ctx := t.Context()
	s, _, _ := newTestService(t)

	source := deploy(t, s, "from/app.txt", "app")

	folder, err := s.Stat(ctx, "generic-local", "from")
	if err != nil {
		t.Fatal(err)
	}

	var checks atomic.Int32

	err = s.Copy(ctx, CopyInput{
		Source:                folder,
		DestinationRepository: "generic-local",
		DestinationPath:       "to",
		Move:                  true,
		User:                  "admin",
		RefuseFile: func(ctx context.Context, _ Item) error {
			if checks.Add(1) == 2 {
				if _, err := s.Deploy(ctx, DeployInput{
					Repository: "generic-local",
					Path:       "from/late.txt",
					Body:       strings.NewReader("late"),
					User:       "admin",
				}); err != nil {
					t.Error(err)
				}
			}

			return nil
		},
	})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("move of a folder a deploy changed during the move = %v, want ErrConflict", err)
	}

	for _, p := range []string{
		source.Path(),
		"from/late.txt",
	} {
		if _, err := s.Stat(ctx, "generic-local", p); err != nil {
			t.Errorf("%s after the refused move: %v", p, err)
		}
	}
}

func TestFilesWithPropertyListsAFileOnceForValuesInTwoCases(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, _, _ := newTestService(t)

	item := deploy(t, s, "pool/hello.deb", "a package")
	if err := s.SetProperties(ctx, item, Properties{
		"deb.distribution": {
			"focal",
			"Focal",
		},
	}, false); err != nil {
		t.Fatal(err)
	}

	got, err := s.FilesWithProperty(ctx, "generic-local", "deb.distribution", "focal")
	if err != nil || len(got) != 1 {
		t.Errorf("FilesWithProperty = %d files, %v; want the file once", len(got), err)
	}
}

func TestTrashKeepsTheItemsPropertiesAndReplacesAnOlderEntry(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, _, _ := newTestService(t)

	for range 2 {
		item := deploy(t, s, "t/f.txt", "trash me")
		if err := s.SetProperties(ctx, item, Properties{
			"qa": {
				"passed",
			},
		}, false); err != nil {
			t.Fatal(err)
		}

		if err := s.DeleteToTrash(ctx, "generic-local", "t/f.txt", "alice"); err != nil {
			t.Fatal(err)
		}
	}

	entries, _, err := s.TrashEntries(
		ctx,
		TrashQuery{
			Max: 10,
		},
	)
	if err != nil || len(entries) != 1 || entries[0].DeletedBy != "alice" {
		t.Fatalf("trash = %+v, %v; want one entry deleted by alice", entries, err)
	}

	props, err := s.Properties(ctx, entries[0].Item)
	if err != nil || first(props["qa"]) != "passed" {
		t.Errorf("trashed item's properties = %v, %v; want qa=passed kept", props, err)
	}
}

func TestSHA256OfSHA1(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	item := deploy(t, s, "by-sha1.txt", "named by sha1")

	if got, err := s.SHA256OfSHA1(t.Context(), strings.ToUpper(item.Checksums.SHA1)); err != nil ||
		got != item.Checksums.SHA256 {
		t.Errorf("SHA256OfSHA1 of stored content = %q, %v; want %q", got, err, item.Checksums.SHA256)
	}

	if got, err := s.SHA256OfSHA1(t.Context(), strings.Repeat("0", 40)); err != nil || got != "" {
		t.Errorf("SHA256OfSHA1 of unknown content = %q, %v; want empty and no error", got, err)
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := s.SHA256OfSHA1(cancelled, item.Checksums.SHA1); err == nil {
		t.Error("SHA256OfSHA1 on a failing database reported no error")
	}
}

func TestCopyRefusesADestinationOverlappingTheSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		destination string
		move        bool
	}{
		{
			name:        "copy into the source",
			destination: "a/b/c",
		},
		{
			name:        "copy onto a folder above the source",
			destination: "a",
		},
		{
			name:        "move onto a folder above the source",
			destination: "a",
			move:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, _, _ := newTestService(t)
			deploy(t, s, "a/b/b/f.txt", "nested")
			deploy(t, s, "a/b/f.txt", "kept")

			src, err := s.Stat(t.Context(), "generic-local", "a/b")
			if err != nil {
				t.Fatal(err)
			}

			err = s.Copy(t.Context(), CopyInput{
				Source:                src,
				DestinationRepository: "generic-local",
				DestinationPath:       tt.destination,
				Move:                  tt.move,
			})
			if !errors.Is(err, ErrSelfCopy) {
				t.Errorf("Copy of a/b to %s = %v, want ErrSelfCopy", tt.destination, err)
			}

			for path, want := range map[string]string{
				"a/b/b/f.txt": "nested",
				"a/b/f.txt":   "kept",
			} {
				item, err := s.Stat(t.Context(), "generic-local", path)
				if err != nil {
					t.Errorf("%s: %v", path, err)

					continue
				}

				rc, err := s.Open(t.Context(), item, nil)
				if err != nil {
					t.Fatal(err)
				}

				got, _ := io.ReadAll(rc)
				_ = rc.Close()

				if string(got) != want {
					t.Errorf("%s = %q, want %q", path, got, want)
				}
			}
		})
	}
}

func TestCopyOfASourceDeletedMeanwhileFails(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	item := deploy(t, s, "gone.txt", "gone")

	if err := s.Delete(t.Context(), "generic-local", "gone.txt"); err != nil {
		t.Fatal(err)
	}

	err := s.Copy(t.Context(), CopyInput{
		Source:                item,
		DestinationRepository: "generic-local",
		DestinationPath:       "copy.txt",
	})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Copy of a deleted source = %v, want ErrNotFound", err)
	}
}
