// Package install_test runs web/layers/site/public/install.sh against a fake GitHub release server.
package install_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const script = "../../web/layers/site/public/install.sh"

func archive(t *testing.T, name, version string) []byte {
	t.Helper()

	var b bytes.Buffer

	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	body := []byte("#!/bin/sh\necho relic version " + version + "\n")

	if err := tw.WriteHeader(&tar.Header{
		Name: name + "/relic",
		Mode: 0o755,
		Size: int64(len(body)),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	return b.Bytes()
}

func releaseServer(t *testing.T, latest string, corrupt, prereleaseOnly bool) *httptest.Server {
	t.Helper()

	files := map[string][]byte{}

	for _, version := range []string{
		"v1.0.0",
		latest,
	} {
		if version == "" {
			continue
		}

		name := fmt.Sprintf("relic_%s_%s_%s", version, runtime.GOOS, runtime.GOARCH)
		tgz := archive(t, name, version)
		sum := sha256.Sum256(tgz)

		if corrupt {
			sum[0] ^= 0xff
		}

		dir := "/releases/download/" + version + "/"
		files[dir+name+".tar.gz"] = tgz
		files[dir+"relic_"+version+"_checksums.txt"] = fmt.Appendf(nil, "%x  %s.tar.gz\n", sum, name)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/releases/latest" {
			if prereleaseOnly {
				http.Redirect(w, r, "/releases", http.StatusFound)

				return
			}

			http.Redirect(w, r, "/releases/tag/"+latest, http.StatusFound)

			return
		}

		if r.URL.Path == "/releases" {
			if latest == "" {
				w.WriteHeader(http.StatusOK)

				return
			}

			_, _ = fmt.Fprintf(w, `<a href="/releases/tag/%s">%s</a>`, latest, latest)

			return
		}

		if tag, ok := strings.CutPrefix(r.URL.Path, "/releases/tag/"); ok {
			if tag != "v1.0.0" && tag != latest {
				http.NotFound(w, r)
			}

			return
		}

		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)

			return
		}

		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)

	return srv
}

func pathWithoutChecksumTools(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	for _, tool := range []string{
		"uname",
		"curl",
		"mktemp",
		"rm",
		"awk",
		"tar",
		"gzip",
		"mkdir",
		"install",
	} {
		found, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s is not installed", tool)
		}

		if err := os.Symlink(found, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

func TestInstallScript(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("install.sh is for Linux and macOS")
	}

	tests := []struct {
		name           string
		version        string
		corrupt        bool
		noChecksumTool bool
		prereleaseOnly bool
		noReleases     bool
		readOnlyDir    bool
		want           string
		failure        string
	}{
		{
			name: "latest release",
			want: "relic version v1.2.3",
		},
		{
			name:           "prerelease fallback",
			prereleaseOnly: true,
			want:           "relic version v1.2.3-beta.1",
		},
		{
			name:    "pinned release",
			version: "v1.0.0",
			want:    "relic version v1.0.0",
		},
		{
			name:    "pinned release without v",
			version: "1.0.0",
			want:    "relic version v1.0.0",
		},
		{
			name:    "unknown release",
			version: "v9.9.9",
			failure: "no release v9.9.9 at",
		},
		{
			name:        "a directory relic cannot write to",
			readOnlyDir: true,
			failure:     "cannot write to",
		},
		{
			name:           "no release found",
			prereleaseOnly: true,
			noReleases:     true,
			failure:        "no release found at",
		},
		{
			name:    "checksum mismatch",
			corrupt: true,
			failure: "checksum mismatch",
		},
		{
			name:           "no checksum tool",
			noChecksumTool: true,
			failure:        "sha256sum or shasum is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			latest := "v1.2.3"
			if tt.prereleaseOnly {
				latest = "v1.2.3-beta.1"
			}

			if tt.noReleases {
				latest = ""
			}

			srv := releaseServer(t, latest, tt.corrupt, tt.prereleaseOnly)
			dir := filepath.Join(t.TempDir(), "bin")

			if tt.readOnlyDir {
				if os.Geteuid() == 0 {
					t.Skip("root writes into a read-only directory")
				}

				if err := os.Mkdir(dir, 0o500); err != nil {
					t.Fatal(err)
				}
			}

			cmd := exec.CommandContext(t.Context(), "sh", script)

			cmd.Env = append(
				os.Environ(),
				"RELIC_REPOSITORY="+srv.URL,
				"RELIC_INSTALL_DIR="+dir,
				"RELIC_VERSION="+tt.version,
			)

			if tt.noChecksumTool {
				cmd.Env = append(cmd.Env, "PATH="+pathWithoutChecksumTools(t))
			}

			out, err := cmd.CombinedOutput()

			if tt.failure != "" {
				if err == nil || !strings.Contains(string(out), tt.failure) {
					t.Fatalf("install succeeded or failed for another reason than %q: %v\n%s", tt.failure, err, out)
				}

				if tt.readOnlyDir && strings.Contains(string(out), "Downloading") {
					t.Errorf("install downloaded before it checked %s:\n%s", dir, out)
				}

				if _, err := os.Stat(filepath.Join(dir, "relic")); err == nil {
					t.Fatalf("relic installed despite %q", tt.failure)
				}

				return
			}

			if err != nil {
				t.Fatalf("install: %v\n%s", err, out)
			}

			got, err := exec.CommandContext(t.Context(), filepath.Join(dir, "relic")).Output()
			if err != nil || strings.TrimSpace(string(got)) != tt.want {
				t.Fatalf("installed relic = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
