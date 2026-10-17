// Package objectstore stores artifact content in an S3-compatible object store.
package objectstore

import (
	"context"
	"errors"
	"io"
	"iter"
	"path"
	"time"
)

// The classes of object storage errors: callers retry ErrTransient and never
// read it as ErrNotFound.
var (
	ErrNotFound    = errors.New("blob not found")
	ErrNoBucket    = errors.New("the bucket or container does not exist")
	ErrTransient   = errors.New("object storage is temporarily unavailable")
	ErrStorageFull = errors.New("object storage is full")
)

// TemporaryKeyPrefix marks staging keys.
const TemporaryKeyPrefix = "tmp/"

// Info describes a stored object.
type Info struct {
	Size int64
}

// Range is an inclusive byte range, as in an HTTP Range header.
type Range struct {
	Start, End int64
}

// Store is the storage backend contract.
type Store interface {
	// Put streams r to key. size is -1 when unknown.
	Put(ctx context.Context, key string, r io.Reader, size int64) error
	// Get opens key, or the given range of it when rng is non-nil.
	Get(ctx context.Context, key string, rng *Range) (io.ReadCloser, Info, error)
	Head(ctx context.Context, key string) (Info, error)
	Copy(ctx context.Context, srcKey, dstKey string) error
	Delete(ctx context.Context, key string) error
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
	// List yields the objects under prefix, a directory such as "sha256/" or
	// TemporaryKeyPrefix, in key order.
	List(ctx context.Context, prefix string) iter.Seq2[Object, error]
}

// Object is a stored object as a listing returns it.
type Object struct {
	Key      string
	Size     int64
	Modified time.Time
}

// Key returns the content-addressed key for a SHA-256 hex digest:
// sha256/<aa>/<bb>/<digest>.
func Key(sha256Hex string) string {
	return path.Join("sha256", sha256Hex[0:2], sha256Hex[2:4], sha256Hex)
}
