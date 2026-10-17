//go:build integration

package cleanup

import (
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func newCleanup(t *testing.T) (*Service, *repository.Service, *database.DB) {
	t.Helper()

	db := databasetest.NewDB(t)
	log := slog.New(slog.DiscardHandler)
	repos := repository.NewService(db, objectstoretest.NewMemory(), log)

	if err := repos.CreateRepository(
		t.Context(),
		repository.Repository{
			Name:   "libs",
			Class:  repository.ClassLocal,
			Format: "maven",
		},
	); err != nil {
		t.Fatal(err)
	}

	for _, v := range []string{
		"1.0",
		"1.1",
		"1.2",
		"2.0",
	} {
		if _, err := repos.Deploy(t.Context(), repository.DeployInput{
			Repository: "libs",
			Path:       "org/app/" + v + "/app-" + v + ".jar",
			Body:       strings.NewReader("jar " + v),
		}); err != nil {
			t.Fatal(err)
		}

		time.Sleep(5 * time.Millisecond)
	}

	s := New(db, repos, log)
	s.now = func() time.Time {
		return time.Now().AddDate(0, 0, 10)
	}

	return s, repos, db
}

func paths(found []Candidate) []string {
	out := make([]string, 0, len(found))
	for _, c := range found {
		out = append(out, c.Path)
	}

	return out
}

func TestCleanupKeepsTheLatestAndMovesTheRestToTheTrash(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, repos, _ := newCleanup(t)

	p := Policy{
		Name:    "old-versions",
		Enabled: true,
		Repositories: []string{
			"libs",
		},
		PathPattern:   "org/app/*",
		OlderThanDays: 5,
		KeepLatest:    2,
	}
	if err := s.Put(ctx, p); err != nil {
		t.Fatal(err)
	}

	found, err := s.Preview(ctx, p)
	if err != nil || !slices.Equal(paths(found), []string{
		"org/app/1.0",
		"org/app/1.1",
	}) {
		t.Fatalf("Preview = %v, %v; want the two oldest versions", paths(found), err)
	}

	if moved, err := s.Run(ctx, "old-versions"); moved != 2 || err != nil {
		t.Fatalf("Run = %d, %v", moved, err)
	}

	for v, want := range map[string]error{
		"1.0": repository.ErrNotFound,
		"1.1": repository.ErrNotFound,
		"1.2": nil,
		"2.0": nil,
	} {
		if _, err := repos.Stat(ctx, "libs", "org/app/"+v); !errors.Is(err, want) {
			t.Errorf("version %s after the run: %v, want %v", v, err, want)
		}
	}

	entries, _, err := repos.TrashEntries(
		ctx,
		repository.TrashQuery{
			Max: 10,
		},
	)
	if err != nil || len(entries) != 2 || entries[0].DeletedBy != "cleanup:old-versions" {
		t.Errorf("trash = %+v, %v; want two entries deleted by cleanup:old-versions", entries, err)
	}

	stored, err := s.Get(ctx, "old-versions")
	if err != nil || stored.LastMoved != 2 || stored.LastRun == nil {
		t.Errorf("policy after the run = %+v, %v", stored, err)
	}
}

func TestCleanupSparesRecentlyDownloadedFiles(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, repos, db := newCleanup(t)

	recent, err := repos.Stat(ctx, "libs", "org/app/1.2/app-1.2.jar")
	if err != nil {
		t.Fatal(err)
	}

	if err := db.RecordDownloads(ctx, database.RecordDownloadsParams{
		ItemIds: []uuid.UUID{
			recent.ID,
		},
		Counts: []int64{
			1,
		},
		Lasts: []time.Time{
			s.now().AddDate(0, 0, -1),
		},
		Bys: []string{
			"ci",
		},
	}); err != nil {
		t.Fatal(err)
	}

	found, err := s.Preview(ctx, Policy{
		Name: "unused",
		Repositories: []string{
			"libs",
		},
		PathPattern:       "**/*.jar",
		NotDownloadedDays: 5,
	})
	if err != nil || slices.Contains(paths(found), recent.Path()) || len(found) != 3 {
		t.Errorf("Preview = %v, %v; want every jar but the recently downloaded one", paths(found), err)
	}
}

func TestCleanupPolicyValidation(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, _, _ := newCleanup(t)

	for name, p := range map[string]Policy{
		"no criterion": {
			Name: "all",
			Repositories: []string{
				"libs",
			},
		},
		"unknown repository": {
			Name: "nowhere",
			Repositories: []string{
				"missing",
			},
			OlderThanDays: 1,
		},
		"bad name": {
			Name: "a b",
			Repositories: []string{
				"libs",
			},
			OlderThanDays: 1,
		},
	} {
		if err := s.Put(ctx, p); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Put = %v, want ErrInvalid", name, err)
		}
	}
}

func TestCleanupSkipsHeldItems(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, repos, _ := newCleanup(t)

	if err := repos.PutHold(ctx, "libs", "org/app/1.0/app-1.0.jar", "audit", "admin"); err != nil {
		t.Fatal(err)
	}

	found, err := s.Preview(ctx, Policy{
		Name: "old",
		Repositories: []string{
			"libs",
		},
		PathPattern:   "org/app/*",
		OlderThanDays: 5,
	})
	if err != nil || slices.Contains(paths(found), "org/app/1.0") || len(found) != 3 {
		t.Errorf("Preview = %v, %v; want every version but the held one", paths(found), err)
	}
}

func TestCleanupSkipsEverythingUnderARepositoryHold(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, repos, _ := newCleanup(t)

	if err := repos.PutHold(ctx, "libs", "", "legal hold", "admin"); err != nil {
		t.Fatal(err)
	}

	found, err := s.Preview(ctx, Policy{
		Name: "old",
		Repositories: []string{
			"libs",
		},
		PathPattern:   "org/app/*",
		OlderThanDays: 5,
	})
	if err != nil || len(found) != 0 {
		t.Errorf("Preview under a hold on the whole repository = %v, %v; want nothing", paths(found), err)
	}
}
