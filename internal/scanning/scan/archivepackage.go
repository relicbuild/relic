package scan

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	packageurl "github.com/package-url/packageurl-go"
)

const maxManifestBytes = 1 << 20

// syftPackage is one entry of a Syft JSON document's artifacts.
type syftPackage struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Version   string            `json:"version"`
	Type      string            `json:"type"`
	FoundBy   string            `json:"foundBy"`
	Locations []Location        `json:"locations"`
	Licenses  []json.RawMessage `json:"licenses"`
	Language  string            `json:"language"`
	CPEs      []json.RawMessage `json:"cpes"`
	PURL      string            `json:"purl"`
}

// withArchivePackage adds to a Syft JSON document the package a Rust crate
// or NuGet package extracted to a dir: source declares, and returns any
// other document unchanged.
func withArchivePackage(sbom []byte, fileName, source string) []byte {
	dir, extracted := strings.CutPrefix(source, "dir:")
	if !extracted {
		return sbom
	}

	pkg, ok := archivePackage(fileName, dir)
	if !ok {
		return sbom
	}

	if format, err := DetectSBOM(sbom); err != nil || format != SyftJSON {
		return sbom
	}

	added, err := withSyftPackage(sbom, pkg)
	if err != nil {
		return sbom
	}

	return added
}

// archivePackage returns the package a Rust crate or NuGet package
// extracted to dir declares in its manifest.
func archivePackage(fileName, dir string) (syftPackage, bool) {
	switch lower := strings.ToLower(fileName); {
	case strings.HasSuffix(lower, ".crate"):
		return cratePackage(dir)
	case strings.HasSuffix(lower, ".nupkg"):
		return nugetPackage(dir)
	}

	return syftPackage{}, false
}

func cratePackage(dir string) (syftPackage, bool) {
	manifests, err := filepath.Glob(filepath.Join(dir, "*", "Cargo.toml"))
	if err != nil || len(manifests) != 1 {
		return syftPackage{}, false
	}

	raw, err := readManifest(manifests[0])
	if err != nil {
		return syftPackage{}, false
	}

	name, version := cargoPackage(raw)
	if name == "" || version == "" {
		return syftPackage{}, false
	}

	return newSyftPackage(
		packageurl.NewPackageURL(packageurl.TypeCargo, "", name, version, nil, ""),
		"rust-crate",
		"rust",
		manifestLocation(dir, manifests[0]),
	), true
}

// cargoPackage reads the name and version of a Cargo.toml's [package] table.
func cargoPackage(raw []byte) (name, version string) {
	inPackage := false

	lines := bufio.NewScanner(bytes.NewReader(raw))
	for lines.Scan() {
		line := strings.TrimSpace(lines.Text())
		if strings.HasPrefix(line, "[") {
			inPackage = line == "[package]"

			continue
		}

		if !inPackage {
			continue
		}

		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}

		switch strings.TrimSpace(key) {
		case "name":
			name = tomlString(value)
		case "version":
			version = tomlString(value)
		}
	}

	return name, version
}

// tomlString reads a basic or literal TOML string without escapes.
func tomlString(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 2 || (value[0] != '"' && value[0] != '\'') {
		return ""
	}

	end := strings.IndexByte(value[1:], value[0])
	if end < 0 || strings.Contains(value[1:end+1], `\`) {
		return ""
	}

	return value[1 : end+1]
}

func nugetPackage(dir string) (syftPackage, bool) {
	manifests, err := filepath.Glob(filepath.Join(dir, "*.nuspec"))
	if err != nil || len(manifests) != 1 {
		return syftPackage{}, false
	}

	raw, err := readManifest(manifests[0])
	if err != nil {
		return syftPackage{}, false
	}

	var nuspec struct {
		Metadata struct {
			ID      string `xml:"id"`
			Version string `xml:"version"`
		} `xml:"metadata"`
	}
	if xml.Unmarshal(raw, &nuspec) != nil {
		return syftPackage{}, false
	}

	id, version := strings.TrimSpace(nuspec.Metadata.ID), strings.TrimSpace(nuspec.Metadata.Version)
	if id == "" || version == "" {
		return syftPackage{}, false
	}

	return newSyftPackage(
		packageurl.NewPackageURL(packageurl.TypeNuget, "", id, version, nil, ""),
		"dotnet",
		"dotnet",
		manifestLocation(dir, manifests[0]),
	), true
}

func readManifest(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // path is in Relic's own temp dir
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = f.Close()
	}()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(io.LimitReader(f, maxManifestBytes)); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func manifestLocation(dir, path string) Location {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		rel = filepath.Base(path)
	}

	return Location{
		Path: "/" + filepath.ToSlash(rel),
	}
}

func newSyftPackage(purl *packageurl.PackageURL, typ, language string, location Location) syftPackage {
	text := purl.ToString()
	sum := sha256.Sum256([]byte(text))

	return syftPackage{
		ID:      hex.EncodeToString(sum[:8]),
		Name:    purl.Name,
		Version: purl.Version,
		Type:    typ,
		FoundBy: "relic-package-manifest",
		Locations: []Location{
			location,
		},
		Licenses: []json.RawMessage{},
		Language: language,
		CPEs:     []json.RawMessage{},
		PURL:     text,
	}
}

// withSyftPackage adds pkg to a Syft JSON document's artifacts unless one
// with the same package URL is there.
func withSyftPackage(sbom []byte, pkg syftPackage) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(sbom, &doc); err != nil {
		return nil, fmt.Errorf("read Syft JSON: %w", err)
	}

	artifacts := make([]json.RawMessage, 0, 1)
	if err := json.Unmarshal(doc["artifacts"], &artifacts); err != nil {
		return nil, fmt.Errorf("read Syft JSON artifacts: %w", err)
	}

	for _, raw := range artifacts {
		var known struct {
			PURL string `json:"purl"`
		}
		if json.Unmarshal(raw, &known) == nil && strings.EqualFold(known.PURL, pkg.PURL) {
			return sbom, nil
		}
	}

	added, err := json.Marshal(pkg)
	if err != nil {
		return nil, err
	}

	artifacts = append(artifacts, added)

	if doc["artifacts"], err = json.Marshal(artifacts); err != nil {
		return nil, err
	}

	return json.Marshal(doc)
}
