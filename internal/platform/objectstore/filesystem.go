package objectstore

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"relic/internal/platform/localdisk"
	"relic/internal/platform/telemetry"
)

// ErrPresignNotSupported is returned by stores that cannot sign URLs.
var ErrPresignNotSupported = errors.New("the object store cannot presign URLs")

const (
	artifactsDir = "artifacts"
	temporaryDir = "temporary"
	stagingDir   = "staging"
)

// FilesystemStore implements Store on a local directory.
type FilesystemStore struct {
	root    *os.Root
	syncDir func(dir string) error
}

// NewFilesystem returns a store rooted at dir, creating its directories.
func NewFilesystem(dir string) (*FilesystemStore, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("object storage path %q must be absolute", dir)
	}

	root := filepath.Clean(dir)
	for _, d := range []string{
		artifactsDir,
		temporaryDir,
		stagingDir,
	} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			return nil, fmt.Errorf("create object storage directory: %w", err)
		}
	}

	if err := localdisk.Check(root); err != nil {
		return nil, err
	}

	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open object storage directory: %w", err)
	}

	telemetry.RegisterDisk(
		telemetry.VolumeObjectStorage,
		root,
		func() (uint64, uint64, error) {
			return localdisk.Usage(root)
		},
		nil,
	)

	s := &FilesystemStore{
		root: r,
	}
	s.syncDir = s.syncDirectory

	return s, nil
}

// Windows cannot sync a directory handle.
func (s *FilesystemStore) syncDirectory(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}

	d, err := s.root.Open(dir)
	if err != nil {
		return err
	}

	defer func() {
		_ = d.Close()
	}()

	if err := d.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
		return fmt.Errorf("sync directory %s: %w", dir, err)
	}

	return nil
}

func (s *FilesystemStore) mkdirAll(dir string) error {
	var missing []string

	for d := dir; d != "."; d = filepath.Dir(d) {
		if _, err := s.root.Stat(d); err == nil {
			break
		}

		missing = append(missing, d)
	}

	if len(missing) == 0 {
		return nil
	}

	if err := s.root.MkdirAll(dir, 0o750); err != nil {
		return err
	}

	for _, d := range slices.Backward(missing) {
		if err := s.syncDir(filepath.Dir(d)); err != nil {
			return err
		}
	}

	return nil
}

func (s *FilesystemStore) rename(staged, dst string) error {
	if err := s.mkdirAll(filepath.Dir(dst)); err != nil {
		return err
	}

	if err := s.root.Rename(staged, dst); err != nil {
		return err
	}

	return s.syncDir(filepath.Dir(dst))
}

func (s *FilesystemStore) path(key string) (string, error) {
	dir, rel := artifactsDir, key
	if rest, ok := strings.CutPrefix(key, TemporaryKeyPrefix); ok {
		dir, rel = temporaryDir, rest
	}

	if rel == "" || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("object key %q is not a relative path", key)
	}

	return filepath.Join(dir, rel), nil
}

// Put implements Store.
func (s *FilesystemStore) Put(ctx context.Context, key string, r io.Reader, _ int64) error {
	dst, err := s.path(key)
	if err != nil {
		return err
	}

	if err := s.publish(dst, func(f *os.File) error {
		_, err := io.Copy(f, contextReader{
			ctx: ctx,
			r:   r,
		})

		return err
	}); err != nil {
		return fmt.Errorf("put %s: %w", key, mapFSErr(err))
	}

	return nil
}

func (s *FilesystemStore) publish(dst string, write func(f *os.File) error) (err error) {
	staged := filepath.Join(stagingDir, "put-"+rand.Text())

	f, err := s.root.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			_ = f.Close()
			_ = s.root.Remove(staged)
		}
	}()

	if err := write(f); err != nil {
		return err
	}

	if err := f.Sync(); err != nil {
		return err
	}

	if err := f.Close(); err != nil {
		return err
	}

	return s.rename(staged, dst)
}

// Get implements Store.
func (s *FilesystemStore) Get(_ context.Context, key string, rng *Range) (io.ReadCloser, Info, error) {
	p, err := s.path(key)
	if err != nil {
		return nil, Info{}, err
	}

	f, err := s.root.Open(p)
	if err != nil {
		return nil, Info{}, fmt.Errorf("get %s: %w", key, mapFSErr(err))
	}

	st, err := f.Stat()
	if err != nil {
		_ = f.Close()

		return nil, Info{}, fmt.Errorf("get %s: %w", key, err)
	}

	if rng == nil {
		return f, Info{
			Size: st.Size(),
		}, nil
	}

	end := min(rng.End, st.Size()-1)
	if rng.Start < 0 || rng.Start > end {
		_ = f.Close()

		return nil, Info{}, fmt.Errorf(
			"get %s: range %d-%d of %d bytes is not satisfiable",
			key,
			rng.Start,
			rng.End,
			st.Size(),
		)
	}

	n := end - rng.Start + 1

	body := sectionReadCloser{
		Reader: io.NewSectionReader(f, rng.Start, n),
		Closer: f,
	}

	return body, Info{
		Size: n,
	}, nil
}

// Head implements Store.
func (s *FilesystemStore) Head(_ context.Context, key string) (Info, error) {
	p, err := s.path(key)
	if err != nil {
		return Info{}, err
	}

	st, err := s.root.Stat(p)
	if err != nil {
		return Info{}, fmt.Errorf("head %s: %w", key, mapFSErr(err))
	}

	return Info{
		Size: st.Size(),
	}, nil
}

// Copy implements Store.
func (s *FilesystemStore) Copy(ctx context.Context, srcKey, dstKey string) error {
	src, err := s.path(srcKey)
	if err != nil {
		return err
	}

	dst, err := s.path(dstKey)
	if err != nil {
		return err
	}

	if _, err := s.root.Stat(src); err != nil {
		return fmt.Errorf("copy %s to %s: %w", srcKey, dstKey, mapFSErr(err))
	}

	if err := s.link(src, dst); err == nil {
		return nil
	}

	in, err := s.root.Open(src)
	if err != nil {
		return fmt.Errorf("copy %s to %s: %w", srcKey, dstKey, mapFSErr(err))
	}

	defer func() {
		_ = in.Close()
	}()

	if err := s.publish(dst, func(f *os.File) error {
		_, err := io.Copy(f, contextReader{
			ctx: ctx,
			r:   in,
		})

		return err
	}); err != nil {
		return fmt.Errorf("copy %s to %s: %w", srcKey, dstKey, mapFSErr(err))
	}

	return nil
}

func (s *FilesystemStore) link(src, dst string) error {
	staged := filepath.Join(stagingDir, "link-"+rand.Text())
	if err := s.root.Link(src, staged); err != nil {
		return err
	}

	if err := s.rename(staged, dst); err != nil {
		_ = s.root.Remove(staged)

		return err
	}

	return nil
}

// Delete implements Store.
func (s *FilesystemStore) Delete(_ context.Context, key string) error {
	p, err := s.path(key)
	if err != nil {
		return err
	}

	if err := s.root.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("delete %s: %w", key, mapFSErr(err))
	}

	return nil
}

// List implements Store.
func (s *FilesystemStore) List(ctx context.Context, prefix string) iter.Seq2[Object, error] {
	return func(yield func(Object, error) bool) {
		dir, rel, keyPrefix := artifactsDir, prefix, ""
		if rest, ok := strings.CutPrefix(prefix, TemporaryKeyPrefix); ok {
			dir, rel, keyPrefix = temporaryDir, rest, TemporaryKeyPrefix
		}

		if rel != "" && !filepath.IsLocal(rel) {
			yield(Object{}, fmt.Errorf("object key prefix %q is not a relative path", prefix))
			return
		}

		top := path.Join(dir, rel)
		stop := errors.New("stop")

		err := fs.WalkDir(s.root.FS(), top, func(p string, d fs.DirEntry, err error) error {
			switch {
			case errors.Is(err, fs.ErrNotExist) && p == top:
				return fs.SkipAll
			case err != nil:
				return err
			case ctx.Err() != nil:
				return ctx.Err()
			case d.IsDir():
				return nil
			}

			info, err := d.Info()
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}

			if err != nil {
				return err
			}

			if !yield(Object{
				Key:      keyPrefix + strings.TrimPrefix(p, dir+"/"),
				Size:     info.Size(),
				Modified: info.ModTime(),
			}, nil) {
				return stop
			}

			return nil
		})
		if err != nil && !errors.Is(err, stop) {
			yield(Object{}, fmt.Errorf("list %s: %w", prefix, mapFSErr(err)))
		}
	}
}

// LocalPath implements Store.
func (s *FilesystemStore) LocalPath(key string) (string, bool) {
	p, err := s.path(key)
	if err != nil {
		return "", false
	}

	return filepath.Join(s.root.Name(), p), true
}

// PresignGet implements Store.
func (s *FilesystemStore) PresignGet(context.Context, string, time.Duration) (string, error) {
	return "", ErrPresignNotSupported
}

// SweepTemporary implements TemporarySweeper: it deletes temporary keys, and
// staging files left by a crash, last written before olderThan ago.
func (s *FilesystemStore) SweepTemporary(ctx context.Context, olderThan time.Duration) (int, error) {
	cutoff := time.Now().Add(-olderThan)
	removed := 0
	workspaces := path.Join(temporaryDir, strings.TrimPrefix(WorkspaceKeyPrefix, TemporaryKeyPrefix))

	for _, dir := range []string{
		temporaryDir,
		stagingDir,
	} {
		err := fs.WalkDir(s.root.FS(), dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			if err := ctx.Err(); err != nil {
				return err
			}

			if d.IsDir() && path.Dir(p) == workspaces {
				swept, err := s.sweepWorkspace(p, d, cutoff)
				if swept {
					removed++
				}

				if err != nil {
					return err
				}

				return fs.SkipDir
			}

			if d.IsDir() {
				return nil
			}

			info, err := d.Info()
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}

				return err
			}

			if info.ModTime().After(cutoff) {
				return nil
			}

			if err := s.root.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}

			removed++

			return nil
		})
		if err != nil {
			return removed, fmt.Errorf("sweep %s: %w", dir, err)
		}
	}

	return removed, nil
}

func (s *FilesystemStore) sweepWorkspace(dir string, d fs.DirEntry, cutoff time.Time) (bool, error) {
	info, err := d.Info()
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	if info.ModTime().After(cutoff) {
		return false, nil
	}

	if err := s.root.RemoveAll(dir); err != nil {
		return false, err
	}

	return true, nil
}

func mapFSErr(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return errors.Join(ErrNotFound, err)
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return errors.Join(ErrStorageFull, err)
	}

	return err
}

type contextReader struct {
	ctx context.Context //nolint:containedctx // scoped to one io.Copy
	r   io.Reader
}

func (c contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}

	return c.r.Read(p)
}

type sectionReadCloser struct {
	io.Reader
	io.Closer
}
