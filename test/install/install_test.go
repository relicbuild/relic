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

type fakeRelease struct {
	latest         string
	corrupt        bool
	corruptIndexer bool
	prereleaseOnly bool
	withoutIndexer bool
}

func indexerBuildNames() []string {
	return []string{
		"relic-indexer-" + runtime.GOOS + "-" + runtime.GOARCH,
		"relic-indexer-linux-arm64",
		"relic-indexer-windows-amd64.exe",
	}
}

func (release fakeRelease) server(t *testing.T) *httptest.Server {
	t.Helper()

	files := map[string][]byte{}
	latest, corrupt, prereleaseOnly := release.latest, release.corrupt, release.prereleaseOnly

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
		sums := fmt.Appendf(nil, "%x  %s.tar.gz\n", sum, name)

		if !release.withoutIndexer {
			for _, build := range indexerBuildNames() {
				body := []byte("indexer " + build)
				buildSum := sha256.Sum256(body)

				if release.corruptIndexer {
					buildSum[0] ^= 0xff
				}

				files[dir+build] = body
				sums = fmt.Appendf(sums, "%x  %s\n", buildSum, build)
			}
		}

		files[dir+"relic_"+version+"_checksums.txt"] = sums
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
		"dirname",
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

	hostBuild := indexerBuildNames()[0]
	noPlatforms := ""
	otherPlatforms := "linux/arm64 windows/amd64"
	notAPlatform := "linux"

	tests := []struct {
		name               string
		version            string
		corrupt            bool
		corruptIndexer     bool
		withoutIndexer     bool
		noChecksumTool     bool
		prereleaseOnly     bool
		noReleases         bool
		readOnlyDir        bool
		readOnlyIndexerDir bool
		customIndexerDir   bool
		indexerPlatforms   *string
		wantIndexerBuilds  []string
		wantOutput         string
		want               string
		failure            string
	}{
		{
			name: "latest release",
			want: "relic version v1.2.3",
			wantIndexerBuilds: []string{
				hostBuild,
			},
		},
		{
			name:             "indexer builds of other platforms",
			indexerPlatforms: &otherPlatforms,
			want:             "relic version v1.2.3",
			wantIndexerBuilds: []string{
				"relic-indexer-linux-arm64",
				"relic-indexer-windows-amd64.exe",
			},
		},
		{
			name:              "no indexer builds on request",
			indexerPlatforms:  &noPlatforms,
			want:              "relic version v1.2.3",
			wantIndexerBuilds: []string{},
		},
		{
			name:             "indexer builds in another directory",
			customIndexerDir: true,
			want:             "relic version v1.2.3",
			wantIndexerBuilds: []string{
				hostBuild,
			},
		},
		{
			name:              "a release without indexer builds",
			withoutIndexer:    true,
			want:              "relic version v1.2.3",
			wantOutput:        "has no " + hostBuild,
			wantIndexerBuilds: []string{},
		},
		{
			name:             "requested indexer build not in the release",
			withoutIndexer:   true,
			indexerPlatforms: &otherPlatforms,
			failure:          "has no entry for relic-indexer-linux-arm64",
		},
		{
			name:           "indexer checksum mismatch",
			corruptIndexer: true,
			failure:        "checksum mismatch for " + hostBuild,
		},
		{
			name:             "indexer platform without an architecture",
			indexerPlatforms: &notAPlatform,
			failure:          "os/arch",
		},
		{
			name:               "an indexer directory relic cannot write to",
			readOnlyIndexerDir: true,
			failure:            "RELIC_INDEXER_DIR",
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

			srv := fakeRelease{
				latest:         latest,
				corrupt:        tt.corrupt,
				corruptIndexer: tt.corruptIndexer,
				prereleaseOnly: tt.prereleaseOnly,
				withoutIndexer: tt.withoutIndexer,
			}.server(t)
			prefix := t.TempDir()
			dir := filepath.Join(prefix, "bin")
			indexerDir := filepath.Join(prefix, "share", "relic", "indexer")

			if tt.readOnlyIndexerDir {
				if os.Geteuid() == 0 {
					t.Skip("root writes into a read-only directory")
				}

				if err := os.Mkdir(filepath.Join(prefix, "share"), 0o500); err != nil {
					t.Fatal(err)
				}
			}

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

			if tt.customIndexerDir {
				indexerDir = filepath.Join(prefix, "elsewhere")
				cmd.Env = append(cmd.Env, "RELIC_INDEXER_DIR="+indexerDir)
			}

			if tt.indexerPlatforms != nil {
				cmd.Env = append(cmd.Env, "RELIC_INDEXER_PLATFORMS="+*tt.indexerPlatforms)
			}

			if tt.noChecksumTool {
				cmd.Env = append(cmd.Env, "PATH="+pathWithoutChecksumTools(t))
			}

			out, err := cmd.CombinedOutput()

			if tt.failure != "" {
				if err == nil || !strings.Contains(string(out), tt.failure) {
					t.Fatalf("install succeeded or failed for another reason than %q: %v\n%s", tt.failure, err, out)
				}

				if (tt.readOnlyDir || tt.readOnlyIndexerDir) && strings.Contains(string(out), "Downloading") {
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

			if !strings.Contains(string(out), tt.wantOutput) {
				t.Errorf("output lacks %q:\n%s", tt.wantOutput, out)
			}

			if tt.wantIndexerBuilds == nil {
				return
			}

			installed, err := filepath.Glob(filepath.Join(indexerDir, "*"))
			if err != nil {
				t.Fatal(err)
			}

			if len(installed) != len(tt.wantIndexerBuilds) {
				t.Fatalf("indexer builds = %v, want %v", installed, tt.wantIndexerBuilds)
			}

			for _, build := range tt.wantIndexerBuilds {
				path := filepath.Join(indexerDir, build)

				body, err := os.ReadFile(path) //nolint:gosec // a file of this test's temporary directory
				if err != nil {
					t.Fatal(err)
				}

				if string(body) != "indexer "+build {
					t.Errorf("%s = %q", build, body)
				}

				if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o755 {
					t.Errorf("%s stat = %v, %v", build, fi, err)
				}
			}
		})
	}
}
