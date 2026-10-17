package rpm

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

func entry(t *testing.T, tp testPackage, location string) Entry {
	t.Helper()

	raw := buildRPM(t, tp)

	p, err := Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(raw)

	return Entry{
		Package:  p,
		Location: location,
		Size:     int64(len(raw)),
		SHA256:   hex.EncodeToString(sum[:]),
		Modified: 1_700_000_100,
	}
}

type repomdDocument struct {
	Data []struct {
		Type         string `xml:"type,attr"`
		Checksum     string `xml:"checksum"`
		OpenChecksum string `xml:"open-checksum"`
		Location     struct {
			Href string `xml:"href,attr"`
		} `xml:"location"`
		Size     int64 `xml:"size"`
		OpenSize int64 `xml:"open-size"`
	} `xml:"data"`
}

type metadataDocument struct {
	Count    int `xml:"packages,attr"`
	Packages []struct {
		Name     string `xml:"name"`
		NameAttr string `xml:"name,attr"`
		Location struct {
			Href string `xml:"href,attr"`
		} `xml:"location"`
		Files       []string `xml:"file"`
		FormatFiles []string `xml:"format>file"`
	} `xml:"package"`
}

func readRepodata(t *testing.T, files Repodata) map[string]metadataDocument {
	t.Helper()

	var md repomdDocument
	if err := xml.Unmarshal(files[RepoMD], &md); err != nil {
		t.Fatalf("repomd.xml: %v\n%s", err, files[RepoMD])
	}

	docs := map[string]metadataDocument{}

	for _, d := range md.Data {
		name := strings.TrimPrefix(d.Location.Href, "repodata/")

		compressed, ok := files[name]
		if !ok {
			t.Fatalf("repomd.xml names %s, which is not generated", name)
		}

		sum := sha256.Sum256(compressed)
		if hex.EncodeToString(sum[:]) != d.Checksum || int64(len(compressed)) != d.Size {
			t.Errorf("%s: checksum or size differs from repomd.xml", name)
		}

		zr, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			t.Fatal(err)
		}

		plain, err := io.ReadAll(zr)
		if err != nil {
			t.Fatal(err)
		}

		open := sha256.Sum256(plain)
		if hex.EncodeToString(open[:]) != d.OpenChecksum || int64(len(plain)) != d.OpenSize {
			t.Errorf("%s: open checksum or size differs from repomd.xml", name)
		}

		var doc metadataDocument
		if err := xml.Unmarshal(plain, &doc); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, plain)
		}

		docs[d.Type] = doc
	}

	return docs
}

func upstreamOf(t *testing.T, files Repodata) UpstreamMetadata {
	t.Helper()

	var md repomdDocument
	if err := xml.Unmarshal(files[RepoMD], &md); err != nil {
		t.Fatal(err)
	}

	open := func(kind string) func() (io.ReadCloser, error) {
		for _, d := range md.Data {
			if d.Type == kind {
				compressed := files[strings.TrimPrefix(d.Location.Href, "repodata/")]

				return func() (io.ReadCloser, error) {
					return gzip.NewReader(bytes.NewReader(compressed))
				}
			}
		}

		return nil
	}

	return UpstreamMetadata{
		Primary:   open("primary"),
		Filelists: open("filelists"),
		Other:     open("other"),
	}
}

func TestBuild(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_200, 0)
	hello := entry(t, testPackage{
		name:    "hello",
		version: "1.0",
		release: "1",
		arch:    "noarch",
		files: []string{
			"/usr/bin/hello",
			"/usr/share/hello/data",
		},
	}, "h/hello-1.0-1.noarch.rpm")

	local, err := Build([]Entry{
		hello,
	}, nil, now)
	if err != nil {
		t.Fatal(err)
	}

	docs := readRepodata(t, local)
	if p := docs["primary"]; p.Count != 1 || len(p.Packages) != 1 || p.Packages[0].Name != "hello" ||
		p.Packages[0].Location.Href != "h/hello-1.0-1.noarch.rpm" {
		t.Errorf("primary = %+v", p)
	}

	if p := docs["primary"]; !slices.Equal(p.Packages[0].FormatFiles, []string{
		"/usr/bin/hello",
	}) {
		t.Errorf("primary lists %v, want only the bin file", p.Packages[0].FormatFiles)
	}

	if f := docs["filelists"]; f.Count != 1 || len(f.Packages[0].Files) != 2 {
		t.Errorf("filelists = %+v", f)
	}

	upstream, err := Build([]Entry{
		entry(t, testPackage{
			name:    "tool",
			version: "2.0",
			release: "1",
			arch:    "x86_64",
		}, "Packages/t/tool-2.0-1.x86_64.rpm"),
		entry(t, testPackage{
			name:    "lib",
			version: "3.0",
			release: "1",
			arch:    "x86_64",
		}, "Packages/l/lib-3.0-1.x86_64.rpm"),
	}, nil, now)
	if err != nil {
		t.Fatal(err)
	}

	merged, err := Build([]Entry{
		hello,
	}, []UpstreamMetadata{
		upstreamOf(t, upstream),
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	docs = readRepodata(t, merged)
	for _, kind := range []string{
		"primary",
		"filelists",
		"other",
	} {
		if d := docs[kind]; d.Count != 3 || len(d.Packages) != 3 {
			t.Errorf("merged %s: count %d, %d packages", kind, d.Count, len(d.Packages))
		}
	}

	hrefs := make([]string, 0, len(docs["primary"].Packages))
	for _, p := range docs["primary"].Packages {
		hrefs = append(hrefs, p.Location.Href)
	}

	if !slices.Equal(hrefs, []string{
		"h/hello-1.0-1.noarch.rpm",
		"Packages/t/tool-2.0-1.x86_64.rpm",
		"Packages/l/lib-3.0-1.x86_64.rpm",
	}) {
		t.Errorf("merged locations = %v", hrefs)
	}
}
