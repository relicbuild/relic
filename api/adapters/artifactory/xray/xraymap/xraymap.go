// Package xraymap converts between the adapter's component IDs and package
// URLs.
package xraymap

import (
	"errors"
	"regexp"
	"strings"

	packageurl "github.com/package-url/packageurl-go"
)

// XrayVersion is the version the scan API reports.
const XrayVersion = "3.131.18"

// ErrUnsupported is returned for component types without a purl mapping.
var ErrUnsupported = errors.New("unsupported component type")

var prefixes = []struct {
	xray, purl string
}{
	{
		"npm",
		packageurl.TypeNPM,
	},
	{
		"gav",
		packageurl.TypeMaven,
	},
	{
		"go",
		packageurl.TypeGolang,
	},
	{
		"pypi",
		packageurl.TypePyPi,
	},
	{
		"nuget",
		packageurl.TypeNuget,
	},
	{
		"cargo",
		packageurl.TypeCargo,
	},
	{
		"rubygems",
		packageurl.TypeGem,
	},
	{
		"conan",
		packageurl.TypeConan,
	},
	{
		"composer",
		packageurl.TypeComposer,
	},
	{
		"cocoapods",
		packageurl.TypeCocoapods,
	},
	{
		"github",
		packageurl.TypeGithub,
	},
	{
		"alpine",
		"apk",
	},
	{
		"deb",
		packageurl.TypeDebian,
	},
	{
		"rpm",
		packageurl.TypeRPM,
	},
}

// ToPURL converts a component ID to a package URL.
func ToPURL(id string) (string, error) {
	scheme, rest, ok := strings.Cut(id, "://")
	if !ok || rest == "" {
		return "", errors.New("invalid component id " + id)
	}

	typ := ""

	for _, p := range prefixes {
		if p.xray == scheme {
			typ = p.purl
		}
	}

	if typ == "" {
		return "", ErrUnsupported
	}

	if typ == "apk" || typ == packageurl.TypeDebian || typ == packageurl.TypeRPM {
		return osPURL(typ, id, rest)
	}

	i := strings.LastIndex(rest, ":")
	if i <= 0 || i == len(rest)-1 {
		return "", errors.New("component id without version " + id)
	}

	name, version := rest[:i], rest[i+1:]
	namespace := ""

	switch typ {
	case packageurl.TypeMaven:
		g, a, ok := strings.Cut(name, ":")
		if !ok {
			return "", errors.New("gav id without group " + id)
		}

		namespace, name = g, a
	case packageurl.TypeNPM, packageurl.TypeGolang, packageurl.TypeGithub, packageurl.TypeComposer:
		if j := strings.LastIndex(name, "/"); j > 0 {
			namespace, name = name[:j], name[j+1:]
		}
	}

	normalized, err := packageurl.FromString(
		packageurl.NewPackageURL(typ, namespace, name, version, nil, "").ToString(),
	)
	if err != nil {
		return "", err
	}

	return normalized.ToString(), nil
}

var (
	distroRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	releaseRe = regexp.MustCompile(`^[A-Za-z0-9._]*$`)
)

func osPURL(typ, id, rest string) (string, error) {
	parts := strings.SplitN(rest, ":", 4)
	if len(parts) != 4 || !distroRe.MatchString(parts[0]) || !releaseRe.MatchString(parts[1]) || parts[2] == "" ||
		parts[3] == "" {
		return "", errors.New("OS package id needs <distro>:<release>:<name>:<version>: " + id)
	}

	var q packageurl.Qualifiers
	if parts[1] != "" {
		q = packageurl.QualifiersFromMap(map[string]string{
			"distro": parts[0] + "-" + parts[1],
		})
	}

	u, err := packageurl.FromString(packageurl.NewPackageURL(typ, parts[0], parts[2], parts[3], q, "").ToString())
	if err != nil {
		return "", err
	}

	return u.ToString(), nil
}

// FromPURL converts a package URL to a component ID.
func FromPURL(p string) (string, error) {
	u, err := packageurl.FromString(p)
	if err != nil {
		return "", err
	}

	scheme := ""

	for _, pr := range prefixes {
		if pr.purl == u.Type {
			scheme = pr.xray
		}
	}

	if scheme == "" {
		return "", ErrUnsupported
	}

	name := u.Name
	switch {
	case u.Type == "apk" || u.Type == packageurl.TypeDebian || u.Type == packageurl.TypeRPM:
		distro, release := u.Namespace, ""
		if d := u.Qualifiers.Map()["distro"]; d != "" {
			if i := strings.LastIndex(d, "-"); i > 0 {
				distro, release = d[:i], d[i+1:]
			} else {
				distro = d
			}
		}

		if distro == "" {
			return "", errors.New("OS package purl without distro: " + p)
		}

		name = distro + ":" + release + ":" + u.Name
	case u.Type == packageurl.TypeMaven:
		name = u.Namespace + ":" + u.Name
	case u.Namespace != "" && (u.Type == packageurl.TypeNPM || u.Type == packageurl.TypeGolang || u.Type == packageurl.TypeGithub || u.Type == packageurl.TypeComposer):
		name = u.Namespace + "/" + u.Name
	}

	return scheme + "://" + name + ":" + u.Version, nil
}

// Severity maps a scanner severity to the scan API's names.
func Severity(s string) string {
	switch strings.ToLower(s) {
	case "critical":
		return "Critical"
	case "high":
		return "High"
	case "medium":
		return "Medium"
	case "low", "negligible":
		return "Low"
	default:
		return "Unknown"
	}
}

const versionless = "0.0.0-relic-any"

// PackageToPURL converts a component name ("gav://org.apache:log4j")
// and an optional version to a package URL.
func PackageToPURL(name, version string) (string, error) {
	if version != "" {
		return ToPURL(name + ":" + version)
	}

	p, err := ToPURL(name + ":" + versionless)
	if err != nil {
		return "", err
	}

	u, err := packageurl.FromString(p)
	if err != nil {
		return "", err
	}

	u.Version = ""

	return u.ToString(), nil
}

// PackageFromPURL converts a package URL to a component name and
// version ("" when the package URL has none).
func PackageFromPURL(p string) (name, version string, err error) {
	u, err := packageurl.FromString(p)
	if err != nil {
		return "", "", err
	}

	version = u.Version
	if u.Version == "" {
		u.Version = versionless
	}

	id, err := FromPURL(u.ToString())
	if err != nil {
		return "", "", err
	}

	return strings.TrimSuffix(id, ":"+u.Version), version, nil
}
