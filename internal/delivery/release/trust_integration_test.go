//go:build integration

package release

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestImportTrustsOnlyItsOwnAndTrustedKeys(t *testing.T) {
	t.Parallel()

	exporter := newReleaseFixture(t)
	spec := Spec{
		Name:    "app",
		Version: "1.0",
		Sources: artifactSource("libs/app.bin"),
	}

	if _, err := exporter.releases.Create(t.Context(), "", spec, "admin", Access{}); err != nil {
		t.Fatal(err)
	}

	exported, err := exporter.releases.Export(t.Context(), "", "app", "1.0", "admin")
	if err != nil {
		t.Fatal(err)
	}

	body, err := exporter.repos.Open(t.Context(), exported, nil)
	if err != nil {
		t.Fatal(err)
	}

	archive, err := io.ReadAll(body)
	_ = body.Close()

	if err != nil {
		t.Fatal(err)
	}

	importer := newReleaseFixture(t)
	importArchive := func() error {
		_, err := importer.releases.Import(t.Context(), bytes.NewReader(archive), int64(len(archive)), "admin")
		return err
	}

	err = importArchive()
	if !errors.Is(err, ErrUntrustedKey) || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("Import of another server's archive = %v; want ErrUntrustedKey naming the fingerprint", err)
	}

	importer.trustReleasesOf(t, exporter)

	if err := importArchive(); err != nil {
		t.Fatalf("Import after trusting the exporting server's key = %v", err)
	}
}
