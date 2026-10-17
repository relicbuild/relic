package objectstore

import (
	"context"
	"errors"
	"io"
	"iter"
	"strings"
	"time"

	"relic/internal/platform/telemetry"
)

// Instrument wraps s so every operation gets a client span and the
// relic_object_storage_* series.
func Instrument(s Store) Store {
	return instrumented{
		s,
	}
}

type instrumented struct {
	next Store
}

func area(key string) string {
	if strings.HasPrefix(key, TemporaryKeyPrefix) {
		return "temporary"
	}

	return "artifacts"
}

func outcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, ErrNotFound):
		return "miss"
	default:
		return "error"
	}
}

func (i instrumented) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	ctx, end := telemetry.StartObjectStorage(ctx, "put", area(key))
	if _, ok := r.(io.Seeker); ok {
		err := i.next.Put(ctx, key, r, size)
		end(outcome(err))

		if err == nil && size > 0 {
			telemetry.ObjectStorageBytes("put", size)
		}

		return err
	}

	cr := &countingReader{
		r: r,
	}
	err := i.next.Put(ctx, key, cr, size)
	end(outcome(err))

	if err == nil {
		telemetry.ObjectStorageBytes("put", cr.n)
	}

	return err
}

func (i instrumented) Get(ctx context.Context, key string, rng *Range) (io.ReadCloser, Info, error) {
	ctx, end := telemetry.StartObjectStorage(ctx, "get", area(key))
	rc, info, err := i.next.Get(ctx, key, rng)
	end(outcome(err))

	if err != nil {
		return rc, info, err
	}

	return &countingReadCloser{
		ReadCloser: rc,
	}, info, nil
}

func (i instrumented) Head(ctx context.Context, key string) (Info, error) {
	ctx, end := telemetry.StartObjectStorage(ctx, "head", area(key))
	info, err := i.next.Head(ctx, key)
	end(outcome(err))

	return info, err
}

func (i instrumented) Copy(ctx context.Context, srcKey, dstKey string) error {
	ctx, end := telemetry.StartObjectStorage(ctx, "copy", area(dstKey))
	err := i.next.Copy(ctx, srcKey, dstKey)
	end(outcome(err))

	return err
}

func (i instrumented) Delete(ctx context.Context, key string) error {
	ctx, end := telemetry.StartObjectStorage(ctx, "delete", area(key))
	err := i.next.Delete(ctx, key)
	end(outcome(err))

	return err
}

func (i instrumented) List(ctx context.Context, prefix string) iter.Seq2[Object, error] {
	return func(yield func(Object, error) bool) {
		ctx, end := telemetry.StartObjectStorage(ctx, "list", area(prefix))

		var failed error

		for o, err := range i.next.List(ctx, prefix) {
			failed = err
			if !yield(o, err) || err != nil {
				break
			}
		}

		end(outcome(failed))
	}
}

func (i instrumented) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	ctx, end := telemetry.StartObjectStorage(ctx, "presign", area(key))
	u, err := i.next.PresignGet(ctx, key, ttl)
	end(outcome(err))

	return u, err
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)

	return n, err
}

type countingReadCloser struct {
	io.ReadCloser
	n int64
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n += int64(n)

	return n, err
}

func (c *countingReadCloser) Close() error {
	telemetry.ObjectStorageBytes("get", c.n)
	c.n = 0

	return c.ReadCloser.Close()
}

// SweepTemporary implements TemporarySweeper for the store it wraps.
func (i instrumented) SweepTemporary(ctx context.Context, olderThan time.Duration) (int, error) {
	if sw, ok := i.next.(TemporarySweeper); ok {
		ctx, end := telemetry.StartObjectStorage(ctx, "sweep", area(TemporaryKeyPrefix))
		n, err := sw.SweepTemporary(ctx, olderThan)
		end(outcome(err))

		return n, err
	}

	return sweepByListing(ctx, i, olderThan)
}
