package rpm

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// RepoMD is the file that lists a repodata folder's metadata files.
const RepoMD = "repomd.xml"

// Entry is a package stored in a repository: its metadata, where it is
// below the repodata root, and the file's size, SHA-256 and time.
type Entry struct {
	Package  Package
	Location string
	Size     int64
	SHA256   string
	Modified int64
}

// UpstreamMetadata opens a remote repository's decompressed primary,
// filelists and other metadata.
type UpstreamMetadata struct {
	Primary   func() (io.ReadCloser, error)
	Filelists func() (io.ReadCloser, error)
	Other     func() (io.ReadCloser, error)
	// Hidden are the package IDs (pkgid checksums) left out.
	Hidden map[string]bool
}

// Repodata is a generated repodata folder: its files by name.
type Repodata map[string][]byte

type metadataKind struct {
	name       string
	root       string
	namespaces string
	render     func(*strings.Builder, Entry)
	upstream   func(UpstreamMetadata) func() (io.ReadCloser, error)
}

var metadataKinds = []metadataKind{
	{
		name:       "primary",
		root:       "metadata",
		namespaces: ` xmlns="http://linux.duke.edu/metadata/common" xmlns:rpm="http://linux.duke.edu/metadata/rpm"`,
		render:     renderPrimary,
		upstream: func(u UpstreamMetadata) func() (io.ReadCloser, error) {
			return u.Primary
		},
	},
	{
		name:       "filelists",
		root:       "filelists",
		namespaces: ` xmlns="http://linux.duke.edu/metadata/filelists"`,
		render:     renderFilelists,
		upstream: func(u UpstreamMetadata) func() (io.ReadCloser, error) {
			return u.Filelists
		},
	},
	{
		name:       "other",
		root:       "otherdata",
		namespaces: ` xmlns="http://linux.duke.edu/metadata/other"`,
		render:     renderOther,
		upstream: func(u UpstreamMetadata) func() (io.ReadCloser, error) {
			return u.Other
		},
	},
}

type dataFile struct {
	kind         string
	compressed   []byte
	checksum     string
	openChecksum string
	openSize     int64
}

// Build generates the repodata of entries and of the upstream repositories'
// metadata, which keeps each upstream package's location.
func Build(entries []Entry, upstreams []UpstreamMetadata, now time.Time) (Repodata, error) {
	files := Repodata{}

	var data []dataFile

	for _, k := range metadataKinds {
		d, err := k.build(entries, upstreams)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k.name, err)
		}

		files[repodataName(d)] = d.compressed
		data = append(data, d)
	}

	files[RepoMD] = repoMD(data, now)

	return files, nil
}

func repodataName(d dataFile) string {
	return d.checksum + "-" + d.kind + ".xml.gz"
}

type upstreamSource struct {
	body     io.ReadCloser
	decoder  *xml.Decoder
	recorder *recorder
	hidden   map[string]bool
}

func (k metadataKind) build(entries []Entry, upstreams []UpstreamMetadata) (dataFile, error) {
	var sources []upstreamSource

	defer func() {
		for _, s := range sources {
			_ = s.body.Close()
		}
	}()

	total := len(entries)

	for _, u := range upstreams {
		open := k.upstream(u)
		if open == nil {
			continue
		}

		body, err := open()
		if err != nil {
			return dataFile{}, err
		}

		rec := &recorder{
			r: body,
		}
		decoder := xml.NewDecoder(rec)

		sources = append(sources, upstreamSource{
			body:     body,
			decoder:  decoder,
			recorder: rec,
			hidden:   u.Hidden,
		})

		count, err := packageCount(decoder)
		if err != nil {
			return dataFile{}, fmt.Errorf("upstream: %w", err)
		}

		total += max(count-len(u.Hidden), 0)
	}

	var compressed bytes.Buffer

	gz := gzip.NewWriter(&compressed)
	openHash := sha256.New()
	out := &countingWriter{
		w: io.MultiWriter(gz, openHash),
	}

	_, _ = fmt.Fprintf(
		out,
		"<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<%s%s packages=\"%d\">\n",
		k.root,
		k.namespaces,
		total,
	)

	for _, e := range entries {
		var b strings.Builder

		k.render(&b, e)
		_, _ = io.WriteString(out, b.String())
	}

	for _, s := range sources {
		if err := copyPackages(out, s.decoder, s.recorder, s.hidden); err != nil {
			return dataFile{}, fmt.Errorf("upstream: %w", err)
		}
	}

	_, _ = fmt.Fprintf(out, "</%s>\n", k.root)

	if out.err != nil {
		return dataFile{}, out.err
	}

	if err := gz.Close(); err != nil {
		return dataFile{}, err
	}

	sum := sha256.Sum256(compressed.Bytes())

	return dataFile{
		kind:         k.name,
		compressed:   compressed.Bytes(),
		checksum:     hex.EncodeToString(sum[:]),
		openChecksum: hex.EncodeToString(openHash.Sum(nil)),
		openSize:     out.n,
	}, nil
}

func packageCount(decoder *xml.Decoder) (int, error) {
	for {
		tok, err := decoder.Token()
		if err != nil {
			return 0, err
		}

		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}

		for _, a := range start.Attr {
			if a.Name.Local == "packages" {
				return strconv.Atoi(a.Value)
			}
		}

		return 0, fmt.Errorf("<%s> has no packages count", start.Name.Local)
	}
}

type packageID struct {
	PkgID     string `xml:"pkgid,attr"`
	Checksums []struct {
		PkgID string `xml:"pkgid,attr"`
		Value string `xml:",chardata"`
	} `xml:"checksum"`
	Location struct {
		Href string `xml:"href,attr"`
	} `xml:"location"`
}

func (p packageID) id() string {
	for _, c := range p.Checksums {
		if c.PkgID == "YES" {
			return strings.TrimSpace(c.Value)
		}
	}

	return p.PkgID
}

func hiddenPackages(
	openPrimary func() (io.ReadCloser, error),
	hide func(location string) bool,
) (map[string]bool, error) {
	body, err := openPrimary()
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = body.Close()
	}()

	hidden := map[string]bool{}
	decoder := xml.NewDecoder(body)

	for {
		tok, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return hidden, nil
		}

		if err != nil {
			return nil, err
		}

		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "package" {
			continue
		}

		var p packageID
		if err := decoder.DecodeElement(&p, &start); err != nil {
			return nil, err
		}

		if hide(p.Location.Href) {
			hidden[p.id()] = true
		}
	}
}

func copyPackages(out io.Writer, decoder *xml.Decoder, rec *recorder, hidden map[string]bool) error {
	for {
		offset := decoder.InputOffset()

		tok, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return err
		}

		start, ok := tok.(xml.StartElement)
		if !ok {
			rec.discardBefore(decoder.InputOffset())
			continue
		}

		if start.Name.Local != "package" {
			return fmt.Errorf("unexpected <%s>", start.Name.Local)
		}

		var p packageID

		if len(hidden) == 0 {
			err = decoder.Skip()
		} else {
			err = decoder.DecodeElement(&p, &start)
		}

		if err != nil {
			return err
		}

		end := decoder.InputOffset()

		if hidden[p.id()] {
			rec.discardBefore(end)
			continue
		}

		raw, err := rec.between(offset, end)
		if err != nil {
			return err
		}

		if _, err := out.Write(append(bytes.TrimSpace(raw), '\n')); err != nil {
			return err
		}

		rec.discardBefore(end)
	}
}

type recorder struct {
	r    io.Reader
	buf  []byte
	base int64
}

func (r *recorder) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.buf = append(r.buf, p[:n]...)

	return n, err
}

func (r *recorder) between(start, end int64) ([]byte, error) {
	if start < r.base || end-r.base > int64(len(r.buf)) {
		return nil, errors.New("the recorded input does not cover the element")
	}

	return r.buf[start-r.base : end-r.base], nil
}

func (r *recorder) discardBefore(offset int64) {
	if offset <= r.base {
		return
	}

	r.buf = append(r.buf[:0], r.buf[offset-r.base:]...)
	r.base = offset
}

type countingWriter struct {
	w   io.Writer
	n   int64
	err error
}

func (c *countingWriter) Write(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}

	n, err := c.w.Write(p)
	c.n += int64(n)
	c.err = err

	return n, err
}

func repoMD(data []dataFile, now time.Time) []byte {
	var b strings.Builder

	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString(
		"<repomd xmlns=\"http://linux.duke.edu/metadata/repo\" xmlns:rpm=\"http://linux.duke.edu/metadata/rpm\">\n",
	)
	fmt.Fprintf(&b, "  <revision>%d</revision>\n", now.Unix())

	for _, d := range data {
		fmt.Fprintf(&b, "  <data type=\"%s\">\n", d.kind)
		fmt.Fprintf(&b, "    <checksum type=\"sha256\">%s</checksum>\n", d.checksum)
		fmt.Fprintf(&b, "    <open-checksum type=\"sha256\">%s</open-checksum>\n", d.openChecksum)
		fmt.Fprintf(&b, "    <location href=\"repodata/%s\"/>\n", repodataName(d))
		fmt.Fprintf(&b, "    <timestamp>%d</timestamp>\n", now.Unix())
		fmt.Fprintf(&b, "    <size>%d</size>\n", len(d.compressed))
		fmt.Fprintf(&b, "    <open-size>%d</open-size>\n", d.openSize)
		b.WriteString("  </data>\n")
	}

	b.WriteString("</repomd>\n")

	return []byte(b.String())
}

func escaped(s string) string {
	var b strings.Builder

	_ = xml.EscapeText(&b, []byte(s))

	return b.String()
}

func versionElement(b *strings.Builder, p Package) {
	fmt.Fprintf(b, "<version epoch=\"%d\" ver=\"%s\" rel=\"%s\"/>", p.Epoch, escaped(p.Version), escaped(p.Release))
}

func renderPrimary(b *strings.Builder, e Entry) {
	p := e.Package

	b.WriteString("<package type=\"rpm\">")
	fmt.Fprintf(b, "<name>%s</name><arch>%s</arch>", escaped(p.Name), escaped(p.Arch))
	versionElement(b, p)
	fmt.Fprintf(b, "<checksum type=\"sha256\" pkgid=\"YES\">%s</checksum>", e.SHA256)
	fmt.Fprintf(b, "<summary>%s</summary><description>%s</description>", escaped(p.Summary), escaped(p.Description))
	fmt.Fprintf(b, "<packager>%s</packager><url>%s</url>", escaped(p.Packager), escaped(p.URL))
	fmt.Fprintf(b, "<time file=\"%d\" build=\"%d\"/>", e.Modified, p.BuildTime)
	fmt.Fprintf(b, "<size package=\"%d\" installed=\"%d\" archive=\"%d\"/>", e.Size, p.InstalledSize, p.ArchiveSize)
	fmt.Fprintf(b, "<location href=\"%s\"/>", escaped(e.Location))
	b.WriteString("<format>")
	fmt.Fprintf(b, "<rpm:license>%s</rpm:license><rpm:vendor>%s</rpm:vendor>", escaped(p.License), escaped(p.Vendor))
	fmt.Fprintf(b, "<rpm:group>%s</rpm:group><rpm:buildhost>%s</rpm:buildhost>", escaped(p.Group), escaped(p.BuildHost))
	fmt.Fprintf(b, "<rpm:sourcerpm>%s</rpm:sourcerpm>", escaped(p.SourceRPM))
	fmt.Fprintf(b, "<rpm:header-range start=\"%d\" end=\"%d\"/>", p.HeaderStart, p.HeaderEnd)

	for _, section := range []struct {
		name string
		deps []Dependency
	}{
		{
			name: "provides",
			deps: p.Provides,
		},
		{
			name: "requires",
			deps: p.Requires,
		},
		{
			name: "conflicts",
			deps: p.Conflicts,
		},
		{
			name: "obsoletes",
			deps: p.Obsoletes,
		},
		{
			name: "suggests",
			deps: p.Suggests,
		},
		{
			name: "recommends",
			deps: p.Recommends,
		},
		{
			name: "supplements",
			deps: p.Supplements,
		},
		{
			name: "enhances",
			deps: p.Enhances,
		},
	} {
		dependencyElements(b, section.name, section.deps)
	}

	for _, f := range p.Files {
		if primaryFile(f.Path) {
			fileElement(b, f)
		}
	}

	b.WriteString("</format></package>\n")
}

func dependencyElements(b *strings.Builder, name string, deps []Dependency) {
	if len(deps) == 0 {
		return
	}

	fmt.Fprintf(b, "<rpm:%s>", name)

	for _, d := range deps {
		fmt.Fprintf(b, "<rpm:entry name=\"%s\"", escaped(d.Name))

		if d.Flags != "" {
			fmt.Fprintf(b, " flags=\"%s\"", d.Flags)
		}

		if d.Version != "" {
			fmt.Fprintf(b, " epoch=\"%d\" ver=\"%s\"", d.Epoch, escaped(d.Version))

			if d.Release != "" {
				fmt.Fprintf(b, " rel=\"%s\"", escaped(d.Release))
			}
		}

		if d.Pre {
			b.WriteString(" pre=\"1\"")
		}

		b.WriteString("/>")
	}

	fmt.Fprintf(b, "</rpm:%s>", name)
}

func primaryFile(p string) bool {
	return strings.HasPrefix(p, "/etc/") || strings.Contains(p, "bin/") || p == "/usr/lib/sendmail"
}

func fileElement(b *strings.Builder, f File) {
	if f.Type != "" {
		fmt.Fprintf(b, "<file type=\"%s\">%s</file>", f.Type, escaped(f.Path))
		return
	}

	fmt.Fprintf(b, "<file>%s</file>", escaped(f.Path))
}

func packageStart(b *strings.Builder, e Entry) {
	fmt.Fprintf(
		b,
		"<package pkgid=\"%s\" name=\"%s\" arch=\"%s\">",
		e.SHA256,
		escaped(e.Package.Name),
		escaped(e.Package.Arch),
	)
	versionElement(b, e.Package)
}

func renderFilelists(b *strings.Builder, e Entry) {
	packageStart(b, e)

	for _, f := range e.Package.Files {
		fileElement(b, f)
	}

	b.WriteString("</package>\n")
}

func renderOther(b *strings.Builder, e Entry) {
	packageStart(b, e)

	for _, c := range e.Package.Changelog {
		fmt.Fprintf(
			b,
			"<changelog author=\"%s\" date=\"%d\">%s</changelog>",
			escaped(c.Author),
			c.Date,
			escaped(c.Text),
		)
	}

	b.WriteString("</package>\n")
}
