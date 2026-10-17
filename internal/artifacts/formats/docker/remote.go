package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/telemetry"
)

const blobsDir = "_blobs"

const maxCachedManifests = 10000

var manifestAccept = strings.Join(
	[]string{
		MediaTypeOCIIndex,
		MediaTypeDockerList,
		MediaTypeOCIManifest,
		MediaTypeDockerManifest,
	},
	", ",
)

// UpstreamImage maps a client's image name to the upstream's: Docker Hub
// keeps official images under library/.
func UpstreamImage(upstreamURL, image string) string {
	if isDockerHub(upstreamURL) && !strings.Contains(image, "/") {
		return "library/" + image
	}

	return image
}

func isDockerHub(u string) bool {
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}

	switch parsed.Hostname() {
	case "registry-1.docker.io", "registry.hub.docker.com", "index.docker.io", "docker.io":
		return true
	}

	return false
}

func upstreamBase(u string) string {
	u = strings.TrimSuffix(u, "/")
	if isDockerHub(u) {
		return "https://registry-1.docker.io"
	}

	return u
}

// Manifest finds image's manifest in any kind of repository: stored for
// a local one, cached or fetched for a remote one.
func (s *Service) Manifest(ctx context.Context, r repository.Repository, image, ref string) (Manifest, error) {
	if r.Class != "remote" {
		return s.GetManifest(ctx, r.Name, image, ref)
	}

	up := s.repos.UpstreamOf(r)
	image = UpstreamImage(up.URL, image)
	cached, err := s.GetManifest(ctx, r.Name, image, ref)

	_, byDigest := ParseDigest(ref)
	switch {
	case err == nil && (byDigest || up.Offline || time.Since(cached.Item.Updated) < up.CachePeriod):
		return cached, nil
	case err != nil && !errors.Is(err, ErrManifestUnknown):
		return Manifest{}, err
	case up.Offline:
		return Manifest{}, ErrManifestUnknown
	}

	fresh, ferr := s.manifestFetches.Do(
		ctx,
		r.Name+"\x00"+image+"\x00"+ref,
		func(ctx context.Context) (Manifest, error) {
			return s.fetchManifest(ctx, r, up, image, ref)
		},
	)
	if ferr != nil && err == nil {
		s.log.WarnContext(
			ctx,
			"serving stale docker manifest",
			"repo",
			r.Name,
			"image",
			image,
			"ref",
			ref,
			"error",
			ferr,
		)

		return cached, nil
	}

	return fresh, ferr
}

func (s *Service) fetchManifest(
	ctx context.Context,
	r repository.Repository,
	up repository.Upstream,
	image, ref string,
) (Manifest, error) {
	resp, err := s.upstreamGet(ctx, up, image, "/v2/"+image+"/manifests/"+ref, manifestAccept)
	if err != nil {
		return Manifest{}, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	switch {
	case notThere(resp.StatusCode):
		return Manifest{}, ErrManifestUnknown
	case resp.StatusCode != http.StatusOK:
		return Manifest{}, fmt.Errorf("%w: manifest %s:%s: %s", repository.ErrUpstream, image, ref, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxManifestBytes+1))
	if err != nil || len(body) > MaxManifestBytes {
		return Manifest{}, fmt.Errorf("%w: manifest %s:%s: unreadable or too large", repository.ErrUpstream, image, ref)
	}

	if _, err := s.putManifest(
		ctx,
		r.Name,
		image,
		ref,
		resp.Header.Get("Content-Type"),
		body,
		repository.SystemUser,
		true,
		nil,
		nil,
	); err != nil {
		return Manifest{}, err
	}

	m, err := s.GetManifest(ctx, r.Name, image, ref)
	if err != nil {
		return Manifest{}, err
	}

	s.linkCached(ctx, r.Name, image, m)

	return m, nil
}

// BlobIn finds a blob in any kind of repository, fetching it from a
// remote's upstream on a miss.
func (s *Service) BlobIn(ctx context.Context, r repository.Repository, image, h string) (repository.Item, error) {
	if r.Class != "remote" {
		return s.Blob(ctx, r.Name, image, h)
	}

	up := s.repos.UpstreamOf(r)
	image = UpstreamImage(up.URL, image)

	item, err := s.Blob(ctx, r.Name, image, h)
	if !errors.Is(err, ErrBlobUnknown) || up.Offline {
		return item, err
	}

	return s.blobFetches.Do(ctx, r.Name+"\x00"+h, func(ctx context.Context) (repository.Item, error) {
		return s.fetchBlob(ctx, r, up, image, h)
	})
}

func (s *Service) fetchBlob(
	ctx context.Context,
	r repository.Repository,
	up repository.Upstream,
	image, h string,
) (repository.Item, error) {
	resp, err := s.upstreamGet(ctx, up, image, "/v2/"+image+"/blobs/sha256:"+h, "")
	if err != nil {
		return repository.Item{}, err
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	switch {
	case notThere(resp.StatusCode):
		return repository.Item{}, ErrBlobUnknown
	case resp.StatusCode != http.StatusOK:
		return repository.Item{}, fmt.Errorf("%w: blob %s@sha256:%s: %s", repository.ErrUpstream, image, h, resp.Status)
	}

	item, err := s.repos.Cache(ctx, repository.DeployInput{
		Repository: r.Name,
		Path:       image + "/" + blobsDir + "/" + LayerName(h),
		Body:       resp.Body,
		User:       repository.SystemUser,
		Expected: repository.Checksums{
			SHA256: h,
		},
		Props: repository.Properties{
			"sha256": {
				h,
			},
		},
	})
	if err != nil {
		return repository.Item{}, err
	}

	s.linkBlob(ctx, r.Name, image, h)

	return item, nil
}

func notThere(status int) bool {
	return status == http.StatusNotFound || status == http.StatusUnauthorized || status == http.StatusForbidden
}

func (s *Service) linkCached(ctx context.Context, repoKey, image string, m Manifest) {
	doc, err := s.readManifest(ctx, m.Item)
	if err != nil || IsIndex(m.MediaType) {
		return
	}

	for _, h := range doc.blobs() {
		if _, err := s.Blob(ctx, repoKey, image, h); err == nil {
			s.linkInto(ctx, repoKey, image, m.Item.Parent, h)
		}
	}
}

func (s *Service) linkBlob(ctx context.Context, repoKey, image, h string) {
	manifests, err := s.repos.FilesNamed(ctx, repoKey, image, ManifestFile)
	if err != nil {
		s.log.WarnContext(ctx, "list cached manifests", "repo", repoKey, "image", image, "error", err)
		return
	}

	for _, mi := range manifests {
		blobs, ok := s.manifestBlobs.get(mi.Checksums.SHA256)
		if !ok {
			doc, err := s.readManifest(ctx, mi)
			if err != nil {
				continue
			}

			blobs = doc.blobs()
			s.manifestBlobs.put(mi.Checksums.SHA256, blobs)
		}

		if slices.Contains(blobs, h) {
			s.linkInto(ctx, repoKey, image, mi.Parent, h)
		}
	}
}

func (s *Service) linkInto(ctx context.Context, repoKey, image, folder, h string) {
	if err := s.link(ctx, repoKey, image, folder, h, repository.SystemUser, true, nil, nil); err != nil {
		s.log.WarnContext(ctx, "link cached blob", "repo", repoKey, "folder", folder, "error", err)
	}
}

func (s *Service) readManifest(ctx context.Context, item repository.Item) (manifestDoc, error) {
	rc, err := s.repos.Open(ctx, item, nil)
	if err != nil {
		return manifestDoc{}, err
	}
	defer func() {
		_ = rc.Close()
	}()

	var doc manifestDoc
	if err := json.NewDecoder(io.LimitReader(rc, MaxManifestBytes)).Decode(&doc); err != nil {
		return manifestDoc{}, ErrManifestInvalid
	}

	return doc, nil
}

func (d manifestDoc) blobs() []string {
	var out []string

	refs := d.Layers
	if d.Config != nil {
		refs = append([]descriptor{
			*d.Config,
		}, refs...)
	}

	for _, r := range refs {
		if h, ok := ParseDigest(r.Digest); ok && len(r.URLs) == 0 {
			out = append(out, h)
		}
	}

	return out
}

func (s *Service) upstreamGet(
	ctx context.Context,
	up repository.Upstream,
	image, path, accept string,
) (*http.Response, error) {
	u := upstreamBase(up.URL) + path
	scope := "repository:" + image + ":pull"
	send := func(authz string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", repository.ErrUpstream, err)
		}

		req.Header.Set("User-Agent", repository.UserAgent)

		if accept != "" {
			req.Header.Set("Accept", accept)
		}

		if authz != "" {
			req.Header.Set("Authorization", authz)
		}

		resp, err := s.repos.UpstreamClient().Do(req.WithContext(telemetry.WithUpstream(ctx, up.Repository, up.Format)))
		if err != nil {
			return nil, fmt.Errorf("%w: %w", repository.ErrUpstream, err)
		}

		return resp, nil
	}

	resp, err := send(s.tokens.get(up.URL+"\x00"+up.Username, scope))
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}

	challenge := resp.Header.Get("WWW-Authenticate")
	_ = resp.Body.Close()

	scheme, params := parseChallenge(challenge)
	switch strings.ToLower(scheme) {
	case "bearer":
		token, ttl, err := s.fetchToken(ctx, up, params, scope)
		if err != nil {
			return nil, err
		}

		s.tokens.put(up.URL+"\x00"+up.Username, scope, "Bearer "+token, ttl)

		return send("Bearer " + token)
	case "basic":
		if up.Username == "" {
			break
		}

		req, _ := http.NewRequest(http.MethodGet, u, nil) //nolint:noctx // only builds the header
		req.SetBasicAuth(up.Username, up.Password)

		return send(req.Header.Get("Authorization"))
	}

	return send("")
}

func (s *Service) fetchToken(
	ctx context.Context,
	up repository.Upstream,
	params map[string]string,
	scope string,
) (string, time.Duration, error) {
	realm, err := url.Parse(params["realm"])
	if err != nil || (realm.Scheme != "https" && realm.Scheme != "http") {
		return "", 0, fmt.Errorf("%w: invalid token realm %q", repository.ErrUpstream, params["realm"])
	}

	q := realm.Query()
	if params["service"] != "" {
		q.Set("service", params["service"])
	}

	q.Set("scope", scope)
	realm.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %w", repository.ErrUpstream, err)
	}

	req.Header.Set("User-Agent", repository.UserAgent)

	if up.Username != "" && repository.SameSite(up.URL, realm.String()) {
		req.SetBasicAuth(up.Username, up.Password)
	}

	resp, err := s.repos.UpstreamClient().Do(req.WithContext(telemetry.WithUpstream(ctx, up.Repository, up.Format)))
	if err != nil {
		return "", 0, fmt.Errorf("%w: token: %w", repository.ErrUpstream, err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("%w: token: %s", repository.ErrUpstream, resp.Status)
	}

	var tr struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil {
		return "", 0, fmt.Errorf("%w: token: %w", repository.ErrUpstream, err)
	}

	token := tr.Token
	if token == "" {
		token = tr.AccessToken
	}

	if token == "" {
		return "", 0, fmt.Errorf("%w: token: empty", repository.ErrUpstream)
	}

	ttl := time.Duration(max(tr.ExpiresIn, 60)) * time.Second

	return token, ttl, nil
}

func parseChallenge(h string) (scheme string, params map[string]string) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	params = map[string]string{}

	for rest != "" {
		rest = strings.TrimLeft(rest, " ,")

		key, after, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}

		var val string

		if strings.HasPrefix(after, `"`) {
			end := strings.Index(after[1:], `"`)
			if end < 0 {
				break
			}

			val, rest = after[1:end+1], after[end+2:]
		} else {
			val, rest, _ = strings.Cut(after, ",")
		}

		params[strings.ToLower(strings.TrimSpace(key))] = val
	}

	return scheme, params
}

type tokenCache struct {
	mu      sync.Mutex
	entries map[string]tokenEntry
}

type tokenEntry struct {
	authz   string
	expires time.Time
}

func (c *tokenCache) get(upstream, scope string) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[upstream+"\x00"+scope]
	if !ok || time.Now().After(e.expires) {
		return ""
	}

	return e.authz
}

func (c *tokenCache) put(upstream, scope, authz string, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.entries == nil || len(c.entries) > 10000 {
		c.entries = map[string]tokenEntry{}
	}

	c.entries[upstream+"\x00"+scope] = tokenEntry{
		authz:   authz,
		expires: time.Now().Add(ttl - 30*time.Second),
	}
}

type manifestBlobsCache struct {
	mu       sync.Mutex
	byDigest map[string][]string
}

func (c *manifestBlobsCache) get(manifestDigest string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	blobs, ok := c.byDigest[manifestDigest]

	return blobs, ok
}

func (c *manifestBlobsCache) put(manifestDigest string, blobs []string) {
	if manifestDigest == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.byDigest == nil || len(c.byDigest) >= maxCachedManifests {
		c.byDigest = map[string][]string{}
	}

	c.byDigest[manifestDigest] = blobs
}
