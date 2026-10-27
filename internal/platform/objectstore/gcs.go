package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

const gcsChunkSize = 16 << 20

// GCSConfig configures a GCSStore.
type GCSConfig struct {
	// Endpoint is an emulator's URL; empty uses Google Cloud Storage with
	// Application Default Credentials (workload identity, a key file).
	Endpoint             string
	ArtifactsBucket      string
	TemporaryBucket      string
	MaxConcurrentUploads int
}

// GCSStore implements Store on Google Cloud Storage.
type GCSStore struct {
	client     *storage.Client
	uploads    uploadGate
	blobs, tmp string
}

// NewGCS returns a store for cfg.
func NewGCS(ctx context.Context, cfg GCSConfig) (*GCSStore, error) {
	var opts []option.ClientOption
	if cfg.Endpoint != "" {
		opts = append(
			opts,
			option.WithEndpoint(strings.TrimSuffix(cfg.Endpoint, "/")+"/storage/v1/"),
			option.WithoutAuthentication(),
			storage.WithJSONReads(),
		)
	}

	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("google cloud storage: %w", err)
	}

	return &GCSStore{
		client:  client,
		uploads: newUploadGate(cfg.MaxConcurrentUploads),
		blobs:   cfg.ArtifactsBucket,
		tmp:     cfg.TemporaryBucket,
	}, nil
}

func (s *GCSStore) locate(key string) (bucket, name string) {
	if rest, ok := strings.CutPrefix(key, TemporaryKeyPrefix); ok {
		return s.tmp, rest
	}

	return s.blobs, key
}

func (s *GCSStore) object(key string) *storage.ObjectHandle {
	bucket, name := s.locate(key)

	return s.client.Bucket(bucket).Object(name)
}

// Put implements Store. A failed read of r cancels the upload, so the key
// is left as it was.
func (s *GCSStore) Put(ctx context.Context, key string, r io.Reader, _ int64) error {
	release, err := s.uploads.acquire(ctx, key)
	if err != nil {
		return err
	}
	defer release()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	w := s.object(key).NewWriter(ctx)
	w.ChunkSize = gcsChunkSize

	if _, err := io.Copy(w, r); err != nil {
		cancel()

		_ = w.Close()

		return fmt.Errorf("put %s: %w", key, gcsErr(err))
	}

	if err := w.Close(); err != nil {
		return fmt.Errorf("put %s: %w", key, gcsErr(err))
	}

	return nil
}

// Get implements Store.
func (s *GCSStore) Get(ctx context.Context, key string, rng *Range) (io.ReadCloser, Info, error) {
	offset, length := int64(0), int64(-1)
	if rng != nil {
		offset, length = rng.Start, rng.End-rng.Start+1
	}

	r, err := s.object(key).NewRangeReader(ctx, offset, length)
	if err != nil {
		return nil, Info{}, fmt.Errorf("get %s: %w", key, gcsErr(err))
	}

	return r, Info{
		Size: r.Remain(),
	}, nil
}

// Head implements Store.
func (s *GCSStore) Head(ctx context.Context, key string) (Info, error) {
	attrs, err := s.object(key).Attrs(ctx)
	if err != nil {
		return Info{}, fmt.Errorf("head %s: %w", key, gcsErr(err))
	}

	return Info{
		Size: attrs.Size,
	}, nil
}

// Copy implements Store with a server-side copy (a rewrite, any size).
func (s *GCSStore) Copy(ctx context.Context, srcKey, dstKey string) error {
	if _, err := s.object(dstKey).CopierFrom(s.object(srcKey)).Run(ctx); err != nil {
		return fmt.Errorf("copy %s to %s: %w", srcKey, dstKey, gcsErr(err))
	}

	return nil
}

// Delete implements Store; a missing key is not an error.
func (s *GCSStore) Delete(ctx context.Context, key string) error {
	err := s.object(key).Delete(ctx)
	if err != nil && !errors.Is(err, storage.ErrObjectNotExist) {
		return fmt.Errorf("delete %s: %w", key, gcsErr(err))
	}

	return nil
}

// PresignGet implements Store with a V4 signed URL, signed by the
// credentials' service account.
func (s *GCSStore) PresignGet(_ context.Context, key string, ttl time.Duration) (string, error) {
	bucket, name := s.locate(key)

	u, err := s.client.Bucket(bucket).SignedURL(name, &storage.SignedURLOptions{
		Method:  http.MethodGet,
		Expires: time.Now().Add(ttl),
		Scheme:  storage.SigningSchemeV4,
	})
	if err != nil {
		return "", fmt.Errorf("presign %s: %w", key, err)
	}

	return u, nil
}

// List implements Store.
func (s *GCSStore) List(ctx context.Context, prefix string) iter.Seq2[Object, error] {
	return func(yield func(Object, error) bool) {
		bucket, namePrefix := s.locate(prefix)
		keyPrefix := strings.TrimSuffix(prefix, namePrefix)

		it := s.client.Bucket(bucket).Objects(ctx, &storage.Query{
			Prefix: namePrefix,
		})
		for {
			attrs, err := it.Next()
			if errors.Is(err, iterator.Done) {
				return
			}

			if err != nil {
				yield(Object{}, fmt.Errorf("list %s: %w", prefix, gcsErr(err)))
				return
			}

			if !yield(Object{
				Key:      keyPrefix + attrs.Name,
				Size:     attrs.Size,
				Modified: attrs.Updated,
			}, nil) {
				return
			}
		}
	}
}

func gcsErr(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}

	if errors.Is(err, storage.ErrBucketNotExist) {
		return errors.Join(ErrNotFound, ErrNoBucket, err)
	}

	if errors.Is(err, storage.ErrObjectNotExist) {
		return errors.Join(ErrNotFound, err)
	}

	if apiErr, ok := errors.AsType[*googleapi.Error](err); ok {
		if apiErr.Code == http.StatusNotFound {
			return errors.Join(ErrNotFound, err)
		}

		if apiErr.Code >= 500 || apiErr.Code == http.StatusTooManyRequests {
			return errors.Join(ErrTransient, err)
		}
	}

	return err
}
