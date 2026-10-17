//go:build integration

package repository

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore"
	"relic/internal/platform/objectstore/objectstoretest"
)

func newFaultyService(t *testing.T) (*Service, *objectstoretest.Faulty, *objectstoretest.Memory) {
	t.Helper()

	memory := objectstoretest.NewMemory()
	blobs := objectstoretest.NewFaulty(memory)
	s := NewService(databasetest.NewDB(t), blobs, slog.New(slog.DiscardHandler))

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

	return s, blobs, memory
}

func TestOpenVerifiesAFullRead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		fault objectstoretest.Fault
		want  error
	}{
		{
			name: "intact",
		},
		{
			name: "corrupt",
			fault: objectstoretest.Fault{
				Op:      objectstoretest.OpGet,
				Corrupt: true,
			},
			want: ErrContentCorrupt,
		},
		{
			name: "missing",
			fault: objectstoretest.Fault{
				Op:  objectstoretest.OpGet,
				Err: objectstore.ErrNotFound,
			},
			want: ErrContentMissing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, blobs, _ := newFaultyService(t)
			item := deploy(t, s, "f.txt", "verified content")

			if tt.fault.Op != "" {
				blobs.Inject(tt.fault)
			}

			body, err := s.Open(t.Context(), item, nil)
			if err == nil {
				_, err = io.ReadAll(body)
				_ = body.Close()
			}

			if !errors.Is(err, tt.want) || (tt.want == nil && err != nil) {
				t.Fatalf("full read = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestOpenLeavesRangedReadsUnverified(t *testing.T) {
	t.Parallel()

	s, _, _ := newFaultyService(t)
	item := deploy(t, s, "f.txt", "verified content")

	body, err := s.Open(t.Context(), item, &objectstore.Range{
		Start: 0,
		End:   7,
	})
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = body.Close()
	}()

	got, err := io.ReadAll(body)
	if err != nil || string(got) != "verified" {
		t.Fatalf("ranged read = %q, %v", got, err)
	}
}

func TestReaderAtReadsNothingIntoAnEmptyBuffer(t *testing.T) {
	t.Parallel()

	blobs, err := objectstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	s := NewService(databasetest.NewDB(t), blobs, slog.New(slog.DiscardHandler))
	if err := s.CreateRepository(t.Context(), Repository{
		Name:   "generic-local",
		Class:  ClassLocal,
		Format: "generic",
	}); err != nil {
		t.Fatal(err)
	}

	item := deploy(t, s, "f.txt", "some content")

	if n, err := s.ReaderAt(t.Context(), item).ReadAt(nil, 4); n != 0 || err != nil {
		t.Errorf("ReadAt of an empty buffer = %d, %v; want 0, nil", n, err)
	}

	buf := make([]byte, 7)
	if n, err := s.ReaderAt(t.Context(), item).ReadAt(buf, 5); n != 7 || err != nil || string(buf) != "content" {
		t.Errorf("ReadAt = %d, %v, %q; want 7, nil, content", n, err, buf)
	}

	if n, err := s.ReaderAt(t.Context(), item).ReadAt(buf, -3); n != 0 || !errors.Is(err, ErrInvalidOffset) {
		t.Errorf("ReadAt at a negative offset = %d, %v; want 0 and ErrInvalidOffset", n, err)
	}
}

func TestRedeployRepairsATruncatedObject(t *testing.T) {
	t.Parallel()

	s, _, memory := newFaultyService(t)
	item := deploy(t, s, "f.txt", "whole content")
	key := objectstore.Key(item.Checksums.SHA256)

	if err := memory.Put(t.Context(), key, strings.NewReader("whole"), 5); err != nil {
		t.Fatal(err)
	}

	deploy(t, s, "again.txt", "whole content")

	body, _, err := memory.Get(t.Context(), key, nil)
	if err != nil {
		t.Fatal(err)
	}

	got, _ := io.ReadAll(body)
	if !bytes.Equal(got, []byte("whole content")) {
		t.Errorf("stored object after the redeploy = %q, want it repaired", got)
	}
}

func TestDeployRestoresNewContentAfterTheCommit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		failures int
		wantErr  bool
	}{
		{
			name:     "a transient storage error is retried",
			failures: 2,
		},
		{
			name:    "a lasting storage error keeps the staged upload",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, blobs, memory := newFaultyService(t)
			blobs.Inject(objectstoretest.Fault{
				Op:        objectstoretest.OpHead,
				KeyPrefix: "sha256/",
				Skip:      2,
				Times:     tt.failures,
			})

			_, err := s.Deploy(t.Context(), DeployInput{
				Repository: "generic-local",
				Path:       "f.txt",
				Body:       strings.NewReader("new content"),
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("Deploy = %v, want an error: %v", err, tt.wantErr)
			}

			staged := 0

			for _, err := range memory.List(t.Context(), objectstore.TemporaryKeyPrefix) {
				if err != nil {
					t.Fatal(err)
				}

				staged++
			}

			want := 0
			if tt.wantErr {
				want = 1
			}

			if staged != want {
				t.Errorf("%d staged uploads left, want %d", staged, want)
			}
		})
	}
}
