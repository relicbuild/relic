package debian

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"relic/internal/artifacts/signingkey"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/klauspost/compress/zstd"
)

const control = "Package: hello\nVersion: 2.10-3\nArchitecture: amd64\nMaintainer: Someone <s@example.org>\nDescription: says hello\n friendly\n"

func tarOf(name, body string) []byte {
	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
	})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()

	return buf.Bytes()
}

func tgzOf(name, body string) []byte {
	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(tarOf(name, body))
	_ = zw.Close()

	return buf.Bytes()
}

func tzstOf(t *testing.T, name, body string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = zw.Write(tarOf(name, body))
	_ = zw.Close()

	return buf.Bytes()
}

func debOf(controlMember string, controlTar []byte) []byte {
	var ar bytes.Buffer
	ar.WriteString("!<arch>\n")

	for _, m := range []struct {
		name string
		body []byte
	}{
		{
			"debian-binary",
			[]byte("2.0\n"),
		},
		{
			controlMember,
			controlTar,
		},
		{
			"data.tar.gz",
			tgzOf("./usr/bin/hello", "x"),
		},
	} {
		fmt.Fprintf(&ar, "%-16s%-12s%-6s%-6s%-8s%-10d`\n", m.name, "0", "0", "0", "100644", len(m.body))
		ar.Write(m.body)

		if len(m.body)%2 == 1 {
			ar.WriteByte('\n')
		}
	}

	return ar.Bytes()
}

func TestControl(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		deb  []byte
	}{
		{
			name: "gzip",
			deb:  debOf("control.tar.gz", tgzOf("./control", control)),
		},
		{
			name: "zstd",
			deb:  debOf("control.tar.zst", tzstOf(t, "./control", control)),
		},
	} {
		got, err := Control(bytes.NewReader(tt.deb))
		if err != nil || got != control {
			t.Fatalf("%s: Control = %q %v", tt.name, got, err)
		}
	}

	got, err := Control(bytes.NewReader(debOf("control.tar.gz", tgzOf("./control", control))))
	if err != nil {
		t.Fatal(err)
	}

	if Field(got, "package") != "hello" || Field(got, "Version") != "2.10-3" {
		t.Errorf("fields of %q", got)
	}

	if _, err := Control(strings.NewReader("nope")); !errors.Is(err, ErrInvalid) {
		t.Errorf("garbage = %v", err)
	}

	oversized := debOf("control.tar.gz", tgzOf("./control", control+strings.Repeat("x", maxControl)))
	if _, err := Control(bytes.NewReader(oversized)); !errors.Is(err, ErrInvalid) {
		t.Errorf("control file above the cap = %v", err)
	}
}

func TestControlClosesTheDecompressor(t *testing.T) {
	deb := debOf("control.tar.zst", tzstOf(t, "./control", control+strings.Repeat("x", 4<<20)))
	before := runtime.NumGoroutine()

	for range 20 {
		_, _ = Control(bytes.NewReader(deb))
	}

	if grown := runtime.NumGoroutine() - before; grown >= 20 {
		t.Errorf("Control left %d goroutines running", grown)
	}
}

func TestReadCapped(t *testing.T) {
	t.Parallel()

	if got, err := readCapped(strings.NewReader("abcd"), 4); err != nil || string(got) != "abcd" {
		t.Errorf("at the limit = %q %v", got, err)
	}

	if _, err := readCapped(strings.NewReader("abcde"), 4); !errors.Is(err, errTooLarge) {
		t.Errorf("above the limit = %v", err)
	}
}

func TestPackagesAndRelease(t *testing.T) {
	t.Parallel()

	p := File{
		Control:  control,
		Filename: "pool/h/hello_2.10-3_amd64.deb",
		Size:     42,
		MD5:      "m",
		SHA1:     "s1",
		SHA256:   "s2",
	}.Paragraph()

	data := Packages([]string{
		p,
		p,
	})
	if got := Paragraphs(data); len(got) != 2 || got[0] != p || got[1] != p {
		t.Fatalf("Paragraphs = %q", got)
	}

	rel := string(
		Release(
			"relic",
			"bookworm",
			[]string{
				"main",
			},
			[]string{
				"amd64",
			},
			[]IndexFile{
				{
					"main/binary-amd64/Packages",
					data,
				},
			},
			time.Unix(0, 0),
		),
	)
	for _, want := range []string{
		"Suite: bookworm\n",
		"Components: main\n",
		"Architectures: amd64\n",
		"SHA256:\n ",
		fmt.Sprintf(" %d main/binary-amd64/Packages\n", len(data)),
	} {
		if !strings.Contains(rel, want) {
			t.Errorf("Release lacks %q:\n%s", want, rel)
		}
	}
}

func TestSignatures(t *testing.T) {
	t.Parallel()

	key, err := openpgp.NewEntity("t", "", "", &packet.Config{
		Algorithm: packet.PubKeyAlgoRSA,
		RSABits:   2048,
	})
	if err != nil {
		t.Fatal(err)
	}

	release := []byte("Suite: bookworm\n")

	in, err := signingkey.ClearSign(release, key)
	if err != nil {
		t.Fatal(err)
	}

	block, _ := clearsign.Decode(in)
	if block == nil || !bytes.Equal(block.Plaintext, release) {
		t.Fatalf("InRelease = %s", in)
	}

	if _, err := block.VerifySignature(openpgp.EntityList{
		key,
	}, nil); err != nil {
		t.Errorf("InRelease signature: %v", err)
	}

	detached, err := signingkey.DetachSign(release, key)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := openpgp.CheckArmoredDetachedSignature(
		openpgp.EntityList{
			key,
		},
		bytes.NewReader(release),
		bytes.NewReader(detached),
		nil,
	); err != nil {
		t.Errorf("Release.gpg: %v", err)
	}
}
