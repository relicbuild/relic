package packages

import (
	"net/url"
	"strings"

	"relic/internal/artifacts/repository"
)

// ClientEndpoints returns the URL a repository's package manager points at
// and its container-registry image prefix, at Relic's own mounts (origin is
// the scheme and host clients reach Relic at).
func ClientEndpoints(origin string, r repository.Repository) (packageManager, containerRegistry string) {
	k := url.PathEscape(r.Name)
	host := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")

	switch r.Format {
	case repository.FormatNpm:
		return origin + "/npm/" + k + "/", ""
	case repository.FormatMaven:
		return origin + "/maven/" + k, ""
	case repository.FormatGradle:
		return origin + "/gradle/" + k, ""
	case repository.FormatDebian:
		return origin + "/debian/" + k, ""
	case repository.FormatAlpine:
		return origin + "/alpine/" + k, ""
	case repository.FormatRPM:
		return origin + "/rpm/" + k, ""
	case repository.FormatPyPI:
		return origin + "/pypi/" + k + "/simple", ""
	case repository.FormatGoModules:
		return origin + "/go/" + k, ""
	case repository.FormatHelm:
		return origin + "/helm/" + k, ""
	case repository.FormatHelmOCI, repository.FormatDocker, repository.FormatOCI:
		return "", host + "/" + r.Name
	case repository.FormatNuGet:
		return origin + "/nuget/" + k + "/index.json", ""
	case repository.FormatCargo:
		return origin + "/cargo/" + k + "/index/", ""
	case repository.FormatRubyGems:
		return origin + "/gems/" + k + "/", ""
	case repository.FormatConan:
		return origin + "/conan/" + k, ""
	case repository.FormatTerraform:
		return origin + "/terraform/" + k + "/providers/", ""
	case repository.FormatNix:
		return origin + "/nix/" + k, ""
	case repository.FormatHuggingFace:
		return origin + "/huggingface/" + k, ""
	default:
		return "", ""
	}
}
