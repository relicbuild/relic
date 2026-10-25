package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"relic/internal/access/project"
	"relic/internal/artifacts/signingkey"
	"relic/internal/platform/database"
)

// Repository is a repository's identity and settings.
type Repository struct {
	Name    string
	Class   Class
	Format  Format
	Project string
	Config  Config
	Created time.Time
	Updated time.Time
}

// StorageOwner reports the format and project a repository named name must
// have when a service stores its own records there; ok is false for every
// other name.
type StorageOwner func(name string) (format Format, project string, ok bool)

// ReserveStorage keeps the names owner claims for repositories of the
// format and project it gives.
func (s *Service) ReserveStorage(owner StorageOwner) {
	s.storageOwners = append(s.storageOwners, owner)
}

func (s *Service) checkStorageName(r Repository) error {
	for _, storageOwner := range s.storageOwners {
		format, owner, ok := storageOwner(r.Name)
		if !ok || r.Format == format && storageProject(r.Project) == owner {
			continue
		}

		if owner == "" {
			return fmt.Errorf(
				"%w: %s is reserved for %s storage outside projects",
				ErrInvalidRepository,
				r.Name,
				format,
			)
		}

		return fmt.Errorf(
			"%w: %s is reserved for project %s's %s storage",
			ErrInvalidRepository,
			r.Name,
			owner,
			format,
		)
	}

	return nil
}

func storageProject(key string) string {
	if project.IsDefault(key) {
		return ""
	}

	return key
}

// CreateRepository stores a new repository.
func (s *Service) CreateRepository(ctx context.Context, r Repository) error {
	if err := r.Validate(); err != nil {
		return err
	}

	if err := s.checkStorageName(r); err != nil {
		return err
	}

	if err := s.checkMembers(ctx, r); err != nil {
		return err
	}

	if err := s.checkKeyPairs(ctx, r); err != nil {
		return err
	}

	config, err := s.encodeConfig(r)
	if err != nil {
		return err
	}

	err = s.db.WithLock(ctx, repositoryKeysLock, func() error {
		if err := s.refuseCaseVariant(ctx, r.Name); err != nil {
			return err
		}

		return s.withProject(ctx, r.Project, "", func() error {
			return s.db.CreateRepository(ctx, database.CreateRepositoryParams{
				Key:     r.Name,
				Class:   string(r.Class),
				Format:  string(r.Format),
				Project: r.Project,
				Config:  config,
			})
		})
	})
	if errors.Is(err, ErrRepositoryKeyCaseConflict) || errors.Is(err, ErrInvalidRepository) {
		return err
	}

	if database.IsUniqueViolation(err) {
		return ErrRepositoryExists
	}

	if err != nil {
		return fmt.Errorf("create repository %s: %w", r.Name, err)
	}

	return nil
}

const repositoryKeysLock = "repository:keys"

// SameOrigin reports whether two URLs have the same scheme, host and port,
// so that a stored credential for one may be sent to the other.
func SameOrigin(stored, updated string) bool {
	a, errA := url.Parse(stored)
	b, errB := url.Parse(updated)

	return errA == nil && errB == nil && a.Host != "" &&
		strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func (s *Service) withProject(ctx context.Context, key, current string, save func() error) error {
	if project.IsDefault(key) || key == current {
		return save()
	}

	if !project.ValidKey(key) {
		return fmt.Errorf("%w: project key %q is not valid", ErrInvalidRepository, key)
	}

	return s.db.WithLock(ctx, project.Lock(key), func() error {
		_, err := s.db.GetProject(ctx, key)
		if errors.Is(database.NotFound(err), database.ErrNotFound) {
			return fmt.Errorf("%w: project %s does not exist", ErrInvalidRepository, key)
		}

		if err != nil {
			return fmt.Errorf("look up project %s: %w", key, err)
		}

		return save()
	})
}

func (s *Service) refuseCaseVariant(ctx context.Context, key string) error {
	repos, err := s.ListRepositories(ctx, "", "")
	if err != nil {
		return err
	}

	for _, rp := range repos {
		if rp.Name != key && strings.EqualFold(rp.Name, key) {
			return fmt.Errorf("%w: %s", ErrRepositoryKeyCaseConflict, rp.Name)
		}
	}

	return nil
}

// UpdateRepository replaces a repository's settings.
func (s *Service) UpdateRepository(ctx context.Context, r Repository) error {
	if err := r.Validate(); err != nil {
		return err
	}

	if err := s.checkStorageName(r); err != nil {
		return err
	}

	existing, err := s.GetRepository(ctx, r.Name)
	if err != nil {
		return err
	}

	if existing.Class != r.Class {
		return fmt.Errorf("%w: cannot change the class from %s to %s", ErrInvalidRepository, existing.Class, r.Class)
	}

	if existing.Format != r.Format {
		return fmt.Errorf("%w: cannot change the format from %s to %s", ErrInvalidRepository, existing.Format, r.Format)
	}

	if err := s.checkMembers(ctx, r); err != nil {
		return err
	}

	if err := s.checkKeyPairs(ctx, r); err != nil {
		return err
	}

	if r.Config.Remote != nil && r.Config.Remote.Password == "" && existing.Config.Remote != nil {
		stored, err := s.storedPassword(ctx, r.Name)
		if err != nil {
			return err
		}

		if stored != "" && !SameOrigin(existing.Config.Remote.URL, r.Config.Remote.URL) {
			return fmt.Errorf("%w: the remote URL's host changed: give the password again", ErrInvalidRepository)
		}

		r.Config.Remote.Password = stored
	}

	config, err := s.encodeConfig(r)
	if err != nil {
		return err
	}

	err = s.withProject(ctx, r.Project, existing.Project, func() error {
		return s.db.UpdateRepository(
			ctx,
			database.UpdateRepositoryParams{
				Key:     r.Name,
				Format:  string(r.Format),
				Project: r.Project,
				Config:  config,
			},
		)
	})
	if errors.Is(err, ErrInvalidRepository) {
		return err
	}

	if err != nil {
		return fmt.Errorf("update repository %s: %w", r.Name, err)
	}

	return nil
}

// GetRepository returns one repository.
func (s *Service) GetRepository(ctx context.Context, key string) (Repository, error) {
	row, err := s.db.GetRepository(ctx, key)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Repository{}, ErrRepositoryNotFound
	}

	if err != nil {
		return Repository{}, fmt.Errorf("get repository %s: %w", key, err)
	}

	return s.repositoryFromRow(row), nil
}

// Quarantines reports whether repository key is a remote repository that
// quarantines what it caches.
func (s *Service) Quarantines(ctx context.Context, key string) (bool, error) {
	r, err := s.GetRepository(ctx, key)
	if errors.Is(err, ErrRepositoryNotFound) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	return r.Class == ClassRemote && r.Upstream().Quarantine, nil
}

// QuarantinedRepositories names the remote repositories that quarantine
// what they cache.
func (s *Service) QuarantinedRepositories(ctx context.Context) ([]string, error) {
	remotes, err := s.ListRepositories(ctx, ClassRemote, "")
	if err != nil {
		return nil, err
	}

	var out []string

	for _, r := range remotes {
		if r.Upstream().Quarantine {
			out = append(out, r.Name)
		}
	}

	return out, nil
}

// ListRepositories returns repositories, optionally filtered by class and
// format (empty values match all).
func (s *Service) ListRepositories(ctx context.Context, class Class, format Format) ([]Repository, error) {
	var params database.ListRepositoriesParams
	if class != "" {
		params.Class = (*string)(&class)
	}

	if format != "" {
		params.Format = (*string)(&format)
	}

	rows, err := s.db.ListRepositories(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}

	repos := make([]Repository, 0, len(rows))
	for _, r := range rows {
		if r.Key == s.trashLayout.Repository {
			continue
		}

		repos = append(repos, s.repositoryFromRow(r))
	}

	return repos, nil
}

// DeleteRepository removes a repository and all its items.
func (s *Service) DeleteRepository(ctx context.Context, key string) error {
	return s.db.WithTx(ctx, func(q database.Querier) error {
		if err := checkHolds(ctx, q, key, ""); err != nil {
			return err
		}

		shas, err := q.DeleteRepositoryItems(ctx, key)
		if err != nil {
			return fmt.Errorf("delete items of %s: %w", key, err)
		}

		if err := releaseAll(ctx, q, shas); err != nil {
			return err
		}

		n, err := q.DeleteRepository(ctx, key)
		if err != nil {
			return fmt.Errorf("delete repository %s: %w", key, err)
		}

		if n == 0 {
			return ErrRepositoryNotFound
		}

		if err := removeMember(ctx, q, key); err != nil {
			return err
		}

		for _, hook := range s.removalHooks {
			if err := hook(ctx, q, key); err != nil {
				return err
			}
		}

		return nil
	})
}

func (s *Service) checkMembers(ctx context.Context, r Repository) error {
	if r.Class != ClassVirtual {
		return nil
	}

	for _, key := range r.Members() {
		member, err := s.GetRepository(ctx, key)
		if errors.Is(err, ErrRepositoryNotFound) {
			return fmt.Errorf("%w: member %s of %s does not exist", ErrInvalidRepository, key, r.Name)
		}

		if err != nil {
			return err
		}

		if !aggregates(r.Format, member.Format) {
			return fmt.Errorf(
				"%w: member %s of %s is a %s repository, not %s",
				ErrInvalidRepository,
				key,
				r.Name,
				member.Format,
				r.Format,
			)
		}

		if key == r.Config.Virtual.DeployTo && member.Class != ClassLocal {
			return fmt.Errorf("%w: deployTo %s is not a local repository", ErrInvalidRepository, key)
		}
	}

	return nil
}

var mavenFamily = []Format{
	FormatMaven,
	FormatGradle,
	FormatIvy,
	FormatSbt,
}

func aggregates(virtual, member Format) bool {
	return virtual == member || slices.Contains(mavenFamily, virtual) && slices.Contains(mavenFamily, member)
}

func removeMember(ctx context.Context, q database.Querier, key string) error {
	class := string(ClassVirtual)

	rows, err := q.ListRepositories(ctx, database.ListRepositoriesParams{
		Class: &class,
	})
	if err != nil {
		return fmt.Errorf("list virtual repositories: %w", err)
	}

	for _, row := range rows {
		var c Config
		if err := json.Unmarshal(row.Config, &c); err != nil {
			return fmt.Errorf("decode repository %s: %w", row.Key, err)
		}

		if c.Virtual == nil || !slices.Contains(c.Virtual.Members, key) {
			continue
		}

		c.Virtual.Members = slices.DeleteFunc(c.Virtual.Members, func(m string) bool {
			return m == key
		})
		if c.Virtual.DeployTo == key {
			c.Virtual.DeployTo = ""
		}

		config, err := json.Marshal(c)
		if err != nil {
			return fmt.Errorf("encode repository %s: %w", row.Key, err)
		}

		if err := q.UpdateRepository(ctx, database.UpdateRepositoryParams{
			Key:     row.Key,
			Format:  row.Format,
			Project: row.Project,
			Config:  config,
		}); err != nil {
			return fmt.Errorf("update repository %s: %w", row.Key, err)
		}
	}

	return nil
}

// RemovalHook drops what refers to a repository, in the transaction that
// deletes it.
type RemovalHook func(ctx context.Context, q database.Querier, key string) error

// RegisterRemovalHook runs hook whenever a repository is deleted.
func (s *Service) RegisterRemovalHook(hook RemovalHook) {
	s.removalHooks = append(s.removalHooks, hook)
}

func (s *Service) repositoryFromRow(row database.Repository) Repository {
	r := Repository{
		Name:    row.Key,
		Class:   Class(row.Class),
		Format:  Format(row.Format),
		Project: row.Project,
		Created: row.Created,
		Updated: row.Updated,
	}
	if err := json.Unmarshal(row.Config, &r.Config); err != nil {
		s.log.Error("repository settings do not decode", "repo", row.Key, "error", err)
	}

	return s.openPassword(r)
}

// KeyPairRef names the key pair that signs the repository's metadata: its
// signing key, else Relic's default for Alpine (RSA), and Debian and RPM
// (OpenPGP) repositories, else "".
func (r Repository) KeyPairRef() string {
	if r.Config.SigningKey != "" {
		return r.Config.SigningKey
	}

	switch r.Format {
	case FormatAlpine:
		return signingkey.DefaultRSA
	case FormatDebian, FormatRPM:
		return signingkey.DefaultGPG
	default:
		return ""
	}
}

// KeyPairRefs names the key pairs that sign the repository's metadata: its
// key pair, then its secondary signing key, if any.
func (r Repository) KeyPairRefs() []string {
	refs := []string{
		r.KeyPairRef(),
	}
	if r.Config.SecondarySigningKey != "" {
		refs = append(refs, r.Config.SecondarySigningKey)
	}

	return refs
}

// KeyPairKind is the kind of key pair that signs format's metadata: rsa for
// Alpine, gpg for Debian and RPM, else "".
func KeyPairKind(format Format) string {
	switch format {
	case FormatAlpine:
		return "rsa"
	case FormatDebian, FormatRPM:
		return "gpg"
	default:
		return ""
	}
}

func (s *Service) checkKeyPairs(ctx context.Context, r Repository) error {
	kind := KeyPairKind(r.Format)
	if kind == "" {
		return nil
	}

	repos, err := s.ListRepositories(ctx, "", "")
	if err != nil {
		return err
	}

	for _, name := range r.KeyPairRefs() {
		stored, err := s.db.GetKeyPair(ctx, name)
		if err == nil && stored.Kind != kind {
			return fmt.Errorf("%w: key pair %s is %s; a %s repository is signed with %s",
				ErrInvalidRepository, name, stored.Kind, r.Format, kind)
		}

		if err != nil && !errors.Is(database.NotFound(err), database.ErrNotFound) {
			return fmt.Errorf("key pair %s: %w", name, err)
		}

		for _, other := range repos {
			otherKind := KeyPairKind(other.Format)
			if other.Name != r.Name && otherKind != "" && otherKind != kind &&
				slices.Contains(other.KeyPairRefs(), name) {
				return fmt.Errorf(
					"%w: key pair %s signs %s, a %s repository, with %s; a %s repository is signed with %s",
					ErrInvalidRepository,
					name,
					other.Name,
					other.Format,
					otherKind,
					r.Format,
					kind,
				)
			}
		}
	}

	return nil
}

// OwnRepositories names the repositories Relic writes itself: build
// information, releases and evidence.
func (s *Service) OwnRepositories(ctx context.Context) ([]string, error) {
	var names []string

	for _, f := range OwnFormats {
		repos, err := s.ListRepositories(ctx, "", f)
		if err != nil {
			return nil, err
		}

		for _, r := range repos {
			names = append(names, r.Name)
		}
	}

	return names, nil
}
