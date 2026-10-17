package scan

import (
	"strings"

	packageurl "github.com/package-url/packageurl-go"
)

// DependencyPURL returns the package URL of a build-info dependency: its ID
// ("group:artifact:version", "name:version", "module:version") read as the
// module type says.
func DependencyPURL(moduleType, id string) (purl string, ok bool) {
	i := strings.LastIndex(id, ":")
	if i <= 0 || i == len(id)-1 {
		return "", false
	}

	name, version := id[:i], id[i+1:]

	var namespace, typ string

	switch strings.ToLower(moduleType) {
	case "maven", "gradle":
		group, artifact, found := strings.Cut(name, ":")
		if !found || group == "" || artifact == "" || strings.Contains(artifact, ":") {
			return "", false
		}

		typ, namespace, name = packageurl.TypeMaven, group, artifact
	case "npm":
		typ = packageurl.TypeNPM

		if scope, n, found := strings.Cut(name, "/"); found && strings.HasPrefix(scope, "@") {
			namespace, name = scope, n
		}
	case "go", "golang":
		typ = packageurl.TypeGolang

		if j := strings.LastIndex(name, "/"); j > 0 {
			namespace, name = name[:j], name[j+1:]
		}
	case "python", "pypi", "pip":
		typ = packageurl.TypePyPi
		name = strings.ToLower(strings.NewReplacer("_", "-", ".", "-").Replace(name))

		for strings.Contains(name, "--") {
			name = strings.ReplaceAll(name, "--", "-")
		}
	case "nuget", "dotnet":
		typ = packageurl.TypeNuget
	default:
		return "", false
	}

	return packageurl.NewPackageURL(typ, namespace, name, version, nil, "").ToString(), true
}
