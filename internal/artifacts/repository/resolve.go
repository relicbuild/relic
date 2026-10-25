package repository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"relic/internal/platform/database"
	"relic/internal/platform/telemetry"
)

// UserAgent identifies Relic to remote upstreams.
const UserAgent = "Relic/0.1 (+https://github.com/relicbuild/relic; remote repository)"

// ErrUpstream is returned when a remote repository's upstream fails.
var ErrUpstream = errors.New("upstream error")

// MaxVirtualDepth is how deep virtual repositories nest before resolution stops.
const MaxVirtualDepth = 8

// Resolve finds the file or folder at path for a download: in the
// repository itself for local repositories, from the cache or upstream for
// remote ones, and in member order for virtual ones.
func (s *Service) Resolve(ctx context.Context, repoKey, path string) (Item, error) {
	return s.resolve(ctx, repoKey, path, 0, false)
}

// resolve finds path in repoKey; localOnly skips remote repositories, for a
// path a virtual repository above keeps to its local members.
func (s *Service) resolve(ctx context.Context, repoKey, path string, depth int, localOnly bool) (Item, error) {
	r, err := s.GetRepository(ctx, repoKey)
	if err != nil {
		return Item{}, err
	}

	switch r.Class {
	case "remote":
		if localOnly {
			return Item{}, ErrNotFound
		}

		return s.resolveRemote(ctx, r, path)
	case "virtual":
		if depth >= MaxVirtualDepth {
			return Item{}, ErrNotFound
		}

		var upstreamErr error

		for _, member := range r.Members() {
			item, err := s.resolve(ctx, member, path, depth+1, localOnly || r.LocalOnly(path))
			switch {
			case err == nil:
				return item, nil
			case errors.Is(err, ErrUpstream):
				upstreamErr = err
			case errors.Is(err, ErrNotFound), errors.Is(err, ErrRepositoryNotFound):
			default:
				return Item{}, err
			}
		}

		if upstreamErr != nil {
			return Item{}, upstreamErr
		}

		return Item{}, ErrNotFound
	default:
		return s.Stat(ctx, repoKey, path)
	}
}

func (s *Service) resolveRemote(ctx context.Context, r Repository, path string) (Item, error) {
	cfg := r.Upstream()
	if cfg.Offline || cfg.URL == "" {
		return s.cachedOnly(ctx, r.Name, path)
	}

	clean, err := CleanPath(path)
	if err != nil {
		return Item{}, err
	}

	if clean == "" {
		return Item{}, ErrNotFound
	}

	return s.resolveCached(ctx, r, cfg, strings.TrimSuffix(cfg.URL, "/")+"/"+escapeSegments(clean), clean)
}

// ResolveURL is Resolve for a remote repository's file whose upstream URL
// is not <url>/<path>, such as a PyPI simple page on the registry host: it
// is cached at path and, if metadata, re-validated after the cache period.
func (s *Service) ResolveURL(ctx context.Context, r Repository, upstreamURL, path string) (Item, error) {
	cfg := r.Upstream()
	if cfg.Offline {
		return s.cachedOnly(ctx, r.Name, path)
	}

	clean, err := CleanPath(path)
	if err != nil {
		return Item{}, err
	}

	if clean == "" {
		return Item{}, ErrNotFound
	}

	return s.resolveCached(ctx, r, cfg, upstreamURL, clean)
}

func (s *Service) cachedOnly(ctx context.Context, repoKey, path string) (Item, error) {
	return s.Stat(ctx, repoKey, path)
}

func (s *Service) resolveCached(
	ctx context.Context,
	r Repository,
	cfg RemoteConfig,
	upstreamURL, path string,
) (Item, error) {
	if cfg.DisableCache {
		return s.fetchUncached(ctx, r, cfg, upstreamURL, path)
	}

	cached, err := s.Stat(ctx, r.Name, path)
	if err == nil {
		period := cfg.CachePeriod()
		if cached.IsFolder() || !s.isMetadata(cached.Name) || s.now().Sub(cached.Updated) < period {
			telemetry.RemoteCache(r.Name, string(r.Format), "hit")
			return cached, nil
		}

		fresh, ferr := s.fetchShared(ctx, r, cfg, upstreamURL, path)
		if ferr != nil {
			telemetry.RemoteCache(r.Name, string(r.Format), "stale")
			s.log.WarnContext(ctx, "serving stale remote metadata", "repo", r.Name, "path", path, "error", ferr)

			return cached, nil
		}

		telemetry.RemoteCache(r.Name, string(r.Format), "miss")

		return fresh, nil
	}

	if !errors.Is(err, ErrNotFound) {
		return cached, err
	}

	missedKey := r.Name + "\x00" + path
	if s.missed.recent(missedKey, s.now()) {
		telemetry.RemoteCache(r.Name, string(r.Format), "absent")
		return Item{}, ErrNotFound
	}

	item, err := s.fetchShared(ctx, r, cfg, upstreamURL, path)
	switch {
	case errors.Is(err, errUpstreamAbsent):
		if period := cfg.MissedRetrievalPeriod(); period > 0 {
			s.missed.remember(missedKey, s.now().Add(period))
		}

		telemetry.RemoteCache(r.Name, string(r.Format), "miss")
	case err == nil, errors.Is(err, ErrNotFound):
		telemetry.RemoteCache(r.Name, string(r.Format), "miss")
	default:
		telemetry.RemoteCache(r.Name, string(r.Format), "error")
	}

	return item, err
}

func (s *Service) fetchShared(
	ctx context.Context,
	r Repository,
	cfg RemoteConfig,
	upstreamURL, clean string,
) (Item, error) {
	return s.fetches.Do(ctx, r.Name+"\x00"+clean, func(ctx context.Context) (Item, error) {
		var item Item

		// Replicas fetch a path once: a waiter finds the file the holder cached.
		err := s.db.WithLock(ctx, "remote-fetch:"+r.Name+"\x00"+clean, func() error {
			cached, err := s.Stat(ctx, r.Name, clean)
			if err == nil && (!s.isMetadata(cached.Name) || s.now().Sub(cached.Updated) < cfg.CachePeriod()) {
				item = cached
				return nil
			}

			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}

			item, err = s.fetchRemote(ctx, r, cfg, upstreamURL, clean)

			return err
		})

		return item, err
	})
}

// fetchUncached fetches path for a remote repository that keeps nothing:
// its cache row is removed once the content is stored, so this request is
// served and garbage collection reclaims the content later.
func (s *Service) fetchUncached(
	ctx context.Context,
	r Repository,
	cfg RemoteConfig,
	upstreamURL, path string,
) (Item, error) {
	item, err := s.fetches.Do(ctx, r.Name+"\x00"+path, func(ctx context.Context) (Item, error) {
		item, err := s.fetchRemote(ctx, r, cfg, upstreamURL, path)
		if err != nil {
			return Item{}, err
		}

		if err := s.db.WithTx(ctx, func(q database.Querier) error {
			if _, err := deleteItem(ctx, q, item); err != nil {
				return err
			}

			return deleteEmptyParents(ctx, q, r.Name, item.Parent)
		}); err != nil {
			return Item{}, fmt.Errorf("drop the uncached %s/%s: %w", r.Name, path, err)
		}

		return item, nil
	})

	switch {
	case err == nil:
		telemetry.RemoteCache(r.Name, string(r.Format), "uncached")
	case errors.Is(err, errUpstreamAbsent), errors.Is(err, ErrNotFound):
		telemetry.RemoteCache(r.Name, string(r.Format), "miss")
	default:
		telemetry.RemoteCache(r.Name, string(r.Format), "error")
	}

	return item, err
}

// deleteEmptyParents deletes folder and the folders above it, up to the
// first that still holds something.
func deleteEmptyParents(ctx context.Context, q database.Querier, repoKey, folder string) error {
	for folder != RootPath && folder != "" {
		parent, name := SplitPath(folder)

		n, err := q.DeleteEmptyFolder(ctx, database.DeleteEmptyFolderParams{
			RepoKey:  repoKey,
			Path:     parent,
			Name:     name,
			FullPath: folder,
		})
		if err != nil {
			return fmt.Errorf("delete the empty folder %s/%s: %w", repoKey, folder, err)
		}

		if n == 0 {
			return nil
		}

		folder = parent
	}

	return nil
}

const upstreamAttempts = 3

func (s *Service) fetchRemote(
	ctx context.Context,
	r Repository,
	cfg RemoteConfig,
	upstreamURL, clean string,
) (Item, error) {
	// The retries share one budget: the per-request timeout per attempt.
	deadline := s.now().Add(upstreamAttempts * requestTimeout(cfg))

	for attempt := 1; ; attempt++ {
		item, wait, retry, err := s.fetchRemoteOnce(ctx, r, cfg, upstreamURL, clean)
		if !retry || attempt == upstreamAttempts {
			return item, err
		}

		if wait == 0 {
			wait = time.Duration(attempt) * 250 * time.Millisecond
		}

		if s.now().Add(wait).After(deadline) {
			return item, err
		}

		s.log.WarnContext(ctx, "retrying remote fetch", "repo", r.Name, "path", clean, "attempt", attempt, "error", err)

		select {
		case <-ctx.Done():
			return Item{}, fmt.Errorf("%w: %w", ErrUpstream, ctx.Err())
		case <-time.After(wait):
		}
	}
}

func requestTimeout(cfg RemoteConfig) time.Duration {
	if cfg.TimeoutMilliseconds > 0 {
		return time.Duration(cfg.TimeoutMilliseconds) * time.Millisecond
	}

	return 15 * time.Second
}

// fetchRemoteOnce makes one attempt: a failure says whether to retry, and
// how long the upstream asked to wait.
func (s *Service) fetchRemoteOnce(
	ctx context.Context,
	r Repository,
	cfg RemoteConfig,
	upstreamURL, clean string,
) (item Item, wait time.Duration, retry bool, err error) {
	timeout := requestTimeout(cfg)

	fetchCtx, cancel := context.WithCancel(telemetry.WithUpstream(ctx, r.Name, string(r.Format)))
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, upstreamURL, nil)
	if err != nil {
		return Item{}, 0, false, fmt.Errorf("%w: %w", ErrUpstream, err)
	}

	req.Header.Set("User-Agent", UserAgent)

	if cfg.Username != "" && SameSite(cfg.URL, upstreamURL) {
		req.SetBasicAuth(cfg.Username, cfg.Password)
	}

	resp, err := s.httpClient(timeout).Do(req)
	if err != nil {
		return Item{}, 0, ctx.Err() == nil && retryable(err), fmt.Errorf("%w: %w", ErrUpstream, err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusGone:
		return Item{}, 0, false, errUpstreamAbsent
	case http.StatusUnauthorized, http.StatusForbidden:
		return Item{}, 0, false, fmt.Errorf("%w: upstream answered %s", ErrNotFound, resp.Status)
	case http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return Item{}, retryAfter(resp.Header, s.now()), true, fmt.Errorf(
			"%w: %s returned %s",
			ErrUpstream,
			req.URL.Redacted(),
			resp.Status,
		)
	default:
		return Item{}, 0, false, fmt.Errorf("%w: %s returned %s", ErrUpstream, req.URL.Redacted(), resp.Status)
	}

	body := &upstreamBody{
		r:       resp.Body,
		idle:    time.AfterFunc(timeout, cancel),
		timeout: timeout,
	}
	defer body.idle.Stop()

	in := DeployInput{
		Repository: r.Name,
		Path:       clean,
		Body:       body,
		Size:       resp.ContentLength,
		User:       SystemUser,
		Expected: Checksums{
			SHA1:   resp.Header.Get("X-Checksum-Sha1"),
			SHA256: resp.Header.Get("X-Checksum-Sha256"),
			MD5:    resp.Header.Get("X-Checksum-Md5"),
		},
		cache: true,
	}

	if fn := s.upstreamCheckFor(r.Format); fn != nil {
		published, err := fn(ctx, r, upstreamURL, clean, in.Expected)
		if errors.Is(err, ErrCoolingDown) {
			return Item{}, 0, false, err
		}

		if err != nil && ctx.Err() == nil {
			s.log.WarnContext(ctx, "upstream checksums unavailable", "repo", r.Name, "path", clean, "error", err)
		}

		if in.Expected == (Checksums{}) {
			in.Expected = published
		}
	}

	item, err = s.Deploy(ctx, in)
	if body.err != nil {
		return Item{}, 0, ctx.Err() == nil, fmt.Errorf("%w: read %s: %w", ErrUpstream, req.URL.Redacted(), body.err)
	}

	if errors.Is(err, ErrChecksumMismatch) {
		return Item{}, 0, false, fmt.Errorf(
			"%w: %s does not match the checksum its upstream publishes: %w",
			ErrUpstream,
			clean,
			err,
		)
	}

	if err != nil {
		return Item{}, 0, false, err
	}

	s.missed.forget(r.Name + "\x00" + clean)

	item.format, item.class, item.source = string(r.Format), string(r.Class), "upstream"

	return item, 0, false, nil
}

// upstreamBody records a failed read, so a cut connection is told apart from a storage failure,
// and cancels the fetch when no bytes arrive for timeout.
type upstreamBody struct {
	r       io.Reader
	err     error
	idle    *time.Timer
	timeout time.Duration
}

func (b *upstreamBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if n > 0 {
		b.idle.Reset(b.timeout)
	}

	if err != nil && !errors.Is(err, io.EOF) {
		b.err = err
	}

	return n, err
}

func (s *Service) httpClient(timeout time.Duration) *http.Client {
	if s.client != nil {
		return s.client
	}

	client, ok := s.upstreams.Load(timeout)
	if !ok {
		client, _ = s.upstreams.LoadOrStore(timeout, newUpstreamClient(timeout, s.allowPrivate, s.upstreamRoots))
	}

	return client.(*http.Client)
}

func escapeSegments(p string) string {
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		parts[i] = url.PathEscape(seg)
	}

	return strings.Join(parts, "/")
}

// Flatten lists the repositories r resolves from, in order: r itself, or a
// virtual repository's members with nested virtual repositories expanded.
// Remote members are left out for a name (a package name or path, or "" for
// none) a virtual repository on the way keeps to its local members.
func (s *Service) Flatten(ctx context.Context, r Repository, name string) []Repository {
	return s.flatten(ctx, r, name, 0, false)
}

func (s *Service) flatten(ctx context.Context, r Repository, name string, depth int, localOnly bool) []Repository {
	switch {
	case r.Class == ClassRemote && localOnly:
		return nil
	case r.Class != ClassVirtual:
		return []Repository{
			r,
		}
	case depth >= MaxVirtualDepth:
		return nil
	}

	localOnly = localOnly || r.LocalOnly(name)

	var out []Repository

	for _, key := range r.Members() {
		if m, err := s.GetRepository(ctx, key); err == nil {
			out = append(out, s.flatten(ctx, m, name, depth+1, localOnly)...)
		}
	}

	return out
}

// Members returns a virtual repository's member keys in resolution order.
func (s *Service) Members(r Repository) []string {
	return r.Members()
}

// DeployTarget returns where a deploy to repoKey lands: the repository
// itself, or a virtual repository's default deployment repository.
func (s *Service) DeployTarget(ctx context.Context, repoKey string) (string, error) {
	r, err := s.GetRepository(ctx, repoKey)
	if err != nil {
		return "", err
	}

	if r.Class != "virtual" {
		return repoKey, nil
	}

	var target string
	if r.Config.Virtual != nil {
		target = r.Config.Virtual.DeployTo
	}

	if target == "" {
		return "", fmt.Errorf("%w: virtual repository %s has no default deployment repository", ErrNotLocal, repoKey)
	}

	return target, nil
}
