//go:build integration

package debian

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"

	"relic/internal/artifacts/repository"
	"relic/internal/artifacts/signingkey"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

type debianFixture struct {
	repos   *repository.Service
	keys    *signingkey.Service
	svc     *Service
	local   repository.Repository
	virtual repository.Repository
	clock   atomic.Int64
}

func newDebianFixture(t testing.TB) *debianFixture {
	t.Helper()

	ctx := t.Context()
	log := slog.New(slog.DiscardHandler)
	db := databasetest.NewDB(t)
	repos := repository.NewService(db, objectstoretest.NewMemory(), log)

	for _, r := range []repository.Repository{
		{
			Name:   "deb-local",
			Class:  repository.ClassLocal,
			Format: repository.FormatDebian,
		},
		{
			Name:   "deb",
			Class:  repository.ClassVirtual,
			Format: repository.FormatDebian,
			Config: repository.Config{
				Virtual: &repository.VirtualConfig{
					Members: []string{
						"deb-local",
					},
				},
			},
		},
	} {
		if err := repos.CreateRepository(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	f := &debianFixture{
		repos: repos,
		keys:  signingkey.New(db, nil),
	}
	f.svc = NewService(db, repos, f.keys, log)

	var err error
	if f.local, err = repos.GetRepository(ctx, "deb-local"); err != nil {
		t.Fatal(err)
	}

	if f.virtual, err = repos.GetRepository(ctx, "deb"); err != nil {
		t.Fatal(err)
	}

	f.clock.Store(time.Now().Unix())
	f.svc.now = func() time.Time {
		return time.Unix(f.clock.Load(), 0)
	}

	return f
}

func (f *debianFixture) advance(d time.Duration) { f.clock.Add(int64(d / time.Second)) }

func (f *debianFixture) deploy(t testing.TB, name string, props repository.Properties) string {
	t.Helper()

	body := "deb " + name
	props[PropControl] = []string{
		"Package: " + name + "\nVersion: 1.0\nArchitecture: amd64\nDescription: " + strings.Repeat("x", 2000) + "\n",
	}
	itemPath := "pool/" + name + "_1.0_amd64.deb"

	if _, err := f.repos.Deploy(t.Context(), repository.DeployInput{
		Repository: f.local.Name,
		Path:       itemPath,
		Body:       strings.NewReader(body),
		Size:       int64(len(body)),
		User:       "test",
		Props:      props,
	}); err != nil {
		t.Fatal(err)
	}

	return itemPath
}

func (f *debianFixture) packages(t *testing.T, r repository.Repository, dist, file string) []string {
	t.Helper()

	return f.packagesFor(t, t.Context(), r, dist, file)
}

func (f *debianFixture) packagesFor(
	t *testing.T,
	ctx context.Context,
	r repository.Repository,
	dist, file string,
) []string {
	t.Helper()

	d, err := f.svc.Distribution(ctx, r, dist)
	if err != nil {
		t.Fatalf("%s %s: %v", r.Name, dist, err)
	}

	paragraphs := Paragraphs(d.Files[file])
	names := make([]string, 0, len(paragraphs))

	for _, p := range paragraphs {
		names = append(names, Field(p, "Package"))
	}

	return names
}

func (f *debianFixture) releaseDate(t *testing.T, dist string) time.Time {
	t.Helper()

	d, err := f.svc.Distribution(t.Context(), f.local, dist)
	if err != nil {
		t.Fatal(err)
	}

	date, err := time.Parse(time.RFC1123Z, Field(string(d.Release), "Date"))
	if err != nil {
		t.Fatalf("Release Date: %v\n%s", err, d.Release)
	}

	return date
}

func TestDistributionMemberErrors(t *testing.T) {
	t.Parallel()

	f := newDebianFixture(t)

	if _, err := f.svc.Distribution(t.Context(), f.local, "bookworm"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("a distribution no member has = %v, want ErrNotFound", err)
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := f.svc.Distribution(cancelled, f.local, "bookworm")
	if err == nil || errors.Is(err, repository.ErrNotFound) {
		t.Errorf("a distribution every member failed to read = %v, want the members' error", err)
	}
}

func TestDistributionServesTheBuiltCopyWhileItRebuilds(t *testing.T) {
	t.Parallel()

	f := newDebianFixture(t)
	bookworm := repository.Properties{
		PropDistribution: {
			"bookworm",
		},
	}
	f.deploy(t, "first", bookworm)

	const file = "main/binary-amd64/Packages"

	if got := f.packages(t, f.virtual, "bookworm", file); !slices.Equal(got, []string{
		"first",
	}) {
		t.Fatalf("the first build lists %v", got)
	}

	f.deploy(t, "second", bookworm)
	f.advance(31 * time.Second)

	if got := f.packages(t, f.virtual, "bookworm", file); !slices.Equal(got, []string{
		"first",
	}) {
		t.Errorf("an expired index = %v, want the built copy served while it rebuilds", got)
	}

	f.svc.indexes.Wait()

	if got := f.packages(t, f.virtual, "bookworm", file); !slices.Equal(got, []string{
		"first",
		"second",
	}) {
		t.Errorf("after the rebuild = %v, want both packages", got)
	}
}

type hidingVisibility struct {
	repo, hidden string
}

func (v hidingVisibility) Visible(repoKey, path string) bool {
	return repoKey != v.repo || !strings.HasPrefix(path, v.hidden)
}

func (v hidingVisibility) Restricted(repoKey string) bool { return repoKey == v.repo }

func TestDistributionServesCallersNoMemberRestrictsFromTheCache(t *testing.T) {
	t.Parallel()

	f := newDebianFixture(t)
	bookworm := repository.Properties{
		PropDistribution: {
			"bookworm",
		},
	}
	f.deploy(t, "first", bookworm)

	const file = "main/binary-amd64/Packages"

	ctx := repository.WithVisibility(t.Context(), hidingVisibility{
		repo: "another-repository",
	})

	if got := f.packagesFor(t, ctx, f.virtual, "bookworm", file); !slices.Equal(got, []string{
		"first",
	}) {
		t.Fatalf("the first build lists %v", got)
	}

	f.deploy(t, "second", bookworm)

	if got := f.packagesFor(t, ctx, f.virtual, "bookworm", file); !slices.Equal(got, []string{
		"first",
	}) {
		t.Errorf("a second request = %v, want the cached build", got)
	}
}

func TestDistributionHidesARestrictedMembersPackages(t *testing.T) {
	t.Parallel()

	f := newDebianFixture(t)
	bookworm := repository.Properties{
		PropDistribution: {
			"bookworm",
		},
	}
	f.deploy(t, "shown", bookworm)
	f.deploy(t, "hidden", bookworm)

	const file = "main/binary-amd64/Packages"

	if got := f.packages(t, f.virtual, "bookworm", file); len(got) != 2 {
		t.Fatalf("an unfiltered build lists %v, want both packages", got)
	}

	ctx := repository.WithVisibility(t.Context(), hidingVisibility{
		repo:   f.local.Name,
		hidden: "pool/hidden",
	})

	for range 2 {
		if got := f.packagesFor(t, ctx, f.virtual, "bookworm", file); !slices.Equal(got, []string{
			"shown",
		}) {
			t.Errorf("a caller who may not see pool/hidden gets %v, want shown only", got)
		}
	}
}

func TestDistributionListsEveryComponentAndArchitecture(t *testing.T) {
	t.Parallel()

	f := newDebianFixture(t)
	f.deploy(t, "hello", repository.Properties{
		PropDistribution: {
			"bookworm",
			"trixie",
		},
		PropComponent: {
			"main",
			"contrib",
		},
		PropArchitecture: {
			"amd64",
			"arm64",
		},
	})

	for _, dist := range []string{
		"bookworm",
		"trixie",
	} {
		d, err := f.svc.Distribution(t.Context(), f.local, dist)
		if err != nil {
			t.Fatal(err)
		}

		if !slices.Equal(d.Components, []string{
			"contrib",
			"main",
		}) || !slices.Equal(d.Architectures, []string{
			"amd64",
			"arm64",
		}) {
			t.Errorf("%s has components %v and architectures %v, want contrib main and amd64 arm64",
				dist, d.Components, d.Architectures)
		}

		for _, file := range []string{
			"main/binary-amd64/Packages",
			"main/binary-arm64/Packages",
			"contrib/binary-amd64/Packages",
			"contrib/binary-arm64/Packages",
		} {
			if got := f.packages(t, f.local, dist, file); !slices.Equal(got, []string{
				"hello",
			}) {
				t.Errorf("%s %s lists %v, want hello", dist, file, got)
			}
		}
	}
}

func TestDistributionReleaseDateMovesForward(t *testing.T) {
	t.Parallel()

	f := newDebianFixture(t)
	bookworm := repository.Properties{
		PropDistribution: {
			"bookworm",
		},
	}
	f.deploy(t, "older", bookworm)
	newest := f.deploy(t, "newest", bookworm)

	before := f.releaseDate(t, "bookworm")

	f.advance(time.Minute)

	if again := f.releaseDate(t, "bookworm"); !again.Equal(before) {
		t.Errorf("the Date of unchanged indexes moved from %s to %s", before, again)
	}

	if err := f.repos.Delete(t.Context(), f.local.Name, newest); err != nil {
		t.Fatal(err)
	}

	f.clock.Store(before.Unix())

	if after := f.releaseDate(t, "bookworm"); !after.After(before) {
		t.Errorf("after deleting the newest package the Date is %s, want later than %s", after, before)
	}
}

func TestSignWithEveryKeyPair(t *testing.T) {
	t.Parallel()

	f := newDebianFixture(t)
	rotating := f.local
	rotating.Config.SigningKey, rotating.Config.SecondarySigningKey = "deb-old", "deb-new"
	release := []byte("Origin: deb-local\nSuite: bookworm\n")

	inRelease, err := f.svc.Sign(t.Context(), rotating, release, false)
	if err != nil {
		t.Fatal(err)
	}

	detached, err := f.svc.Sign(t.Context(), rotating, release, true)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range rotating.KeyPairRefs() {
		key, _, err := f.keys.GPG(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}

		trusted := openpgp.EntityList{
			key,
		}

		block, _ := clearsign.Decode(inRelease)
		if block == nil {
			t.Fatal("InRelease does not decode")
		}

		if _, err := block.VerifySignature(trusted, nil); err != nil {
			t.Errorf("InRelease does not verify with %s alone: %v", name, err)
		}

		if _, err := openpgp.CheckArmoredDetachedSignature(
			trusted,
			bytes.NewReader(release),
			bytes.NewReader(detached),
			nil,
		); err != nil {
			t.Errorf("Release.gpg does not verify with %s alone: %v", name, err)
		}
	}
}

func BenchmarkDistributionBurst(b *testing.B) {
	f := newDebianFixture(b)

	for i := range 300 {
		f.deploy(b, fmt.Sprintf("package%03d", i), repository.Properties{
			PropDistribution: {
				"bookworm",
			},
		})
	}

	const parallel = 8

	for b.Loop() {
		f.advance(time.Hour)

		var wg sync.WaitGroup
		for range parallel {
			wg.Go(func() {
				if _, err := f.svc.Distribution(b.Context(), f.virtual, "bookworm"); err != nil {
					b.Error(err)
				}
			})
		}

		wg.Wait()
	}
}
