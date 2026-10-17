package repository

import (
	"context"
	"fmt"
	"io"

	"relic/internal/platform/objectstore"
)

// ReaderAt reads a stored file by byte ranges, e.g. so zip readers fetch
// the central directory without downloading the whole file.
func (s *Service) ReaderAt(ctx context.Context, item Item) io.ReaderAt {
	return rangeReader{
		ctx:  ctx,
		s:    s,
		item: item,
	}
}

type rangeReader struct {
	ctx  context.Context //nolint:containedctx // io.ReaderAt takes no context per call
	s    *Service
	item Item
}

func (r rangeReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("%w: %d", ErrInvalidOffset, off)
	}

	if off >= r.item.Size {
		return 0, io.EOF
	}

	if len(p) == 0 {
		return 0, nil
	}

	end := min(off+int64(len(p)), r.item.Size) - 1

	rc, err := r.s.Open(r.ctx, r.item, &objectstore.Range{
		Start: off,
		End:   end,
	})
	if err != nil {
		return 0, fmt.Errorf("read range: %w", err)
	}

	defer func() {
		_ = rc.Close()
	}()

	n, err := io.ReadFull(rc, p[:end-off+1])
	if err == nil && n < len(p) {
		err = io.EOF
	}

	return n, err
}

// ReadSeeker opens a stored file for sequential reads from any offset, one
// store read per seek; reads from offset 0 are verified (Open).
func (s *Service) ReadSeeker(ctx context.Context, item Item) (io.ReadSeekCloser, error) {
	r := &itemReader{
		ctx:  ctx,
		s:    s,
		item: item,
	}

	if item.Size == 0 {
		return r, nil
	}

	body, err := s.Open(ctx, item, nil)
	if err != nil {
		return nil, err
	}

	r.body = body

	return r, nil
}

type itemReader struct {
	ctx  context.Context //nolint:containedctx // io.ReadSeeker takes no context per call
	s    *Service
	item Item
	pos  int64
	body io.ReadCloser
}

func (r *itemReader) Read(p []byte) (int, error) {
	if r.pos >= r.item.Size {
		return 0, io.EOF
	}

	if r.body == nil {
		var rng *objectstore.Range
		if r.pos > 0 {
			rng = &objectstore.Range{
				Start: r.pos,
				End:   r.item.Size - 1,
			}
		}

		body, err := r.s.Open(r.ctx, r.item, rng)
		if err != nil {
			return 0, err
		}

		r.body = body
	}

	n, err := r.body.Read(p)
	r.pos += int64(n)

	return n, err
}

func (r *itemReader) Seek(offset int64, whence int) (int64, error) {
	pos := offset

	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		pos += r.pos
	case io.SeekEnd:
		pos += r.item.Size
	default:
		return 0, fmt.Errorf("seek: invalid whence %d", whence)
	}

	if pos < 0 {
		return 0, fmt.Errorf("seek: negative position %d", pos)
	}

	if pos != r.pos {
		_ = r.Close()
		r.pos = pos
	}

	return pos, nil
}

func (r *itemReader) Close() error {
	if r.body == nil {
		return nil
	}

	err := r.body.Close()
	r.body = nil

	return err
}
