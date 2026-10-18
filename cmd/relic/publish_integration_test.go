//go:build integration

package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPublishCommands(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")

	if _, err := relic(
		t,
		config,
		"password\n",
		"auth",
		"login",
		srv.URL,
		"--user",
		"admin",
		"--password-stdin",
	); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(t, config, `apiVersion: relic.build/v1
kind: Repository
metadata: {name: go-local}
spec: {class: local, format: gomod}
---
apiVersion: relic.build/v1
kind: Repository
metadata: {name: tf-local}
spec: {class: local, format: terraform}
---
apiVersion: relic.build/v1
kind: Repository
metadata: {name: hf-local}
spec: {class: local, format: huggingface}
---
apiVersion: relic.build/v1
kind: Repository
metadata: {name: npm-local}
spec: {class: local, format: npm}
`, "apply", "-f", "-"); err != nil {
		t.Fatal(err)
	}

	write := func(files map[string]string) string {
		t.Helper()

		root := t.TempDir()
		for name, content := range files {
			p := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}

		return root
	}

	download := func(target string) string {
		t.Helper()

		out, err := relic(t, config, "", "artifact", "download", target, "-o", "-")
		if err != nil {
			t.Fatalf("download %s: %v", target, err)
		}

		return out
	}

	mod := write(map[string]string{
		"go.mod":    "module example.com/Lib\n\ngo 1.22\n",
		"lib.go":    "package lib\n",
		".git/HEAD": "ref: refs/heads/main\n",
	})
	if out, err := relic(
		t,
		config,
		"",
		"package",
		"publish",
		"go",
		"go-local",
		"v1.2.0",
		"--dir",
		mod,
		"--build",
		"lib/1",
	); err != nil ||
		out != "example.com/Lib@v1.2.0 published to go-local\n" {
		t.Fatalf("go publish = %q %v", out, err)
	}

	zipped := download("go-local/example.com/Lib/@v/v1.2.0.zip")

	zr, err := zip.NewReader(bytes.NewReader([]byte(zipped)), int64(len(zipped)))
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
	}

	slices.Sort(names)

	if !slices.Equal(names, []string{
		"example.com/Lib@v1.2.0/go.mod",
		"example.com/Lib@v1.2.0/lib.go",
	}) {
		t.Errorf("zip = %v", names)
	}

	if !strings.Contains(download("go-local/example.com/Lib/@v/v1.2.0.info"), `"Version":"v1.2.0"`) {
		t.Error("no .info")
	}

	if _, err := relic(t, config, "", "package", "publish", "go", "go-local", "1.2", "--dir", mod); err == nil {
		t.Error("a version that is not semver was accepted")
	}

	tf := write(map[string]string{
		"network/main.tf":                  "output \"x\" { value = 1 }\n",
		"network/.terraform/cache":         "skip",
		"network/terraform.tfstate":        "{}",
		"network/modules/sub/variables.tf": "",
	})
	if out, err := relic(
		t,
		config,
		"",
		"package",
		"publish",
		"terraform",
		"tf-local",
		"v1.0.0",
		"--namespace",
		"acme",
		"--provider",
		"aws",
		"--dir",
		filepath.Join(tf, "network"),
	); err != nil || out != "acme/network/aws 1.0.0 published to tf-local\n" {
		t.Fatalf("terraform publish = %q %v", out, err)
	}

	for _, args := range [][]string{
		{
			"banana",
			"--namespace",
			"acme",
		},
		{
			"2.0.0",
			"--namespace",
			"a/b",
		},
		{
			"2.0.0",
			"--namespace",
			"",
		},
		{
			"2.0.0",
			"--namespace",
			"acme",
			"--name",
			"net/work",
		},
	} {
		publish := append([]string{
			"package",
			"publish",
			"terraform",
			"tf-local",
		}, append(args, "--provider", "aws", "--dir", filepath.Join(tf, "network"))...)
		if _, err := relic(t, config, "", publish...); err == nil {
			t.Errorf("terraform publish %v succeeded, though the registry cannot list it", args)
		}
	}

	if _, err := relic(
		t,
		config,
		"",
		"package",
		"publish",
		"terraform",
		"tf-local",
		"2.0.0",
		"--namespace",
		"acme",
		"--provider",
		"AWS",
		"--dir",
		filepath.Join(tf, "network"),
	); err == nil {
		t.Error("terraform publish with an upper-case provider succeeded")
	}

	module := download("tf-local/acme/network/aws/v1.0.0.zip")

	tz, err := zip.NewReader(bytes.NewReader([]byte(module)), int64(len(module)))
	if err != nil {
		t.Fatalf("module zip: %v", err)
	}

	moduleNames := make([]string, 0, len(tz.File))
	for _, f := range tz.File {
		moduleNames = append(moduleNames, f.Name)
	}

	slices.Sort(moduleNames)

	if !slices.Equal(moduleNames, []string{
		"main.tf",
		"modules/sub/variables.tf",
	}) {
		t.Errorf("module zip = %v", moduleNames)
	}

	v1 := write(map[string]string{
		"config.json":  `{"layers": 2}`,
		"weights.bin":  "w1",
		".git/objects": "skip",
	})
	v2 := write(map[string]string{
		"weights.bin": "w2",
		"README.md":   "# tiny",
	})

	for _, d := range []string{
		v1,
		v2,
	} {
		if _, err := relic(t, config, "", "package", "huggingface", "upload", d, "hf-local", "acme/tiny"); err != nil {
			t.Fatal(err)
		}
	}

	if os.Geteuid() != 0 {
		v3 := write(map[string]string{
			"a.txt": "readable",
			"z.bin": "unreadable",
		})
		if err := os.Chmod(filepath.Join(v3, "z.bin"), 0); err != nil {
			t.Fatal(err)
		}

		if _, err := relic(t, config, "", "package", "huggingface", "upload", v3, "hf-local", "acme/tiny"); err == nil {
			t.Error("an upload with an unreadable file succeeded")
		}
	}

	out := filepath.Join(dir, "tiny")
	if got, err := relic(t, config, "", "package", "hf", "download", "hf-local", "acme/tiny", out); err != nil ||
		got != "acme/tiny@main: 3 files in "+out+"\n" {
		t.Fatalf("download = %q %v", got, err)
	}

	for name, want := range map[string]string{
		"config.json": `{"layers": 2}`,
		"weights.bin": "w2",
		"README.md":   "# tiny",
	} {
		if b, _ := os.ReadFile(filepath.Join(out, name)); string(b) != want {
			t.Errorf("%s = %q", name, b)
		}
	}

	ep, err := relic(t, config, "", "api", "repositories/hf-local/client-endpoints")
	if err != nil {
		t.Fatal(err)
	}

	var endpoints struct {
		PackageManager string
	}
	if err := json.Unmarshal([]byte(ep), &endpoints); err != nil || endpoints.PackageManager == "" {
		t.Fatalf("endpoints = %q %v", ep, err)
	}

	req, _ := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		endpoints.PackageManager+"/api/models/acme/tiny/revision/main",
		nil,
	)
	req.SetBasicAuth("admin", "password")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	var info struct {
		Siblings []struct {
			Rfilename string
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil || len(info.Siblings) != 3 {
		t.Errorf("hub revision = %d %+v %v", resp.StatusCode, info, err)
	}

	if _, err := relic(t, config, "", "package", "hf", "download", "hf-local", "acme/none", out); err == nil {
		t.Error("download of a missing model succeeded")
	}

	for _, args := range [][]string{
		{
			"acme/slash",
			"--revision",
			"feature/x",
		},
		{
			"acme/a/b",
		},
		{
			"acme/x y",
		},
		{
			"acme/t1",
			"--revision",
			"v 1",
		},
	} {
		upload := append([]string{
			"package",
			"hf",
			"upload",
			v1,
			"hf-local",
		}, args...)
		if _, err := relic(t, config, "", upload...); err == nil {
			t.Errorf("upload %v succeeded, though the Hub API cannot read it back", args)
		}
	}

	for _, args := range [][]string{
		{
			"publish",
			"go",
			"npm-local",
			"v1.3.0",
			"--dir",
			mod,
		},
		{
			"publish",
			"terraform",
			"npm-local",
			"1.0.0",
			"--namespace",
			"acme",
			"--provider",
			"aws",
			"--dir",
			filepath.Join(tf, "network"),
		},
		{
			"hf",
			"upload",
			v1,
			"npm-local",
			"acme/tiny",
		},
	} {
		_, err := relic(t, config, "", append([]string{
			"package",
		}, args...)...)
		if err == nil || !strings.Contains(err.Error(), "npm-local is a npm repository") {
			t.Errorf("%s %s into an npm repository: %v", args[0], args[1], err)
		}
	}
}

func TestHuggingFaceDownloadFromRemote(t *testing.T) {
	t.Parallel()

	const commit = "0123456789abcdef0123456789abcdef01234567"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models/org/up/revision/main":
			_, _ = w.Write([]byte(`{"id":"org/up","sha":"` + commit + `","siblings":[` +
				`{"rfilename":"config.json"},{"rfilename":"onnx/model.onnx"}]}`))
		case "/org/up/resolve/" + commit + "/config.json":
			_, _ = w.Write([]byte(`{"up": true}`))
		case "/org/up/resolve/" + commit + "/onnx/model.onnx":
			_, _ = w.Write([]byte("onnx"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	srv := testServer(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")

	if _, err := relic(
		t,
		config,
		"password\n",
		"auth",
		"login",
		srv.URL,
		"--user",
		"admin",
		"--password-stdin",
	); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(t, config, `apiVersion: relic.build/v1
kind: Repository
metadata: {name: hf-remote}
spec: {class: remote, format: huggingface, remote: {url: "`+upstream.URL+`"}}
`, "apply", "-f", "-"); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "up")
	if got, err := relic(t, config, "", "package", "hf", "download", "hf-remote", "org/up", out); err != nil ||
		got != "org/up@main: 2 files in "+out+"\n" {
		t.Fatalf("download = %q %v", got, err)
	}

	for name, want := range map[string]string{
		"config.json":     `{"up": true}`,
		"onnx/model.onnx": "onnx",
	} {
		if b, _ := os.ReadFile(filepath.Join(out, filepath.FromSlash(name))); string(b) != want {
			t.Errorf("%s = %q", name, b)
		}
	}

	_, err := relic(t, config, "", "package", "hf", "download", "hf-remote", "org/up", out, "--revision", "dev")
	if err == nil || !strings.Contains(err.Error(), "has no revision dev") {
		t.Errorf("download of a missing revision = %v", err)
	}

	_, err = relic(t, config, "", "package", "hf", "download", "hf-none", "org/up", out)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("download from a missing repository = %v", err)
	}
}
