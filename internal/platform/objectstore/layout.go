package objectstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// LayoutVersion is the version of the key layout this Relic reads and
// writes: content at Key, staging under TemporaryKeyPrefix.
const LayoutVersion = 1

// LayoutKey is the key of the object that records a store's layout version.
const LayoutKey = "layout.json"

// ErrLayout is returned for a store whose layout this Relic does not read.
var ErrLayout = errors.New("object storage has another layout")

type layoutRecord struct {
	Version int `json:"version"`
}

// RecordLayout writes LayoutVersion to a store that records none, and
// returns ErrLayout for a store that records another version and
// ErrNoBucket for one whose bucket does not exist yet.
func RecordLayout(ctx context.Context, store Store) error {
	body, _, err := store.Get(ctx, LayoutKey, nil)
	if errors.Is(err, ErrNoBucket) {
		return fmt.Errorf("reading the object storage layout: %w", err)
	}

	if errors.Is(err, ErrNotFound) {
		record, err := json.Marshal(layoutRecord{
			Version: LayoutVersion,
		})
		if err != nil {
			return fmt.Errorf("encoding the object storage layout: %w", err)
		}

		record = append(record, '\n')
		if err := store.Put(ctx, LayoutKey, bytes.NewReader(record), int64(len(record))); err != nil {
			return fmt.Errorf("recording the object storage layout: %w", err)
		}

		return nil
	}

	if err != nil {
		return fmt.Errorf("reading the object storage layout: %w", err)
	}

	defer func() {
		_ = body.Close()
	}()

	var recorded layoutRecord
	if err := json.NewDecoder(io.LimitReader(body, 1<<10)).Decode(&recorded); err != nil {
		return fmt.Errorf("%w: %s is not a layout record: %w", ErrLayout, LayoutKey, err)
	}

	if recorded.Version != LayoutVersion {
		return fmt.Errorf(
			"%w: %s records version %d, this Relic reads version %d",
			ErrLayout,
			LayoutKey,
			recorded.Version,
			LayoutVersion,
		)
	}

	return nil
}

// AwaitLayout records the layout as RecordLayout does, trying again every
// interval while the bucket does not exist or the store is temporarily
// unavailable, until ctx is done.
func AwaitLayout(ctx context.Context, store Store, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		err := RecordLayout(ctx, store)
		if !errors.Is(err, ErrNoBucket) && !errors.Is(err, ErrTransient) {
			return err
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for the object storage bucket: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
