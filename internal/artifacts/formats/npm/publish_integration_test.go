//go:build integration

package npm_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"relic/internal/artifacts/formats/npm"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

type hiddenPaths map[string]bool

func (h hiddenPaths) Visible(_, path string) bool { return !h[path] }

func (hiddenPaths) Restricted(string) bool { return true }

func publishDocument(t *testing.T, raw string) npm.PublishDocument {
	t.Helper()

	var doc npm.PublishDocument
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}

	return doc
}

func TestPublishAndPackument(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	repos := repository.NewService(db, objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))
	svc := npm.NewService(db, repos, slog.New(slog.DiscardHandler))

	if err := repos.CreateRepository(ctx, repository.Repository{
		Name:   "npm-local",
		Class:  repository.ClassLocal,
		Format: npm.Format,
	}); err != nil {
		t.Fatal(err)
	}

	tarball := base64.StdEncoding.EncodeToString([]byte("tarball"))

	partial := publishDocument(t, `{"name":"demo","versions":{
	  "1.0.0":{"dist":{"tarball":"http://relic/npm/demo/-/demo-1.0.0.tgz"}},
	  "1.1.0":{"dist":{"tarball":"http://relic/npm/demo/-/demo-1.1.0.tgz"}}},
	  "_attachments":{"demo-1.0.0.tgz":{"data":"`+tarball+`"},"other.tgz":{"data":"`+tarball+`"}}}`)
	if err := svc.Publish(ctx, "npm-local", partial, "admin"); !errors.Is(err, npm.ErrInvalid) {
		t.Fatalf("a publish missing one attachment = %v, want ErrInvalid", err)
	}

	_, err := repos.Stat(ctx, "npm-local", npm.TarballPath("demo", "1.0.0"))
	if !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("a refused publish stored a tarball: %v", err)
	}

	for _, version := range []string{
		"1.0.0",
		"2.0.0",
	} {
		doc := publishDocument(t, `{"name":"demo","versions":{
		  "`+version+`":{"dist":{"tarball":"http://relic/npm/demo/-/demo-`+version+`.tgz"}}},
		  "_attachments":{"demo-`+version+`.tgz":{"data":"`+tarball+`"}}}`)
		if err := svc.Publish(ctx, "npm-local", doc, "admin"); err != nil {
			t.Fatal(err)
		}
	}

	restricted := repository.WithVisibility(ctx, hiddenPaths{
		npm.TarballPath("demo", "1.0.0"): true,
	})

	doc, err := svc.Packument(restricted, "npm-local", "demo", "http://relic/npm")
	if err != nil {
		t.Fatal(err)
	}

	times, _ := doc["time"].(map[string]any)
	if times["created"] != times["2.0.0"] || times["modified"] != times["2.0.0"] {
		t.Errorf("time = %v; want created and modified from the one visible version", times)
	}
}

func TestVirtualPackumentKeepsLocalPublishTimes(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	repos := repository.NewService(db, objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))
	svc := npm.NewService(db, repos, slog.New(slog.DiscardHandler))

	for _, r := range []repository.Repository{
		{
			Name:   "npm-local",
			Class:  repository.ClassLocal,
			Format: npm.Format,
		},
		{
			Name:   "npm",
			Class:  repository.ClassVirtual,
			Format: npm.Format,
			Config: repository.Config{
				Virtual: &repository.VirtualConfig{
					Members: []string{
						"npm-local",
					},
				},
			},
		},
	} {
		if err := repos.CreateRepository(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	tarball := base64.StdEncoding.EncodeToString([]byte("tarball"))
	doc := publishDocument(t, `{"name":"@acme/ylib","versions":{
	  "1.0.0":{"dist":{"tarball":"http://relic/npm/@acme/ylib/-/ylib-1.0.0.tgz"}}},
	  "_attachments":{"@acme/ylib-1.0.0.tgz":{"data":"`+tarball+`"}}}`)

	if err := svc.Publish(ctx, "npm-local", doc, "admin"); err != nil {
		t.Fatal(err)
	}

	local, err := svc.Packument(ctx, "npm-local", "@acme/ylib", "http://relic/npm")
	if err != nil {
		t.Fatal(err)
	}

	virtual, err := svc.Packument(ctx, "npm", "@acme/ylib", "http://relic/npm")
	if err != nil {
		t.Fatal(err)
	}

	localTimes, _ := local["time"].(map[string]any)
	virtualTimes, _ := virtual["time"].(map[string]any)

	for _, key := range []string{
		"1.0.0",
		"created",
		"modified",
	} {
		if localTimes[key] == nil || virtualTimes[key] != localTimes[key] {
			t.Errorf(
				"time[%q] = %v through the virtual, %v in the local repository",
				key,
				virtualTimes[key],
				localTimes[key],
			)
		}
	}
}
