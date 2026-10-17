package rpm

import (
	"bytes"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/rpmpack"
)

type testPackage struct {
	name     string
	version  string
	release  string
	epoch    uint32
	arch     string
	provides []string
	requires []string
	files    []string
	dirs     []string
	ghosts   []string
}

func buildRPM(t *testing.T, tp testPackage) []byte {
	t.Helper()

	meta := rpmpack.RPMMetaData{
		Name:        tp.name,
		Version:     tp.version,
		Release:     tp.release,
		Epoch:       tp.epoch,
		Arch:        tp.arch,
		Summary:     tp.name + " summary",
		Description: tp.name + " <description> & more",
		Licence:     "MIT",
		BuildTime:   time.Unix(1_700_000_000, 0),
	}

	for _, p := range tp.provides {
		if err := meta.Provides.Set(p); err != nil {
			t.Fatal(err)
		}
	}

	for _, r := range tp.requires {
		if err := meta.Requires.Set(r); err != nil {
			t.Fatal(err)
		}
	}

	r, err := rpmpack.NewRPM(meta)
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range tp.files {
		r.AddFile(rpmpack.RPMFile{
			Name: f,
			Body: []byte("content of " + f),
			Mode: 0o100755,
		})
	}

	for _, d := range tp.dirs {
		r.AddFile(rpmpack.RPMFile{
			Name: d,
			Mode: 0o40755,
		})
	}

	for _, g := range tp.ghosts {
		r.AddFile(rpmpack.RPMFile{
			Name: g,
			Mode: 0o100644,
			Type: rpmpack.GhostFile,
		})
	}

	var buf bytes.Buffer
	if err := r.Write(&buf); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func TestParse(t *testing.T) {
	t.Parallel()

	raw := buildRPM(t, testPackage{
		name:    "hello",
		version: "1.2",
		release: "3",
		epoch:   2,
		arch:    "x86_64",
		provides: []string{
			"hello-tools=1.2",
		},
		requires: []string{
			"libc>=2.0",
		},
		files: []string{
			"/usr/bin/hello",
			"/usr/share/doc/hello/README",
		},
		dirs: []string{
			"/usr/share/doc/hello",
		},
		ghosts: []string{
			"/var/log/hello.log",
		},
	})

	p, err := Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}

	if p.Name != "hello" || p.Version != "1.2" || p.Release != "3" || p.Epoch != 2 || p.Arch != "x86_64" {
		t.Errorf("identity = %+v", p)
	}

	if p.EVR() != "2:1.2-3" {
		t.Errorf("EVR = %q", p.EVR())
	}

	if p.HeaderStart <= 0 || p.HeaderEnd <= p.HeaderStart {
		t.Errorf("header range = %d-%d", p.HeaderStart, p.HeaderEnd)
	}

	if !slices.ContainsFunc(p.Provides, func(d Dependency) bool {
		return d.Name == "hello-tools" && d.Flags == "EQ" && d.Version == "1.2"
	}) {
		t.Errorf("provides = %+v", p.Provides)
	}

	if !slices.ContainsFunc(p.Requires, func(d Dependency) bool {
		return d.Name == "libc" && d.Flags == "GE" && d.Version == "2.0"
	}) {
		t.Errorf("requires = %+v", p.Requires)
	}

	if slices.ContainsFunc(p.Requires, func(d Dependency) bool {
		return strings.HasPrefix(d.Name, "rpmlib(")
	}) {
		t.Errorf("rpmlib requirements are listed: %+v", p.Requires)
	}

	fileTypes := map[string]string{}
	for _, f := range p.Files {
		fileTypes[f.Path] = f.Type
	}

	if !maps.Equal(fileTypes, map[string]string{
		"/usr/bin/hello":              "",
		"/usr/share/doc/hello/README": "",
		"/usr/share/doc/hello":        "dir",
		"/var/log/hello.log":          "ghost",
	}) {
		t.Errorf("files = %v", fileTypes)
	}

	if _, err := Parse(strings.NewReader("not an rpm")); !errors.Is(err, ErrInvalid) {
		t.Errorf("Parse of garbage = %v, want ErrInvalid", err)
	}
}
