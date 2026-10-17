package endoflife

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/package-url/packageurl-go"

	"relic/internal/platform/database"
	"relic/internal/platform/telemetry"
)

// DefaultDepsDevURL is deps.dev's API.
const DefaultDepsDevURL = "https://api.deps.dev"

const (
	depsDevTTL         = 7 * 24 * time.Hour
	depsDevConcurrency = 8
	unknownMaintained  = -1
)

// depsDevSystems maps package URL types to deps.dev's package systems.
var depsDevSystems = map[string]string{
	"npm":    "NPM",
	"pypi":   "PYPI",
	"maven":  "MAVEN",
	"golang": "GO",
	"cargo":  "CARGO",
	"nuget":  "NUGET",
	"gem":    "RUBYGEMS",
}

// DepsDev reads deprecation and OpenSSF Scorecard maintenance from
// deps.dev, keeping each answer for a week. It sends package names and
// versions to deps.dev.
type DepsDev struct {
	db     *database.DB
	base   string
	client *http.Client
	log    *slog.Logger
	now    func() time.Time
}

// NewDepsDev returns a DepsDev of base (DefaultDepsDevURL or a mirror);
// client nil uses a default one.
func NewDepsDev(db *database.DB, base string, client *http.Client, log *slog.Logger) *DepsDev {
	if client == nil {
		client = &http.Client{
			Timeout: 15 * time.Second,
		}
	}

	return &DepsDev{
		db:     db,
		base:   strings.TrimSuffix(base, "/"),
		client: client,
		log:    log,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

// Risks returns the deprecated and unmaintained packages among pkgs. A
// package deps.dev cannot answer for is left out, and logged.
func (d *DepsDev) Risks(ctx context.Context, pkgs []Package) []Risk {
	var (
		mu  sync.Mutex
		out []Risk
		wg  sync.WaitGroup
	)

	sem := make(chan struct{}, depsDevConcurrency)

	for _, p := range pkgs {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() {
				<-sem
			}()

			v, ok, err := d.version(ctx, p)
			if err != nil {
				d.log.WarnContext(ctx, "deps.dev: package not checked", "purl", p.PURL, "error", err)
			}

			if !ok {
				return
			}

			mu.Lock()
			defer mu.Unlock()

			out = append(out, versionRisks(p, v)...)
		})
	}

	wg.Wait()

	return out
}

func versionRisks(p Package, v database.DepsDevVersion) []Risk {
	var out []Risk

	if v.Deprecated {
		detail := "deprecated by its maintainers"
		if v.Reason != "" {
			detail += ": " + v.Reason
		}

		out = append(out, Risk{
			Kind:     KindDeprecated,
			Severity: SeverityMedium,
			PURL:     p.PURL,
			Name:     p.Name,
			Version:  p.Version,
			Detail:   truncate(detail, 1000),
		})
	}

	if v.Maintained == 0 {
		out = append(out, Risk{
			Kind:     KindUnmaintained,
			Severity: SeverityMedium,
			PURL:     p.PURL,
			Name:     p.Name,
			Version:  p.Version,
			Detail:   "no activity in its source repository for 90 days (OpenSSF Scorecard: Maintained 0/10)",
		})
	}

	return out
}

// version returns deps.dev's answer for p, from the cache when it is
// fresh; ok is false for package types deps.dev does not know. With an
// error, ok is true when the answer still holds: a partial one with a
// deprecation, or a whole one the cache refused.
func (d *DepsDev) version(ctx context.Context, p Package) (database.DepsDevVersion, bool, error) {
	system, name, version, ok := depsDevPackage(p.PURL)
	if !ok {
		return database.DepsDevVersion{}, false, nil
	}

	cached, err := d.db.GetDepsDevVersion(ctx, database.GetDepsDevVersionParams{
		System:  system,
		Name:    name,
		Version: version,
	})

	switch {
	case err == nil && d.now().Sub(cached.Fetched) < depsDevTTL:
		return cached, true, nil
	case err != nil && !errors.Is(database.NotFound(err), database.ErrNotFound):
		return database.DepsDevVersion{}, false, err
	}

	v, err := d.fetch(ctx, system, name, version)

	telemetry.DepsDevLookup(err)

	if err != nil {
		return v, v.Deprecated, err
	}

	if err := d.db.SaveDepsDevVersion(ctx, database.SaveDepsDevVersionParams(v)); err != nil {
		return v, true, fmt.Errorf("cache the answer: %w", err)
	}

	return v, true, nil
}

// depsDevPackage is the deps.dev system, package name and version of a
// package URL; ok is false for one deps.dev does not know.
func depsDevPackage(purl string) (system, name, version string, ok bool) {
	pu, err := packageurl.FromString(purl)
	if err != nil || pu.Version == "" {
		return "", "", "", false
	}

	system, ok = depsDevSystems[pu.Type]
	if !ok {
		return "", "", "", false
	}

	name = pu.Name

	switch {
	case pu.Type == "maven" && pu.Namespace != "":
		name = pu.Namespace + ":" + pu.Name
	case pu.Namespace != "":
		name = pu.Namespace + "/" + pu.Name
	}

	return system, name, pu.Version, true
}

type depsDevVersion struct {
	IsDeprecated     bool   `json:"isDeprecated"`
	DeprecatedReason string `json:"deprecatedReason"`
	RelatedProjects  []struct {
		ProjectKey struct {
			ID string `json:"id"`
		} `json:"projectKey"`
		RelationType string `json:"relationType"`
	} `json:"relatedProjects"`
}

type depsDevProject struct {
	Scorecard *struct {
		Checks []struct {
			Name  string `json:"name"`
			Score int    `json:"score"`
		} `json:"checks"`
	} `json:"scorecard"`
}

func (d *DepsDev) fetch(ctx context.Context, system, name, version string) (database.DepsDevVersion, error) {
	ctx, span := telemetry.Tracer().Start(ctx, "endoflife.DepsDev")
	defer span.End()

	out := database.DepsDevVersion{
		System:     system,
		Name:       name,
		Version:    version,
		Maintained: unknownMaintained,
		Fetched:    d.now(),
	}

	var v depsDevVersion

	found, err := d.get(
		ctx,
		"/v3/systems/"+system+"/packages/"+url.PathEscape(name)+"/versions/"+url.PathEscape(version),
		&v,
	)
	if err != nil || !found {
		return out, err
	}

	out.Deprecated, out.Reason = v.IsDeprecated, truncate(v.DeprecatedReason, 1000)

	for _, rp := range v.RelatedProjects {
		if rp.RelationType != "SOURCE_REPO" || rp.ProjectKey.ID == "" {
			continue
		}

		var proj depsDevProject

		found, err := d.get(ctx, "/v3/projects/"+url.PathEscape(rp.ProjectKey.ID), &proj)
		if err != nil {
			return out, err
		}

		if found && proj.Scorecard != nil {
			for _, c := range proj.Scorecard.Checks {
				if c.Name == "Maintained" {
					out.Maintained = int32(max(min(c.Score, 10), unknownMaintained)) //nolint:gosec // bounded
				}
			}
		}

		break
	}

	return out, nil
}

func (d *DepsDev) get(ctx context.Context, path string, into any) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.base+path, nil)
	if err != nil {
		return false, err
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return false, err
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("deps.dev answered %s", resp.Status)
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(into); err != nil {
		return false, fmt.Errorf("read deps.dev's answer: %w", err)
	}

	return true, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return strings.ToValidUTF8(s[:n], "")
}
