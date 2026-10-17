package repository

import (
	"context"
	"net/http"
	"time"
)

// SystemUser records files Relic writes itself (remote caches, calculated
// metadata).
const SystemUser = "_system"

// Upstream is a remote repository's upstream, for formats that speak their
// own protocol to it (Docker registries).
type Upstream struct {
	Repository, Format string
	URL                string
	Username, Password string
	Offline            bool
	CachePeriod        time.Duration
}

// UpstreamOf returns r's upstream settings.
func (s *Service) UpstreamOf(r Repository) Upstream {
	cfg := r.Upstream()

	return Upstream{
		Repository:  r.Name,
		Format:      string(r.Format),
		URL:         cfg.URL,
		Username:    cfg.Username,
		Password:    cfg.Password,
		Offline:     cfg.Offline,
		CachePeriod: cfg.CachePeriod(),
	}
}

// UpstreamClient is the HTTP client for upstream requests.
func (s *Service) UpstreamClient() *http.Client { return s.httpClient(15 * time.Second) }

// Cache stores a file fetched from upstream in remote repository in.Repo.
func (s *Service) Cache(ctx context.Context, in DeployInput) (Item, error) {
	in.cache = true
	return s.Deploy(ctx, in)
}

// CacheByChecksum is DeployByChecksum for a remote repository's cache.
func (s *Service) CacheByChecksum(ctx context.Context, in DeployInput) (Item, error) {
	in.cache = true
	return s.DeployByChecksum(ctx, in)
}

// Environments returns the environments a repository is assigned to.
func (r Repository) Environments() []string { return r.Config.Environments }
