package scan

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"relic/internal/artifacts/formats/alpine"
	"relic/internal/artifacts/formats/gems"

	packageurl "github.com/package-url/packageurl-go"
)

type distro struct {
	ID        string `json:"id"`
	VersionID string `json:"versionID"`
}

func filePackage(fileName, file string) (syftPackage, bool) {
	lower := strings.ToLower(fileName)
	if !strings.HasSuffix(lower, ".gem") && !strings.HasSuffix(lower, ".apk") {
		return syftPackage{}, false
	}

	f, err := os.Open(file) //nolint:gosec // Relic's own temp file
	if err != nil {
		return syftPackage{}, false
	}

	defer func() {
		_ = f.Close()
	}()

	location := Location{
		Path: "/" + fileName,
	}

	if strings.HasSuffix(lower, ".gem") {
		return gemPackage(f, location)
	}

	return apkPackage(f, location)
}

func gemPackage(r io.Reader, location Location) (syftPackage, bool) {
	spec, err := gems.ReadSpec(r)
	if err != nil || spec.Name == "" || spec.Version == "" {
		return syftPackage{}, false
	}

	var qualifiers packageurl.Qualifiers
	if spec.Platform != "" && spec.Platform != "ruby" {
		qualifiers = packageurl.QualifiersFromMap(map[string]string{
			"platform": spec.Platform,
		})
	}

	return newSyftPackage(
		packageurl.NewPackageURL(packageurl.TypeGem, "", spec.Name, spec.Version, qualifiers, ""),
		"gem",
		"ruby",
		location,
	), true
}

func apkPackage(r io.Reader, location Location) (syftPackage, bool) {
	pkg, err := alpine.Parse(r)
	if err != nil {
		return syftPackage{}, false
	}

	qualifiers := map[string]string{}
	if pkg.Arch != "" {
		qualifiers["arch"] = pkg.Arch
	}

	if pkg.Origin != "" {
		qualifiers["upstream"] = pkg.Origin
	}

	return newSyftPackage(
		packageurl.NewPackageURL(
			packageurl.TypeApk,
			"alpine",
			pkg.Name,
			pkg.Version,
			packageurl.QualifiersFromMap(qualifiers),
			"",
		),
		"apk",
		"",
		location,
	), true
}

func withFilePackage(sbom []byte, fileName, file string) []byte {
	pkg, ok := filePackage(fileName, file)
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

func osPackageType(fileName string) string {
	switch ext := strings.ToLower(path.Ext(fileName)); ext {
	case ".deb", ".rpm", ".apk":
		return ext[1:]
	}

	return ""
}

var (
	rpmRelease    = regexp.MustCompile(`\.(el|fc|amzn)(\d+)(?:[._]|$)`)
	debianRelease = regexp.MustCompile(`[+~]deb(\d+)u`)
	ubuntuRelease = regexp.MustCompile(`ubuntu[0-9.]*?(\d{2}\.\d{2})(?:\.|$)`)
	alpineBranch  = regexp.MustCompile(`(?:^|/)v(\d+\.\d+)(?:/|$)`)
)

var rpmDistros = map[string]string{
	"el":   "rhel",
	"fc":   "fedora",
	"amzn": "amazonlinux",
}

func releaseDistro(pkgType, version string) (distro, bool) {
	switch pkgType {
	case "rpm":
		if m := rpmRelease.FindStringSubmatch(version); m != nil {
			return distro{
				ID:        rpmDistros[m[1]],
				VersionID: m[2],
			}, true
		}
	case "deb":
		if m := debianRelease.FindStringSubmatch(version); m != nil {
			return distro{
				ID:        "debian",
				VersionID: m[1],
			}, true
		}

		if m := ubuntuRelease.FindStringSubmatch(version); m != nil {
			return distro{
				ID:        "ubuntu",
				VersionID: m[1],
			}, true
		}
	}

	return distro{}, false
}

var debianCodenames = map[string]distro{
	"buster": {
		ID:        "debian",
		VersionID: "10",
	},
	"bullseye": {
		ID:        "debian",
		VersionID: "11",
	},
	"bookworm": {
		ID:        "debian",
		VersionID: "12",
	},
	"trixie": {
		ID:        "debian",
		VersionID: "13",
	},
	"bionic": {
		ID:        "ubuntu",
		VersionID: "18.04",
	},
	"focal": {
		ID:        "ubuntu",
		VersionID: "20.04",
	},
	"jammy": {
		ID:        "ubuntu",
		VersionID: "22.04",
	},
	"noble": {
		ID:        "ubuntu",
		VersionID: "24.04",
	},
}

func distributionDistro(pkgType string, distributions []string) (distro, bool) {
	if pkgType != "deb" {
		return distro{}, false
	}

	var releases []distro

	for _, distribution := range distributions {
		codename, _, _ := strings.Cut(strings.ToLower(distribution), "-")
		if d, ok := debianCodenames[codename]; ok {
			releases = append(releases, d)
		}
	}

	if len(releases) == 0 {
		return distro{}, false
	}

	return slices.MinFunc(releases, func(a, b distro) int {
		return cmp.Or(
			strings.Compare(a.ID, b.ID),
			cmp.Compare(releaseNumber(a.VersionID), releaseNumber(b.VersionID)),
		)
	}), true
}

func releaseNumber(versionID string) float64 {
	number, err := strconv.ParseFloat(versionID, 64)
	if err != nil {
		return 0
	}

	return number
}

func pathDistro(pkgType string, folders []string) (distro, bool) {
	if pkgType != "apk" {
		return distro{}, false
	}

	for _, folder := range folders {
		if m := alpineBranch.FindStringSubmatch(folder); m != nil {
			return distro{
				ID:        "alpine",
				VersionID: m[1],
			}, true
		}
	}

	return distro{}, false
}

func osPackageVersions(sbom []byte, pkgType string) []string {
	var doc struct {
		Artifacts []syftPackage `json:"artifacts"`
	}
	if json.Unmarshal(sbom, &doc) != nil {
		return nil
	}

	var out []string

	for _, a := range doc.Artifacts {
		if a.Type == pkgType {
			out = append(out, a.Version)
		}
	}

	return out
}

func withDistro(sbom []byte, d distro) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(sbom, &doc); err != nil {
		return nil, fmt.Errorf("read Syft JSON: %w", err)
	}

	var known distro
	if raw, ok := doc["distro"]; ok && json.Unmarshal(raw, &known) == nil && known.ID != "" {
		return sbom, nil
	}

	raw, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}

	doc["distro"] = raw

	return json.Marshal(doc)
}

func osPackageDistro(sbom []byte, fileName string, folders, distributions []string) ([]byte, string) {
	pkgType := osPackageType(fileName)
	if pkgType == "" {
		return sbom, ""
	}

	versions := osPackageVersions(sbom, pkgType)
	if len(versions) == 0 {
		return sbom, ""
	}

	d, ok := distro{}, false
	for _, v := range versions {
		if d, ok = releaseDistro(pkgType, v); ok {
			break
		}
	}

	if !ok {
		d, ok = distributionDistro(pkgType, distributions)
	}

	if !ok {
		d, ok = pathDistro(pkgType, folders)
	}

	if !ok {
		return sbom, "the package names no distribution release, so its OS vulnerabilities were not matched"
	}

	added, err := withDistro(sbom, d)
	if err != nil {
		return sbom, "the SBOM could not take the package's distribution, so its OS vulnerabilities were not matched"
	}

	return added, ""
}
