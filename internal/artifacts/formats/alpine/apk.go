// Package alpine implements Alpine Linux (apk) repositories: packages laid
// out as <repo>/<vX.Y>/<branch>/<arch>/<name>-<version>.apk, and a signed
// APKINDEX.tar.gz generated for each such directory (contract
// cargo-and-os-packages.md §2).
package alpine

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // apk's package checksum and index signature are SHA-1 by format
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"time"
)

// ErrInvalid is returned for files that are not apk packages.
var ErrInvalid = errors.New("invalid apk package")

const (
	maxControl = 1 << 20
	maxMember  = 16 << 20
)

// Package is what an index stanza says about a package.
type Package struct {
	Checksum                                       string
	Name                                           string
	Version                                        string
	Arch                                           string
	Size                                           int64
	Installed                                      int64
	Desc, URL, License, Origin, Maintainer, Commit string
	BuildDate                                      string
	Depends, Provides, InstallIf                   []string
	ProviderPriority                               string
}

// countingReader counts and optionally hashes the bytes it hands out. It is
// a flate.Reader (it has ReadByte), so gzip reads exactly one member's
// bytes from it and member boundaries are exact.
type countingReader struct {
	r    *bufio.Reader
	n    int64
	hash hash.Hash
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)

	c.n += int64(n)
	if c.hash != nil {
		c.hash.Write(p[:n])
	}

	return n, err
}

func (c *countingReader) ReadByte() (byte, error) {
	b, err := c.r.ReadByte()
	if err == nil {
		c.n++
		if c.hash != nil {
			c.hash.Write([]byte{
				b,
			})
		}
	}

	return b, err
}

// Parse reads an .apk: an optional signature segment, the control segment
// (.PKGINFO), then the data.
func Parse(r io.Reader) (Package, error) {
	cr := &countingReader{
		r: bufio.NewReader(r),
	}

	var (
		pkg     Package
		control []byte
	)
	for segment := 0; control == nil; segment++ {
		if segment == 2 {
			return Package{}, fmt.Errorf("%w: no .PKGINFO", ErrInvalid)
		}

		h := sha1.New() //nolint:gosec // apk format
		cr.hash = h
		files, err := readMember(cr)
		cr.hash = nil

		if err != nil {
			return Package{}, fmt.Errorf("%w: %w", ErrInvalid, err)
		}

		if info, ok := files[".PKGINFO"]; ok {
			control = info
			pkg.Checksum = "Q1" + base64.StdEncoding.EncodeToString(h.Sum(nil))
		}
	}

	if _, err := io.Copy(io.Discard, cr); err != nil {
		return Package{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	pkg.Size = cr.n
	parseInfo(&pkg, control)

	if pkg.Name == "" || pkg.Version == "" {
		return Package{}, fmt.Errorf("%w: .PKGINFO has no pkgname or pkgver", ErrInvalid)
	}

	return pkg, nil
}

func readMember(cr *countingReader) (map[string][]byte, error) {
	zr, err := gzip.NewReader(cr)
	if err != nil {
		return nil, err
	}

	zr.Multistream(false)

	files := map[string][]byte{}
	kept := 0

	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}

		if err != nil {
			return nil, err
		}

		if strings.HasPrefix(hdr.Name, ".") && hdr.Typeflag == tar.TypeReg {
			b, err := io.ReadAll(io.LimitReader(tr, maxControl))
			if err != nil {
				return nil, err
			}

			kept += len(b)
			if kept > maxMember {
				return nil, errors.New("package control files too large")
			}

			files[hdr.Name] = b
		}
	}

	n, err := io.Copy(io.Discard, io.LimitReader(zr, maxMember+1))
	if err != nil {
		return nil, err
	}

	if n > maxMember {
		return nil, errors.New("package segment too large")
	}

	return files, nil
}

func parseInfo(p *Package, info []byte) {
	for line := range strings.SplitSeq(string(info), "\n") {
		k, v, ok := strings.Cut(line, " = ")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}

		switch strings.TrimSpace(k) {
		case "pkgname":
			p.Name = v
		case "pkgver":
			p.Version = v
		case "arch":
			p.Arch = v
		case "size":
			_, _ = fmt.Sscan(v, &p.Installed)
		case "pkgdesc":
			p.Desc = v
		case "url":
			p.URL = v
		case "license":
			p.License = v
		case "origin":
			p.Origin = v
		case "maintainer":
			p.Maintainer = v
		case "commit":
			p.Commit = v
		case "builddate":
			p.BuildDate = v
		case "depend":
			p.Depends = append(p.Depends, v)
		case "provides":
			p.Provides = append(p.Provides, v)
		case "install_if":
			p.InstallIf = append(p.InstallIf, strings.Fields(v)...)
		case "provider_priority":
			p.ProviderPriority = v
		}
	}
}

// Stanza renders the package's APKINDEX record (without the blank line).
func (p Package) Stanza() string {
	var b strings.Builder

	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s:%s\n", k, v)
		}
	}
	line("C", p.Checksum)
	line("P", p.Name)
	line("V", p.Version)
	line("A", p.Arch)
	line("S", fmt.Sprint(p.Size))
	line("I", fmt.Sprint(p.Installed))
	line("T", p.Desc)
	line("U", p.URL)
	line("L", p.License)
	line("o", p.Origin)
	line("m", p.Maintainer)
	line("t", p.BuildDate)
	line("c", p.Commit)
	line("k", p.ProviderPriority)
	line("D", strings.Join(p.Depends, " "))
	line("p", strings.Join(p.Provides, " "))
	line("i", strings.Join(p.InstallIf, " "))

	return b.String()
}

// Signer is an RSA key pair that signs indexes, and its name.
type Signer struct {
	Key  *rsa.PrivateKey
	Name string
}

// Index builds a signed APKINDEX.tar.gz from stanzas: a signature segment
// (.SIGN.RSA.<name>.rsa.pub for each signer, an RSA PKCS#1 v1.5 signature
// over the SHA-1 of the next gzip member) followed by the index segment.
func Index(stanzas []string, description string, signers []Signer, now time.Time) ([]byte, error) {
	var body bytes.Buffer
	for _, s := range stanzas {
		body.WriteString(strings.TrimRight(s, "\n"))
		body.WriteString("\n\n")
	}

	index, err := gzipTar([]tarFile{
		{
			"DESCRIPTION",
			[]byte(description),
		},
		{
			"APKINDEX",
			body.Bytes(),
		},
	}, now, true)
	if err != nil {
		return nil, err
	}

	sum := sha1.Sum(index) //nolint:gosec // apk format

	signatures := make([]tarFile, 0, len(signers))

	for _, signer := range signers {
		sig, err := rsa.SignPKCS1v15(rand.Reader, signer.Key, crypto.SHA1, sum[:])
		if err != nil {
			return nil, fmt.Errorf("sign index with %s: %w", signer.Name, err)
		}

		signatures = append(signatures, tarFile{
			".SIGN.RSA." + signer.Name + ".rsa.pub",
			sig,
		})
	}

	signature, err := gzipTar(signatures, now, false)
	if err != nil {
		return nil, err
	}

	return append(signature, index...), nil
}

type tarFile struct {
	name string
	body []byte
}

func gzipTar(files []tarFile, now time.Time, terminate bool) ([]byte, error) {
	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)

	tw := tar.NewWriter(zw)
	for _, f := range files {
		if err := tw.WriteHeader(
			&tar.Header{
				Name:     f.name,
				Mode:     0o644,
				Size:     int64(len(f.body)),
				ModTime:  now,
				Typeflag: tar.TypeReg,
				Format:   tar.FormatUSTAR,
			},
		); err != nil {
			return nil, err
		}

		if _, err := tw.Write(f.body); err != nil {
			return nil, err
		}
	}

	var err error
	if terminate {
		err = tw.Close()
	} else {
		err = tw.Flush()
	}

	if err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// Stanzas reads the records of an APKINDEX.tar.gz (for merging remote
// indexes into a virtual repository's).
func Stanzas(r io.Reader) ([]string, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("read index: %w", err)
	}

	tr := tar.NewReader(io.LimitReader(zr, 256<<20))
	for {
		hdr, err := tr.Next()
		if err != nil {
			return nil, fmt.Errorf("read index: no APKINDEX: %w", err)
		}

		if hdr.Name != "APKINDEX" {
			continue
		}

		body, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read index: %w", err)
		}

		var out []string

		for s := range strings.SplitSeq(string(body), "\n\n") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s+"\n")
			}
		}

		return out, nil
	}
}
