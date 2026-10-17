// Package debian implements Debian (apt) repositories the way Relic
// lays them out: .deb files anywhere in the repository, placed in
// distributions by the deb.distribution, deb.component and
// deb.architecture properties, with dists/<dist>/... indexes generated and
// signed with OpenPGP.
package debian

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/md5"  //nolint:gosec // Release and Packages list MD5Sum by format
	"crypto/sha1" //nolint:gosec // and SHA1
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// ErrInvalid is returned for files that are not Debian packages.
var ErrInvalid = errors.New("invalid deb package")

var errTooLarge = errors.New("file too large")

const (
	maxControl = 1 << 20
	maxIndex   = 1 << 30
)

// Control reads the control file of a .deb (an ar archive whose
// control.tar.{gz,xz,zst,} member holds ./control).
func Control(r io.Reader) (string, error) {
	br := bufio.NewReader(r)

	magic := make([]byte, 8)
	if _, err := io.ReadFull(br, magic); err != nil || string(magic) != "!<arch>\n" {
		return "", fmt.Errorf("%w: not an ar archive", ErrInvalid)
	}

	for {
		hdr := make([]byte, 60)
		if _, err := io.ReadFull(br, hdr); err != nil {
			return "", fmt.Errorf("%w: no control member", ErrInvalid)
		}

		name := strings.TrimRight(strings.TrimSpace(string(hdr[:16])), "/")

		size, err := strconv.ParseInt(strings.TrimSpace(string(hdr[48:58])), 10, 64)
		if err != nil || size < 0 {
			return "", fmt.Errorf("%w: bad member size", ErrInvalid)
		}

		if !strings.HasPrefix(name, "control.tar") {
			if _, err := br.Discard(int(size + size%2)); err != nil {
				return "", fmt.Errorf("%w: %w", ErrInvalid, err)
			}

			continue
		}

		body, err := decompress(path.Ext(name), io.LimitReader(br, size))
		if err != nil {
			return "", fmt.Errorf("%w: %w", ErrInvalid, err)
		}

		ctl, err := controlFile(body)
		_ = body.Close()

		return ctl, err
	}
}

func controlFile(controlTar io.Reader) (string, error) {
	tr := tar.NewReader(controlTar)
	for {
		h, err := tr.Next()
		if err != nil {
			return "", fmt.Errorf("%w: control.tar has no control file", ErrInvalid)
		}

		if strings.TrimPrefix(h.Name, "./") == "control" {
			b, err := readCapped(tr, maxControl)
			if err != nil {
				return "", fmt.Errorf("%w: %w", ErrInvalid, err)
			}

			return strings.TrimSpace(string(b)) + "\n", nil
		}
	}
}

func decompress(ext string, r io.Reader) (io.ReadCloser, error) {
	switch ext {
	case ".gz":
		return gzip.NewReader(r)
	case ".xz":
		xzReader, err := xz.NewReader(r)
		if err != nil {
			return nil, err
		}

		return io.NopCloser(xzReader), nil
	case ".zst":
		d, err := zstd.NewReader(r)
		if err != nil {
			return nil, err
		}

		return d.IOReadCloser(), nil
	case ".tar":
		return io.NopCloser(r), nil
	}

	return nil, fmt.Errorf("unsupported compression %q", ext)
}

func readCapped(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}

	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: more than %d bytes", errTooLarge, limit)
	}

	return b, nil
}

// Field returns a control paragraph's value for key.
func Field(paragraph, key string) string {
	for line := range strings.SplitSeq(paragraph, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && !strings.HasPrefix(line, " ") && strings.EqualFold(k, key) {
			return strings.TrimSpace(v)
		}
	}

	return ""
}

// File is a package file as a Packages entry describes it.
type File struct {
	Control           string
	Filename          string
	Size              int64
	MD5, SHA1, SHA256 string
}

// Paragraph renders the Packages entry for f.
func (f File) Paragraph() string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(f.Control, "\n"))
	fmt.Fprintf(
		&b,
		"\nFilename: %s\nSize: %d\nMD5sum: %s\nSHA1: %s\nSHA256: %s\n",
		f.Filename,
		f.Size,
		f.MD5,
		f.SHA1,
		f.SHA256,
	)

	return b.String()
}

// Packages joins paragraphs into a Packages file.
func Packages(paragraphs []string) []byte {
	var b bytes.Buffer
	for _, p := range paragraphs {
		b.WriteString(strings.TrimRight(p, "\n"))
		b.WriteString("\n\n")
	}

	return b.Bytes()
}

// Paragraphs splits a Packages file.
func Paragraphs(data []byte) []string {
	var out []string

	for p := range strings.SplitSeq(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n\n") {
		if p = strings.Trim(p, "\n"); p != "" {
			out = append(out, p+"\n")
		}
	}

	return out
}

// Gzip compresses data (Packages.gz).
func Gzip(data []byte) ([]byte, error) {
	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// ReadIndex decompresses an index file by its name (Packages.xz, .gz or
// plain).
func ReadIndex(name string, r io.Reader) ([]byte, error) {
	if strings.HasSuffix(name, "Packages") {
		return readCapped(r, maxIndex)
	}

	body, err := decompress(path.Ext(name), r)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = body.Close()
	}()

	return readCapped(body, maxIndex)
}

// IndexFile is one file a Release lists.
type IndexFile struct {
	Path string
	Data []byte
}

// Release renders a Release file for dist listing files.
func Release(label, dist string, components, archs []string, files []IndexFile, now time.Time) []byte {
	var b strings.Builder
	fmt.Fprintf(
		&b,
		"Origin: %s\nLabel: %s\nSuite: %s\nCodename: %s\nDate: %s\n",
		label,
		label,
		dist,
		dist,
		now.UTC().Format(time.RFC1123Z),
	)
	fmt.Fprintf(&b, "Architectures: %s\nComponents: %s\n", strings.Join(archs, " "), strings.Join(components, " "))

	//nolint:gosec // format
	//nolint:gosec // format
	for _, sum := range []struct {
		name string
		f    func([]byte) string
	}{
		{
			"MD5Sum",
			func(d []byte) string {
				s := md5.Sum(d)
				return hex.EncodeToString(s[:])
			},
		},
		{
			"SHA1",
			func(d []byte) string {
				s := sha1.Sum(d)
				return hex.EncodeToString(s[:])
			},
		},
		{
			"SHA256",
			func(d []byte) string {
				s := sha256.Sum256(d)
				return hex.EncodeToString(s[:])
			},
		},
	} {
		b.WriteString(sum.name + ":\n")

		for _, f := range files {
			fmt.Fprintf(&b, " %s %d %s\n", sum.f(f.Data), len(f.Data), f.Path)
		}
	}

	return []byte(b.String())
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}

	slices.Sort(out)

	return out
}
