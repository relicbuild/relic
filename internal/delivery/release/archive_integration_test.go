//go:build integration

package release_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"relic/internal/delivery/release"
)

func exportedArchive(t *testing.T) (*release.Service, []byte) {
	t.Helper()

	ctx := t.Context()
	releases, repos, _ := newReleases(t)

	if _, err := releases.Create(
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
	); err != nil {
		t.Fatal(err)
	}

	exported, err := releases.Export(ctx, "", "app", "1.0", "admin")
	if err != nil {
		t.Fatal(err)
	}

	archive := readItem(t, repos, exported)

	if err := releases.Delete(ctx, "", "app", "1.0"); err != nil {
		t.Fatal(err)
	}

	return releases, archive
}

func withExtraEntry(t *testing.T, archive []byte, name string, body []byte) []byte {
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

		w, err := zw.Create(f.Name)
		if err != nil {
			t.Fatal(err)
		}

		_, _ = w.Write(b)
	}

	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = w.Write(body)

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return out.Bytes()
}

func TestImportRefusesAnIndexThatDiffersFromTheSignedManifest(t *testing.T) {
	t.Parallel()

	releases, archive := exportedArchive(t)

	edits := map[string]func(map[string]any){
		"a changed creator": func(index map[string]any) {
			index["created_by"] = "nobody"
		},
		"a changed creation time": func(index map[string]any) {
			index["created"] = "2020-01-01T00:00:00Z"
		},
		"a changed project": func(index map[string]any) {
			index["project"] = "other"
		},
	}

	for name, edit := range edits {
		changed := rewrite(t, archive, "release.json", func(b []byte) []byte {
			var index map[string]any

			_ = json.Unmarshal(b, &index)
			edit(index)
			out, _ := json.Marshal(index)

			return out
		})

		if _, err := releases.Import(
			t.Context(),
			bytes.NewReader(changed),
			int64(len(changed)),
			"admin",
		); !errors.Is(
			err,
			release.ErrInvalid,
		) {
			t.Errorf("import of an archive with %s = %v; want ErrInvalid", name, err)
		}
	}
}

func TestImportRefusesAnArchiveWithRepeatedEntries(t *testing.T) {
	t.Parallel()

	releases, archive := exportedArchive(t)
	repeated := withExtraEntry(t, archive, "artifacts/app.bin", []byte("application"))

	if _, err := releases.Import(
		t.Context(),
		bytes.NewReader(repeated),
		int64(len(repeated)),
		"admin",
	); !errors.Is(
		err,
		release.ErrInvalid,
	) {
		t.Errorf("import of an archive with a repeated entry = %v; want ErrInvalid", err)
	}
}
