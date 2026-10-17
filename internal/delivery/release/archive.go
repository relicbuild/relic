package release

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"strings"
	"time"
	"uuid"

	"relic/internal/artifacts/repository"
	"relic/internal/delivery/evidence"
	"relic/internal/platform/telemetry"
)

const (
	archiveIndex    = "release.json"
	archiveKey      = "signing-key.pem"
	maxArchiveIndex = 64 << 20
)

type archiveIndexDoc struct {
	Project   string     `json:"project"`
	Name      string     `json:"name"`
	Version   string     `json:"version"`
	Tag       string     `json:"tag,omitempty"`
	CreatedBy string     `json:"created_by"`
	Created   time.Time  `json:"created"`
	Artifacts []Artifact `json:"artifacts"`
}

func exportFolder(b Release) string { return ".exports/" + b.Folder() }

func exportPath(b Release) string {
	return exportFolder(b) + "/" + b.Name + "-" + b.Version + ".zip"
}

// ExportArchive returns the version's export archive, or repo.ErrNotFound
// when it was never exported.
func (s *Service) ExportArchive(ctx context.Context, project, name, version string) (repository.Item, error) {
	b, err := s.Get(ctx, project, name, version)
	if err != nil {
		return repository.Item{}, err
	}

	return s.repos.Stat(ctx, b.Repository, exportPath(b))
}

// Export writes the version's archive into its storing repository.
func (s *Service) Export(ctx context.Context, project, name, version, user string) (_ repository.Item, err error) {
	ctx, done := telemetry.Operation(ctx, "release.Export", func(err error) {
		telemetry.ReleaseOp("export", err)
	})
	defer func() {
		done(err)
	}()

	b, err := s.Get(ctx, project, name, version)
	if err != nil {
		return repository.Item{}, err
	}

	if b.Draft {
		return repository.Item{}, fmt.Errorf("%w: a draft cannot be exported", ErrInvalid)
	}

	tmp, err := os.CreateTemp("", "relic-rbe-*.zip")
	if err != nil {
		return repository.Item{}, fmt.Errorf("export: %w", err)
	}

	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	if err := s.writeArchive(ctx, tmp, b); err != nil {
		return repository.Item{}, err
	}

	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return repository.Item{}, fmt.Errorf("export: %w", err)
	}

	return s.repos.Deploy(
		ctx,
		repository.DeployInput{
			Repository: b.Repository,
			Path:       exportPath(b),
			Body:       tmp,
			User:       user,
		},
	)
}

func (s *Service) writeArchive(ctx context.Context, w io.Writer, b Release) error {
	zw := zip.NewWriter(w)

	index, err := json.Marshal(archiveIndexDoc{
		Project:   b.Project,
		Name:      b.Name,
		Version:   b.Version,
		Tag:       b.Tag,
		CreatedBy: b.CreatedBy,
		Created:   b.Created,
		Artifacts: nonNil(b.Artifacts),
	})
	if err != nil {
		return fmt.Errorf("encode archive index: %w", err)
	}

	if err := writeZipEntry(zw, archiveIndex, bytes.NewReader(index)); err != nil {
		return err
	}

	_, publicKey, err := s.keys.RSA(ctx, SigningKey)
	if err != nil {
		return fmt.Errorf("signing key: %w", err)
	}

	if err := writeZipEntry(zw, archiveKey, strings.NewReader(publicKey)); err != nil {
		return err
	}

	files := []struct {
		entry, path string
	}{
		{
			s.layout.ManifestName,
			b.Folder() + "/" + s.layout.ManifestName,
		},
	}
	for _, a := range b.Artifacts {
		files = append(files, struct {
			entry, path string
		}{
			"artifacts/" + a.Path,
			b.artifactPath(a),
		})
	}

	for _, f := range files {
		it, err := s.repos.Stat(ctx, b.Repository, f.path)
		if err != nil {
			return fmt.Errorf("export %s: %w", f.path, err)
		}

		rc, err := s.repos.Open(ctx, it, nil)
		if err != nil {
			return fmt.Errorf("export %s: %w", f.path, err)
		}

		err = writeZipEntry(zw, f.entry, rc)
		_ = rc.Close()

		if err != nil {
			return err
		}
	}

	if err := zw.Close(); err != nil {
		return fmt.Errorf("export: %w", err)
	}

	return nil
}

func writeZipEntry(zw *zip.Writer, name string, r io.Reader) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("export %s: %w", name, err)
	}

	if _, err := io.Copy(w, r); err != nil {
		return fmt.Errorf("export %s: %w", name, err)
	}

	return nil
}

// Import stores a release version from an archive written by Export.
func (s *Service) Import(ctx context.Context, archive io.ReaderAt, size int64, user string) (_ Release, err error) {
	ctx, done := telemetry.Operation(ctx, "release.Import", func(err error) {
		telemetry.ReleaseOp("import", err)
	})
	defer func() {
		done(err)
	}()

	zr, err := zip.NewReader(archive, size)
	if err != nil {
		return Release{}, fmt.Errorf("%w: not a release archive: %w", ErrInvalid, err)
	}

	entries := map[string]*zip.File{}
	for _, f := range zr.File {
		entries[f.Name] = f
	}

	var idx archiveIndexDoc
	if err := readZipJSON(entries[archiveIndex], &idx); err != nil {
		return Release{}, err
	}

	if !namePattern.MatchString(idx.Name) || !namePattern.MatchString(idx.Version) {
		return Release{}, fmt.Errorf("%w: invalid name or version in archive", ErrInvalid)
	}

	for _, a := range idx.Artifacts {
		clean, err := repository.CleanPath(a.Path)
		if err != nil || clean != a.Path || entries["artifacts/"+a.Path] == nil || len(a.SHA256) != 64 {
			return Release{}, fmt.Errorf("%w: archive entry for %q is missing or invalid", ErrInvalid, a.Path)
		}
	}

	if err := s.verifyManifest(ctx, entries, idx); err != nil {
		return Release{}, err
	}

	b := Release{
		ID:         uuid.NewV7(),
		Project:    idx.Project,
		Repository: s.layout.Repository(idx.Project),
		Name:       idx.Name,
		Version:    idx.Version,
		Status:     StatusProcessing,
		Tag:        idx.Tag,
		CreatedBy:  idx.CreatedBy,
		Created:    idx.Created,
	}
	if err := s.ensureRepo(ctx, b.Repository, b.Project); err != nil {
		return Release{}, err
	}

	if err := s.insert(ctx, b); err != nil {
		return Release{}, err
	}

	if err := s.importFiles(ctx, &b, idx.Artifacts, entries, user); err != nil {
		s.discard(ctx, b)
		return Release{}, err
	}

	return b, nil
}

func (s *Service) importFiles(
	ctx context.Context,
	b *Release,
	arts []Artifact,
	entries map[string]*zip.File,
	user string,
) error {
	deploy := func(f *zip.File, path string, expected repository.Checksums, limit int64) error {
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("%w: archive entry %s: %w", ErrInvalid, f.Name, err)
		}
		defer func() {
			_ = rc.Close()
		}()

		_, err = s.repos.Deploy(
			ctx,
			repository.DeployInput{
				Repository: b.Repository,
				Path:       path,
				Body:       io.LimitReader(rc, limit),
				Expected:   expected,
				User:       user,
			},
		)

		return err
	}
	for _, a := range arts {
		if err := deploy(
			entries["artifacts/"+a.Path],
			b.artifactPath(a),
			repository.Checksums{
				SHA256: a.SHA256,
			},
			a.Size+1,
		); err != nil {
			return fmt.Errorf("import %s: %w", a.Path, err)
		}
	}

	if err := deploy(
		entries[s.layout.ManifestName],
		b.Folder()+"/"+s.layout.ManifestName,
		repository.Checksums{},
		maxArchiveIndex,
	); err != nil {
		return fmt.Errorf("import manifest: %w", err)
	}

	b.Artifacts = arts

	return s.recordContent(ctx, b)
}

func readZipJSON(f *zip.File, v any) error {
	if f == nil {
		return fmt.Errorf("%w: archive has no %s", ErrInvalid, archiveIndex)
	}

	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalid, f.Name, err)
	}

	defer func() {
		_ = rc.Close()
	}()

	dec := json.NewDecoder(io.LimitReader(rc, maxArchiveIndex))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalid, f.Name, err)
	}

	return nil
}

// verifyManifest checks that the archive's manifest is signed by the key the
// archive carries and names exactly the index's release and artifacts.
func (s *Service) verifyManifest(ctx context.Context, entries map[string]*zip.File, idx archiveIndexDoc) error {
	if entries[s.layout.ManifestName] == nil {
		return fmt.Errorf("%w: archive has no %s", ErrInvalid, s.layout.ManifestName)
	}

	var env evidence.Envelope
	if err := readZipJSON(entries[s.layout.ManifestName], &env); err != nil {
		return err
	}

	pemText, err := readZipFile(entries[archiveKey])
	if err != nil {
		return err
	}

	key, _, err := evidence.ParsePublicKey(string(pemText))
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalid, archiveKey, err)
	}

	if !evidence.Verify(key, env) {
		return fmt.Errorf("%w: the manifest's signature does not verify with the archive's key", ErrInvalid)
	}

	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return fmt.Errorf("%w: manifest payload: %w", ErrInvalid, err)
	}

	var statement struct {
		Subject []struct {
			Name   string            `json:"name"`
			Digest map[string]string `json:"digest"`
		} `json:"subject"`
		Predicate struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"predicate"`
	}
	if err := json.Unmarshal(payload, &statement); err != nil {
		return fmt.Errorf("%w: manifest payload: %w", ErrInvalid, err)
	}

	signed := map[string]string{}
	for _, sub := range statement.Subject {
		signed[sub.Name] = sub.Digest["sha256"]
	}

	listed := map[string]string{}
	for _, a := range idx.Artifacts {
		listed[a.Path] = a.SHA256
	}

	if statement.Predicate.Name != idx.Name || statement.Predicate.Version != idx.Version ||
		!maps.Equal(signed, listed) {
		return fmt.Errorf("%w: the archive's index does not match its signed manifest", ErrInvalid)
	}

	fingerprint := sha256.Sum256(pemText)
	s.log.InfoContext(ctx, "release archive signature verified", "name", idx.Name, "version", idx.Version,
		"key_sha256", hex.EncodeToString(fingerprint[:]))

	return nil
}

func readZipFile(f *zip.File) ([]byte, error) {
	if f == nil {
		return nil, fmt.Errorf("%w: archive has no %s", ErrInvalid, archiveKey)
	}

	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, f.Name, err)
	}

	defer func() {
		_ = rc.Close()
	}()

	b, err := io.ReadAll(io.LimitReader(rc, maxArchiveIndex))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, f.Name, err)
	}

	return b, nil
}
