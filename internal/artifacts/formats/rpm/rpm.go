// Package rpm indexes RPM packages and builds the repodata dnf and yum read.
package rpm

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrInvalid reports a file that is not a readable RPM package.
var ErrInvalid = errors.New("invalid rpm package")

const maxHeaderBytes = 32 << 20

const (
	senseLess      = 0x02
	senseGreater   = 0x04
	senseEqual     = 0x08
	sensePrereq    = 0x40
	senseScriptPre = 0x200
	senseScriptPst = 0x400
)

const (
	tagName            = 1000
	tagVersion         = 1001
	tagRelease         = 1002
	tagEpoch           = 1003
	tagSummary         = 1004
	tagDescription     = 1005
	tagBuildTime       = 1006
	tagBuildHost       = 1007
	tagSize            = 1009
	tagVendor          = 1011
	tagLicense         = 1014
	tagPackager        = 1015
	tagGroup           = 1016
	tagURL             = 1020
	tagArch            = 1022
	tagOldFileNames    = 1027
	tagFileModes       = 1030
	tagFileFlags       = 1037
	tagSourceRPM       = 1044
	tagArchiveSize     = 1046
	tagProvideName     = 1047
	tagRequireFlags    = 1048
	tagRequireName     = 1049
	tagRequireVersion  = 1050
	tagConflictFlags   = 1053
	tagConflictName    = 1054
	tagConflictVersion = 1055
	tagChangelogTime   = 1080
	tagChangelogName   = 1081
	tagChangelogText   = 1082
	tagObsoleteName    = 1090
	tagProvideFlags    = 1112
	tagProvideVersion  = 1113
	tagObsoleteFlags   = 1114
	tagObsoleteVersion = 1115
	tagDirIndexes      = 1116
	tagBaseNames       = 1117
	tagDirNames        = 1118
	tagLongSize        = 5009
	tagRecommendName   = 5046
	tagSuggestName     = 5049
	tagSupplementName  = 5052
	tagEnhanceName     = 5055
)

const (
	signatureTagLongArchiveSize = 271
	signatureTagArchiveSize     = 1007
)

const (
	fileFlagGhost = 1 << 6
	fileTypeMask  = 0o170000
	fileTypeDir   = 0o040000
)

const maxChangelogEntries = 10

// Package is what repodata says about an RPM package.
type Package struct {
	Name          string           `json:"name"`
	Arch          string           `json:"arch"`
	Epoch         int              `json:"epoch"`
	Version       string           `json:"version"`
	Release       string           `json:"release"`
	Summary       string           `json:"summary,omitempty"`
	Description   string           `json:"description,omitempty"`
	Packager      string           `json:"packager,omitempty"`
	URL           string           `json:"url,omitempty"`
	License       string           `json:"license,omitempty"`
	Vendor        string           `json:"vendor,omitempty"`
	Group         string           `json:"group,omitempty"`
	BuildHost     string           `json:"buildHost,omitempty"`
	SourceRPM     string           `json:"sourceRpm,omitempty"`
	BuildTime     int64            `json:"buildTime"`
	InstalledSize uint64           `json:"installedSize"`
	ArchiveSize   uint64           `json:"archiveSize"`
	HeaderStart   int              `json:"headerStart"`
	HeaderEnd     int              `json:"headerEnd"`
	Provides      []Dependency     `json:"provides,omitempty"`
	Requires      []Dependency     `json:"requires,omitempty"`
	Conflicts     []Dependency     `json:"conflicts,omitempty"`
	Obsoletes     []Dependency     `json:"obsoletes,omitempty"`
	Recommends    []Dependency     `json:"recommends,omitempty"`
	Suggests      []Dependency     `json:"suggests,omitempty"`
	Supplements   []Dependency     `json:"supplements,omitempty"`
	Enhances      []Dependency     `json:"enhances,omitempty"`
	Files         []File           `json:"files,omitempty"`
	Changelog     []ChangelogEntry `json:"changelog,omitempty"`
}

// Dependency is one entry of a package's provides, requires and the like;
// Flags is EQ, LT, LE, GT or GE when a version is given.
type Dependency struct {
	Name    string `json:"name"`
	Flags   string `json:"flags,omitempty"`
	Epoch   int    `json:"epoch,omitempty"`
	Version string `json:"version,omitempty"`
	Release string `json:"release,omitempty"`
	Pre     bool   `json:"pre,omitempty"`
}

// File is a path a package installs; Type is "dir", "ghost" or empty for a
// file.
type File struct {
	Path string `json:"path"`
	Type string `json:"type,omitempty"`
}

// ChangelogEntry is one entry of a package's changelog; Date is in Unix
// seconds.
type ChangelogEntry struct {
	Author string `json:"author"`
	Date   int64  `json:"date"`
	Text   string `json:"text"`
}

// EVR is the package's epoch:version-release.
func (p Package) EVR() string {
	return fmt.Sprintf("%d:%s-%s", p.Epoch, p.Version, p.Release)
}

// Parse reads the header of an RPM package.
func Parse(r io.Reader) (Package, error) {
	p, err := parse(io.LimitReader(r, maxHeaderBytes))
	if err != nil {
		return Package{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	return p, nil
}

func parse(r io.Reader) (Package, error) {
	if err := readLead(r); err != nil {
		return Package{}, err
	}

	signature, err := readHeader(r, true)
	if err != nil {
		return Package{}, fmt.Errorf("signature: %w", err)
	}

	h, err := readHeader(r, false)
	if err != nil {
		return Package{}, err
	}

	if h.text(tagName) == "" || h.text(tagVersion) == "" {
		return Package{}, errors.New("no name or version")
	}

	p := Package{
		Name:          h.text(tagName),
		Arch:          h.text(tagArch),
		Epoch:         int(h.number(tagEpoch)),
		Version:       h.text(tagVersion),
		Release:       h.text(tagRelease),
		Summary:       h.text(tagSummary),
		Description:   h.text(tagDescription),
		Packager:      h.text(tagPackager),
		URL:           h.text(tagURL),
		License:       h.text(tagLicense),
		Vendor:        h.text(tagVendor),
		Group:         h.text(tagGroup),
		BuildHost:     h.text(tagBuildHost),
		SourceRPM:     h.text(tagSourceRPM),
		BuildTime:     h.number(tagBuildTime),
		InstalledSize: firstPositive(h.number(tagLongSize), h.number(tagSize)),
		ArchiveSize: firstPositive(
			signature.number(signatureTagLongArchiveSize),
			signature.number(signatureTagArchiveSize),
			h.number(signatureTagLongArchiveSize),
			h.number(tagArchiveSize),
		),
		HeaderStart: leadSize + signature.size,
		HeaderEnd:   leadSize + signature.size + h.size,
		Changelog:   changelog(h),
	}

	relations := []struct {
		deps     *[]Dependency
		tag      uint32
		requires bool
	}{
		{
			deps: &p.Provides,
			tag:  tagProvideName,
		},
		{
			deps:     &p.Requires,
			tag:      tagRequireName,
			requires: true,
		},
		{
			deps: &p.Conflicts,
			tag:  tagConflictName,
		},
		{
			deps: &p.Obsoletes,
			tag:  tagObsoleteName,
		},
		{
			deps: &p.Recommends,
			tag:  tagRecommendName,
		},
		{
			deps: &p.Suggests,
			tag:  tagSuggestName,
		},
		{
			deps: &p.Supplements,
			tag:  tagSupplementName,
		},
		{
			deps: &p.Enhances,
			tag:  tagEnhanceName,
		},
	}
	for _, relation := range relations {
		deps, err := dependencies(h, relation.tag, relation.requires)
		if err != nil {
			return Package{}, err
		}

		*relation.deps = deps
	}

	if p.Files, err = files(h); err != nil {
		return Package{}, err
	}

	return p, nil
}

func firstPositive(values ...int64) uint64 {
	for _, v := range values {
		if v > 0 {
			return uint64(v)
		}
	}

	return 0
}

func dependencies(h header, nameTag uint32, requires bool) ([]Dependency, error) {
	names := h.strings[nameTag]
	flagTag, versionTag := dependencyTags(nameTag)
	flags := h.numbers[flagTag]
	versions := h.strings[versionTag]

	if len(flags) != len(names) || len(versions) != len(names) {
		return nil, fmt.Errorf(
			"tag %d: %d names, %d flags and %d versions",
			nameTag,
			len(names),
			len(flags),
			len(versions),
		)
	}

	var out []Dependency

	for i, name := range names {
		if requires && strings.HasPrefix(name, "rpmlib(") {
			continue
		}

		epoch, version, release := parseEVR(versions[i])

		dep := Dependency{
			Name:    name,
			Flags:   senseFlags(flags[i]),
			Version: version,
			Release: release,
			Pre:     requires && flags[i]&(sensePrereq|senseScriptPre|senseScriptPst) != 0,
		}
		if dep.Version != "" {
			dep.Epoch = epoch
		}

		out = append(out, dep)
	}

	return out, nil
}

func dependencyTags(nameTag uint32) (flagTag, versionTag uint32) {
	switch nameTag {
	case tagProvideName:
		return tagProvideFlags, tagProvideVersion
	case tagRequireName:
		return tagRequireFlags, tagRequireVersion
	case tagConflictName:
		return tagConflictFlags, tagConflictVersion
	case tagObsoleteName:
		return tagObsoleteFlags, tagObsoleteVersion
	}

	return nameTag + 2, nameTag + 1
}

func parseEVR(evr string) (epoch int, version, release string) {
	if e, rest, found := strings.Cut(evr, ":"); found {
		epoch, evr = parseEpoch(e), rest
	}

	version, release, _ = strings.Cut(evr, "-")

	return epoch, version, release
}

func parseEpoch(digits string) int {
	epoch := 0

	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return 0
		}

		epoch = epoch*10 + int(digit-'0')
	}

	return epoch
}

func files(h header) ([]File, error) {
	paths := h.strings[tagOldFileNames]

	if names := h.strings[tagBaseNames]; len(names) > 0 {
		dirs := h.strings[tagDirNames]
		indexes := h.numbers[tagDirIndexes]

		if len(indexes) != len(names) {
			return nil, fmt.Errorf("%d file names and %d directory indexes", len(names), len(indexes))
		}

		paths = make([]string, len(names))
		for i, name := range names {
			if indexes[i] < 0 || indexes[i] >= int64(len(dirs)) {
				return nil, fmt.Errorf("file %q: directory index %d of %d", name, indexes[i], len(dirs))
			}

			paths[i] = dirs[indexes[i]] + name
		}
	}

	modes := h.numbers[tagFileModes]
	flags := h.numbers[tagFileFlags]

	if len(paths) > 0 && (len(modes) != len(paths) || len(flags) != len(paths)) {
		return nil, fmt.Errorf("%d files, %d modes and %d flags", len(paths), len(modes), len(flags))
	}

	var out []File

	for i, path := range paths {
		file := File{
			Path: path,
		}

		switch {
		case flags[i]&fileFlagGhost != 0:
			file.Type = "ghost"
		case modes[i]&fileTypeMask == fileTypeDir:
			file.Type = "dir"
		}

		out = append(out, file)
	}

	return out, nil
}

func senseFlags(flags int64) string {
	switch flags & (senseLess | senseGreater | senseEqual) {
	case senseEqual:
		return "EQ"
	case senseLess:
		return "LT"
	case senseLess | senseEqual:
		return "LE"
	case senseGreater:
		return "GT"
	case senseGreater | senseEqual:
		return "GE"
	}

	return ""
}

func changelog(h header) []ChangelogEntry {
	times := h.numbers[tagChangelogTime]
	names := h.strings[tagChangelogName]
	texts := h.strings[tagChangelogText]

	n := min(len(times), len(names), len(texts), maxChangelogEntries)
	out := make([]ChangelogEntry, 0, n)

	for i := range n {
		out = append(out, ChangelogEntry{
			Author: names[i],
			Date:   times[i],
			Text:   texts[i],
		})
	}

	return out
}
