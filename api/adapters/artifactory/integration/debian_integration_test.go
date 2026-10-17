//go:build integration

package integration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
)

func debPackage(t *testing.T, name, version, arch string) []byte {
	t.Helper()

	tgz := func(file, body string) []byte {
		var buf bytes.Buffer

		zw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(zw)
		_ = tw.WriteHeader(&tar.Header{
			Name:     file,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		})
		_, _ = tw.Write([]byte(body))
		_ = tw.Close()
		_ = zw.Close()

		return buf.Bytes()
	}
	control := fmt.Sprintf(
		"Package: %s\nVersion: %s\nArchitecture: %s\nMaintainer: Test <t@example.org>\nDescription: test\n",
		name,
		version,
		arch,
	)

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
			"control.tar.gz",
			tgz("./control", control),
		},
		{
			"data.tar.gz",
			tgz("./usr/share/doc/"+name+"/x", "x"),
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

func TestDebianRepository(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/deb-local",
			body:   `{"rclass":"local","packageType":"debian"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/deb",
			body:   `{"rclass":"virtual","packageType":"debian","repositories":["deb-local"],"defaultDeploymentRepo":"deb-local"}`,
		},
	)

	for _, p := range []struct {
		file, name, arch string
	}{
		{
			"pool/h/hello_1.0_amd64.deb",
			"hello",
			"amd64",
		},
		{
			"pool/d/docs_1.0_all.deb",
			"docs",
			"all",
		},
	} {
		path := "/artifactory/deb-local/" + p.file + ";deb.distribution=bookworm;deb.component=main;deb.architecture=" + p.arch
		if resp, body := do(
			t,
			srv,
			call{
				method: "PUT",
				path:   path,
				body:   string(debPackage(t, p.name, "1.0", p.arch)),
			},
		); resp.StatusCode != 201 {
			t.Fatalf("deploy %s = %d %s", p.file, resp.StatusCode, body)
		}
	}

	_, pub := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/security/keypair/public/repositories/deb-local",
	})

	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(pub))
	if err != nil {
		t.Fatalf("public key: %v %q", err, pub)
	}

	for _, repoKey := range []string{
		"deb-local",
		"deb",
	} {
		base := "/artifactory/" + repoKey + "/dists/bookworm/"
		resp, inRelease := do(t, srv, call{
			method: "GET",
			path:   base + "InRelease",
		})

		block, _ := clearsign.Decode([]byte(inRelease))
		if resp.StatusCode != 200 || block == nil {
			t.Fatalf("%s InRelease = %d %q", repoKey, resp.StatusCode, inRelease)
		}

		if _, err := block.VerifySignature(ring, nil); err != nil {
			t.Errorf("%s InRelease signature: %v", repoKey, err)
		}

		release := string(block.Plaintext)

		_, plainRelease := do(t, srv, call{
			method: "GET",
			path:   base + "Release",
		})

		time.Sleep(1100 * time.Millisecond)

		_, detached := do(t, srv, call{
			method: "GET",
			path:   base + "Release.gpg",
		})
		if _, err := openpgp.CheckArmoredDetachedSignature(
			ring,
			strings.NewReader(plainRelease),
			strings.NewReader(detached),
			nil,
		); err != nil {
			t.Errorf("%s Release.gpg fetched a second after Release does not verify it: %v", repoKey, err)
		}

		_, packages := do(t, srv, call{
			method: "GET",
			path:   base + "main/binary-amd64/Packages",
		})

		sum := sha256.Sum256([]byte(packages))
		if !strings.Contains(
			release,
			fmt.Sprintf(" %s %d main/binary-amd64/Packages\n", hex.EncodeToString(sum[:]), len(packages)),
		) {
			t.Errorf("%s Release does not list the served Packages:\n%s", repoKey, release)
		}

		for _, want := range []string{
			"Package: hello\n",
			"Package: docs\n",
			"Filename: pool/h/hello_1.0_amd64.deb\n",
			"SHA256: ",
		} {
			if !strings.Contains(packages, want) {
				t.Errorf("%s Packages lacks %q:\n%s", repoKey, want, packages)
			}
		}

		resp, compressed := do(
			t,
			srv,
			call{
				method: "GET",
				path:   base + "main/binary-amd64/Packages.gz",
			},
		)
		if resp.StatusCode != 200 {
			t.Errorf("%s Packages.gz = %d", repoKey, resp.StatusCode)
			continue
		}

		gz, err := gzip.NewReader(strings.NewReader(compressed))
		if err != nil {
			t.Fatalf("%s Packages.gz: %v", repoKey, err)
		}

		decompressed, err := io.ReadAll(gz)
		if err != nil {
			t.Fatalf("%s Packages.gz: %v", repoKey, err)
		}

		if string(decompressed) != packages {
			t.Errorf("%s Packages.gz holds:\n%s\nwant the served Packages:\n%s", repoKey, decompressed, packages)
		}
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/deb-local/dists/trixie/InRelease",
		},
	); resp.StatusCode != 404 {
		t.Errorf("unknown distribution = %d, want 404", resp.StatusCode)
	}

	if resp, body := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/gpg/key/public",
	}); resp.StatusCode != 200 ||
		!strings.Contains(body, "BEGIN PGP PUBLIC KEY BLOCK") {
		t.Errorf("default GPG key = %d", resp.StatusCode)
	}
}
