// Package rpm indexes RPM packages and builds the repodata dnf and yum read.
package rpm

import (
	"errors"
	"fmt"
	"io"
	"strings"

	rpmheader "github.com/cavaliergopher/rpm"
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
	tagChangelogTime = 1080
	tagChangelogName = 1081
	tagChangelogText = 1082
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
	h, err := rpmheader.Read(io.LimitReader(r, maxHeaderBytes))
	if err != nil {
		return Package{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	if h.Name() == "" || h.Version() == "" {
		return Package{}, fmt.Errorf("%w: no name or version", ErrInvalid)
	}

	start, end := h.HeaderRange()

	p := Package{
		Name:          h.Name(),
		Arch:          h.Architecture(),
		Epoch:         h.Epoch(),
		Version:       h.Version(),
		Release:       h.Release(),
		Summary:       h.Summary(),
		Description:   h.Description(),
		Packager:      h.Packager(),
		URL:           h.URL(),
		License:       h.License(),
		Vendor:        h.Vendor(),
		Group:         strings.Join(h.Groups(), ", "),
		BuildHost:     h.BuildHost(),
		SourceRPM:     h.SourceRPM(),
		BuildTime:     h.BuildTime().Unix(),
		InstalledSize: h.Size(),
		ArchiveSize:   h.ArchiveSize(),
		HeaderStart:   start,
		HeaderEnd:     end,
		Provides:      dependencies(h.Provides(), false),
		Requires:      dependencies(h.Requires(), true),
		Conflicts:     dependencies(h.Conflicts(), false),
		Obsoletes:     dependencies(h.Obsoletes(), false),
		Recommends:    dependencies(h.Recommends(), false),
		Suggests:      dependencies(h.Suggests(), false),
		Supplements:   dependencies(h.Supplements(), false),
		Enhances:      dependencies(h.Enhances(), false),
		Changelog:     changelog(&h.Header),
	}

	for _, f := range h.Files() {
		file := File{
			Path: f.Name(),
		}

		switch {
		case f.Flags()&rpmheader.FileFlagGhost != 0:
			file.Type = "ghost"
		case f.IsDir():
			file.Type = "dir"
		}

		p.Files = append(p.Files, file)
	}

	return p, nil
}

func dependencies(deps []rpmheader.Dependency, requires bool) []Dependency {
	var out []Dependency

	for _, d := range deps {
		if requires && strings.HasPrefix(d.Name(), "rpmlib(") {
			continue
		}

		dep := Dependency{
			Name:    d.Name(),
			Flags:   senseFlags(d.Flags()),
			Version: d.Version(),
			Release: d.Release(),
			Pre:     requires && d.Flags()&(sensePrereq|senseScriptPre|senseScriptPst) != 0,
		}
		if dep.Version != "" {
			dep.Epoch = d.Epoch()
		}

		out = append(out, dep)
	}

	return out
}

func senseFlags(flags int) string {
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

func changelog(h *rpmheader.Header) []ChangelogEntry {
	times := h.GetTag(tagChangelogTime).Int64Slice()
	names := h.GetTag(tagChangelogName).StringSlice()
	texts := h.GetTag(tagChangelogText).StringSlice()

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
