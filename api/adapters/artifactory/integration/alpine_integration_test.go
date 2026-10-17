//go:build integration

package integration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // apk format
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"relic/internal/artifacts/formats/alpine"
)

func apkPackage(t *testing.T, name, version string) []byte {
	t.Helper()
	return apkPackageFor(t, name, version, "x86_64")
}

func apkPackageFor(t *testing.T, name, version, arch string) []byte {
	t.Helper()

	segment := func(files map[string]string, terminate bool) []byte {
		var buf bytes.Buffer

		zw := gzip.NewWriter(&buf)

		tw := tar.NewWriter(zw)
		for n, body := range files {
			_ = tw.WriteHeader(
				&tar.Header{
					Name:     n,
					Mode:     0o644,
					Size:     int64(len(body)),
					Typeflag: tar.TypeReg,
					Format:   tar.FormatUSTAR,
				},
			)
			_, _ = tw.Write([]byte(body))
		}

		if terminate {
			_ = tw.Close()
		} else {
			_ = tw.Flush()
		}

		_ = zw.Close()

		return buf.Bytes()
	}
	info := "pkgname = " + name + "\npkgver = " + version + "\narch = " + arch + "\nsize = 10\npkgdesc = test\n"

	return append(
		segment(map[string]string{
			".PKGINFO": info,
		}, false),
		segment(map[string]string{
			"usr/bin/" + name: "x",
		}, true)...)
}

func TestAlpineRepository(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/alpine-local",
			body:   `{"rclass":"local","packageType":"alpine"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/alpine",
			body:   `{"rclass":"virtual","packageType":"alpine","repositories":["alpine-local"],"defaultDeploymentRepo":"alpine-local"}`,
		},
	)

	dir := "v3.21/main/x86_64"
	for _, p := range [][2]string{
		{
			"hello",
			"1.0-r0",
		},
		{
			"world",
			"2.1-r3",
		},
	} {
		if resp, body := do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/alpine-local/" + dir + "/" + p[0] + "-" + p[1] + ".apk",
				body:   string(apkPackage(t, p[0], p[1])),
			},
		); resp.StatusCode != 201 {
			t.Fatalf("deploy = %d %s", resp.StatusCode, body)
		}
	}

	var cfg struct {
		PrimaryKeyPairRef string
	}

	_, body := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/repositories/alpine-local",
	})
	if json.Unmarshal([]byte(body), &cfg) != nil || cfg.PrimaryKeyPairRef != "relic-rsa" {
		t.Errorf("repository config = %s", body)
	}

	resp, pubPEM := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/security/keypair/public/repositories/alpine-local",
		},
	)

	block, _ := pem.Decode([]byte(pubPEM))
	if resp.StatusCode != 200 || block == nil || block.Type != "PUBLIC KEY" {
		t.Fatalf("public key = %d %q", resp.StatusCode, pubPEM)
	}

	pubAny, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}

	pub := pubAny.(*rsa.PublicKey)

	for _, repoKey := range []string{
		"alpine-local",
		"alpine",
	} {
		resp, index := do(t, srv, call{
			method: "GET",
			path:   "/artifactory/" + repoKey + "/" + dir + "/APKINDEX.tar.gz",
		})
		if resp.StatusCode != 200 {
			t.Fatalf("%s index = %d %s", repoKey, resp.StatusCode, index)
		}

		stanzas, err := alpine.Stanzas(strings.NewReader(index))
		if err != nil || len(stanzas) != 2 || !strings.Contains(strings.Join(stanzas, ""), "P:world\nV:2.1-r3\n") {
			t.Errorf("%s stanzas = %q %v", repoKey, stanzas, err)
		}

		verifyAPKIndex(t, []byte(index), pub)

		time.Sleep(1100 * time.Millisecond)

		if _, again := do(t, srv, call{
			method: "GET",
			path:   "/artifactory/" + repoKey + "/" + dir + "/APKINDEX.tar.gz",
		}); again != index {
			t.Errorf("%s: the index changed a second later, with no package changed", repoKey)
		}

		if head, _ := do(t, srv, call{
			method: "HEAD",
			path:   "/artifactory/" + repoKey + "/" + dir + "/APKINDEX.tar.gz",
		}); head.StatusCode != 200 || head.Header.Get("Content-Length") != strconv.Itoa(len(index)) {
			t.Errorf(
				"%s HEAD index = %d length %q, want %d",
				repoKey,
				head.StatusCode,
				head.Header.Get("Content-Length"),
				len(index),
			)
		}
	}

	if resp, body := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/alpine-local/" + dir + "/scripts-1.0-r0.apk",
			body:   string(apkPackageFor(t, "scripts", "1.0-r0", "noarch")),
		},
	); resp.StatusCode != 201 {
		t.Fatalf("deploy noarch = %d %s", resp.StatusCode, body)
	}

	for _, repoKey := range []string{
		"alpine-local",
		"alpine",
	} {
		_, index := do(t, srv, call{
			method: "GET",
			path:   "/artifactory/" + repoKey + "/" + dir + "/APKINDEX.tar.gz",
		})
		stanzas, err := alpine.Stanzas(strings.NewReader(index))

		joined := strings.Join(stanzas, "\n")
		if err != nil || !strings.Contains(joined, "P:scripts\nV:1.0-r0\nA:x86_64\n") ||
			strings.Contains(joined, "A:noarch") {
			t.Errorf("%s noarch stanza = %q %v", repoKey, stanzas, err)
		}
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/alpine-local/v3.99/main/x86_64/APKINDEX.tar.gz",
		},
	); resp.StatusCode != 404 {
		t.Errorf("index of a missing folder = %d, want 404", resp.StatusCode)
	}
}

func verifyAPKIndex(t *testing.T, index []byte, pub *rsa.PublicKey) {
	t.Helper()

	r := bytes.NewReader(index)

	zr, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}

	zr.Multistream(false)
	tr := tar.NewReader(zr)

	hdr, err := tr.Next()
	if err != nil || !strings.HasPrefix(hdr.Name, ".SIGN.RSA.relic-rsa") {
		t.Fatalf("signature entry = %v %v", hdr, err)
	}

	sig, _ := io.ReadAll(tr)
	_, _ = io.Copy(io.Discard, io.LimitReader(zr, 64<<20))

	sum := sha1.Sum(index[len(index)-r.Len():]) //nolint:gosec // apk format
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA1, sum[:], sig); err != nil {
		t.Errorf("index signature: %v", err)
	}
}
