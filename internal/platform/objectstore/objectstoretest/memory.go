// Package objectstoretest provides an in-memory blob.Store for tests.
package objectstoretest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"iter"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"relic/internal/platform/objectstore"
)

// Memory is a concurrency-safe in-memory blob.Store.
type Memory struct {
	mu       sync.Mutex
	objects  map[string][]byte
	modified map[string]time.Time
}

// NewMemory returns an empty store.
func NewMemory() *Memory {
	return &Memory{
		objects:  map[string][]byte{},
		modified: map[string]time.Time{},
	}
}

// Put implements blob.Store.
func (m *Memory) Put(ctx context.Context, key string, r io.Reader, _ int64) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.objects[key] = b
	m.modified[key] = time.Now()

	return nil
}

// Get implements blob.Store.
func (m *Memory) Get(_ context.Context, key string, rng *objectstore.Range) (io.ReadCloser, objectstore.Info, error) {
	m.mu.Lock()
	b, ok := m.objects[key]
	m.mu.Unlock()

	if !ok {
		return nil, objectstore.Info{}, fmt.Errorf("get %s: %w", key, objectstore.ErrNotFound)
	}

	if rng != nil {
		b = b[rng.Start : min(rng.End, int64(len(b))-1)+1]
	}

	return io.NopCloser(bytes.NewReader(b)), objectstore.Info{
		Size: int64(len(b)),
	}, nil
}

// Head implements blob.Store.
func (m *Memory) Head(_ context.Context, key string) (objectstore.Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	b, ok := m.objects[key]
	if !ok {
		return objectstore.Info{}, fmt.Errorf("head %s: %w", key, objectstore.ErrNotFound)
	}

	return objectstore.Info{
		Size: int64(len(b)),
	}, nil
}

// Copy implements blob.Store.
func (m *Memory) Copy(_ context.Context, srcKey, dstKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	b, ok := m.objects[srcKey]
	if !ok {
		return fmt.Errorf("copy %s: %w", srcKey, objectstore.ErrNotFound)
	}

	m.objects[dstKey] = slices.Clone(b)
	m.modified[dstKey] = time.Now()

	return nil
}

// Delete implements blob.Store.
func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.objects, key)
	delete(m.modified, key)

	return nil
}

// PresignGet implements blob.Store with a fake URL.
func (m *Memory) PresignGet(_ context.Context, key string, _ time.Duration) (string, error) {
	return "memory://" + key, nil
}

// Keys returns the stored keys in sorted order.
func (m *Memory) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return slices.Sorted(maps.Keys(m.objects))
}

// List implements objectstore.Store.
func (m *Memory) List(_ context.Context, prefix string) iter.Seq2[objectstore.Object, error] {
	return func(yield func(objectstore.Object, error) bool) {
		m.mu.Lock()

		var objects []objectstore.Object

		for _, k := range slices.Sorted(maps.Keys(m.objects)) {
			if strings.HasPrefix(k, prefix) {
				objects = append(objects, objectstore.Object{
					Key:      k,
					Size:     int64(len(m.objects[k])),
					Modified: m.modified[k],
				})
			}
		}

		m.mu.Unlock()

		for _, o := range objects {
			if !yield(o, nil) {
				return
			}
		}
	}
}

// SetModified backdates a stored object.
func (m *Memory) SetModified(key string, t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.modified[key] = t
}
