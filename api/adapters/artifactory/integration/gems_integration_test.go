//go:build integration

package integration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func gemFile(t *testing.T, name, version string) []byte {
	t.Helper()

	meta := "--- !ruby/object:Gem::Specification\nname: " + name + "\nversion: !ruby/object:Gem::Version\n  version: " + version +
		"\nplatform: ruby\nauthors:\n- Relic\nsummary: demo\ndependencies:\n- !ruby/object:Gem::Dependency\n  name: rake\n" +
		"  requirement: !ruby/object:Gem::Requirement\n    requirements:\n    - - \">=\"\n      - !ruby/object:Gem::Version\n        version: '13'\n" +
		"  type: :runtime\n  prerelease: false\n"

	var gz bytes.Buffer

	zw := gzip.NewWriter(&gz)
	_, _ = io.WriteString(zw, meta)
	_ = zw.Close()

	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)
	for fname, body := range map[string][]byte{
		"metadata.gz": gz.Bytes(),
		"data.tar.gz": {},
	} {
		_ = tw.WriteHeader(&tar.Header{
			Name: fname,
			Mode: 0o644,
			Size: int64(len(body)),
		})
		_, _ = tw.Write(body)
	}

	_ = tw.Close()

	return buf.Bytes()
}

func TestGemsRepository(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/gems-local",
			body:   `{"rclass":"local","packageType":"gems"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/gems",
			body:   `{"rclass":"virtual","packageType":"gems","repositories":["gems-local"],"defaultDeploymentRepo":"gems-local"}`,
		},
	)

	for _, v := range []string{
		"1.0.0",
		"1.1.0",
		"2.0.0.beta1",
	} {
		if resp, body := do(
			t,
			srv,
			call{
				method: "POST",
				path:   "/artifactory/api/gems/gems/api/v1/gems",
				body:   string(gemFile(t, "relic_demo", v)),
			},
		); resp.StatusCode != 200 {
			t.Fatalf("push %s = %d %s", v, resp.StatusCode, body)
		}
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/gems/gems/api/v1/gems",
			body:   string(gemFile(t, "relic_demo", "1.0.0")),
		},
	); resp.StatusCode != 409 {
		t.Errorf("repush = %d, want 409", resp.StatusCode)
	}

	if props := storageProps(t, srv, "gems-local/gems/relic_demo-1.1.0.gem"); props["gem.name"][0] != "relic_demo" {
		t.Errorf("properties = %v", props)
	}

	_, versions := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/gems/gems/versions",
	})
	if !strings.Contains(versions, "\n---\nrelic_demo 1.0.0,1.1.0,2.0.0.beta1 ") {
		t.Errorf("versions = %q", versions)
	}

	_, info := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/gems/gems/info/relic_demo",
	})
	if !strings.HasPrefix(info, "---\n1.0.0 rake:>= 13|checksum:") {
		t.Errorf("info = %q", info)
	}

	for _, idx := range []struct {
		name    string
		listed  []string
		omitted []string
	}{
		{
			name: "specs.4.8.gz",
			listed: []string{
				"1.0.0",
				"1.1.0",
			},
			omitted: []string{
				"2.0.0.beta1",
			},
		},
		{
			name: "latest_specs.4.8.gz",
			listed: []string{
				"1.1.0",
			},
			omitted: []string{
				"1.0.0",
				"2.0.0.beta1",
			},
		},
		{
			name: "prerelease_specs.4.8.gz",
			listed: []string{
				"2.0.0.beta1",
			},
			omitted: []string{
				"1.0.0",
				"1.1.0",
			},
		},
	} {
		resp, body := do(t, srv, call{
			method: "GET",
			path:   "/artifactory/api/gems/gems/" + idx.name,
		})

		zr, err := gzip.NewReader(strings.NewReader(body))
		if resp.StatusCode != 200 || err != nil {
			t.Fatalf("%s = %d, %v", idx.name, resp.StatusCode, err)
		}

		raw, _ := io.ReadAll(zr)
		if !bytes.HasPrefix(raw, []byte{
			4,
			8,
			'[',
		}) || !bytes.Contains(raw, []byte("relic_demo")) {
			t.Errorf("%s = %q", idx.name, raw)
		}

		for _, v := range idx.listed {
			if !bytes.Contains(raw, []byte(v)) {
				t.Errorf("%s does not list %s: %q", idx.name, v, raw)
			}
		}

		for _, v := range idx.omitted {
			if bytes.Contains(raw, []byte(v)) {
				t.Errorf("%s lists %s: %q", idx.name, v, raw)
			}
		}
	}

	_, rz := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/gems/gems/quick/Marshal.4.8/relic_demo-1.0.0.gemspec.rz",
		},
	)

	zr, err := zlib.NewReader(strings.NewReader(rz))
	if err != nil {
		t.Fatal(err)
	}

	spec, _ := io.ReadAll(zr)
	if !bytes.HasPrefix(spec, []byte{
		4,
		8,
		'u',
		':',
	}) || !bytes.Contains(spec, []byte("Gem::Specification")) ||
		!bytes.Contains(spec, []byte("rake")) {
		t.Errorf("quick spec = %q", spec)
	}

	if resp, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/gems/gems/gems/relic_demo-1.0.0.gem",
		},
	); resp.StatusCode != 200 ||
		len(got) == 0 {
		t.Errorf("gem download = %d", resp.StatusCode)
	}

	if _, deps := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/gems/gems/api/v1/dependencies?gems=relic_demo",
		},
	); !strings.Contains(deps, "relic_demo") ||
		!strings.Contains(deps, ">= 13") {
		t.Errorf("dependencies = %q", deps)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/gems/gems/info/missing",
		},
	); resp.StatusCode != 404 {
		t.Errorf("unknown gem info = %d", resp.StatusCode)
	}
}

func TestGemsAPIKeyAndYank(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/gems",
			body:   `{"rclass":"local","packageType":"gems"}`,
		},
	)

	const api = "/artifactory/api/gems/gems/api/v1/"

	resp, yml := do(t, srv, call{
		method: "GET",
		path:   api + "api_key.yaml",
	})

	key, ok := strings.CutPrefix(strings.TrimSpace(yml), "---\n:rubygems_api_key: ")
	if resp.StatusCode != 200 || !ok || !strings.HasPrefix(key, "Bearer ") || strings.Contains(yml, "password") {
		t.Fatalf("api_key.yaml = %d %q", resp.StatusCode, yml)
	}

	if _, plain := do(t, srv, call{
		method: "GET",
		path:   api + "api_key",
	}); !strings.HasPrefix(plain, "Bearer ") {
		t.Errorf("api_key = %q", plain)
	}

	if resp, body := do(
		t,
		srv,
		call{
			method: "POST",
			path:   api + "gems",
			body:   string(gemFile(t, "yankme", "1.0.0")),
			noAuth: true,
			headers: map[string]string{
				"Authorization": key,
			},
		},
	); resp.StatusCode != 200 {
		t.Fatalf("push with the key = %d %s", resp.StatusCode, body)
	}

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   api + "api_key.yaml",
		noAuth: true,
	}); resp.StatusCode != 401 {
		t.Errorf("anonymous api_key.yaml = %d, want 401", resp.StatusCode)
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/security/users/dev",
			body:   `{"email":"d@example.com","password":"Dev-pass-12345"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/dev",
			body:   `{"name":"dev","repo":{"repositories":["gems"],"include-patterns":["**"],"actions":{"users":{"dev":["read","write"]}}}}`,
		},
	)

	yank := func(user string) int {
		c := call{
			method: "DELETE",
			path:   api + "gems/yank",
			body:   "gem_name=yankme&version=1.0.0",
			headers: map[string]string{
				"Content-Type": "application/x-www-form-urlencoded",
			},
		}
		if user != "" {
			c.noAuth = true
			c.headers["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(user))
		}

		resp, _ := do(t, srv, c)

		return resp.StatusCode
	}
	if got := yank("dev:Dev-pass-12345"); got != 403 {
		t.Errorf("yank without delete permission = %d, want 403", got)
	}

	if got := yank(""); got != 200 {
		t.Errorf("yank = %d, want 200", got)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/storage/gems/gems/yankme-1.0.0.gem",
		},
	); resp.StatusCode != 404 {
		t.Errorf("yanked gem still stored: %d", resp.StatusCode)
	}

	if _, versions := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/gems/gems/versions",
	}); strings.Contains(
		versions,
		"yankme",
	) {
		t.Errorf("yanked gem still indexed:\n%s", versions)
	}
}

func TestGemsInfoFile(t *testing.T) {
	t.Parallel()

	srv := server(t)
	for _, key := range []string{
		"gems-a",
		"gems-b",
	} {
		if resp, body := do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/repositories/" + key,
				body:   `{"rclass":"local","packageType":"gems"}`,
			},
		); resp.StatusCode >= 300 {
			t.Fatalf("create %s = %d %s", key, resp.StatusCode, body)
		}
	}

	if resp, body := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/gems/gems-a/api/v1/gems",
			body:   string(gemFile(t, "relic_info", "1.2.0")),
		},
	); resp.StatusCode != 200 {
		t.Fatalf("push = %d %s", resp.StatusCode, body)
	}

	if resp, body := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/copy/gems-a/gems/relic_info-1.2.0.gem?to=gems-b%2Fgems%2Frelic_info-1.2.0.gem",
		},
	); resp.StatusCode != 200 {
		t.Fatalf("copy = %d %s", resp.StatusCode, body)
	}

	for _, key := range []string{
		"gems-a",
		"gems-b",
	} {
		resp, body := do(t, srv, call{
			method: "GET",
			path:   "/artifactory/api/storage/" + key + "/info/relic_info",
		})

		var info struct {
			Repo, Path string
			Checksums  struct {
				SHA256 string
			}
		}

		_ = json.Unmarshal([]byte(body), &info)
		if resp.StatusCode != 200 || info.Repo != key || info.Path != "/info/relic_info" ||
			len(info.Checksums.SHA256) != 64 {
			t.Errorf("%s storage info = %d %s", key, resp.StatusCode, body)
		}

		if resp, body := do(
			t,
			srv,
			call{
				method: "GET",
				path:   "/artifactory/" + key + "/info/relic_info",
			},
		); resp.StatusCode != 200 ||
			!strings.Contains(body, "1.2.0 ") {
			t.Errorf("%s download = %d %q", key, resp.StatusCode, body)
		}
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/storage/gems-b/info/missing",
		},
	); resp.StatusCode != 404 {
		t.Errorf("unknown gem = %d", resp.StatusCode)
	}
}
