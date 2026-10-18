package repository

import (
	"context"
	"crypto/md5"  //nolint:gosec // MD5 is a wire checksum here, not a security control
	"crypto/sha1" //nolint:gosec // content addressing and client checksums, not security
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"uuid"

	"relic/internal/access/auth"
	"relic/internal/platform/database"
	"relic/internal/platform/events"
	"relic/internal/platform/objectstore"
	"relic/internal/platform/sealing"
	"relic/internal/platform/telemetry"

	"go.opentelemetry.io/otel/trace"
)

// Service implements repository and artifact operations.
type Service struct {
	db            *database.DB
	blobs         objectstore.Store
	log           *slog.Logger
	now           func() time.Time
	client        *http.Client
	upstreams     sync.Map
	allowPrivate  bool
	maxUploadSize int64
	trashDisabled bool
	trashLayout   TrashLayout
	hooks         map[Format]DeployHook
	deleteHooks   map[Format]DeployHook
	removalHooks  []RemovalHook
	metadata      []func(name string) bool
	box           *sealing.Box
	fetches       Coalescer[Item]
	missed        missedRetrievals
	versionGate   VersionGate
	events        events.Publisher
	projectQuota  func(context.Context, string) (int64, error)
	storageOwners []StorageOwner

	upstreamChecksMu sync.Mutex
	upstreamChecks   map[Format]UpstreamCheck
}

// RegisterMetadata marks files (by name) as mutable metadata for remote
// repositories (maven-metadata.xml, for example).
func (s *Service) RegisterMetadata(isMetadata func(name string) bool) {
	s.metadata = append(s.metadata, isMetadata)
}

func (s *Service) isMetadata(name string) bool {
	for _, f := range s.metadata {
		if f(name) {
			return true
		}
	}

	return false
}

// DeployHook indexes a file after it is committed to a repository of the
// format it was registered for.
type DeployHook func(ctx context.Context, r Repository, item Item) error

// RegisterHook runs hook after deploys to repositories of format.
func (s *Service) RegisterHook(format Format, hook DeployHook) {
	if s.hooks == nil {
		s.hooks = map[Format]DeployHook{}
	}

	s.hooks[format] = hook
}

// SetEvents publishes deploys and deletes to p.
func (s *Service) SetEvents(p events.Publisher) { s.events = p }

func (s *Service) publishDeployed(ctx context.Context, in DeployInput, item Item) {
	events.Publish(ctx, s.events, events.Event{
		Type:    events.ArtifactDeployed,
		Subject: item.Repository + "/" + item.Path(),
		Actor:   in.User,
		Data: map[string]any{
			"repository": item.Repository,
			"path":       item.Path(),
			"sha256":     item.Checksums.SHA256,
			"size":       item.Size,
		},
	})
}

// WithLock runs fn while holding the lock named key, across every Relic
// process sharing the database.
func (s *Service) WithLock(ctx context.Context, key string, fn func() error) error {
	return s.db.WithLock(ctx, key, fn)
}

// RegisterDeleteHook runs hook after an item (a file or a folder with its
// contents) is deleted or moved out of a repository of format.
func (s *Service) RegisterDeleteHook(format Format, hook DeployHook) {
	if s.deleteHooks == nil {
		s.deleteHooks = map[Format]DeployHook{}
	}

	s.deleteHooks[format] = hook
}

func (s *Service) runHook(ctx context.Context, item Item) {
	if item.IsFolder() {
		return
	}

	s.runHookOf(ctx, s.hooks, item)
}

func (s *Service) runHookOf(ctx context.Context, hooks map[Format]DeployHook, item Item) {
	if len(hooks) == 0 {
		return
	}

	r, err := s.GetRepository(ctx, item.Repository)
	if err != nil {
		return
	}

	hook, ok := hooks[r.Format]
	if !ok {
		return
	}

	if err := hook(ctx, r, item); err != nil {
		s.log.WarnContext(ctx, "format indexing failed", "repo", item.Repository, "path", item.Path(), "error", err)
	}
}

// SetHTTPClient overrides the client used to reach remote upstreams (tests).
func (s *Service) SetHTTPClient(c *http.Client) { s.client = c }

// AllowPrivateUpstreams lets remote repositories reach loopback and private
// addresses (RELIC_UPSTREAM_ALLOW_PRIVATE_ADDRESSES).
func (s *Service) AllowPrivateUpstreams(allow bool) { s.allowPrivate = allow }

// SetMaxUploadSize limits every upload to n bytes (RELIC_MAX_UPLOAD_SIZE);
// 0 leaves only the repositories' own limits.
func (s *Service) SetMaxUploadSize(n int64) { s.maxUploadSize = n }

func (s *Service) checkUploadLimit(ctx context.Context, repoKey string, size int64) error {
	limit, err := s.UploadLimit(ctx, repoKey)
	if err != nil {
		return err
	}

	if limit > 0 && size > limit {
		return fmt.Errorf("%w: %d bytes, the limit is %d", ErrTooLarge, size, limit)
	}

	return nil
}

// UploadLimit is the largest upload repoKey accepts, 0 for no limit: the
// smaller of the server's and the repository's limits.
func (s *Service) UploadLimit(ctx context.Context, repoKey string) (int64, error) {
	r, err := s.GetRepository(ctx, repoKey)
	if err != nil {
		return 0, err
	}

	limits := []int64{}

	for _, n := range []int64{
		s.maxUploadSize,
		r.Config.MaxUploadSize,
	} {
		if n > 0 {
			limits = append(limits, n)
		}
	}

	if len(limits) == 0 {
		return 0, nil
	}

	return slices.Min(limits), nil
}

// LimitedBody reads r, failing with ErrTooLarge past limit bytes.
func LimitedBody(r io.Reader, limit int64) *LimitedReader {
	return &LimitedReader{
		r:    r,
		left: limit,
	}
}

// LimitedReader is a body read under an upload limit.
type LimitedReader struct {
	r    io.Reader
	left int64
}

// Read implements io.Reader.
func (l *LimitedReader) Read(p []byte) (int, error) {
	if int64(len(p)) > l.left+1 {
		p = p[:l.left+1]
	}

	n, err := l.r.Read(p)
	l.left -= int64(n)

	if l.left < 0 {
		return n, ErrTooLarge
	}

	return n, err
}

// Exceeded reports whether more than the limit was offered.
func (l *LimitedReader) Exceeded() bool { return l.left < 0 }

// NewService returns a Service storing metadata in db and content in blobs.
func NewService(db *database.DB, blobs objectstore.Store, log *slog.Logger) *Service {
	return &Service{
		db:          db,
		blobs:       blobs,
		log:         log,
		trashLayout: DefaultTrashLayout,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

// DeployInput describes an artifact upload.
type DeployInput struct {
	Repository string
	Path       string
	Body       io.Reader
	// Size is Body's length, 0 or -1 when unknown.
	Size      int64
	Expected  Checksums
	Props     Properties
	User      string
	Authorize func(path string, overwrite bool) error

	cache bool
}

// Deploy streams in.Body into the store and records it at in.Path.
func (s *Service) Deploy(ctx context.Context, in DeployInput) (item Item, err error) {
	ctx, span := telemetry.Tracer().
		Start(ctx, "repo.Deploy", trace.WithAttributes(telemetry.AttrRepository.String(in.Repository)))
	defer func() {
		telemetry.End(span, err)
	}()

	r, err := s.checkDeployTarget(ctx, &in)
	if err != nil {
		return Item{}, err
	}

	span.SetAttributes(telemetry.AttrFormat.String(r.Format), telemetry.AttrClass.String(r.Class))

	defer func() {
		if err == nil && !in.cache {
			telemetry.ArtifactUploaded(in.Repository, r.Format, r.Class, item.Size, false)
			s.publishDeployed(ctx, in, item)
		}
	}()

	body := in.Body

	var limited *LimitedReader

	if !in.cache {
		limit, err := s.UploadLimit(ctx, in.Repository)
		if err != nil {
			return Item{}, err
		}

		if limit > 0 && in.Size > limit {
			return Item{}, fmt.Errorf("%w: %d bytes, the limit is %d", ErrTooLarge, in.Size, limit)
		}

		if limit > 0 {
			limited = LimitedBody(body, limit)
			body = limited
		}
	}

	tmpKey := objectstore.TemporaryKeyPrefix + uuid.NewV7().String()

	h := newHasher()
	if err := s.blobs.Put(ctx, tmpKey, io.TeeReader(body, h), max(in.Size, -1)); err != nil {
		if limited != nil && limited.Exceeded() {
			err = ErrTooLarge
		}

		return Item{}, fmt.Errorf("stage upload: %w", err)
	}
	defer func() {
		if errors.Is(err, errContentNotRestored) {
			s.log.ErrorContext(ctx, "kept the staged upload of an item whose content could not be restored",
				"key", tmpKey, "repo", in.Repository, "path", in.Path, "error", err)

			return
		}

		if err := s.blobs.Delete(context.WithoutCancel(ctx), tmpKey); err != nil {
			s.log.WarnContext(ctx, "delete staged upload", "key", tmpKey, "error", err)
		}
	}()

	staged, err := s.blobs.Head(ctx, tmpKey)
	if err != nil {
		return Item{}, fmt.Errorf("check staged upload: %w", err)
	}

	if staged.Size != h.size {
		return Item{}, fmt.Errorf("%w: stored %d of %d bytes", ErrIncompleteUpload, staged.Size, h.size)
	}

	got := h.sums()
	if err := verify(in.Expected, got); err != nil {
		return Item{}, err
	}

	return s.commitFile(ctx, in, database.Blob{
		Sha256: got.SHA256,
		Sha1:   got.SHA1,
		Md5:    got.MD5,
		Size:   h.size,
	}, tmpKey)
}

// DeployByChecksum records in.Path pointing at content Relic already stores,
// identified by in.Expected (SHA-256 preferred, else SHA-1).
func (s *Service) DeployByChecksum(ctx context.Context, in DeployInput) (item Item, err error) {
	ctx, span := telemetry.Tracer().
		Start(ctx, "repo.DeployByChecksum", trace.WithAttributes(telemetry.AttrRepository.String(in.Repository)))

	defer func() {
		if errors.Is(err, ErrChecksumUnknown) {
			span.End()
			return
		}

		telemetry.End(span, err)
	}()

	r, err := s.checkDeployTarget(ctx, &in)
	if err != nil {
		return Item{}, err
	}

	span.SetAttributes(telemetry.AttrFormat.String(r.Format), telemetry.AttrClass.String(r.Class))

	defer func() {
		if err == nil && !in.cache {
			telemetry.ArtifactUploaded(in.Repository, r.Format, r.Class, item.Size, true)
			s.publishDeployed(ctx, in, item)
		}
	}()

	var b database.Blob

	switch {
	case in.Expected.SHA256 != "":
		b, err = s.db.GetBlob(ctx, strings.ToLower(in.Expected.SHA256))
	case in.Expected.SHA1 != "":
		b, err = s.db.GetBlobBySHA1(ctx, strings.ToLower(in.Expected.SHA1))
	default:
		return Item{}, ErrChecksumUnknown
	}

	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Item{}, ErrChecksumUnknown
	}

	if err != nil {
		return Item{}, fmt.Errorf("look up checksum: %w", err)
	}

	if err := verify(in.Expected, Checksums{
		SHA1:   b.Sha1,
		SHA256: b.Sha256,
		MD5:    b.Md5,
	}); err != nil {
		return Item{}, ErrChecksumUnknown
	}

	if !in.cache {
		if err := s.checkUploadLimit(ctx, in.Repository, b.Size); err != nil {
			return Item{}, err
		}
	}

	return s.commitFile(ctx, in, b, "")
}

func (s *Service) checkDeployTarget(ctx context.Context, in *DeployInput) (database.Repository, error) {
	r, err := s.db.GetRepository(ctx, in.Repository)
	if err != nil {
		if errors.Is(database.NotFound(err), database.ErrNotFound) {
			return r, ErrRepositoryNotFound
		}

		return r, fmt.Errorf("get repository %s: %w", in.Repository, err)
	}

	if r.Class != string(ClassLocal) && (r.Class != string(ClassRemote) || !in.cache) {
		return r, ErrNotLocal
	}

	p, err := CleanPath(in.Path)
	if err != nil {
		return r, err
	}

	if p == "" {
		return r, ErrInvalidPath
	}

	in.Path = p

	return r, nil
}

// Blob invariant: a blobs row that exists implies its object exists.
func (s *Service) commitFile(ctx context.Context, in DeployInput, b database.Blob, tmpKey string) (Item, error) {
	now := s.now()
	key := objectstore.Key(b.Sha256)
	parent, name := SplitPath(in.Path)

	r, err := s.GetRepository(ctx, in.Repository)
	if err != nil {
		return Item{}, err
	}

	quota := r.Config.MaxStorage
	if in.cache {
		quota = 0
	}

	var projectQuota int64

	if !in.cache {
		if projectQuota, err = s.quotaOfProject(ctx, r.Project); err != nil {
			return Item{}, err
		}
	}

	if err := s.storeObject(ctx, key, tmpKey, b.Size); err != nil {
		return Item{}, err
	}

	var (
		item    database.Item
		recheck bool
	)

	err = s.db.WithTx(ctx, func(q database.Querier) error {
		if in.Authorize != nil {
			existing, err := q.GetItemForUpdate(ctx, database.GetItemForUpdateParams{
				RepoKey: in.Repository,
				Path:    parent,
				Name:    name,
			})
			if err == nil && existing.Type != TypeFolder {
				if err := in.Authorize(in.Path, true); err != nil {
					return err
				}
			} else if err != nil && !database.IsNoRows(err) {
				return fmt.Errorf("lock %s/%s: %w", parent, name, err)
			}
		}

		var err error
		if recheck, err = upsertFile(
			ctx,
			q,
			in.Repository,
			parent,
			name,
			b,
			in.User,
			now,
			r.Config.Immutable,
		); err != nil {
			return err
		}

		item, err = q.GetItem(ctx, database.GetItemParams{
			RepoKey: in.Repository,
			Path:    parent,
			Name:    name,
		})
		if err != nil {
			return fmt.Errorf("reload item: %w", err)
		}

		if in.Props != nil {
			if err := replaceProperties(ctx, q, item.ID, in.Props); err != nil {
				return err
			}
		}

		if err := checkQuota(ctx, q, in.Repository, quota); err != nil {
			return err
		}

		return checkProjectQuota(ctx, q, r.Project, projectQuota)
	})
	if err != nil {
		if tmpKey != "" {
			s.discardObject(ctx, b.Sha256)
		}

		return Item{}, err
	}

	if recheck {
		if err := s.restoreObject(ctx, b.Sha256, tmpKey, b.Size); err != nil {
			return Item{}, err
		}
	}

	it := domainItem(item)
	s.runHook(ctx, it)

	return it, nil
}

// errContentNotRestored marks a deploy whose item is committed but whose
// object could not be written back after a garbage collection.
var errContentNotRestored = errors.New("the item is stored but its content could not be restored")

// restoreObject writes a deployed blob's object back if a garbage collection
// removed it while the deploy committed, retrying transient storage errors.
func (s *Service) restoreObject(ctx context.Context, sha256Hex, tmpKey string, size int64) error {
	key := objectstore.Key(sha256Hex)

	var err error

	for attempt := range restoreAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("%w: %w", errContentNotRestored, ctx.Err())
			case <-time.After(restoreBackoff << (2 * (attempt - 1))):
			}
		}

		if err = s.db.WithLock(ctx, blobLock(sha256Hex), func() error {
			return s.storeObject(ctx, key, tmpKey, size)
		}); err == nil {
			return nil
		}
	}

	return fmt.Errorf("%w: %w", errContentNotRestored, err)
}

const (
	restoreAttempts = 3
	restoreBackoff  = 50 * time.Millisecond
)

// discardObject deletes the object a failed deploy promoted, unless a blob
// row references it: a deploy that inserts the row later finds the object
// missing under the same lock and writes it again.
func (s *Service) discardObject(ctx context.Context, sha256Hex string) {
	ctx = context.WithoutCancel(ctx)

	err := s.db.WithLock(ctx, blobLock(sha256Hex), func() error {
		_, err := s.db.GetBlob(ctx, sha256Hex)
		if !database.IsNoRows(err) {
			return err
		}

		return s.blobs.Delete(ctx, objectstore.Key(sha256Hex))
	})
	if err != nil {
		s.log.WarnContext(ctx, "discard the object of a failed deploy", "sha256", sha256Hex, "error", err)
	}
}

// storeObject makes key hold the staged object tmpKey, unless it already
// holds size bytes; with nothing staged (a deploy by checksum) the object
// must exist. Content-addressed keys make the copy idempotent.
func (s *Service) storeObject(ctx context.Context, key, tmpKey string, size int64) error {
	stored, err := s.blobs.Head(ctx, key)

	switch {
	case err == nil && stored.Size == size:
		return nil
	case err != nil && !errors.Is(err, objectstore.ErrNotFound):
		return fmt.Errorf("check blob: %w", err)
	case tmpKey == "":
		return ErrChecksumUnknown
	}

	if err := s.blobs.Copy(ctx, tmpKey, key); err != nil {
		return fmt.Errorf("promote blob: %w", err)
	}

	promoted, err := s.blobs.Head(ctx, key)
	if err != nil {
		return fmt.Errorf("check promoted blob: %w", err)
	}

	if promoted.Size != size {
		return fmt.Errorf("%w: promoted %d of %d bytes", ErrIncompleteUpload, promoted.Size, size)
	}

	return nil
}

// Transactions that change items and blobs lock the item first.
func upsertFile(
	ctx context.Context,
	q database.Querier,
	repoKey, parent, name string,
	b database.Blob,
	user string,
	now time.Time,
	immutable bool,
) (recheck bool, err error) {
	existing, err := q.GetItemForUpdate(
		ctx,
		database.GetItemForUpdateParams{
			RepoKey: repoKey,
			Path:    parent,
			Name:    name,
		},
	)

	switch {
	case err == nil && existing.Type == TypeFolder:
		return false, ErrConflict
	case err == nil:
		if immutable && *existing.Sha256 != b.Sha256 && user != SystemUser {
			return false, fmt.Errorf("%w: %s/%s/%s exists", ErrImmutable, repoKey, parent, name)
		}

		if err := checkHolds(ctx, q, repoKey, JoinPath(parent, name)); err != nil {
			return false, err
		}

		recheck, err = adjustBlobs(ctx, q, &b, []string{
			*existing.Sha256,
		})
		if err != nil {
			return false, err
		}

		if err := q.OverwriteFile(ctx, database.OverwriteFileParams{
			ID:       existing.ID,
			Size:     b.Size,
			Sha1:     &b.Sha1,
			Sha256:   &b.Sha256,
			Md5:      &b.Md5,
			Now:      now,
			Username: user,
		}); err != nil {
			return false, fmt.Errorf("overwrite %s/%s: %w", parent, name, err)
		}

		return recheck, nil
	case !database.IsNoRows(err):
		return false, fmt.Errorf("lock %s/%s: %w", parent, name, err)
	}

	if err := ensureFolders(ctx, q, repoKey, parent, user, now); err != nil {
		return false, err
	}

	if recheck, err = adjustBlobs(ctx, q, &b, nil); err != nil {
		return false, err
	}

	_, err = q.InsertFile(ctx, database.InsertFileParams{
		ID:       uuid.NewV7(),
		RepoKey:  repoKey,
		Path:     parent,
		Name:     name,
		Size:     b.Size,
		Sha1:     &b.Sha1,
		Sha256:   &b.Sha256,
		Md5:      &b.Md5,
		Now:      now,
		Username: user,
	})

	switch {
	case database.IsNoRows(err):
		return false, fmt.Errorf("insert %s/%s: %w", parent, name, database.ErrRetryTransaction)
	case err != nil:
		return false, fmt.Errorf("insert %s/%s: %w", parent, name, err)
	}

	return recheck, nil
}

// checkQuota fails when the repository's files, the transaction's included,
// exceed quota; 0 is no quota. It locks the repository row, so deploys
// that check the same quota run one at a time from here to their commit.
func checkQuota(ctx context.Context, q database.Querier, repoKey string, quota int64) error {
	if quota <= 0 {
		return nil
	}

	if _, err := q.LockRepository(ctx, repoKey); err != nil {
		return fmt.Errorf("lock repository %s: %w", repoKey, err)
	}

	used, err := q.RepositoryUsedBytes(ctx, repoKey)
	if err != nil {
		return fmt.Errorf("storage used by %s: %w", repoKey, err)
	}

	if used > quota {
		return fmt.Errorf("%w: %s would use %d of %d bytes", ErrQuotaExceeded, repoKey, used, quota)
	}

	return nil
}

// SetProjectQuota bounds the size of each project's repositories by what fn
// returns for the project, 0 for no bound.
func (s *Service) SetProjectQuota(fn func(context.Context, string) (int64, error)) {
	s.projectQuota = fn
}

func (s *Service) quotaOfProject(ctx context.Context, project string) (int64, error) {
	if project == "" || s.projectQuota == nil {
		return 0, nil
	}

	quota, err := s.projectQuota(ctx, project)
	if err != nil {
		return 0, fmt.Errorf("quota of project %s: %w", project, err)
	}

	return quota, nil
}

// checkProjectQuota fails when the files of the project's repositories, the
// transaction's included, exceed quota; 0 is no quota. Concurrent deploys to
// different repositories of a project may each pass it.
func checkProjectQuota(ctx context.Context, q database.Querier, project string, quota int64) error {
	if quota <= 0 {
		return nil
	}

	used, err := q.ProjectUsedBytes(ctx, project)
	if err != nil {
		return fmt.Errorf("storage used by project %s: %w", project, err)
	}

	if used > quota {
		return fmt.Errorf("%w: project %s would use %d of %d bytes", ErrQuotaExceeded, project, used, quota)
	}

	return nil
}

// ProjectUsedBytes is the size of the files of a project's repositories.
func (s *Service) ProjectUsedBytes(ctx context.Context, project string) (int64, error) {
	used, err := s.db.ProjectUsedBytes(ctx, project)
	if err != nil {
		return 0, fmt.Errorf("storage used by project %s: %w", project, err)
	}

	return used, nil
}

// Projects maps the repositories that name a project to it.
func (s *Service) Projects(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.ListRepositoryProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list repository projects: %w", err)
	}

	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Key] = r.Project
	}

	return out, nil
}

// UsedBytes is the size of a repository's files.
func (s *Service) UsedBytes(ctx context.Context, repoKey string) (int64, error) {
	used, err := s.db.RepositoryUsedBytes(ctx, repoKey)
	if err != nil {
		return 0, fmt.Errorf("storage used by %s: %w", repoKey, err)
	}

	return used, nil
}

// adjustBlobs locks blob rows in digest order before changing their
// references. It reports whether the acquired blob had no row or was being
// collected, so its object must be checked once the transaction commits.
func adjustBlobs(ctx context.Context, q database.Querier, acquire *database.Blob, release []string) (bool, error) {
	digests := slices.Clone(release)
	if acquire != nil {
		digests = append(digests, acquire.Sha256)
	}

	if len(digests) == 0 {
		return false, nil
	}

	slices.Sort(digests)

	if _, err := q.LockBlobs(ctx, slices.Compact(digests)); err != nil {
		return false, fmt.Errorf("lock blobs: %w", err)
	}

	recheck := false

	if acquire != nil {
		deleting, err := q.BlobDeleting(ctx, acquire.Sha256)

		switch {
		case database.IsNoRows(err):
			recheck = true
		case err != nil:
			return false, fmt.Errorf("check blob: %w", err)
		default:
			recheck = deleting
		}

		if err := q.AcquireBlob(
			ctx,
			database.AcquireBlobParams{
				Sha256: acquire.Sha256,
				Sha1:   acquire.Sha1,
				Md5:    acquire.Md5,
				Size:   acquire.Size,
			},
		); err != nil {
			return false, fmt.Errorf("acquire blob: %w", err)
		}
	}

	if len(release) > 0 {
		if err := q.ReleaseBlobs(ctx, release); err != nil {
			return false, fmt.Errorf("release blobs: %w", err)
		}
	}

	return recheck, nil
}

func ensureFolders(ctx context.Context, q database.Querier, repoKey, path, user string, now time.Time) error {
	if path == RootPath {
		return nil
	}

	var (
		ids            []uuid.UUID
		parents, names []string
	)

	segments := strings.Split(path, "/")
	for i := range segments {
		parent, name := SplitPath(strings.Join(segments[:i+1], "/"))

		ids, parents, names = append(ids, uuid.NewV7()), append(parents, parent), append(names, name)
	}

	files, err := q.EnsureFolders(
		ctx,
		database.EnsureFoldersParams{
			Ids:      ids,
			Paths:    parents,
			Names:    names,
			RepoKey:  repoKey,
			Now:      now,
			Username: user,
		},
	)
	if err != nil {
		return fmt.Errorf("create folders %s: %w", path, err)
	}

	if len(files) > 0 {
		return ErrConflict
	}

	return nil
}

func replaceProperties(ctx context.Context, q database.Querier, id uuid.UUID, props Properties) error {
	if err := props.validate(); err != nil {
		return err
	}

	if err := q.DeleteItemProperties(ctx, id); err != nil {
		return fmt.Errorf("clear properties: %w", err)
	}

	keys, vals := props.flatten()
	if len(keys) == 0 {
		return nil
	}

	if err := q.AddItemProperties(
		ctx,
		database.AddItemPropertiesParams{
			ItemID: id,
			Keys:   keys,
			Vals:   vals,
		},
	); err != nil {
		return fmt.Errorf("set properties: %w", err)
	}

	return nil
}

// CreateFolder creates a folder (and its ancestors) at path.
func (s *Service) CreateFolder(ctx context.Context, repoKey, path, user string) (Item, error) {
	in := DeployInput{
		Repository: repoKey,
		Path:       path,
	}
	if _, err := s.checkDeployTarget(ctx, &in); err != nil {
		return Item{}, err
	}

	var item database.Item

	err := s.db.WithTx(ctx, func(q database.Querier) error {
		if err := ensureFolders(ctx, q, repoKey, in.Path, user, s.now()); err != nil {
			return err
		}

		parent, name := SplitPath(in.Path)

		var err error

		item, err = q.GetItem(ctx, database.GetItemParams{
			RepoKey: repoKey,
			Path:    parent,
			Name:    name,
		})

		return err
	})
	if err != nil {
		return Item{}, err
	}

	return domainItem(item), nil
}

// Stat returns the item at path.
func (s *Service) Stat(ctx context.Context, repoKey, path string) (Item, error) {
	r, err := s.db.GetRepository(ctx, repoKey)
	if err != nil {
		if errors.Is(database.NotFound(err), database.ErrNotFound) {
			return Item{}, ErrRepositoryNotFound
		}

		return Item{}, fmt.Errorf("get repository %s: %w", repoKey, err)
	}

	p, err := CleanPath(path)
	if err != nil {
		return Item{}, err
	}

	if p == "" {
		return Item{
			Repository: repoKey,
			Parent:     RootPath,
			Name:       RootPath,
			Type:       TypeFolder,
			Created:    r.Created,
			Modified:   r.Updated,
			Updated:    r.Updated,
		}, nil
	}

	parent, name := SplitPath(p)

	row, err := s.db.GetItem(ctx, database.GetItemParams{
		RepoKey: repoKey,
		Path:    parent,
		Name:    name,
	})
	if err != nil {
		if errors.Is(database.NotFound(err), database.ErrNotFound) {
			return Item{}, ErrNotFound
		}

		return Item{}, fmt.Errorf("stat %s/%s: %w", repoKey, p, err)
	}

	it := domainItem(row)

	it.format, it.class, it.source = r.Format, r.Class, "local"
	if r.Class == string(ClassRemote) {
		it.source = "cache"
	}

	return it, nil
}

// RecordDownload counts a full download of item for telemetry (the
// relic_artifact_downloads_total series).
func (s *Service) RecordDownload(ctx context.Context, item Item) {
	if item.format == "" {
		if r, err := s.db.GetRepository(ctx, item.Repository); err == nil {
			item.format, item.class, item.source = r.Format, r.Class, "local"
			if r.Class == string(ClassRemote) {
				item.source = "cache"
			}
		}
	}

	telemetry.ArtifactDownloaded(ctx, item.Repository, item.format, item.class, item.source, item.Size)
}

// Open returns the content of a file item, or the given byte range of it.
func (s *Service) Open(ctx context.Context, item Item, rng *objectstore.Range) (io.ReadCloser, error) {
	if item.IsFolder() {
		return nil, ErrNotAFile
	}

	rc, _, err := s.blobs.Get(ctx, objectstore.Key(item.Checksums.SHA256), rng)
	if errors.Is(err, objectstore.ErrNotFound) {
		telemetry.BlobIntegrityFailure(telemetry.BlobMissing)
		s.log.ErrorContext(ctx, "stored content is missing", "sha256", item.Checksums.SHA256,
			"repo", item.Repository, "path", item.Path())

		return nil, fmt.Errorf("open %s/%s: %w: %w", item.Repository, item.Path(), ErrContentMissing, err)
	}

	if err != nil {
		return nil, fmt.Errorf("open %s/%s: %w", item.Repository, item.Path(), err)
	}

	if rng != nil {
		return rc, nil
	}

	return &verifiedReader{
		ReadCloser: rc,
		hash:       sha256.New(),
		item:       item,
		corrupt: func(got string, size int64) {
			telemetry.BlobIntegrityFailure(telemetry.BlobCorrupt)
			s.log.ErrorContext(ctx, "stored content does not match its checksum", "sha256", item.Checksums.SHA256,
				"got_sha256", got, "size", item.Size, "got_size", size, "repo", item.Repository, "path", item.Path())
		},
	}, nil
}

// verifiedReader checks the digest on the read that reaches the item's size
// and withholds that read's bytes when it differs, so a corrupt body never
// arrives whole.
type verifiedReader struct {
	io.ReadCloser

	hash    hash.Hash
	size    int64
	checked bool
	item    Item
	corrupt func(got string, size int64)
}

func (v *verifiedReader) Read(p []byte) (int, error) {
	n, err := v.ReadCloser.Read(p)
	_, _ = v.hash.Write(p[:n])
	v.size += int64(n)

	reachedSize := v.size >= v.item.Size && (n > 0 || errors.Is(err, io.EOF))
	if v.checked || (!reachedSize && !errors.Is(err, io.EOF)) {
		return n, err
	}

	v.checked = true

	got := hex.EncodeToString(v.hash.Sum(nil))
	if got == v.item.Checksums.SHA256 && v.size == v.item.Size {
		return n, err
	}

	v.corrupt(got, v.size)

	return 0, fmt.Errorf("read %s/%s: %w", v.item.Repository, v.item.Path(), ErrContentCorrupt)
}

// Children lists the items directly inside a folder.
func (s *Service) Children(ctx context.Context, folder Item) ([]Item, error) {
	parent := folder.Path()
	if folder.Name == RootPath {
		parent = RootPath
	}

	rows, err := s.db.ListChildren(ctx, database.ListChildrenParams{
		RepoKey: folder.Repository,
		Path:    parent,
	})
	if err != nil {
		return nil, fmt.Errorf("list %s/%s: %w", folder.Repository, parent, err)
	}

	return visibleOnly(ctx, itemsFromRows(rows)), nil
}

// Subtree returns item and, for folders, every item below it.
func (s *Service) Subtree(ctx context.Context, item Item) ([]Item, error) {
	return subtree(ctx, s.db, item)
}

func subtree(ctx context.Context, q database.Querier, item Item) ([]Item, error) {
	var (
		rows []database.Item
		err  error
	)
	if item.Name == RootPath {
		rows, err = q.ListRepositoryItems(ctx, item.Repository)
	} else {
		full := item.Path()
		rows, err = q.ListSubtree(
			ctx,
			database.ListSubtreeParams{
				RepoKey:  item.Repository,
				Path:     item.Parent,
				Name:     item.Name,
				FullPath: full,
				Prefix:   likePrefix(full),
			},
		)
	}

	if err != nil {
		return nil, fmt.Errorf("list subtree %s/%s: %w", item.Repository, item.Path(), err)
	}

	return visibleOnly(ctx, itemsFromRows(rows)), nil
}

func itemsFromRows(rows []database.Item) []Item {
	items := make([]Item, len(rows))
	for i, r := range rows {
		items[i] = domainItem(r)
	}

	return items
}

// Delete removes the item at path and, for folders, everything below it.
func (s *Service) Delete(ctx context.Context, repoKey, path string) error {
	item, err := s.Stat(ctx, repoKey, path)
	if err != nil {
		return err
	}

	held := item.Path()
	if item.Name == RootPath {
		held = ""
	}

	if err := s.db.WithTx(ctx, func(q database.Querier) error {
		if _, err := deleteItem(ctx, q, item); err != nil {
			return err
		}

		return checkHolds(ctx, q, item.Repository, held)
	}); err != nil {
		return err
	}

	s.runHookOf(ctx, s.deleteHooks, item)
	s.publishDeleted(ctx, item)

	return nil
}

func (s *Service) publishDeleted(ctx context.Context, item Item) {
	events.Publish(ctx, s.events, events.Event{
		Type:    events.ArtifactDeleted,
		Subject: item.Repository + "/" + item.Path(),
		Actor:   auth.FromContext(ctx).Name(),
		Data: map[string]any{
			"repository": item.Repository,
			"path":       item.Path(),
			"folder":     item.IsFolder(),
		},
	})
}

func releaseAll(ctx context.Context, q database.Querier, shas []*string) error {
	released := make([]string, 0, len(shas))
	for _, s := range shas {
		if s != nil {
			released = append(released, *s)
		}
	}

	_, err := adjustBlobs(ctx, q, nil, released)

	return err
}

// Properties returns an item's properties.
func (s *Service) Properties(ctx context.Context, item Item) (Properties, error) {
	if item.Name == RootPath {
		return Properties{}, nil
	}

	row, err := s.db.GetItem(ctx, database.GetItemParams{
		RepoKey: item.Repository,
		Path:    item.Parent,
		Name:    item.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("get %s/%s: %w", item.Repository, item.Path(), database.NotFound(err))
	}

	rows, err := s.db.GetItemProperties(ctx, row.ID)
	if err != nil {
		return nil, fmt.Errorf("get properties of %s/%s: %w", item.Repository, item.Path(), err)
	}

	props := Properties{}
	for _, r := range rows {
		props[r.Key] = append(props[r.Key], r.Value)
	}

	return props, nil
}

// SetProperties adds values to an item's properties (recursively for folders
// when recursive is set).
func (s *Service) SetProperties(ctx context.Context, item Item, props Properties, recursive bool) error {
	return s.editProperties(ctx, item, recursive, func(q database.Querier, id uuid.UUID) error {
		return setProperties(ctx, q, id, props)
	})
}

// setProperties replaces the values of props' keys, keeping other keys.
func setProperties(ctx context.Context, q database.Querier, id uuid.UUID, props Properties) error {
	if err := props.validate(); err != nil {
		return err
	}

	if err := q.DeleteItemPropertyKeys(
		ctx,
		database.DeleteItemPropertyKeysParams{
			ItemID: id,
			Keys:   props.Keys(),
		},
	); err != nil {
		return fmt.Errorf("clear properties: %w", err)
	}

	keys, vals := props.flatten()
	if err := q.AddItemProperties(
		ctx,
		database.AddItemPropertiesParams{
			ItemID: id,
			Keys:   keys,
			Vals:   vals,
		},
	); err != nil {
		return fmt.Errorf("add properties: %w", err)
	}

	return nil
}

// DeleteProperties removes the given keys from an item (recursively for
// folders when recursive is set).
func (s *Service) DeleteProperties(ctx context.Context, item Item, keys []string, recursive bool) error {
	return s.editProperties(ctx, item, recursive, func(q database.Querier, id uuid.UUID) error {
		if err := q.DeleteItemPropertyKeys(
			ctx,
			database.DeleteItemPropertyKeysParams{
				ItemID: id,
				Keys:   keys,
			},
		); err != nil {
			return fmt.Errorf("delete properties: %w", err)
		}

		return nil
	})
}

func (s *Service) editProperties(
	ctx context.Context,
	item Item,
	recursive bool,
	edit func(database.Querier, uuid.UUID) error,
) error {
	targets := []Item{
		item,
	}
	if recursive && item.IsFolder() {
		var err error
		if targets, err = s.Subtree(ctx, item); err != nil {
			return err
		}
	}

	return s.db.WithTx(ctx, func(q database.Querier) error {
		for _, t := range targets {
			if t.Name == RootPath {
				continue
			}

			row, err := q.GetItem(ctx, database.GetItemParams{
				RepoKey: t.Repository,
				Path:    t.Parent,
				Name:    t.Name,
			})
			if err != nil {
				return fmt.Errorf("get %s/%s: %w", t.Repository, t.Path(), database.NotFound(err))
			}

			if err := edit(q, row.ID); err != nil {
				return err
			}
		}

		return nil
	})
}

type hasher struct {
	sha1, sha256, md5 interface {
		io.Writer
		Sum([]byte) []byte
	}
	size int64
}

func newHasher() *hasher {
	//nolint:gosec // wire checksums
	return &hasher{
		sha1:   sha1.New(),
		sha256: sha256.New(),
		md5:    md5.New(),
	}
}

func (h *hasher) Write(p []byte) (int, error) {
	_, _ = h.sha1.Write(p)
	_, _ = h.sha256.Write(p)
	_, _ = h.md5.Write(p)
	h.size += int64(len(p))

	return len(p), nil
}

func (h *hasher) sums() Checksums {
	return Checksums{
		SHA1:   hex.EncodeToString(h.sha1.Sum(nil)),
		SHA256: hex.EncodeToString(h.sha256.Sum(nil)),
		MD5:    hex.EncodeToString(h.md5.Sum(nil)),
	}
}

// ChecksumsOf returns the wire checksums of b (e.g. a generated document).
func ChecksumsOf(b []byte) Checksums {
	h := newHasher()
	_, _ = h.Write(b)

	return h.sums()
}

func verify(expected, actual Checksums) error {
	for _, c := range []struct {
		name, want, got string
	}{
		{
			"SHA-1",
			expected.SHA1,
			actual.SHA1,
		},
		{
			"SHA-256",
			expected.SHA256,
			actual.SHA256,
		},
		{
			"MD5",
			expected.MD5,
			actual.MD5,
		},
	} {
		if c.want != "" && !strings.EqualFold(c.want, c.got) {
			return fmt.Errorf("%w: client %s %s, actual %s", ErrChecksumMismatch, c.name, c.want, c.got)
		}
	}

	return nil
}
