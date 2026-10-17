package objectstore_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"relic/internal/platform/objectstore"
	"relic/internal/platform/objectstore/objectstoretest"
	"relic/internal/platform/telemetry/telemetrytest"
)

func newFilesystem(t *testing.T) *objectstore.FilesystemStore {
	t.Helper()

	s, err := objectstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func TestFilesystemStore(t *testing.T) {
	t.Parallel()

	objectstoretest.RunContract(t, func(t *testing.T) objectstore.Store {
		t.Helper()

		return newFilesystem(t)
	})
}

func TestFilesystemStoreThroughOpen(t *testing.T) {
	t.Parallel()

	objectstoretest.RunContract(t, func(t *testing.T) objectstore.Store {
		t.Helper()

		s, err := objectstore.Open(t.Context(), objectstore.Config{
			Type:           objectstore.TypeFilesystem,
			FilesystemPath: t.TempDir(),
		})
		if err != nil {
			t.Fatal(err)
		}

		return s
	})
}

func TestMemoryStore(t *testing.T) {
	t.Parallel()

	objectstoretest.RunContract(t, func(*testing.T) objectstore.Store {
		return objectstoretest.NewMemory()
	})
}

func TestFilesystemStoreRefusesKeysOutsideItsRoot(t *testing.T) {
	t.Parallel()

	s := newFilesystem(t)
	for _, key := range []string{
		"../escape",
		"sha256/../../escape",
		"/etc/passwd",
		objectstore.TemporaryKeyPrefix + "../escape",
		objectstore.TemporaryKeyPrefix,
		"",
	} {
		if err := s.Put(t.Context(), key, strings.NewReader("x"), 1); err == nil {
			t.Errorf("Put(%q) succeeded", key)
		}

		if _, err := s.Head(t.Context(), key); err == nil || errors.Is(err, objectstore.ErrNotFound) {
			t.Errorf("Head(%q) = %v, want a refusal", key, err)
		}
	}
}

func TestFilesystemStoreSweepsOldTemporaryKeys(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s, err := objectstore.NewFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	for _, key := range []string{
		objectstore.TemporaryKeyPrefix + "old",
		objectstore.TemporaryKeyPrefix + "new",
		objectstore.Key("ab" + strings.Repeat("0", 62)),
	} {
		if err := s.Put(ctx, key, strings.NewReader("x"), 1); err != nil {
			t.Fatal(err)
		}
	}

	old := time.Now().Add(-2 * objectstore.TemporaryTTL)
	stale := filepath.Join(dir, "staging", "put-crashed")

	if err := os.WriteFile(stale, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{
		filepath.Join(dir, "temporary", "old"),
		filepath.Join(dir, "artifacts", "sha256", "ab", "00", "ab"+strings.Repeat("0", 62)),
		stale,
	} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	sweeps := func() float64 {
		return telemetrytest.Value(t, "relic_object_storage_ops_total", map[string]string{
			"op":     "sweep",
			"result": "success",
		})
	}
	sweepsBefore := sweeps()

	n, err := objectstore.SweepTemporary(ctx, objectstore.Instrument(s), objectstore.TemporaryTTL)
	if err != nil || n != 2 {
		t.Fatalf("SweepTemporary = %d, %v; want 2 (the old temporary key and the staging file)", n, err)
	}

	if sweeps()-sweepsBefore < 1 {
		t.Error("the store's own sweep was not counted as an object storage operation")
	}

	if _, err := s.Head(ctx, objectstore.TemporaryKeyPrefix+"old"); !errors.Is(err, objectstore.ErrNotFound) {
		t.Errorf("the old temporary key survived: %v", err)
	}

	for _, key := range []string{
		objectstore.TemporaryKeyPrefix + "new",
		objectstore.Key("ab" + strings.Repeat("0", 62)),
	} {
		if _, err := s.Head(ctx, key); err != nil {
			t.Errorf("%s was swept: %v", key, err)
		}
	}
}

func TestFilesystemStoreCannotPresign(t *testing.T) {
	t.Parallel()

	_, err := newFilesystem(t).PresignGet(t.Context(), "sha256/x", time.Minute)
	if !errors.Is(err, objectstore.ErrPresignNotSupported) {
		t.Errorf("PresignGet = %v, want ErrPresignNotSupported", err)
	}
}

func TestOpen(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     objectstore.Config
		wantErr string
	}{
		{
			name: "s3",
			cfg: objectstore.Config{
				Type: objectstore.TypeS3,
			},
		},
		{
			name: "a relative filesystem path",
			cfg: objectstore.Config{
				Type:           objectstore.TypeFilesystem,
				FilesystemPath: "relative/dir",
			},
			wantErr: "must be absolute",
		},
		{
			name: "an unknown type",
			cfg: objectstore.Config{
				Type: "ftp",
			},
			wantErr: `unknown object storage type "ftp"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := objectstore.Open(t.Context(), tt.cfg)
			if tt.wantErr == "" && err != nil ||
				tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Errorf("Open = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestFilesystemStoreSyncsDirectoriesAfterPublishing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s, err := objectstore.NewFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}

	digest := "abcd" + strings.Repeat("0", 60)
	key := objectstore.Key(digest)
	file := filepath.Join("artifacts", filepath.FromSlash(key))

	var synced []string

	s.ObserveDirectorySyncs(func(d string) {
		synced = append(synced, d)

		if d == filepath.Dir(file) {
			if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
				t.Errorf("directory %s synced before the file was renamed into it: %v", d, err)
			}
		}
	})

	if err := s.Put(t.Context(), objectstore.TemporaryKeyPrefix+"upload", strings.NewReader("content"), 7); err != nil {
		t.Fatal(err)
	}

	if err := s.Copy(t.Context(), objectstore.TemporaryKeyPrefix+"upload", key); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"temporary",
		"artifacts",
		filepath.Join("artifacts", "sha256"),
		filepath.Join("artifacts", "sha256", "ab"),
		filepath.Join("artifacts", "sha256", "ab", "cd"),
	} {
		if !slices.Contains(synced, want) {
			t.Errorf("synced %q, want %s among them", synced, want)
		}
	}
}

// Not parallel: it counts the object storage operations the sweep records.
func TestSweepTemporaryListsStoresWithoutTheirOwnSweep(t *testing.T) {
	ctx := t.Context()
	s := objectstoretest.NewMemory()

	old := objectstore.TemporaryKeyPrefix + "old"
	fresh := objectstore.TemporaryKeyPrefix + "fresh"
	content := objectstore.Key("ab" + strings.Repeat("0", 62))

	for _, k := range []string{
		old,
		fresh,
		content,
	} {
		if err := s.Put(ctx, k, strings.NewReader("x"), 1); err != nil {
			t.Fatal(err)
		}
	}

	s.SetModified(old, time.Now().Add(-2*objectstore.TemporaryTTL))
	s.SetModified(content, time.Now().Add(-2*objectstore.TemporaryTTL))

	ops := func(op string) float64 {
		return telemetrytest.Value(t, "relic_object_storage_ops_total", map[string]string{
			"op":     op,
			"result": "success",
		})
	}
	lists, deletes := ops("list"), ops("delete")

	n, err := objectstore.SweepTemporary(ctx, objectstore.Instrument(s), objectstore.TemporaryTTL)
	if n != 1 || err != nil {
		t.Fatalf("SweepTemporary = %d, %v; want the old temporary key", n, err)
	}

	if ops("list")-lists != 1 || ops("delete")-deletes != 1 {
		t.Errorf("the sweep recorded %v lists and %v deletes, want 1 of each", ops("list")-lists, ops("delete")-deletes)
	}

	if got := s.Keys(); !slices.Equal(got, []string{
		content,
		fresh,
	}) {
		t.Errorf("keys left = %v", got)
	}
}
