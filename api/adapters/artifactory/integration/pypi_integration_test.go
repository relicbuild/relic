//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func twineUpload(t *testing.T, srv *httptest.Server, repoKey, name, version, file, content string) int {
	t.Helper()

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)

	sum := sha256.Sum256([]byte(content))
	for k, v := range map[string]string{
		":action":          "file_upload",
		"protocol_version": "1",
		"name":             name,
		"version":          version,
		"sha256_digest":    hex.EncodeToString(sum[:]),
		"requires_python":  ">=3.8",
		"summary":          "a test",
	} {
		_ = mw.WriteField(k, v)
	}

	fw, _ := mw.CreateFormFile("content", file)
	_, _ = io.WriteString(fw, content)
	_ = mw.Close()
	resp, _ := do(t, srv, call{
		method: "POST",
		path:   "/artifactory/api/pypi/" + repoKey,
		body:   body.String(),
		headers: map[string]string{
			"Content-Type": mw.FormDataContentType(),
		},
	})

	return resp.StatusCode
}

func TestPyPIRepository(t *testing.T) {
	t.Parallel()

	var files, registry *httptest.Server

	files = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/packages/ab/cd/requests-2.0.0-py3-none-any.whl" {
			_, _ = io.WriteString(w, "remote wheel")
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(files.Close)

	registry = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/simple/requests/" {
			_, _ = io.WriteString(
				w,
				`<html><body><a href="`+files.URL+`/packages/ab/cd/requests-2.0.0-py3-none-any.whl#sha256=abc" data-requires-python="&gt;=3.7">requests-2.0.0-py3-none-any.whl</a></body></html>`,
			)

			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(registry.Close)

	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/py-local",
			body:   `{"rclass":"local","packageType":"pypi"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/py-remote",
			body:   `{"rclass":"remote","packageType":"pypi","url":"` + files.URL + `","pyPIRegistryUrl":"` + registry.URL + `"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/py",
			body:   `{"rclass":"virtual","packageType":"pypi","repositories":["py-local","py-remote"],"defaultDeploymentRepo":"py-local"}`,
		},
	)

	if got := twineUpload(t, srv, "py", "My_Pkg", "1.0.0", "my_pkg-1.0.0-py3-none-any.whl", "wheel bytes"); got != 200 {
		t.Fatalf("upload = %d", got)
	}

	if got := twineUpload(t, srv, "py-local", "My_Pkg", "1.0.0", "my_pkg-1.0.0.tar.gz", "sdist bytes"); got != 200 {
		t.Fatalf("upload sdist = %d", got)
	}

	if props := storageProps(
		t,
		srv,
		"py-local/my-pkg/1.0.0/my_pkg-1.0.0-py3-none-any.whl",
	); props["pypi.normalized.name"][0] != "my-pkg" ||
		props["pypi.version"][0] != "1.0.0" {
		t.Errorf("properties = %v", props)
	}

	_, page := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/pypi/py/simple/my-pkg/",
	})

	sum := sha256.Sum256([]byte("wheel bytes"))
	for _, want := range []string{
		`href="../../packages/my-pkg/1.0.0/my_pkg-1.0.0-py3-none-any.whl#sha256=` + hex.EncodeToString(sum[:]) + `"`,
		`data-requires-python="&gt;=3.8"`,
		">my_pkg-1.0.0.tar.gz</a>",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("simple page lacks %s:\n%s", want, page)
		}
	}

	if _, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/pypi/py/packages/my-pkg/1.0.0/my_pkg-1.0.0-py3-none-any.whl",
		},
	); got != "wheel bytes" {
		t.Errorf("package = %q", got)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/pypi/py/simple/My_Pkg/",
		},
	); resp.StatusCode != 200 {
		t.Errorf("unnormalized name = %d", resp.StatusCode)
	}

	if _, alias := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/pypi/py/my-pkg/",
	}); !strings.Contains(
		alias,
		`href="../packages/my-pkg/1.0.0/`,
	) {
		t.Errorf("poetry alias page = %s", alias)
	}

	if _, root := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/pypi/py/simple/",
	}); !strings.Contains(
		root,
		`href="my-pkg/"`,
	) {
		t.Errorf("root index = %s", root)
	}

	_, rpage := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/pypi/py/simple/requests/",
	})
	if !strings.Contains(rpage, `href="../../packages/packages/ab/cd/requests-2.0.0-py3-none-any.whl#sha256=abc"`) ||
		!strings.Contains(rpage, `data-requires-python="&gt;=3.7"`) {
		t.Errorf("remote page = %s", rpage)
	}

	if _, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/pypi/py/packages/packages/ab/cd/requests-2.0.0-py3-none-any.whl",
		},
	); got != "remote wheel" {
		t.Errorf("remote package = %q", got)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/pypi/py/simple/nothing-here/",
		},
	); resp.StatusCode != 404 {
		t.Errorf("unknown project = %d", resp.StatusCode)
	}

	if resp, body := do(
		t,
		srv,
		call{
			method: "POST",
			path: "/artifactory/api/copy/py-remote-cache/packages/ab/cd/requests-2.0.0-py3-none-any.whl" +
				"?to=py-local%2Fpackages%2Fab%2Fcd%2Frequests-2.0.0-py3-none-any.whl",
		},
	); resp.StatusCode != 200 {
		t.Fatalf("copy the cached wheel into the local repository = %d %s", resp.StatusCode, body)
	}

	if resp, body := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/py-local/incoming/Other.Pkg-2.0.tar.gz",
			body:   "deployed sdist",
		},
	); resp.StatusCode != 201 {
		t.Fatalf("deploy a distribution outside the layout = %d %s", resp.StatusCode, body)
	}

	copied := sha256.Sum256([]byte("remote wheel"))

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{
			name: "a distribution copied to the cache's path",
			path: "/artifactory/api/pypi/py-local/simple/requests/",
			want: `href="../../packages/packages/ab/cd/requests-2.0.0-py3-none-any.whl#sha256=` +
				hex.EncodeToString(copied[:]) + `"`,
		},
		{
			name: "a distribution deployed outside the layout",
			path: "/artifactory/api/pypi/py-local/simple/other-pkg/",
			want: `href="../../packages/incoming/Other.Pkg-2.0.tar.gz#sha256=`,
		},
		{
			name: "the copied project in the root index",
			path: "/artifactory/api/pypi/py-local/simple/",
			want: `href="requests/"`,
		},
		{
			name: "the deployed project in the root index",
			path: "/artifactory/api/pypi/py-local/simple/",
			want: `href="other-pkg/"`,
		},
	} {
		if resp, page := do(t, srv, call{
			method: "GET",
			path:   tc.path,
		}); resp.StatusCode != 200 || !strings.Contains(page, tc.want) {
			t.Errorf("%s: %s = %d, lacks %s:\n%s", tc.name, tc.path, resp.StatusCode, tc.want, page)
		}
	}

	if _, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/pypi/py-local/packages/packages/ab/cd/requests-2.0.0-py3-none-any.whl",
		},
	); got != "remote wheel" {
		t.Errorf("copied package = %q", got)
	}

	twineUpload(t, srv, "py-local", "x", "1", "evil/../x-1.tar.gz", "x")

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/storage/py-local/x/1/x-1.tar.gz",
		},
	); resp.StatusCode != 200 {
		t.Errorf("sanitized upload = %d", resp.StatusCode)
	}

	if got := twineUpload(t, srv, "py-local", "x", "1", "notes.txt", "x"); got != 400 {
		t.Errorf("non-distribution upload = %d, want 400", got)
	}
}

func TestPyPIReupload(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/pypi-local",
			body:   `{"rclass":"local","packageType":"pypi"}`,
		},
	)

	if got := twineUpload(t, srv, "pypi-local", "demo", "1.0.0", "demo-1.0.0.tar.gz", "sdist"); got != 200 {
		t.Fatalf("first upload = %d", got)
	}

	if got := twineUpload(t, srv, "pypi-local", "demo", "1.0.0", "demo-1.0.0.tar.gz", "sdist"); got != 200 {
		t.Errorf("admin overwrite = %d, want 200", got)
	}

	for _, u := range []struct {
		name, actions string
	}{
		{
			"dev",
			`["read","write"]`,
		},
		{
			"viewer",
			`["read"]`,
		},
	} {
		do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/security/users/" + u.name,
				body:   `{"email":"x@example.com","password":"Pass-word-1234"}`,
			},
		)
		do(
			t,
			srv,
			call{
				method: "POST",
				path:   "/artifactory/api/v2/security/permissions/" + u.name,
				body:   `{"name":"` + u.name + `","repo":{"repositories":["pypi-local"],"include-patterns":["**"],"actions":{"users":{"` + u.name + `":` + u.actions + `}}}}`,
			},
		)
	}

	as := func(user string) int {
		var body bytes.Buffer

		mw := multipart.NewWriter(&body)
		_ = mw.WriteField(":action", "file_upload")
		_ = mw.WriteField("name", "demo")
		_ = mw.WriteField("version", "1.0.0")
		fw, _ := mw.CreateFormFile("content", "demo-1.0.0.tar.gz")
		_, _ = io.WriteString(fw, "other")
		_ = mw.Close()
		resp, _ := do(
			t,
			srv,
			call{
				method: "POST",
				path:   "/artifactory/api/pypi/pypi-local",
				body:   body.String(),
				noAuth: true,
				headers: map[string]string{
					"Content-Type":  mw.FormDataContentType(),
					"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":Pass-word-1234")),
				},
			},
		)

		return resp.StatusCode
	}
	if got := as("dev"); got != 409 {
		t.Errorf("deploy-only re-upload = %d, want 409", got)
	}

	if got := as("viewer"); got != 403 {
		t.Errorf("read-only upload = %d, want 403", got)
	}

	refusedBeforeTheContent := func() (int, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()

		pr, pw := io.Pipe()

		t.Cleanup(func() {
			_ = pw.Close()
		})

		mw := multipart.NewWriter(pw)

		go func() {
			_ = mw.WriteField(":action", "file_upload")
			_ = mw.WriteField("name", "demo")
			_ = mw.WriteField("version", "2.0.0")

			fw, err := mw.CreateFormFile("content", "demo-2.0.0.tar.gz")
			if err != nil {
				return
			}

			chunk := bytes.Repeat([]byte{
				'x',
			}, 64<<10)
			for {
				if _, err := fw.Write(chunk); err != nil {
					return
				}
			}
		}()

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/artifactory/api/pypi/pypi-local", pr)
		if err != nil {
			return 0, err
		}

		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.SetBasicAuth("viewer", "Pass-word-1234")

		resp, err := srv.Client().Do(req)
		if err != nil {
			return 0, err
		}

		_ = resp.Body.Close()

		return resp.StatusCode, nil
	}
	if got, err := refusedBeforeTheContent(); err != nil || got != 403 {
		t.Errorf("read-only upload of endless content = %d, %v; want 403 before the content is read", got, err)
	}

	upload := func(fields [][2]string) int {
		var body bytes.Buffer

		mw := multipart.NewWriter(&body)

		for _, f := range fields {
			if f[0] == "content" {
				fw, _ := mw.CreateFormFile("content", "late-1.0.0.tar.gz")
				_, _ = io.WriteString(fw, f[1])

				continue
			}

			_ = mw.WriteField(f[0], f[1])
		}

		_ = mw.Close()
		resp, _ := do(t, srv, call{
			method: "POST",
			path:   "/artifactory/api/pypi/pypi-local",
			body:   body.String(),
			headers: map[string]string{
				"Content-Type": mw.FormDataContentType(),
			},
		})

		return resp.StatusCode
	}
	if got := upload([][2]string{
		{
			"content",
			"late",
		},
		{
			"name",
			"late",
		},
		{
			"version",
			"1.0.0",
		},
		{
			"sha256_digest",
			strings.Repeat("0", 64),
		},
	}); got == 200 {
		t.Errorf("a digest after the content was not checked")
	}

	if got := upload([][2]string{
		{
			"content",
			"late",
		},
		{
			":action",
			"doc_upload",
		},
	}); got != 400 {
		t.Errorf("unsupported action = %d, want 400", got)
	}

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/storage/pypi-local/late/1.0.0/late-1.0.0.tar.gz",
	}); resp.StatusCode != 404 {
		t.Errorf("a refused upload was stored: %d", resp.StatusCode)
	}
}

func TestPyPIJSONAPI(t *testing.T) {
	t.Parallel()

	srv := server(t)
	if resp, body := do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/repositories/py-json",
		body:   `{"key":"py-json","rclass":"local","packageType":"pypi"}`,
	}); resp.StatusCode >= 300 {
		t.Fatalf("create = %d %s", resp.StatusCode, body)
	}

	for _, v := range []string{
		"1.0.0",
		"1.1.0",
	} {
		if code := twineUpload(t, srv, "py-json", "Demo_Pkg", v, "demo_pkg-"+v+".tar.gz", "sdist "+v); code != 200 {
			t.Fatalf("upload %s = %d", v, code)
		}
	}

	type file struct {
		Filename    string            `json:"filename"`
		URL         string            `json:"url"`
		Digests     map[string]string `json:"digests"`
		PackageType string            `json:"packagetype"`
	}

	type doc struct {
		Info struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"info"`
		URLs     []file            `json:"urls"`
		Releases map[string][]file `json:"releases"`
	}

	get := func(t *testing.T, path string) (int, doc) {
		t.Helper()
		resp, body := do(t, srv, call{
			method: "GET",
			path:   "/artifactory/api/pypi/py-json/pypi/" + path,
		})

		var d doc
		if resp.StatusCode == 200 {
			if err := json.Unmarshal([]byte(body), &d); err != nil {
				t.Fatalf("%s: %v\n%s", path, err, body)
			}
		}

		return resp.StatusCode, d
	}
	t.Run("a release", func(t *testing.T) {
		code, d := get(t, "demo-pkg/1.0.0/json")
		if code != 200 || d.Info.Name != "demo-pkg" || d.Info.Version != "1.0.0" || len(d.URLs) != 1 {
			t.Fatalf("= %d %+v", code, d)
		}

		f := d.URLs[0]
		if f.Filename != "demo_pkg-1.0.0.tar.gz" || f.PackageType != "sdist" || len(f.Digests["sha256"]) != 64 ||
			!strings.HasPrefix(f.URL, "../../../packages/") {
			t.Errorf("url entry = %+v", f)
		}

		if resp, _ := do(
			t,
			srv,
			call{
				method: "GET",
				path:   "/artifactory/api/pypi/py-json/pypi/demo-pkg/1.0.0/" + f.URL,
			},
		); resp.StatusCode != 200 {
			t.Errorf("download through the relative url = %d", resp.StatusCode)
		}
	})
	t.Run("the project", func(t *testing.T) {
		code, d := get(t, "Demo_Pkg/json")
		if code != 200 || d.Info.Version != "1.1.0" || len(d.Releases) != 2 || len(d.Releases["1.0.0"]) != 1 {
			t.Errorf("= %d %+v", code, d)
		}
	})
	t.Run("unknown release and project", func(t *testing.T) {
		if code, _ := get(t, "demo-pkg/9.9.9/json"); code != 404 {
			t.Errorf("unknown release = %d", code)
		}

		if code, _ := get(t, "missing/json"); code != 404 {
			t.Errorf("unknown project = %d", code)
		}
	})
}
