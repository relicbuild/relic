package huggingface

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"go.opentelemetry.io/otel/trace"

	"relic/internal/artifacts/formats/huggingface/xet"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/objectstore"
	"relic/internal/platform/telemetry"
)

const (
	xetStaging = ".hf-xet"
	// xetIndexCacheSize bounds the xorb indexes one shard keeps in memory.
	xetIndexCacheSize = 64
)

func xorbPath(h xet.Hash) string { return xetStaging + "/xorbs/" + h.String() }

func xorbIndexPath(h xet.Hash) string { return xorbPath(h) + ".index" }

// XorbStored reports whether repoKey holds the xorb with hash h.
func (s *Service) XorbStored(ctx context.Context, repoKey string, h xet.Hash) bool {
	_, err := s.repos.Stat(ctx, repoKey, xorbIndexPath(h))
	return err == nil
}

// XorbSize is the length of the xorb with hash h in repoKey.
func (s *Service) XorbSize(ctx context.Context, repoKey string, h xet.Hash) (int64, error) {
	if !s.XorbStored(ctx, repoKey, h) {
		return 0, repository.ErrNotFound
	}

	item, err := s.repos.Stat(ctx, repoKey, xorbPath(h))
	if err != nil {
		return 0, err
	}

	return item.Size, nil
}

// StoreXorb stores a xorb a client uploaded to repoKey, with the index of
// its chunks, after checking it hashes to h. It reports false when repoKey
// already held the xorb.
func (s *Service) StoreXorb(
	ctx context.Context,
	repoKey string,
	h xet.Hash,
	body io.Reader,
	user string,
) (inserted bool, err error) {
	ctx, span := telemetry.Tracer().Start(
		ctx,
		"huggingface.StoreXorb",
		trace.WithAttributes(telemetry.AttrRepository.String(repoKey)),
	)

	var size int64

	defer func() {
		telemetry.XetUploaded("xorb", size, err)
		telemetry.End(span, err)
	}()

	if s.XorbStored(ctx, repoKey, h) {
		return false, nil
	}

	checked := xet.NewXorbReader(body, h)

	item, err := s.repos.Deploy(
		ctx,
		repository.DeployInput{
			Repository: repoKey,
			Path:       xorbPath(h),
			Body:       checked,
			User:       user,
		},
	)
	if err != nil {
		return false, fmt.Errorf("store xorb %s: %w", h, err)
	}

	index, err := checked.Index().MarshalBinary()
	if err != nil {
		return false, err
	}

	if _, err := s.repos.Deploy(
		ctx,
		repository.DeployInput{
			Repository: repoKey,
			Path:       xorbIndexPath(h),
			Body:       bytes.NewReader(index),
			Size:       int64(len(index)),
			User:       user,
		},
	); err != nil {
		return false, fmt.Errorf("store xorb %s index: %w", h, err)
	}

	size = item.Size

	return true, nil
}

// xorbIndexes reads the chunk indexes of a repository's xorbs, keeping the
// latest ones.
type xorbIndexes struct {
	service *Service
	repoKey string
	cached  map[xet.Hash]xet.Index
}

func (x *xorbIndexes) index(ctx context.Context, h xet.Hash) (xet.Index, error) {
	if index, ok := x.cached[h]; ok {
		return index, nil
	}

	item, err := x.service.repos.Stat(ctx, x.repoKey, xorbIndexPath(h))
	if errors.Is(err, repository.ErrNotFound) {
		return nil, fmt.Errorf("%w: xorb %s was never uploaded", xet.ErrInvalid, h)
	}

	if err != nil {
		return nil, err
	}

	rc, err := x.service.repos.Open(ctx, item, nil)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = rc.Close()
	}()

	encoded, err := io.ReadAll(io.LimitReader(rc, xet.MaxXorbChunks*64))
	if err != nil {
		return nil, fmt.Errorf("read xorb %s index: %w", h, err)
	}

	index, err := xet.ParseIndex(encoded)
	if err != nil {
		return nil, err
	}

	if len(x.cached) >= xetIndexCacheSize {
		clear(x.cached)
	}

	x.cached[h] = index

	return index, nil
}

// RegisterShard registers the files of a shard a client uploaded to
// repoKey: each file's terms must point at chunks of xorbs repoKey holds,
// with matching verification and file hashes, and each file is rebuilt
// from them and staged as the LFS object its SHA-256 names, for a commit
// to link.
func (s *Service) RegisterShard(ctx context.Context, repoKey string, body io.Reader, user string) (err error) {
	ctx, span := telemetry.Tracer().Start(
		ctx,
		"huggingface.RegisterShard",
		trace.WithAttributes(telemetry.AttrRepository.String(repoKey)),
	)
	started := time.Now()

	counted := &countingReader{
		src: repository.LimitedBody(body, xet.MaxShardSize),
	}

	defer func() {
		telemetry.XetUploaded("shard", counted.read, err)
		telemetry.XetShardRegistered(ctx, err, time.Since(started))
		telemetry.End(span, err)
	}()

	shard, err := xet.ParseShard(counted)
	if err != nil {
		return err
	}

	indexes := &xorbIndexes{
		service: s,
		repoKey: repoKey,
		cached:  map[xet.Hash]xet.Index{},
	}

	for _, listed := range shard.Xorbs {
		if !s.XorbStored(ctx, repoKey, listed.Hash) {
			return fmt.Errorf("%w: xorb %s was never uploaded", xet.ErrInvalid, listed.Hash)
		}
	}

	for _, f := range shard.Files {
		if err := s.registerFile(ctx, repoKey, indexes, f, user); err != nil {
			return err
		}
	}

	return nil
}

type countingReader struct {
	src  io.Reader
	read int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.src.Read(p)
	c.read += int64(n)

	return n, err
}

func (s *Service) registerFile(
	ctx context.Context,
	repoKey string,
	indexes *xorbIndexes,
	f xet.File,
	user string,
) error {
	if !f.Verification {
		return fmt.Errorf("%w: file %s has no verification hashes", xet.ErrInvalid, f.Hash)
	}

	if !oidPattern.MatchString(f.SHA256) {
		return fmt.Errorf("%w: file %s has no SHA-256", xet.ErrInvalid, f.Hash)
	}

	var chunks []xet.Chunk

	for _, t := range f.Terms {
		index, err := indexes.index(ctx, t.Xorb)
		if err != nil {
			return err
		}

		if int(t.End) > len(index) {
			return fmt.Errorf("%w: file %s takes chunks past the end of xorb %s", xet.ErrInvalid, f.Hash, t.Xorb)
		}

		taken := index[t.Start:t.End]

		hashes := make([]xet.Hash, len(taken))

		var size uint64

		for i, e := range taken {
			hashes[i] = e.Hash
			size += uint64(e.Size)
		}

		if size != uint64(t.Size) {
			return fmt.Errorf(
				"%w: file %s claims %d bytes of xorb %s, not %d",
				xet.ErrInvalid,
				f.Hash,
				t.Size,
				t.Xorb,
				size,
			)
		}

		if xet.VerificationHash(hashes) != t.Verification {
			return fmt.Errorf("%w: file %s has a wrong verification hash", xet.ErrInvalid, f.Hash)
		}

		chunks = append(chunks, taken.Chunks()...)
	}

	if got := xet.FileHash(chunks); got != f.Hash {
		return fmt.Errorf("%w: the terms of file %s hash to %s", xet.ErrInvalid, f.Hash, got)
	}

	if s.StagedLFS(ctx, repoKey, f.SHA256) {
		return nil
	}

	inRepo, err := s.repos.FindBySHA256(ctx, repoKey, "", f.SHA256)
	if err != nil {
		return err
	}

	if len(inRepo) > 0 {
		return nil
	}

	return s.rebuildFile(ctx, repoKey, indexes, f, user)
}

func (s *Service) rebuildFile(
	ctx context.Context,
	repoKey string,
	indexes *xorbIndexes,
	f xet.File,
	user string,
) (err error) {
	ctx, span := telemetry.Tracer().Start(
		ctx,
		"huggingface.RebuildFile",
		trace.WithAttributes(telemetry.AttrRepository.String(repoKey)),
	)
	defer func() {
		telemetry.End(span, err)
	}()

	pr, pw := io.Pipe()

	go func() {
		pw.CloseWithError(s.writeTerms(ctx, repoKey, indexes, f.Terms, pw))
	}()

	defer func() {
		_ = pr.Close()
	}()

	item, err := s.repos.Deploy(
		ctx,
		repository.DeployInput{
			Repository: repoKey,
			Path:       lfsStaging + "/" + f.SHA256,
			Body:       pr,
			Size:       int64(f.Size()), //nolint:gosec // a file's size fits int64
			Expected: repository.Checksums{
				SHA256: f.SHA256,
			},
			User: user,
		},
	)
	if errors.Is(err, repository.ErrChecksumMismatch) {
		return fmt.Errorf("%w: file %s does not have SHA-256 %s", xet.ErrInvalid, f.Hash, f.SHA256)
	}

	if err != nil {
		return fmt.Errorf("rebuild file %s: %w", f.Hash, err)
	}

	telemetry.XetFileRebuilt(item.Size)

	return nil
}

// writeTerms writes the data of terms to w, chunk by chunk.
func (s *Service) writeTerms(
	ctx context.Context,
	repoKey string,
	indexes *xorbIndexes,
	terms []xet.Term,
	w io.Writer,
) error {
	for _, t := range terms {
		index, err := indexes.index(ctx, t.Xorb)
		if err != nil {
			return err
		}

		if err := s.writeTerm(ctx, repoKey, index, t, w); err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) writeTerm(ctx context.Context, repoKey string, index xet.Index, t xet.Term, w io.Writer) error {
	item, err := s.repos.Stat(ctx, repoKey, xorbPath(t.Xorb))
	if err != nil {
		return fmt.Errorf("xorb %s: %w", t.Xorb, err)
	}

	rc, err := s.repos.Open(ctx, item, &objectstore.Range{
		Start: int64(index.Start(int(t.Start))),
		End:   int64(index[t.End-1].End) - 1,
	})
	if err != nil {
		return fmt.Errorf("open xorb %s: %w", t.Xorb, err)
	}

	defer func() {
		_ = rc.Close()
	}()

	chunks := xet.NewChunkReader(rc)
	for _, e := range index[t.Start:t.End] {
		data, err := chunks.Next()
		if err != nil {
			return fmt.Errorf("xorb %s: %w", t.Xorb, err)
		}

		if len(data) != int(e.Size) {
			return fmt.Errorf("%w: xorb %s changed in storage", xet.ErrInvalid, t.Xorb)
		}

		if _, err := w.Write(data); err != nil {
			return err
		}
	}

	return nil
}
