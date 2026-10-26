package artifactory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"relic/api/adapters/artifactory/wire"
	"relic/internal/artifacts/repository"
	"relic/internal/delivery/replication"
)

type replicationConfig struct {
	Username                 string `json:"username"`
	Password                 string `json:"password"`
	URL                      string `json:"url"`
	CronExp                  string `json:"cronExp"`
	RepoKey                  string `json:"repoKey"`
	EnableEventReplication   bool   `json:"enableEventReplication"`
	SocketTimeoutMillis      int    `json:"socketTimeoutMillis"`
	Enabled                  bool   `json:"enabled"`
	SyncDeletes              bool   `json:"syncDeletes"`
	SyncProperties           bool   `json:"syncProperties"`
	SyncStatistics           bool   `json:"syncStatistics"`
	PathPrefix               string `json:"pathPrefix"`
	IncludePathPrefixPattern string `json:"includePathPrefixPattern"`
	Proxy                    string `json:"proxy"`
}

const replicationExtension = "artifactory"

type replicationExtras struct {
	CronExp                  string `json:"cronExp,omitempty"`
	SocketTimeoutMillis      int    `json:"socketTimeoutMillis,omitempty"`
	SyncStatistics           bool   `json:"syncStatistics,omitempty"`
	IncludePathPrefixPattern string `json:"includePathPrefixPattern,omitempty"`
	Proxy                    string `json:"proxy,omitempty"`
}

func replicationFromArtifactory(repository string, c replicationConfig) (replication.Config, error) {
	if c.URL != "" && !namesTargetRepository(c.URL) {
		return replication.Config{}, fmt.Errorf(
			"%w: url %q must end with the target repository",
			replication.ErrInvalid,
			c.URL,
		)
	}

	out := replication.Config{
		Repository:     repository,
		TargetURL:      c.URL,
		Username:       c.Username,
		Password:       c.Password,
		Enabled:        c.Enabled,
		TimeoutSeconds: (c.SocketTimeoutMillis + 999) / 1000,
		SyncDeletes:    c.SyncDeletes,
		SyncProperties: c.SyncProperties,
		PathPrefix:     c.PathPrefix,
		OnChange:       c.EnableEventReplication,
	}

	extras := replicationExtras{
		SyncStatistics:           c.SyncStatistics,
		IncludePathPrefixPattern: c.IncludePathPrefixPattern,
		Proxy:                    c.Proxy,
	}
	if c.CronExp != "" {
		schedule, err := cronFromQuartz(c.CronExp)
		if err != nil {
			return replication.Config{}, fmt.Errorf("%w: %w", replication.ErrInvalid, err)
		}

		out.Schedule = schedule
		if quartzFromCron(schedule) != c.CronExp {
			extras.CronExp = c.CronExp
		}
	}

	if out.TimeoutSeconds*1000 != c.SocketTimeoutMillis {
		extras.SocketTimeoutMillis = c.SocketTimeoutMillis
	}

	if extras != (replicationExtras{}) {
		raw, err := json.Marshal(extras)
		if err != nil {
			return replication.Config{}, err
		}

		out.Extensions = map[string]json.RawMessage{
			replicationExtension: raw,
		}
	}

	return out, nil
}

func replicationToArtifactory(c replication.Config) replicationConfig {
	var extras replicationExtras
	if raw := c.Extensions[replicationExtension]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &extras)
	}

	out := replicationConfig{
		Username:                 c.Username,
		URL:                      c.TargetURL,
		RepoKey:                  c.Repository,
		EnableEventReplication:   c.OnChange,
		SocketTimeoutMillis:      c.TimeoutSeconds * 1000,
		Enabled:                  c.Enabled,
		SyncDeletes:              c.SyncDeletes,
		SyncProperties:           c.SyncProperties,
		SyncStatistics:           extras.SyncStatistics,
		PathPrefix:               c.PathPrefix,
		IncludePathPrefixPattern: extras.IncludePathPrefixPattern,
		Proxy:                    extras.Proxy,
	}
	if c.Schedule != "" {
		out.CronExp = quartzFromCron(c.Schedule)
		if s, err := cronFromQuartz(extras.CronExp); extras.CronExp != "" && err == nil && s == c.Schedule {
			out.CronExp = extras.CronExp
		}
	}

	if ms := extras.SocketTimeoutMillis; ms != 0 && (ms+999)/1000 == c.TimeoutSeconds {
		out.SocketTimeoutMillis = ms
	}

	return out
}

var replicationStatuses = map[replication.Status]string{
	replication.StatusNever:  "never_run",
	replication.StatusOK:     "ok",
	replication.StatusFailed: "error",
}

// SetReplications sets the service that stores push replications.
func (h *Handler) SetReplications(s *replication.Service) { h.replications = s }

func (h *Handler) putReplication(w http.ResponseWriter, r *http.Request) {
	h.saveReplication(w, r, false)
}

func (h *Handler) updateReplication(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("repoKey")
	if _, err := h.replications.Get(r.Context(), key); err != nil {
		h.replicationError(w, r, key, err)
		return
	}

	h.saveReplication(w, r, true)
}

func (h *Handler) saveReplication(w http.ResponseWriter, r *http.Request, update bool) {
	key := r.PathValue("repoKey")
	if _, err := h.repos.GetRepository(r.Context(), key); err != nil {
		h.writeError(w, r, err)
		return
	}

	var cfg replicationConfig
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&cfg); err != nil {
		wire.WriteError(w, http.StatusBadRequest, "invalid replication configuration")
		return
	}

	status, save := http.StatusCreated, h.replications.Save
	if update {
		status, save = http.StatusOK, h.replications.Update
	}

	native, err := replicationFromArtifactory(key, cfg)
	if err == nil {
		err = save(r.Context(), native)
	}

	if errors.Is(err, replication.ErrInvalid) {
		wire.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err != nil {
		h.replicationError(w, r, key, err)
		return
	}

	w.WriteHeader(status)
}

func (h *Handler) replicationError(w http.ResponseWriter, r *http.Request, key string, err error) {
	if errors.Is(err, replication.ErrNotConfigured) {
		wire.WriteError(w, http.StatusNotFound, "No replication configured for "+key)
		return
	}

	h.writeError(w, r, err)
}

func (h *Handler) getReplication(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("repoKey")

	rep, err := h.replications.Get(r.Context(), key)
	if err != nil {
		h.replicationError(w, r, key, err)
		return
	}

	wire.WriteJSON(w, http.StatusOK, []replicationConfig{
		replicationToArtifactory(rep.Config),
	})
}

// deleteReplication serves DELETE /api/replications/{repoKey} (exactly 200).
func (h *Handler) deleteReplication(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("repoKey")
	if err := h.replications.Delete(r.Context(), key); err != nil {
		h.replicationError(w, r, key, err)
		return
	}

	writeText(w, http.StatusOK, "Replication of "+key+" deleted.")
}

func (h *Handler) replicationStatus(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("repoKey")

	rep, err := h.replications.Get(r.Context(), key)
	if err != nil {
		h.replicationError(w, r, key, err)
		return
	}

	out := map[string]string{
		"status": replicationStatuses[rep.LastStatus],
	}
	if rep.LastRun != nil {
		out["lastCompleted"] = formatTime(*rep.LastRun)
	}

	if rep.LastError != "" {
		out["error"] = rep.LastError
	}

	wire.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) executeReplication(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("repoKey")
	if _, err := h.replications.Get(r.Context(), key); err != nil {
		h.replicationError(w, r, key, err)
		return
	}

	if h.enqueue == nil {
		wire.WriteError(w, http.StatusServiceUnavailable, "replication queue unavailable")
		return
	}

	if err := h.enqueue(r.Context(), key); err != nil {
		h.writeError(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusAccepted, map[string]string{
		"message": "Replication of " + key + " queued",
	})
}

func namesTargetRepository(raw string) bool {
	u, err := url.Parse(raw)

	return err == nil && u.Host != "" && strings.Trim(u.Path, "/") != ""
}

// ReplicationTargets reaches push-replication targets over the adapter's
// REST API.
func ReplicationTargets(client *http.Client) replication.Dial {
	if client == nil {
		client = &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
			},
		}
	}

	return func(cfg replication.Config) (replication.Target, error) {
		u := strings.TrimSuffix(cfg.TargetURL, "/")

		i := strings.LastIndex(u, "/")
		if i < 0 || !namesTargetRepository(u) {
			return nil, fmt.Errorf("invalid replication url %q", cfg.TargetURL)
		}

		t := &replicationTarget{
			client:  client,
			cfg:     cfg,
			base:    u[:i],
			repo:    u[i+1:],
			timeout: 15 * time.Second,
		}
		if cfg.TimeoutSeconds > 0 {
			t.timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
		}

		return t, nil
	}
}

type replicationTarget struct {
	client  *http.Client
	cfg     replication.Config
	base    string
	repo    string
	timeout time.Duration
}

func (t *replicationTarget) fileURL(path string) string {
	return t.base + "/" + escapePath(t.repo+"/"+path)
}

func remoteFile(resp *http.Response) replication.RemoteFile {
	modified, _ := http.ParseTime(resp.Header.Get("Last-Modified"))

	return replication.RemoteFile{
		Found: resp.StatusCode == http.StatusOK,
		Checksums: repository.Checksums{
			SHA1:   resp.Header.Get("X-Checksum-Sha1"),
			SHA256: resp.Header.Get("X-Checksum-Sha256"),
			MD5:    resp.Header.Get("X-Checksum-Md5"),
		},
		Size:     resp.ContentLength,
		Modified: modified,
	}
}

func (t *replicationTarget) Stat(ctx context.Context, path string) (replication.RemoteFile, error) {
	resp, err := t.do(ctx, http.MethodHead, t.fileURL(path), nil, 0, nil)
	if err != nil {
		return replication.RemoteFile{}, err
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return replication.RemoteFile{}, fmt.Errorf("target returned %s", resp.Status)
	}

	return remoteFile(resp), nil
}

func (t *replicationTarget) Open(ctx context.Context, path string) (io.ReadCloser, replication.RemoteFile, error) {
	resp, err := t.do(ctx, http.MethodGet, t.fileURL(path), nil, 0, nil)
	if err != nil {
		return nil, replication.RemoteFile{}, err
	}

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()

		return nil, replication.RemoteFile{}, fmt.Errorf("target returned %s", resp.Status)
	}

	return resp.Body, remoteFile(resp), nil
}

func checksumHeader(sums repository.Checksums) http.Header {
	return http.Header{
		"X-Checksum-Sha1": {
			sums.SHA1,
		},
		"X-Checksum-Sha256": {
			sums.SHA256,
		},
		"X-Checksum-Md5": {
			sums.MD5,
		},
	}
}

func (t *replicationTarget) DeployByChecksum(
	ctx context.Context,
	path string,
	sums repository.Checksums,
	props repository.Properties,
) (bool, error) {
	header := checksumHeader(sums)
	header.Set("X-Checksum-Deploy", "true")

	resp, err := t.do(ctx, http.MethodPut, t.fileURL(path)+matrixParams(props), nil, 0, header)
	if err != nil {
		return false, err
	}

	_ = resp.Body.Close()

	return resp.StatusCode/100 == 2, nil
}

func (t *replicationTarget) Deploy(
	ctx context.Context,
	path string,
	body io.Reader,
	size int64,
	sums repository.Checksums,
	props repository.Properties,
) error {
	resp, err := t.do(ctx, http.MethodPut, t.fileURL(path)+matrixParams(props), body, size, checksumHeader(sums))
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("target returned %s", resp.Status)
	}

	return nil
}

func (t *replicationTarget) storageURL(path string) string {
	return t.base + "/api/storage/" + escapePath(t.repo+"/"+path)
}

func (t *replicationTarget) Properties(ctx context.Context, path string) (repository.Properties, error) {
	resp, err := t.do(ctx, http.MethodGet, t.storageURL(path)+"?properties", nil, 0, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return repository.Properties{}, nil
	default:
		return nil, fmt.Errorf("target returned %s", resp.Status)
	}

	var body itemProperties
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode target properties: %w", err)
	}

	return body.Properties, nil
}

func (t *replicationTarget) SetProperties(ctx context.Context, path string, props repository.Properties) error {
	query := "?properties=" + strings.TrimPrefix(matrixParams(props), ";")

	return t.changeProperties(ctx, http.MethodPut, t.storageURL(path)+query)
}

func (t *replicationTarget) DeleteProperties(ctx context.Context, path string, keys []string) error {
	escaped := make([]string, 0, len(keys))
	for _, k := range keys {
		escaped = append(escaped, url.QueryEscape(k))
	}

	return t.changeProperties(ctx, http.MethodDelete, t.storageURL(path)+"?properties="+strings.Join(escaped, ","))
}

func (t *replicationTarget) changeProperties(ctx context.Context, method, rawURL string) error {
	resp, err := t.do(ctx, method, rawURL, nil, 0, nil)
	if err != nil {
		return err
	}

	_ = resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("target returned %s", resp.Status)
	}

	return nil
}

func (t *replicationTarget) Files(ctx context.Context) ([]string, error) {
	resp, err := t.do(ctx, http.MethodGet, t.base+"/api/storage/"+escapePath(t.repo)+"/?list&deep=1", nil, 0, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", resp.Status)
	}

	return targetFiles(resp.Body, maxTargetListingBytes)
}

const maxTargetListingBytes = 1 << 30

func targetFiles(body io.Reader, limit int64) ([]string, error) {
	limited := &io.LimitedReader{
		R: body,
		N: limit + 1,
	}

	paths, err := decodeTargetFiles(json.NewDecoder(limited))
	if limited.N <= 0 {
		return nil, fmt.Errorf("target listing larger than %d bytes", limit)
	}

	if err != nil {
		return nil, fmt.Errorf("decode target listing: %w", err)
	}

	return paths, nil
}

func decodeTargetFiles(dec *json.Decoder) ([]string, error) {
	if err := expectDelim(dec, '{'); err != nil {
		return nil, err
	}

	var paths []string

	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, err
		}

		if key != "files" {
			var skipped json.RawMessage
			if err := dec.Decode(&skipped); err != nil {
				return nil, err
			}

			continue
		}

		if err := expectDelim(dec, '['); err != nil {
			return nil, err
		}

		for dec.More() {
			var f struct {
				URI    string `json:"uri"`
				Folder bool   `json:"folder"`
			}
			if err := dec.Decode(&f); err != nil {
				return nil, err
			}

			if !f.Folder {
				paths = append(paths, strings.TrimPrefix(f.URI, "/"))
			}
		}

		if err := expectDelim(dec, ']'); err != nil {
			return nil, err
		}
	}

	return paths, expectDelim(dec, '}')
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}

	if token != want {
		return fmt.Errorf("got %v, want %v", token, want)
	}

	return nil
}

func (t *replicationTarget) Delete(ctx context.Context, path string) error {
	resp, err := t.do(ctx, http.MethodDelete, t.fileURL(path), nil, 0, nil)
	if err != nil {
		return err
	}

	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("%s", resp.Status)
	}

	return nil
}

func (t *replicationTarget) do(
	ctx context.Context,
	method, rawURL string,
	body io.Reader,
	size int64,
	header http.Header,
) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, t.timeout*4)

	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		cancel()
		return nil, err
	}

	maps.Copy(req.Header, header)
	req.Header.Set("User-Agent", repository.UserAgent)

	if body != nil {
		req.ContentLength = size
	}

	if t.cfg.Username != "" {
		req.SetBasicAuth(t.cfg.Username, t.cfg.Password)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}

	resp.Body = cancelOnClose{
		resp.Body,
		cancel,
	}

	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	defer c.cancel()
	return c.ReadCloser.Close()
}

func matrixParams(props repository.Properties) string {
	var b strings.Builder

	for _, k := range props.Keys() {
		for _, v := range props[k] {
			b.WriteString(";" + url.QueryEscape(k) + "=" + url.QueryEscape(strings.ReplaceAll(v, ",", `\,`)))
		}
	}

	return b.String()
}
