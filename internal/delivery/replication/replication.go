// Package replication pushes a local repository's content to another
// repository.
package replication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database"
	"relic/internal/platform/sealing"

	"github.com/robfig/cron/v3"
)

// Config is a repository's replication settings.
type Config struct {
	// Repository is the replicated repository.
	Repository string `json:"-"`
	// TargetURL is the target repository's URL, in the form the Target
	// dialer reads; empty for a pull replication of a remote repository.
	TargetURL string `json:"targetUrl,omitempty"`
	Username  string `json:"username,omitempty"`
	// Password is sealed at rest; reads never return it.
	Password string `json:"password,omitempty"`
	// Schedule is a standard 5-field cron expression ("0 12 * * *").
	Schedule string `json:"schedule,omitempty"`
	Enabled  bool   `json:"enabled"`
	// TimeoutSeconds bounds each request to the target; 0 uses the default.
	TimeoutSeconds int  `json:"timeoutSeconds,omitempty"`
	SyncDeletes    bool `json:"syncDeletes,omitempty"`
	SyncProperties bool `json:"syncProperties,omitempty"`
	// PathPrefix limits the push to one folder.
	PathPrefix string `json:"pathPrefix,omitempty"`
	// OnChange also synchronizes after each change as it is made.
	OnChange bool `json:"onChange,omitempty"`
	// Direction is push (the default), pull or federated.
	Direction Direction `json:"direction,omitempty"`
	// Extensions keeps settings only an API adapter understands, by adapter
	// name.
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
}

// Direction is which way a replication copies files.
type Direction string

// Directions.
const (
	// DirectionPush copies the repository's files to the target.
	DirectionPush Direction = "push"
	// DirectionPull copies the target's files that the repository lacks.
	DirectionPull Direction = "pull"
	// DirectionFederated keeps the repository and the target alike both
	// ways: each run pulls, then pushes; the last write wins, and a delete
	// wins over an older write.
	DirectionFederated Direction = "federated"
)

// Effective is the direction, push when unset.
func (c Config) Effective() Direction {
	if c.Direction == "" {
		return DirectionPush
	}

	return c.Direction
}

// Status is how a replication's last run went.
type Status string

// Statuses.
const (
	StatusNever  Status = "never"
	StatusOK     Status = "ok"
	StatusFailed Status = "failed"
)

// Errors.
var (
	// ErrNotConfigured is returned for repositories without a replication.
	ErrNotConfigured = errors.New("no replication configured")
	// ErrInvalid is a replication Relic cannot run.
	ErrInvalid = errors.New("invalid replication")
)

// Validate checks settings before they are stored.
func (c Config) Validate() error {
	if c.Schedule != "" {
		if err := ValidSchedule(c.Schedule); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
	}

	if c.TimeoutSeconds < 0 {
		return fmt.Errorf("%w: the timeout must not be negative", ErrInvalid)
	}

	switch c.Direction {
	case "", DirectionPush, DirectionPull, DirectionFederated:
	default:
		return fmt.Errorf("%w: direction %q: push, pull or federated", ErrInvalid, c.Direction)
	}

	return nil
}

// Result summarizes one run.
type Result struct {
	Pushed, Pulled, Skipped, Deleted, Failed int
}

// RemoteFile is a file as the target has it.
type RemoteFile struct {
	Found     bool
	Checksums repository.Checksums
	Size      int64
	Modified  time.Time
}

// Target is the far end of a replication: one repository of another server.
type Target interface {
	// Stat returns the target's file at path; Found is false when the
	// target has none there.
	Stat(ctx context.Context, path string) (RemoteFile, error)
	// Open reads the target's file at path.
	Open(ctx context.Context, path string) (io.ReadCloser, RemoteFile, error)
	// DeployByChecksum stores the file at path from content the target
	// already has, with props when they are not nil; false when the target
	// lacks the content.
	DeployByChecksum(
		ctx context.Context,
		path string,
		sums repository.Checksums,
		props repository.Properties,
	) (bool, error)
	// Deploy stores body at path, with props when they are not nil.
	Deploy(
		ctx context.Context,
		path string,
		body io.Reader,
		size int64,
		sums repository.Checksums,
		props repository.Properties,
	) error
	// Properties returns the properties of the target's file at path.
	Properties(ctx context.Context, path string) (repository.Properties, error)
	// SetProperties sets the values of props' keys on the target's file at
	// path, keeping its other keys.
	SetProperties(ctx context.Context, path string, props repository.Properties) error
	// DeleteProperties removes keys from the target's file at path.
	DeleteProperties(ctx context.Context, path string, keys []string) error
	// Files lists the paths of every file in the target repository.
	Files(ctx context.Context) ([]string, error)
	// Delete removes the file at path; a missing file is not an error.
	Delete(ctx context.Context, path string) error
}

// Dial opens the target a replication pushes to.
type Dial func(cfg Config) (Target, error)

// Pusher runs push replications.
type Pusher struct {
	db    *database.DB
	repos *repository.Service
	dial  Dial
	log   *slog.Logger
	now   func() time.Time
	box   *sealing.Box
}

// SetSecrets sets the box that opens stored push passwords.
func (p *Pusher) SetSecrets(box *sealing.Box) { p.box = box }

// PasswordPlace is where a replication's push password is sealed.
func PasswordPlace(repository string) string { return "replications/" + repository + "/password" }

// NewPusher returns a Pusher that reaches targets through dial (nil for a
// Pusher that only loads settings).
func NewPusher(db *database.DB, repos *repository.Service, dial Dial, log *slog.Logger) *Pusher {
	return &Pusher{
		db:    db,
		repos: repos,
		dial:  dial,
		log:   log,
		now:   time.Now,
	}
}

// Load returns a repository's replication settings.
func (p *Pusher) Load(ctx context.Context, repository string) (Config, database.Replication, error) {
	row, err := p.db.GetReplication(ctx, repository)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Config{}, database.Replication{}, ErrNotConfigured
	}

	if err != nil {
		return Config{}, database.Replication{}, err
	}

	var cfg Config
	if err := json.Unmarshal(row.Config, &cfg); err != nil {
		return Config{}, database.Replication{}, fmt.Errorf("decode replication %s: %w", repository, err)
	}

	cfg.Repository = row.Repository

	return cfg, row, nil
}

// Run pushes repository to its target and records the outcome.
func (p *Pusher) Run(ctx context.Context, repository string) (Result, error) {
	cfg, _, err := p.Load(ctx, repository)
	if err != nil {
		return Result{}, err
	}

	if cfg.TargetURL == "" {
		return Result{}, fmt.Errorf("%w: %s has no target", ErrNotConfigured, repository)
	}

	var res Result

	cfg.Password, err = p.box.OpenString(cfg.Password, PasswordPlace(repository))

	runErr := err
	if runErr == nil {
		res, runErr = p.sync(ctx, cfg)
	}

	status, msg := StatusOK, ""
	if runErr != nil {
		status, msg = StatusFailed, runErr.Error()
	}

	now := p.now()

	err = p.db.RecordReplicationRun(
		ctx,
		database.RecordReplicationRunParams{
			Repository: repository,
			LastRun:    &now,
			LastStatus: string(status),
			LastError:  msg,
		},
	)
	if err != nil {
		return res, errors.Join(runErr, err)
	}

	return res, runErr
}

func (p *Pusher) sync(ctx context.Context, cfg Config) (Result, error) {
	if p.dial == nil {
		return Result{}, errors.New("replication: no way to reach targets")
	}

	t, err := p.dial(cfg)
	if err != nil {
		return Result{}, err
	}

	switch cfg.Effective() {
	case DirectionPull:
		return p.pull(ctx, t, cfg)
	case DirectionFederated:
		pulled, err := p.pull(ctx, t, cfg)
		if err != nil {
			return pulled, err
		}

		pushed, err := p.push(ctx, t, cfg)
		pushed.Pulled, pushed.Skipped = pulled.Pulled, pushed.Skipped+pulled.Skipped
		pushed.Failed += pulled.Failed

		return pushed, err
	case DirectionPush:
	}

	return p.push(ctx, t, cfg)
}

func (p *Pusher) push(ctx context.Context, t Target, cfg Config) (Result, error) {
	root, err := p.repos.Stat(ctx, cfg.Repository, "")
	if err != nil {
		return Result{}, err
	}

	items, err := p.repos.Subtree(ctx, root)
	if err != nil {
		return Result{}, err
	}

	prefix := strings.Trim(cfg.PathPrefix, "/")

	var (
		res    Result
		failed []error
	)

	source := map[string]bool{}

	for _, it := range items {
		if it.IsFolder() || !underPrefix(it.Path(), prefix) {
			continue
		}

		source[it.Path()] = true

		pushed, err := p.pushFile(ctx, t, cfg, it)
		if ctx.Err() != nil {
			return res, errors.Join(append(failed, ctx.Err())...)
		}

		if err != nil {
			res.Failed++

			if len(failed) < maxReportedFailures {
				failed = append(failed, fmt.Errorf("push %s: %w", it.Path(), err))
			}

			continue
		}

		if pushed {
			res.Pushed++
		} else {
			res.Skipped++
		}
	}

	switch {
	case cfg.Effective() == DirectionFederated:
		n, err := p.pushTombstones(ctx, t, cfg)
		res.Deleted = n

		if err != nil {
			failed = append(failed, err)
		}
	case cfg.SyncDeletes:
		n, err := syncDeletes(ctx, t, prefix, source)
		res.Deleted = n

		if err != nil {
			failed = append(failed, err)
		}
	}

	if res.Failed > maxReportedFailures {
		failed = append(failed, fmt.Errorf("and %d more files", res.Failed-maxReportedFailures))
	}

	return res, errors.Join(failed...)
}

// maxReportedFailures caps the files a failed run's error names.
const maxReportedFailures = 10

func underPrefix(path, prefix string) bool {
	return prefix == "" || path == prefix || strings.HasPrefix(path, prefix+"/")
}

func (p *Pusher) pushFile(ctx context.Context, t Target, cfg Config, it repository.Item) (bool, error) {
	remote, err := t.Stat(ctx, it.Path())
	if err != nil {
		return false, err
	}

	if cfg.Effective() == DirectionFederated && remote.Found && remote.Modified.After(it.Modified) {
		return false, nil
	}

	same := remote.Found && strings.EqualFold(remote.Checksums.SHA1, it.Checksums.SHA1)
	if same && !cfg.SyncProperties {
		return false, nil
	}

	var props repository.Properties

	if cfg.SyncProperties {
		if props, err = p.repos.Properties(ctx, it); err != nil {
			return false, err
		}
	}

	if same {
		return syncTargetProperties(ctx, t, it.Path(), props)
	}

	if err := p.deploy(ctx, t, it, props); err != nil {
		return false, err
	}

	if remote.Found && cfg.SyncProperties {
		if _, err := syncTargetProperties(ctx, t, it.Path(), props); err != nil {
			return true, err
		}
	}

	return true, nil
}

func (p *Pusher) deploy(ctx context.Context, t Target, it repository.Item, props repository.Properties) error {
	if ok, err := t.DeployByChecksum(ctx, it.Path(), it.Checksums, props); err != nil || ok {
		return err
	}

	body, err := p.repos.Open(ctx, it, nil)
	if err != nil {
		return err
	}

	defer func() {
		_ = body.Close()
	}()

	return t.Deploy(ctx, it.Path(), body, it.Size, it.Checksums, props)
}

func syncTargetProperties(ctx context.Context, t Target, path string, want repository.Properties) (bool, error) {
	have, err := t.Properties(ctx, path)
	if err != nil {
		return false, fmt.Errorf("target properties: %w", err)
	}

	var removed []string

	for _, k := range have.Keys() {
		if _, ok := want[k]; !ok {
			removed = append(removed, k)
		}
	}

	changed := repository.Properties{}

	for k, values := range want {
		if !sameValues(have[k], values) {
			changed[k] = values
		}
	}

	if len(removed) > 0 {
		if err := t.DeleteProperties(ctx, path, removed); err != nil {
			return false, fmt.Errorf("delete target properties: %w", err)
		}
	}

	if len(changed) > 0 {
		if err := t.SetProperties(ctx, path, changed); err != nil {
			return false, fmt.Errorf("set target properties: %w", err)
		}
	}

	return len(removed)+len(changed) > 0, nil
}

func sameValues(a, b []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}

func syncDeletes(ctx context.Context, t Target, prefix string, source map[string]bool) (int, error) {
	files, err := t.Files(ctx)
	if err != nil {
		return 0, fmt.Errorf("list target: %w", err)
	}

	var doomed []string

	for _, path := range files {
		if !source[path] && underPrefix(path, prefix) {
			doomed = append(doomed, path)
		}
	}

	sort.Strings(doomed)

	for _, path := range doomed {
		if err := t.Delete(ctx, path); err != nil {
			return 0, fmt.Errorf("delete %s on target: %w", path, err)
		}
	}

	return len(doomed), nil
}

// Due reports whether a standard 5-field cron expression ("0 12 * * *",
// or a descriptor such as @daily) fired between last and now.
func Due(expr string, last *time.Time, now time.Time, window time.Duration) (bool, error) {
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return false, fmt.Errorf("schedule %q: %w", expr, err)
	}

	from := now.Add(-window)
	if last != nil && last.After(from) {
		from = *last
	}

	return !sched.Next(from).After(now), nil
}

// ValidSchedule reports whether expr is a schedule Due accepts.
func ValidSchedule(expr string) error {
	if _, err := cron.ParseStandard(expr); err != nil {
		return fmt.Errorf("schedule %q: %w", expr, err)
	}

	return nil
}

// SealStored seals push passwords stored before RELIC_ENCRYPTION_KEY was set.
func SealStored(ctx context.Context, db *database.DB, box *sealing.Box) error {
	if !box.Enabled() {
		return nil
	}

	rows, err := db.ListReplications(ctx)
	if err != nil {
		return fmt.Errorf("list replications: %w", err)
	}

	for _, r := range rows {
		var doc map[string]any
		if err := json.Unmarshal(r.Config, &doc); err != nil {
			return fmt.Errorf("decode replication %s: %w", r.Repository, err)
		}

		pw, _ := doc["password"].(string)
		if pw == "" || sealing.SealedString(pw) {
			continue
		}

		if doc["password"], err = box.SealString(pw, PasswordPlace(r.Repository)); err != nil {
			return err
		}

		sealed, err := json.Marshal(doc)
		if err != nil {
			return err
		}

		if err := db.UpsertReplication(
			ctx,
			database.UpsertReplicationParams{
				Repository: r.Repository,
				Config:     sealed,
			},
		); err != nil {
			return fmt.Errorf("seal replication %s: %w", r.Repository, err)
		}
	}

	return nil
}
