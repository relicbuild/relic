//go:build integration

package release_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"relic/internal/artifacts/repository"
	"relic/internal/artifacts/signingkey"
	"relic/internal/delivery/evidence"
	"relic/internal/delivery/release"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func newReleases(t *testing.T) (*release.Service, *repository.Service, *signingkey.Service) {
	t.Helper()

	db := databasetest.NewDB(t)
	log := slog.New(slog.DiscardHandler)
	repos := repository.NewService(db, objectstoretest.NewMemory(), log)
	keys := signingkey.New(db, nil)

	if err := repos.CreateRepository(
		t.Context(),
		repository.Repository{
			Name:   "libs",
			Class:  repository.ClassLocal,
			Format: "generic",
		},
	); err != nil {
		t.Fatal(err)
	}

	if _, err := repos.Deploy(
		t.Context(),
		repository.DeployInput{
			Repository: "libs",
			Path:       "app.bin",
			Body:       strings.NewReader("application"),
		},
	); err != nil {
		t.Fatal(err)
	}

	return release.New(db, repos, nil, keys, log), repos, keys
}

func readItem(t *testing.T, repos *repository.Service, item repository.Item) []byte {
	t.Helper()

	body, err := repos.Open(t.Context(), item, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = body.Close()
	}()

	b, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// rewrite returns archive with the entry name changed by edit.
func rewrite(t *testing.T, archive []byte, name string, edit func([]byte) []byte) []byte {
	t.Helper()

	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer

	zw := zip.NewWriter(&out)

	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}

		b, _ := io.ReadAll(rc)
		_ = rc.Close()

		if f.Name == name {
			b = edit(b)
		}

		w, err := zw.Create(f.Name)
		if err != nil {
			t.Fatal(err)
		}

		_, _ = w.Write(b)
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return out.Bytes()
}

func TestReleaseManifestIsSignedAndImportVerifiesIt(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	releases, repos, keys := newReleases(t)

	b, err := releases.Create(
		ctx,
		"",
		release.Spec{
			Name:    "app",
			Version: "1.0",
			Sources: []release.Source{
				{
					Kind: release.SourceArtifacts,
					Artifacts: []release.ArtifactRef{
						{
							Path: "libs/app.bin",
						},
					},
				},
			},
		},
		"admin",
		release.Access{},
	)
	if err != nil {
		t.Fatal(err)
	}

	manifest, err := repos.Stat(ctx, b.Repository, b.Folder()+"/"+release.DefaultLayout.ManifestName)
	if err != nil {
		t.Fatal(err)
	}

	var env evidence.Envelope
	if err := json.Unmarshal(readItem(t, repos, manifest), &env); err != nil {
		t.Fatal(err)
	}

	pemText, err := keys.PublicKey(ctx, release.SigningKey)
	if err != nil {
		t.Fatal(err)
	}

	pub, _, err := evidence.ParsePublicKey(pemText)
	if err != nil {
		t.Fatal(err)
	}

	if !evidence.Verify(pub, env) {
		t.Fatalf("the manifest's %d signatures do not verify with the %s key", len(env.Signatures), release.SigningKey)
	}

	exported, err := releases.Export(ctx, "", "app", "1.0", "admin")
	if err != nil {
		t.Fatal(err)
	}

	archive := readItem(t, repos, exported)

	if err := releases.Delete(ctx, "", "app", "1.0"); err != nil {
		t.Fatal(err)
	}

	tampered := map[string][]byte{
		"a manifest whose payload changed": rewrite(
			t,
			archive,
			release.DefaultLayout.ManifestName,
			func(b []byte) []byte {
				var e evidence.Envelope

				_ = json.Unmarshal(b, &e)
				e.Payload = strings.Replace(e.Payload, "A", "B", 1)
				out, _ := json.Marshal(e)

				return out
			},
		),
		"an unsigned manifest": rewrite(t, archive, release.DefaultLayout.ManifestName, func(b []byte) []byte {
			var e evidence.Envelope

			_ = json.Unmarshal(b, &e)
			e.Signatures = nil
			out, _ := json.Marshal(e)

			return out
		}),
	}

	for name, a := range tampered {
		if _, err := releases.Import(
			ctx,
			bytes.NewReader(a),
			int64(len(a)),
			"admin",
		); !errors.Is(
			err,
			release.ErrInvalid,
		) {
			t.Errorf("import of %s = %v, want ErrInvalid", name, err)
		}
	}

	if _, err := releases.Import(ctx, bytes.NewReader(archive), int64(len(archive)), "admin"); err != nil {
		t.Fatalf("import of the exported archive: %v", err)
	}
}
