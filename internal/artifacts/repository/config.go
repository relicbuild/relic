package repository

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"relic/internal/access/auth"
)

// Class is what a repository does.
type Class string

// Repository classes.
const (
	// ClassLocal holds files deployed to Relic.
	ClassLocal Class = "local"
	// ClassRemote is a caching proxy of an upstream.
	ClassRemote Class = "remote"
	// ClassVirtual aggregates other repositories.
	ClassVirtual Class = "virtual"
)

// Format is the kind of packages a repository holds.
type Format string

// Formats.
const (
	FormatAlpine           Format = "alpine"
	FormatAnsible          Format = "ansible"
	FormatBower            Format = "bower"
	FormatCargo            Format = "cargo"
	FormatChef             Format = "chef"
	FormatCocoaPods        Format = "cocoapods"
	FormatComposer         Format = "composer"
	FormatConan            Format = "conan"
	FormatConda            Format = "conda"
	FormatCran             Format = "cran"
	FormatDebian           Format = "debian"
	FormatDocker           Format = "docker"
	FormatGeneric          Format = "generic"
	FormatGitLFS           Format = "gitlfs"
	FormatGoModules        Format = "gomod"
	FormatGradle           Format = "gradle"
	FormatHelm             Format = "helm"
	FormatHelmOCI          Format = "helmoci"
	FormatHuggingFace      Format = "huggingface"
	FormatIvy              Format = "ivy"
	FormatMachineLearning  Format = "machinelearning"
	FormatMaven            Format = "maven"
	FormatNix              Format = "nix"
	FormatNpm              Format = "npm"
	FormatNuGet            Format = "nuget"
	FormatOCI              Format = "oci"
	FormatOpkg             Format = "opkg"
	FormatP2               Format = "p2"
	FormatPub              Format = "pub"
	FormatPuppet           Format = "puppet"
	FormatPyPI             Format = "pypi"
	FormatRPM              Format = "rpm"
	FormatRubyGems         Format = "rubygems"
	FormatSbt              Format = "sbt"
	FormatSwift            Format = "swift"
	FormatTerraform        Format = "terraform"
	FormatTerraformBackend Format = "terraformbackend"
	FormatVagrant          Format = "vagrant"
	FormatVCS              Format = "vcs"
	// The agent registries: MCP server definitions and agent skill bundles.
	FormatMCP    Format = "mcp"
	FormatSkills Format = "skills"
	// The kinds of Relic's own repositories: build information, releases and
	// evidence.
	FormatBuild    Format = "build"
	FormatRelease  Format = "release"
	FormatEvidence Format = "evidence"
)

// Formats lists every format.
var Formats = []Format{
	FormatAlpine,
	FormatAnsible,
	FormatBower,
	FormatCargo,
	FormatChef,
	FormatCocoaPods,
	FormatComposer,
	FormatConan,
	FormatConda,
	FormatCran,
	FormatDebian,
	FormatDocker,
	FormatGeneric,
	FormatGitLFS,
	FormatGoModules,
	FormatGradle,
	FormatHelm,
	FormatHelmOCI,
	FormatHuggingFace,
	FormatIvy,
	FormatMachineLearning,
	FormatMaven,
	FormatNix,
	FormatNpm,
	FormatNuGet,
	FormatOCI,
	FormatOpkg,
	FormatP2,
	FormatPub,
	FormatPuppet,
	FormatPyPI,
	FormatRPM,
	FormatRubyGems,
	FormatSbt,
	FormatSwift,
	FormatTerraform,
	FormatTerraformBackend,
	FormatVagrant,
	FormatVCS,
	FormatMCP,
	FormatSkills,
	FormatBuild,
	FormatRelease,
	FormatEvidence,
}

// Known reports whether f is one of Formats.
func (f Format) Known() bool { return slices.Contains(Formats, f) }

// Config is a repository's settings, in Relic's own terms.
type Config struct {
	Description   string                     `json:"description,omitempty"`
	Notes         string                     `json:"notes,omitempty"`
	Environments  []string                   `json:"environments,omitempty"`
	SigningKey    string                     `json:"signingKey,omitempty"`
	Architectures []string                   `json:"architectures,omitempty"`
	MetadataDepth int32                      `json:"metadataDepth,omitempty"`
	MaxUploadSize int64                      `json:"maxUploadSize,omitempty"`
	MaxStorage    int64                      `json:"maxStorage,omitempty"`
	Immutable     bool                       `json:"immutable,omitempty"`
	Remote        *RemoteConfig              `json:"remote,omitempty"`
	Virtual       *VirtualConfig             `json:"virtual,omitempty"`
	Extensions    map[string]json.RawMessage `json:"extensions,omitempty"`
}

// RemoteConfig is how a remote repository reaches its upstream.
type RemoteConfig struct {
	URL                 string `json:"url"`
	Username            string `json:"username,omitempty"`
	Password            string `json:"password,omitempty"`
	Offline             bool   `json:"offline,omitempty"`
	DisableCache        bool   `json:"disableCache,omitempty"`
	CachePeriodSeconds  int64  `json:"cachePeriodSeconds,omitempty"`
	TimeoutMilliseconds int64  `json:"timeoutMilliseconds,omitempty"`
	IndexURL            string `json:"indexUrl,omitempty"`
	// MissedRetrievalCachePeriodSeconds is how long an upstream 404 is
	// remembered: 30 minutes when unset, never when 0.
	MissedRetrievalCachePeriodSeconds *int64 `json:"missedRetrievalCachePeriodSeconds,omitempty"`
	// CooldownDays hides and refuses versions published upstream more
	// recently, for formats whose metadata says when (npm, PyPI).
	CooldownDays int32 `json:"cooldownDays,omitempty"`
	// Quarantine holds the files the repository caches until Relic Scan finds
	// them not malicious, or an administrator releases them.
	Quarantine bool `json:"quarantine,omitempty"`
}

// VirtualConfig is what a virtual repository aggregates.
type VirtualConfig struct {
	Members  []string `json:"members"`
	DeployTo string   `json:"deployTo,omitempty"`
	// LocalOnlyPatterns are Ant patterns on package names (or paths) that
	// resolve only from local members, never from remote ones: protection
	// against dependency confusion.
	LocalOnlyPatterns []string `json:"localOnlyPatterns,omitempty"`
}

const defaultCachePeriod = 7200 * time.Second

// CachePeriod is how long cached remote metadata is fresh.
func (c RemoteConfig) CachePeriod() time.Duration {
	if c.CachePeriodSeconds > 0 {
		return time.Duration(c.CachePeriodSeconds) * time.Second
	}

	return defaultCachePeriod
}

// Members are a virtual repository's members, in resolution order.
func (r Repository) Members() []string {
	if r.Config.Virtual == nil {
		return nil
	}

	return r.Config.Virtual.Members
}

// LocalOnly reports whether a virtual repository resolves name, a package
// name or a path, from its local members only: a pattern matches the name,
// or the path below it. A PyPI repository compares names as PEP 503
// normalizes them.
func (r Repository) LocalOnly(name string) bool {
	if r.Config.Virtual == nil || name == "" {
		return false
	}

	if r.Format == FormatPyPI {
		name = normalizedPyPIName(name)
	}

	for _, p := range r.Config.Virtual.LocalOnlyPatterns {
		if r.Format == FormatPyPI {
			p = normalizedPyPIName(p)
		}

		if auth.MatchesAntPattern(p, name) || auth.MatchesAntPattern(p+"/**", name) {
			return true
		}
	}

	return false
}

var pypiSeparators = regexp.MustCompile(`[-_.]+`)

func normalizedPyPIName(name string) string {
	return strings.ToLower(pypiSeparators.ReplaceAllString(name, "-"))
}

// Upstream returns a remote repository's upstream settings (zero for other
// classes).
func (r Repository) Upstream() RemoteConfig {
	if r.Config.Remote == nil {
		return RemoteConfig{}
	}

	return *r.Config.Remote
}

var (
	repoKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	reservedKeys   = []string{
		"api",
		"ui",
		"list",
	}
	classes = []Class{
		ClassLocal,
		ClassRemote,
		ClassVirtual,
	}
)

// Validate checks a repository before it is stored: a valid key, a known
// class, a known format (generic by default) and the settings block its
// class needs.
func (r *Repository) Validate() error {
	if !repoKeyPattern.MatchString(r.Name) || slices.Contains(reservedKeys, strings.ToLower(r.Name)) {
		return fmt.Errorf("%w: invalid repository key %q", ErrInvalidRepository, r.Name)
	}

	if !slices.Contains(classes, r.Class) {
		return fmt.Errorf("%w: class must be local, remote or virtual", ErrInvalidRepository)
	}

	if r.Format == "" {
		r.Format = FormatGeneric
	}

	if !r.Format.Known() {
		return fmt.Errorf("%w: unknown format %q", ErrInvalidRepository, r.Format)
	}

	if r.Config.MaxUploadSize < 0 || r.Config.MaxStorage < 0 {
		return fmt.Errorf("%w: maxUploadSize and maxStorage cannot be negative", ErrInvalidRepository)
	}

	if r.Config.Remote != nil && r.Class != ClassRemote {
		return fmt.Errorf("%w: a %s repository takes no remote settings", ErrInvalidRepository, r.Class)
	}

	if r.Config.Virtual != nil && r.Class != ClassVirtual {
		return fmt.Errorf("%w: a %s repository takes no virtual settings", ErrInvalidRepository, r.Class)
	}

	switch r.Class {
	case ClassRemote:
		if r.Config.Remote == nil {
			r.Config.Remote = &RemoteConfig{}
		}
	case ClassVirtual:
		if r.Config.Virtual == nil {
			r.Config.Virtual = &VirtualConfig{}
		}

		if r.Config.Virtual.Members == nil {
			r.Config.Virtual.Members = []string{}
		}

		if slices.Contains(r.Config.Virtual.Members, r.Name) {
			return fmt.Errorf("%w: virtual repository %s cannot be its own member", ErrInvalidRepository, r.Name)
		}

		if d := r.Config.Virtual.DeployTo; d != "" && !slices.Contains(r.Config.Virtual.Members, d) {
			return fmt.Errorf("%w: deployTo %s is not a member of %s", ErrInvalidRepository, d, r.Name)
		}
	case ClassLocal:
	}

	if !storableFields(reflect.ValueOf(r.Config)) {
		return fmt.Errorf("%w: the settings hold text that is not UTF-8 or holds NUL", ErrInvalidRepository)
	}

	return nil
}
