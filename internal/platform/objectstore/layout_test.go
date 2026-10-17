package objectstore_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"relic/internal/platform/objectstore"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestRecordLayout(t *testing.T) {
	t.Parallel()

	const current = `{"version":1}` + "\n"

	content := objectstore.Key("ab" + strings.Repeat("0", 62))

	tests := []struct {
		name       string
		stored     map[string]string
		wantErr    error
		wantRecord string
	}{
		{
			name:       "an empty store gets the record",
			wantRecord: current,
		},
		{
			name: "a store with content and no record gets it",
			stored: map[string]string{
				content: "bytes",
			},
			wantRecord: current,
		},
		{
			name: "a store that records this version is left as it is",
			stored: map[string]string{
				objectstore.LayoutKey: `{"version": 1, "writtenBy": "another node"}`,
			},
			wantRecord: `{"version": 1, "writtenBy": "another node"}`,
		},
		{
			name: "a store that records a later version is refused",
			stored: map[string]string{
				objectstore.LayoutKey: `{"version":2}`,
			},
			wantErr:    objectstore.ErrLayout,
			wantRecord: `{"version":2}`,
		},
		{
			name: "a store that records an earlier version is refused",
			stored: map[string]string{
				objectstore.LayoutKey: `{"version":0}`,
			},
			wantErr:    objectstore.ErrLayout,
			wantRecord: `{"version":0}`,
		},
		{
			name: "a record without a version is refused",
			stored: map[string]string{
				objectstore.LayoutKey: `{}`,
			},
			wantErr:    objectstore.ErrLayout,
			wantRecord: `{}`,
		},
		{
			name: "a record that cannot be read is refused",
			stored: map[string]string{
				objectstore.LayoutKey: "not json",
			},
			wantErr:    objectstore.ErrLayout,
			wantRecord: "not json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := objectstoretest.NewMemory()
			for key, text := range tt.stored {
				if err := store.Put(t.Context(), key, strings.NewReader(text), int64(len(text))); err != nil {
					t.Fatal(err)
				}
			}

			if err := objectstore.RecordLayout(t.Context(), store); !errors.Is(err, tt.wantErr) {
				t.Errorf("RecordLayout = %v, want %v", err, tt.wantErr)
			}

			if got := objectstoretest.Read(t, store, objectstore.LayoutKey, nil); got != tt.wantRecord {
				t.Errorf("the store records %q, want %q", got, tt.wantRecord)
			}
		})
	}
}

type bucketCreatedLater struct {
	objectstore.Store

	mu           sync.Mutex
	missingTries int
	reads        int
}

func (s *bucketCreatedLater) Get(
	ctx context.Context,
	key string,
	byteRange *objectstore.Range,
) (io.ReadCloser, objectstore.Info, error) {
	s.mu.Lock()
	s.reads++
	missing := s.reads <= s.missingTries
	s.mu.Unlock()

	if missing {
		return nil, objectstore.Info{}, fmt.Errorf("get %s: %w", key, objectstore.ErrNoBucket)
	}

	return s.Store.Get(ctx, key, byteRange)
}

func TestRecordLayoutWithoutTheBucket(t *testing.T) {
	t.Parallel()

	store := &bucketCreatedLater{
		Store:        objectstoretest.NewMemory(),
		missingTries: 1,
	}

	if err := objectstore.RecordLayout(t.Context(), store); !errors.Is(err, objectstore.ErrNoBucket) {
		t.Errorf("RecordLayout without the bucket = %v, want ErrNoBucket", err)
	}
}

func TestRecordLayoutWhenTheRecordCannotBeRead(t *testing.T) {
	t.Parallel()

	memory := objectstoretest.NewMemory()
	store := objectstoretest.NewFaulty(memory)
	store.Inject(objectstoretest.Fault{
		Op:        objectstoretest.OpGet,
		KeyPrefix: objectstore.LayoutKey,
	})

	err := objectstore.RecordLayout(t.Context(), store)
	if !errors.Is(err, objectstoretest.ErrInjected) || errors.Is(err, objectstore.ErrLayout) {
		t.Errorf("RecordLayout over a failing read = %v, want the read's error", err)
	}

	if _, err := memory.Head(t.Context(), objectstore.LayoutKey); !errors.Is(err, objectstore.ErrNotFound) {
		t.Errorf("a record was written after the failing read: %v", err)
	}
}

func TestAwaitLayout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		stored     string
		wantErr    error
		wantRecord string
	}{
		{
			name:       "the record is written once the bucket exists",
			wantRecord: `{"version":1}` + "\n",
		},
		{
			name:       "a bucket that records another layout is refused",
			stored:     `{"version":2}`,
			wantErr:    objectstore.ErrLayout,
			wantRecord: `{"version":2}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := &bucketCreatedLater{
				Store:        objectstoretest.NewMemory(),
				missingTries: 3,
			}

			if tt.stored != "" {
				if err := store.Put(
					t.Context(),
					objectstore.LayoutKey,
					strings.NewReader(tt.stored),
					int64(len(tt.stored)),
				); err != nil {
					t.Fatal(err)
				}
			}

			if err := objectstore.AwaitLayout(t.Context(), store, time.Millisecond); !errors.Is(err, tt.wantErr) {
				t.Errorf("AwaitLayout = %v, want %v", err, tt.wantErr)
			}

			if got := objectstoretest.Read(t, store, objectstore.LayoutKey, nil); got != tt.wantRecord {
				t.Errorf("the store records %q, want %q", got, tt.wantRecord)
			}
		})
	}
}

func TestAwaitLayoutStopsWithItsContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	store := &bucketCreatedLater{
		Store:        objectstoretest.NewMemory(),
		missingTries: 1 << 30,
	}

	if err := objectstore.AwaitLayout(ctx, store, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("AwaitLayout after cancel = %v, want context.Canceled", err)
	}
}
