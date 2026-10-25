package scan

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func tarBytes(t *testing.T, files map[string][]byte) []byte {
	t.Helper()

	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func gzipped(t *testing.T, b []byte) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func gemBytes(t *testing.T, name, version, platform string, data map[string][]byte) []byte {
	t.Helper()

	meta := "--- !ruby/object:Gem::Specification\nname: " + name + "\nversion: !ruby/object:Gem::Version\n  version: " +
		version + "\nplatform: " + platform + "\nsummary: demo\n"

	return tarBytes(t, map[string][]byte{
		"metadata.gz": gzipped(t, []byte(meta)),
		"data.tar.gz": gzipped(t, tarBytes(t, data)),
	})
}

func apkBytes(t *testing.T) []byte {
	t.Helper()

	info := "pkgname = zlib\npkgver = 1.2.11-r3\narch = x86_64\norigin = zlib\n"

	return append(
		gzipped(t, tarBytes(t, map[string][]byte{
			".PKGINFO": []byte(info),
		})),
		gzipped(t, tarBytes(t, map[string][]byte{
			"lib/libz.so.1": []byte("elf"),
		}))...,
	)
}

func TestWithFilePackage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		fileName string
		body     []byte
		want     string
		wantType string
	}{
		{
			name:     "a gem",
			fileName: "rack-2.2.3.gem",
			body:     gemBytes(t, "rack", "2.2.3", "ruby", nil),
			want:     "pkg:gem/rack@2.2.3",
			wantType: "gem",
		},
		{
			name:     "a platform gem",
			fileName: "nokogiri-1.13.0-java.gem",
			body:     gemBytes(t, "nokogiri", "1.13.0", "java", nil),
			want:     "pkg:gem/nokogiri@1.13.0?platform=java",
			wantType: "gem",
		},
		{
			name:     "an Alpine package",
			fileName: "zlib-1.2.11-r3.apk",
			body:     apkBytes(t),
			want:     "pkg:apk/alpine/zlib@1.2.11-r3?arch=x86_64&upstream=zlib",
			wantType: "apk",
		},
		{
			name:     "a file that is no gem",
			fileName: "broken.gem",
			body:     []byte("not a tar"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			file := filepath.Join(t.TempDir(), tt.fileName)
			if err := os.WriteFile(file, tt.body, 0o600); err != nil {
				t.Fatal(err)
			}

			sbom := withFilePackage([]byte(`{"artifacts":[],"schema":{"version":"16.1.10"}}`), tt.fileName, file)

			var doc struct {
				Artifacts []syftPackage `json:"artifacts"`
			}
			if err := json.Unmarshal(sbom, &doc); err != nil {
				t.Fatal(err)
			}

			if tt.want == "" {
				if len(doc.Artifacts) != 0 {
					t.Errorf("packages = %+v, want none", doc.Artifacts)
				}

				return
			}

			if len(doc.Artifacts) != 1 || doc.Artifacts[0].PURL != tt.want || doc.Artifacts[0].Type != tt.wantType {
				t.Errorf("packages = %+v, want %s of type %s", doc.Artifacts, tt.want, tt.wantType)
			}
		})
	}
}

func TestOSPackageDistro(t *testing.T) {
	t.Parallel()

	sbomOf := func(typ, version string) []byte {
		return []byte(`{"artifacts":[{"name":"p","version":"` + version + `","type":"` + typ + `"}],"distro":{}}`)
	}

	tests := []struct {
		name          string
		fileName      string
		sbom          []byte
		folders       []string
		want          distro
		wantUnmatched bool
	}{
		{
			name:     "a Red Hat Enterprise Linux 8 package",
			fileName: "zlib-1.2.11-17.el8.x86_64.rpm",
			sbom:     sbomOf("rpm", "0:1.2.11-17.el8"),
			want: distro{
				ID:        "rhel",
				VersionID: "8",
			},
		},
		{
			name:     "an update of a Red Hat Enterprise Linux 8 package",
			fileName: "zlib-1.2.11-18.el8_5.x86_64.rpm",
			sbom:     sbomOf("rpm", "0:1.2.11-18.el8_5"),
			want: distro{
				ID:        "rhel",
				VersionID: "8",
			},
		},
		{
			name:     "a Fedora package",
			fileName: "zlib-1.2.13-3.fc39.x86_64.rpm",
			sbom:     sbomOf("rpm", "1.2.13-3.fc39"),
			want: distro{
				ID:        "fedora",
				VersionID: "39",
			},
		},
		{
			name:     "an Amazon Linux package",
			fileName: "zlib-1.2.11-33.amzn2.0.5.x86_64.rpm",
			sbom:     sbomOf("rpm", "1.2.11-33.amzn2.0.5"),
			want: distro{
				ID:        "amazonlinux",
				VersionID: "2",
			},
		},
		{
			name:     "a Debian security update",
			fileName: "zlib1g_1.2.11.dfsg-2+deb11u2_amd64.deb",
			sbom:     sbomOf("deb", "1:1.2.11.dfsg-2+deb11u2"),
			want: distro{
				ID:        "debian",
				VersionID: "11",
			},
		},
		{
			name:     "an Ubuntu security update",
			fileName: "zlib1g_1.2.11.dfsg-2ubuntu9.2_amd64.deb",
			sbom:     sbomOf("deb", "1:1.2.11.dfsg-2ubuntu0.22.04.1"),
			want: distro{
				ID:        "ubuntu",
				VersionID: "22.04",
			},
		},
		{
			name:          "a Debian package that names no release",
			fileName:      "liblz4-1_1.9.3-2_amd64.deb",
			sbom:          sbomOf("deb", "1.9.3-2"),
			wantUnmatched: true,
		},
		{
			name:     "an Alpine package in a branch folder",
			fileName: "zlib-1.2.11-r3.apk",
			sbom:     sbomOf("apk", "1.2.11-r3"),
			folders: []string{
				"alpine/v3.12/main/x86_64",
			},
			want: distro{
				ID:        "alpine",
				VersionID: "3.12",
			},
		},
		{
			name:     "an Alpine package outside a branch folder",
			fileName: "zlib-1.2.11-r3.apk",
			sbom:     sbomOf("apk", "1.2.11-r3"),
			folders: []string{
				"uploads",
			},
			wantUnmatched: true,
		},
		{
			name:     "a package that is no OS package",
			fileName: "lib.jar",
			sbom:     sbomOf("java-archive", "1.0"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, unmatched := osPackageDistro(tt.sbom, tt.fileName, tt.folders)
			if (unmatched != "") != tt.wantUnmatched {
				t.Errorf("unmatched = %q, want it %v", unmatched, tt.wantUnmatched)
			}

			var doc struct {
				Distro distro `json:"distro"`
			}
			if err := json.Unmarshal(got, &doc); err != nil {
				t.Fatal(err)
			}

			if doc.Distro != tt.want {
				t.Errorf("distro = %+v, want %+v", doc.Distro, tt.want)
			}
		})
	}
}
