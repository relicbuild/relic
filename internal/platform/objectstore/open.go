package objectstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

// The kinds of object store Open builds.
const (
	TypeS3         = "s3"
	TypeAzure      = "azure"
	TypeGCS        = "gcs"
	TypeFilesystem = "filesystem"
)

// TemporaryTTL is how long a temporary key lives.
const TemporaryTTL = 24 * time.Hour

// Config selects and configures an object store.
type Config struct {
	Type           string
	S3             S3Config
	Azure          AzureConfig
	GCS            GCSConfig
	FilesystemPath string
}

// Open returns the store cfg selects, measured and traced.
func Open(ctx context.Context, cfg Config) (Store, error) {
	switch cfg.Type {
	case TypeS3:
		if cfg.S3.AccessKey == "" {
			awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.S3.Region))
			if err != nil {
				return nil, fmt.Errorf("aws credentials: %w", err)
			}

			cfg.S3.Credentials = awsCfg.Credentials
		}

		return Instrument(NewS3(cfg.S3)), nil
	case TypeAzure:
		s, err := NewAzure(cfg.Azure)
		if err != nil {
			return nil, err
		}

		return Instrument(s), nil
	case TypeGCS:
		s, err := NewGCS(ctx, cfg.GCS)
		if err != nil {
			return nil, err
		}

		return Instrument(s), nil
	case TypeFilesystem:
		s, err := NewFilesystem(cfg.FilesystemPath)
		if err != nil {
			return nil, err
		}

		return Instrument(s), nil
	default:
		return nil, fmt.Errorf(
			"unknown object storage type %q (want %s, %s, %s or %s)",
			cfg.Type,
			TypeS3,
			TypeAzure,
			TypeGCS,
			TypeFilesystem,
		)
	}
}

// TemporarySweeper is a store that expires temporary keys itself.
type TemporarySweeper interface {
	// SweepTemporary deletes temporary keys last written before olderThan
	// ago and returns how many it deleted.
	SweepTemporary(ctx context.Context, olderThan time.Duration) (int, error)
}

// SweepTemporary deletes temporary keys last written before olderThan ago:
// through s itself when it is a TemporarySweeper, by listing them otherwise.
func SweepTemporary(ctx context.Context, s Store, olderThan time.Duration) (int, error) {
	if sw, ok := s.(TemporarySweeper); ok {
		return sw.SweepTemporary(ctx, olderThan)
	}

	return sweepByListing(ctx, s, olderThan)
}

func sweepByListing(ctx context.Context, s Store, olderThan time.Duration) (int, error) {
	cutoff := time.Now().Add(-olderThan)
	removed := 0

	for o, err := range s.List(ctx, TemporaryKeyPrefix) {
		if err != nil {
			return removed, err
		}

		if o.Modified.After(cutoff) {
			continue
		}

		if err := s.Delete(ctx, o.Key); err != nil && !errors.Is(err, ErrNotFound) {
			return removed, err
		}

		removed++
	}

	return removed, nil
}
