package scan

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"relic/internal/platform/scripttest"
)

func readFixture(t *testing.T, name string) map[string]*GoVulnerability {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}

	found := map[string]*GoVulnerability{}
	if err := readGovulncheck(b, found); err != nil {
		t.Fatal(err)
	}

	return found
}

func TestReadGovulncheck(t *testing.T) {
	t.Parallel()

	reach := readFixture(t, "govulncheck-reach.json")

	parse := reach["GO-2021-0113 golang.org/x/text"]
	if parse == nil || !parse.Reachable || !strings.Contains(parse.Trace, "golang.org/x/text/language.Parse") {
		t.Errorf("GO-2021-0113 in a binary calling language.Parse = %+v, want reachable", parse)
	}

	if parse != nil && !strings.Contains(strings.Join(parse.Aliases, " "), "CVE-2021-38561") {
		t.Errorf("aliases = %v", parse.Aliases)
	}

	if v := reach["GO-2022-1059 golang.org/x/text"]; v == nil || v.Reachable {
		t.Errorf("GO-2022-1059 (not called) = %+v, want reported and not reachable", v)
	}

	noreach := readFixture(t, "govulncheck-noreach.json")
	if v := noreach["GO-2021-0113 golang.org/x/text"]; v == nil || v.Reachable || v.Trace != "" {
		t.Errorf("GO-2021-0113 in a binary that only imports the package = %+v, want not reachable", v)
	}

	if err := readGovulncheck([]byte(`{"finding":`), map[string]*GoVulnerability{}); err == nil {
		t.Error("a broken stream was read")
	}
}

func TestReachabilityState(t *testing.T) {
	t.Parallel()

	index := ReachabilityIndex{
		"aa": {
			{
				OsvID:  "GO-2021-0113",
				Module: "golang.org/x/text",
				Aliases: []string{
					"CVE-2021-38561",
					"GHSA-ppp9-7jff-5vj2",
				},
				Reachable: true,
				Trace:     "golang.org/x/text/language.Parse",
			},
			{
				OsvID:  "GO-2022-1059",
				Module: "golang.org/x/text",
				Aliases: []string{
					"CVE-2022-32149",
				},
			},
			{
				OsvID:  "GO-2025-1000",
				Module: "stdlib",
				Aliases: []string{
					"CVE-2025-1000",
				},
			},
		},
	}

	tests := []struct {
		name, sha, vulnID, purl string
		cves                    []string
		want                    string
	}{
		{
			"by GHSA alias, called",
			"aa",
			"GHSA-ppp9-7jff-5vj2",
			"pkg:golang/golang.org/x/text@v0.3.6",
			nil,
			Reachable,
		},
		{
			"by CVE, reported but not called",
			"aa",
			"GHSA-69ch-w2m2-3vjp",
			"pkg:golang/golang.org/x/text@v0.3.6",
			[]string{
				"CVE-2022-32149",
			},
			NotReachable,
		},
		{
			"the standard library",
			"aa",
			"CVE-2025-1000",
			"pkg:golang/stdlib@1.27.0",
			nil,
			NotReachable,
		},
		{
			"not reported",
			"aa",
			"GHSA-xxxx-xxxx-xxxx",
			"pkg:golang/golang.org/x/text@v0.3.6",
			nil,
			ReachabilityUnknown,
		},
		{
			"another module",
			"aa",
			"GO-2021-0113",
			"pkg:golang/golang.org/x/net@v0.1.0",
			nil,
			ReachabilityUnknown,
		},
		{
			"not Go",
			"aa",
			"CVE-2021-38561",
			"pkg:npm/text@1.0.0",
			nil,
			ReachabilityUnknown,
		},
		{
			"another artifact",
			"bb",
			"GO-2021-0113",
			"pkg:golang/golang.org/x/text@v0.3.6",
			nil,
			ReachabilityUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, trace := index.State(tt.sha, tt.vulnID, tt.cves, tt.purl)
			if got != tt.want {
				t.Errorf("State = %q, want %q", got, tt.want)
			}

			if (got == Reachable) != (trace != "") {
				t.Errorf("trace = %q for %q", trace, got)
			}
		})
	}
}

func TestGoBinaryPaths(t *testing.T) {
	t.Parallel()

	got := goBinaryPaths([]byte(`{"artifacts":[
		{"type":"go-module","metadataType":"go-module-buildinfo-entry","name":"golang.org/x/text",
		 "locations":[{"path":"/usr/bin/app"}]},
		{"type":"go-module","metadataType":"go-module-buildinfo-entry","name":"stdlib",
		 "locations":[{"path":"/usr/bin/app"},{"path":"/opt/tool"}]},
		{"type":"go-module","metadataType":"go-module-entry","name":"github.com/google/uuid",
		 "locations":[{"path":"/src/go.mod"}]},
		{"type":"npm","name":"left-pad","locations":[{"path":"/app/package.json"}]}]}`))
	if strings.Join(got, ",") != "/opt/tool,/usr/bin/app" {
		t.Errorf("goBinaryPaths = %v", got)
	}
}

func TestImageFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	blobs := filepath.Join(dir, "blobs", "sha256")

	if err := os.MkdirAll(blobs, 0o700); err != nil {
		t.Fatal(err)
	}

	blob := func(b []byte) string {
		sum := sha256.Sum256(b)
		h := hex.EncodeToString(sum[:])

		if err := os.WriteFile(filepath.Join(blobs, h), b, 0o600); err != nil {
			t.Fatal(err)
		}

		return h
	}
	layer := func(files map[string]string, compress bool) string {
		var buf bytes.Buffer

		tw := tar.NewWriter(&buf)
		for name, body := range files {
			if err := tw.WriteHeader(&tar.Header{
				Name:     name,
				Mode:     0o755,
				Size:     int64(len(body)),
				Typeflag: tar.TypeReg,
			}); err != nil {
				t.Fatal(err)
			}

			_, _ = tw.Write([]byte(body))
		}

		_ = tw.Close()

		if !compress {
			return blob(buf.Bytes())
		}

		var gz bytes.Buffer

		zw := gzip.NewWriter(&gz)
		_, _ = zw.Write(buf.Bytes())
		_ = zw.Close()

		return blob(gz.Bytes())
	}

	first := layer(map[string]string{
		"usr/bin/app": "old app",
		"opt/tool":    "tool",
		"opt/gone":    "deleted later",
	}, true)
	second := layer(map[string]string{
		"usr/bin/app":  "new app",
		"opt/.wh.gone": "",
	}, false)
	manifest := blob(fmt.Appendf(
		nil,
		`{"schemaVersion":2,"layers":[{"digest":"sha256:%s"},{"digest":"sha256:%s"}]}`,
		first,
		second,
	))

	if err := os.WriteFile(filepath.Join(dir, "index.json"),
		fmt.Appendf(nil, `{"schemaVersion":2,"manifests":[{"digest":"sha256:%s"}]}`, manifest), 0o600); err != nil {
		t.Fatal(err)
	}

	files, err := imageFiles(dir, []string{
		"/usr/bin/app",
		"/opt/tool",
		"/opt/gone",
		"/not/there",
	}, filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}

	if len(files) != 2 {
		t.Fatalf("files = %v, want the app and the tool", files)
	}

	for p, want := range map[string]string{
		"/usr/bin/app": "new app",
		"/opt/tool":    "tool",
	} {
		if b, _ := os.ReadFile(files[p]); string(b) != want {
			t.Errorf("%s = %q, want %q", p, b, want)
		}
	}

	opaque := layer(map[string]string{
		".wh..wh..opq": "",
	}, false)
	manifest = blob(fmt.Appendf(
		nil,
		`{"schemaVersion":2,"layers":[{"digest":"sha256:%s"},{"digest":"sha256:%s"},{"digest":"sha256:%s"}]}`,
		first,
		second,
		opaque,
	))

	if err := os.WriteFile(filepath.Join(dir, "index.json"),
		fmt.Appendf(nil, `{"schemaVersion":2,"manifests":[{"digest":"sha256:%s"}]}`, manifest), 0o600); err != nil {
		t.Fatal(err)
	}

	files, err = imageFiles(dir, []string{
		"/usr/bin/app",
		"/opt/tool",
	}, filepath.Join(t.TempDir(), "out"))
	if err != nil || len(files) != 0 {
		t.Errorf("after an opaque whiteout of the root, files = %v, %v; want none", files, err)
	}

	if _, err := imageFiles(t.TempDir(), nil, filepath.Join(t.TempDir(), "out")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a directory without index.json: err = %v, want the missing file named", err)
	}
}

func TestGoReachabilityAnalyze(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{
		"b",
		"c",
	} {
		if err := os.WriteFile(filepath.Join(root, "bin", name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tool := filepath.Join(t.TempDir(), "govulncheck")
	scripttest.Write(t, tool, `#!/bin/sh
for last; do :; done
case "$last" in */bin/c) exit 3;; esac
echo "extract of $last"
`)

	g := &GoReachability{
		Exec: Exec{
			Path: tool,
		},
		DatabaseDir: filepath.Join(t.TempDir(), "go"),
	}
	sbom := []byte(`{"artifacts":[{"type":"go-module","metadataType":"go-module-buildinfo-entry","name":"stdlib",
		"locations":[{"path":"/bin/a"},{"path":"/bin/b"},{"path":"/bin/c"}]}]}`)

	analysis, err := g.Analyze(t.Context(), "dir:"+root, sbom, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "/bin/c") {
		t.Errorf("err = %v, want the failed extract of /bin/c", err)
	}

	vulns, extracts := analysis.Vulnerabilities, analysis.Extracts
	if len(vulns) != 0 || len(extracts) != 1 || extracts[0].Executable != "/bin/b" {
		t.Fatalf("vulns = %v, extracts = %+v; want only /bin/b's extract", vulns, extracts)
	}

	if analysis.DatabasePresent {
		t.Error("an analysis without the Go vulnerability database says it had it")
	}

	if data, err := gunzip(
		extracts[0].Data,
	); err != nil ||
		!strings.HasSuffix(strings.TrimSpace(string(data)), "/bin/b") {
		t.Errorf("extract of /bin/b = %q, %v", data, err)
	}
}
