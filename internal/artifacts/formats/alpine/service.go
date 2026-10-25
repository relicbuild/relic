package alpine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"relic/internal/artifacts/repository"
	"relic/internal/artifacts/signingkey"
)

// Format is the repository format this package serves.
const Format = repository.FormatAlpine

// IndexFile is the index apk reads from each <vX.Y>/<branch>/<arch> folder.
const IndexFile = "APKINDEX.tar.gz"

// Properties set on each indexed package.
const (
	PropIndex   = "apk.index"
	PropName    = "apk.name"
	PropVersion = "apk.version"
	PropArch    = "apk.arch"
)

// Service indexes and serves Alpine repositories.
type Service struct {
	repos *repository.Service
	keys  *signingkey.Service
	log   *slog.Logger
}

// NewService returns a Service; it indexes .apk files deployed to Alpine
// repositories, and remotes refresh their indexes like other metadata.
func NewService(repos *repository.Service, keySvc *signingkey.Service, log *slog.Logger) *Service {
	s := &Service{
		repos: repos,
		keys:  keySvc,
		log:   log,
	}
	repos.RegisterHook(Format, s.indexPackage)
	repos.RegisterMetadata(func(name string) bool {
		return name == IndexFile
	})

	return s
}

func (s *Service) indexPackage(ctx context.Context, r repository.Repository, item repository.Item) error {
	if r.Class == "remote" || !strings.HasSuffix(item.Name, ".apk") {
		return nil
	}

	rc, err := s.repos.Open(ctx, item, nil)
	if err != nil {
		return err
	}

	defer func() {
		_ = rc.Close()
	}()

	pkg, err := Parse(rc)
	if err != nil {
		s.log.WarnContext(ctx, "not indexing an invalid apk", "repo", r.Name, "path", item.Path(), "error", err)
		return nil
	}

	return s.repos.SetProperties(ctx, item, repository.Properties{
		PropIndex: {
			pkg.Stanza(),
		},
		PropName: {
			pkg.Name,
		},
		PropVersion: {
			pkg.Version,
		},
		PropArch: {
			pkg.Arch,
		},
	}, false)
}

// Index builds the signed APKINDEX.tar.gz of folder dir (<vX.Y>/<branch>/
// <arch>) in r.
func (s *Service) Index(ctx context.Context, r repository.Repository, dir string) ([]byte, error) {
	var (
		stanzas  []string
		modified time.Time
	)

	seen := map[string]bool{}
	found := false

	var memberErr error

	for _, m := range s.members(ctx, r, 0) {
		list, changed, ok, err := s.memberStanzas(ctx, m, dir)
		if err != nil {
			s.log.WarnContext(ctx, "skipping member index", "repo", r.Name, "member", m.Name, "error", err)

			memberErr = cmp.Or(memberErr, err)

			continue
		}

		found = found || ok

		if changed.After(modified) {
			modified = changed
		}

		for _, st := range list {
			id := field(st, "P") + "\x00" + field(st, "V")
			if !seen[id] {
				seen[id] = true

				stanzas = append(stanzas, st)
			}
		}
	}

	if !found && memberErr != nil {
		return nil, memberErr
	}

	if !found {
		return nil, repository.ErrNotFound
	}

	refs := r.KeyPairRefs()
	signers := make([]Signer, 0, len(refs))

	for _, ref := range refs {
		key, _, err := s.keys.RSA(ctx, ref)
		if err != nil {
			return nil, err
		}

		signers = append(signers, Signer{
			Key:  key,
			Name: ref,
		})
	}

	return Index(stanzas, fmt.Sprintf("%s %s", r.Name, dir), signers, modified)
}

func (s *Service) memberStanzas(
	ctx context.Context,
	m repository.Repository,
	dir string,
) ([]string, time.Time, bool, error) {
	if m.Class == "remote" {
		item, err := s.repos.Resolve(ctx, m.Name, path.Join(dir, IndexFile))
		if errors.Is(err, repository.ErrNotFound) {
			return nil, time.Time{}, false, nil
		}

		if err != nil {
			return nil, time.Time{}, false, err
		}

		rc, err := s.repos.Open(ctx, item, nil)
		if err != nil {
			return nil, time.Time{}, false, err
		}

		defer func() {
			_ = rc.Close()
		}()

		list, err := Stanzas(rc)
		if err != nil || !repository.Restricted(ctx, m.Name) {
			return list, item.Modified, err == nil, err
		}

		visible := list[:0]
		for _, st := range list {
			if repository.Visible(ctx, m.Name, path.Join(dir, field(st, "P")+"-"+field(st, "V")+".apk")) {
				visible = append(visible, st)
			}
		}

		return visible, item.Modified, true, nil
	}

	folder, err := s.repos.Stat(ctx, m.Name, dir)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, time.Time{}, false, nil
	}

	if err != nil {
		return nil, time.Time{}, false, err
	}

	kids, err := s.repos.Children(ctx, folder)
	if err != nil {
		return nil, time.Time{}, false, err
	}

	var out []string

	modified := folder.Modified

	for _, k := range kids {
		if k.IsFolder() || !strings.HasSuffix(k.Name, ".apk") {
			continue
		}

		props, err := s.repos.Properties(ctx, k)
		if err != nil {
			return nil, time.Time{}, false, err
		}

		if st := props[PropIndex]; len(st) > 0 {
			out = append(out, rewriteArch(st[0], path.Base(dir)))

			if k.Modified.After(modified) {
				modified = k.Modified
			}
		}
	}

	return out, modified, true, nil
}

func (s *Service) members(ctx context.Context, r repository.Repository, depth int) []repository.Repository {
	if r.Class != "virtual" {
		return []repository.Repository{
			r,
		}
	}

	if depth >= repository.MaxVirtualDepth {
		return nil
	}

	var out []repository.Repository

	for _, key := range s.repos.Members(r) {
		if m, err := s.repos.GetRepository(ctx, key); err == nil {
			out = append(out, s.members(ctx, m, depth+1)...)
		}
	}

	return out
}

func rewriteArch(stanza, arch string) string {
	return strings.Replace(stanza, "\nA:noarch\n", "\nA:"+arch+"\n", 1)
}

func field(stanza, key string) string {
	for line := range strings.SplitSeq(stanza, "\n") {
		if v, ok := strings.CutPrefix(line, key+":"); ok {
			return v
		}
	}

	return ""
}
