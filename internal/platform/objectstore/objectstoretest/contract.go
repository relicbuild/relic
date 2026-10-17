package objectstoretest

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"relic/internal/platform/objectstore"
)

// RunContract checks that the stores newStore returns behave as
// objectstore.Store promises.
func RunContract(t *testing.T, newStore func(t *testing.T) objectstore.Store) {
	t.Helper()

	t.Run("list in key order", func(t *testing.T) {
		t.Parallel()

		s := newStore(t)
		ctx := t.Context()

		keys := []string{
			objectstore.Key("cc" + strings.Repeat("0", 62)),
			objectstore.Key("aa" + strings.Repeat("0", 62)),
			objectstore.Key("ab" + strings.Repeat("1", 62)),
			objectstore.TemporaryKeyPrefix + "upload-b",
			objectstore.TemporaryKeyPrefix + "upload-a",
		}
		for i, k := range keys {
			if err := s.Put(ctx, k, strings.NewReader(strings.Repeat("x", i+1)), int64(i+1)); err != nil {
				t.Fatalf("Put(%s): %v", k, err)
			}
		}

		for _, tt := range []struct {
			prefix string
			want   []string
			sizes  []int64
		}{
			{
				prefix: "sha256/",
				want: []string{
					keys[1],
					keys[2],
					keys[0],
				},
				sizes: []int64{
					2,
					3,
					1,
				},
			},
			{
				prefix: objectstore.TemporaryKeyPrefix,
				want: []string{
					keys[4],
					keys[3],
				},
				sizes: []int64{
					5,
					4,
				},
			},
		} {
			var (
				got   []string
				sizes []int64
			)

			for o, err := range s.List(ctx, tt.prefix) {
				if err != nil {
					t.Fatalf("List(%s): %v", tt.prefix, err)
				}

				if time.Since(o.Modified) > time.Hour {
					t.Errorf("List(%s): %s modified %v", tt.prefix, o.Key, o.Modified)
				}

				got, sizes = append(got, o.Key), append(sizes, o.Size)
			}

			if !slices.Equal(got, tt.want) || !slices.Equal(sizes, tt.sizes) {
				t.Errorf("List(%s) = %v %v, want %v %v", tt.prefix, got, sizes, tt.want, tt.sizes)
			}
		}
	})

	t.Run("put, head, copy, ranged and full reads, delete", func(t *testing.T) {
		t.Parallel()

		s := newStore(t)
		ctx := t.Context()
		big := randomBytes(t, 12<<20)

		tmpKey := objectstore.TemporaryKeyPrefix + "upload-1"
		if err := s.Put(ctx, tmpKey, bytes.NewReader(big), -1); err != nil {
			t.Fatalf("Put: %v", err)
		}

		info, err := s.Head(ctx, tmpKey)
		if err != nil || info.Size != int64(len(big)) {
			t.Fatalf("Head = %+v, %v", info, err)
		}

		key := objectstore.Key("ab" + strings.Repeat("0", 62))
		if err := s.Copy(ctx, tmpKey, key); err != nil {
			t.Fatalf("Copy: %v", err)
		}

		if got := Read(t, s, key, &objectstore.Range{
			Start: 100,
			End:   199,
		}); got != string(big[100:200]) {
			t.Error("ranged read of the copy differs")
		}

		if got := Read(t, s, key, nil); got != string(big) {
			t.Error("full read of the copy differs")
		}

		for _, k := range []string{
			tmpKey,
			key,
		} {
			if err := s.Delete(ctx, k); err != nil {
				t.Fatalf("Delete(%s): %v", k, err)
			}

			if _, err := s.Head(ctx, k); !errors.Is(err, objectstore.ErrNotFound) {
				t.Errorf("Head after delete = %v, want ErrNotFound", err)
			}
		}

		if _, _, err := s.Get(ctx, key, nil); !errors.Is(err, objectstore.ErrNotFound) {
			t.Errorf("Get after delete = %v, want ErrNotFound", err)
		}

		if err := s.Delete(ctx, objectstore.Key("cd"+strings.Repeat("0", 62))); err != nil {
			t.Errorf("deleting a missing key = %v, want nil", err)
		}
	})

	t.Run("a range past the end is cut at the end", func(t *testing.T) {
		t.Parallel()

		s := newStore(t)
		key := objectstore.Key("ef" + strings.Repeat("0", 62))
		put(t, s, key, "0123456789")

		if got := Read(t, s, key, &objectstore.Range{
			Start: 5,
			End:   100,
		}); got != "56789" {
			t.Errorf("range 5-100 of 10 bytes = %q, want 56789", got)
		}
	})

	t.Run("the layout record is apart from content", func(t *testing.T) {
		t.Parallel()

		s := newStore(t)

		for range 2 {
			if err := objectstore.RecordLayout(t.Context(), s); err != nil {
				t.Fatalf("RecordLayout: %v", err)
			}
		}

		if got := Read(t, s, objectstore.LayoutKey, nil); got != "{\"version\":1}\n" {
			t.Errorf("the store records %q", got)
		}

		for o, err := range s.List(t.Context(), "sha256/") {
			t.Errorf("the record is listed as content: %v %v", o, err)
		}
	})

	t.Run("temporary keys are apart from content", func(t *testing.T) {
		t.Parallel()

		s := newStore(t)
		put(t, s, objectstore.TemporaryKeyPrefix+"x", "staged")

		if _, err := s.Head(t.Context(), "x"); !errors.Is(err, objectstore.ErrNotFound) {
			t.Errorf("a temporary key is visible without its prefix: %v", err)
		}
	})

	t.Run("a put replaces the key", func(t *testing.T) {
		t.Parallel()

		s := newStore(t)
		key := objectstore.TemporaryKeyPrefix + "replaced"
		put(t, s, key, "first")
		put(t, s, key, "second")

		if got := Read(t, s, key, nil); got != "second" {
			t.Errorf("read after the second put = %q", got)
		}
	})

	t.Run("a copy onto an existing key replaces it", func(t *testing.T) {
		t.Parallel()

		s := newStore(t)
		src := objectstore.TemporaryKeyPrefix + "copy-source"
		dst := objectstore.TemporaryKeyPrefix + "copy-target"

		put(t, s, src, "new")
		put(t, s, dst, "old")

		if err := s.Copy(t.Context(), src, dst); err != nil {
			t.Fatal(err)
		}

		if got := Read(t, s, dst, nil); got != "new" {
			t.Errorf("target after the copy = %q", got)
		}
	})

	t.Run("copying a missing key is ErrNotFound", func(t *testing.T) {
		t.Parallel()

		s := newStore(t)

		err := s.Copy(t.Context(), objectstore.TemporaryKeyPrefix+"missing", objectstore.TemporaryKeyPrefix+"to")
		if !errors.Is(err, objectstore.ErrNotFound) {
			t.Errorf("Copy = %v, want ErrNotFound", err)
		}
	})

	t.Run("a failed put leaves no key", func(t *testing.T) {
		t.Parallel()

		s := newStore(t)
		key := objectstore.TemporaryKeyPrefix + "failed"

		err := s.Put(t.Context(), key, io.MultiReader(strings.NewReader("partial"), failingReader{}), -1)
		if err == nil {
			t.Fatal("Put of a failing body succeeded")
		}

		if _, err := s.Head(t.Context(), key); !errors.Is(err, objectstore.ErrNotFound) {
			t.Errorf("Head after a failed put = %v, want ErrNotFound", err)
		}
	})

	t.Run("concurrent puts of one key leave one whole body", func(t *testing.T) {
		t.Parallel()

		s := newStore(t)
		key := objectstore.TemporaryKeyPrefix + "raced"
		bodies := make([]string, 8)

		var wg sync.WaitGroup

		for i := range bodies {
			bodies[i] = strings.Repeat(fmt.Sprint(i), 256<<10)

			wg.Go(func() {
				if err := s.Put(t.Context(), key, strings.NewReader(bodies[i]), int64(len(bodies[i]))); err != nil {
					t.Errorf("Put %d: %v", i, err)
				}
			})
		}

		wg.Wait()

		got := Read(t, s, key, nil)
		for _, b := range bodies {
			if got == b {
				return
			}
		}

		t.Errorf("the key holds a mix of bodies (%d bytes)", len(got))
	})
}

// Read returns the content of key, or of rng of it, failing the test on an
// error.
func Read(t *testing.T, s objectstore.Store, key string, rng *objectstore.Range) string {
	t.Helper()

	rc, _, err := s.Get(t.Context(), key, rng)
	if err != nil {
		t.Fatalf("Get(%s): %v", key, err)
	}

	defer func() {
		_ = rc.Close()
	}()

	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

func put(t *testing.T, s objectstore.Store, key, body string) {
	t.Helper()

	if err := s.Put(t.Context(), key, strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("Put(%s): %v", key, err)
	}
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()

	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}

	return b
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("the body broke off")
}
