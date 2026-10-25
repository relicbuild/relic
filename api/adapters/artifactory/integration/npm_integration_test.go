//go:build integration

package integration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func npmTarball(t *testing.T, name, version string) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	pkg := `{"name":"` + name + `","version":"` + version + `","description":"test package"}`
	for file, content := range map[string]string{
		"package/package.json": pkg,
		"package/index.js":     "module.exports = 1",
	} {
		_ = tw.WriteHeader(&tar.Header{
			Name:     file,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		})
		_, _ = tw.Write([]byte(content))
	}

	_ = tw.Close()
	_ = gz.Close()

	return buf.Bytes()
}

func publishDoc(name, version string, tgz []byte) string {
	sum := sha512.Sum512(tgz)
	file := name[strings.LastIndex(name, "/")+1:] + "-" + version + ".tgz"
	doc := map[string]any{
		"name": name,
		"dist-tags": map[string]string{
			"latest": version,
		},
		"versions": map[string]any{
			version: map[string]any{
				"name":        name,
				"version":     version,
				"_npmVersion": "10.9.0",
				"dist": map[string]string{
					"tarball":   "http://ignored/" + name + "/-/" + file,
					"integrity": "sha512-" + base64.StdEncoding.EncodeToString(sum[:]),
				},
			},
		},
		"_attachments": map[string]any{
			file: map[string]any{
				"data":   base64.StdEncoding.EncodeToString(tgz),
				"length": len(tgz),
			},
		},
	}
	b, _ := json.Marshal(doc)

	return string(b)
}

func getJSON(t *testing.T, srv *httptest.Server, path string, v any) int {
	t.Helper()

	resp, body := do(t, srv, call{
		method: "GET",
		path:   path,
	})
	if resp.StatusCode == 200 {
		if err := json.Unmarshal([]byte(body), v); err != nil {
			t.Fatalf("%s: %v\n%s", path, err, body)
		}
	}

	return resp.StatusCode
}

type packument struct {
	Name     string            `json:"name"`
	DistTags map[string]string `json:"dist-tags"`
	Versions map[string]struct {
		Version string `json:"version"`
		Dist    struct {
			Tarball, Shasum, Integrity string
		} `json:"dist"`
	} `json:"versions"`
}

func TestNpmRegistry(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/npm-local",
			body:   `{"rclass":"local","packageType":"npm"}`,
		},
	)

	base := "/artifactory/api/npm/npm-local"

	published := map[string][]byte{}

	for _, pkg := range []struct {
		name, version, path string
	}{
		{
			"left-pad",
			"1.0.0",
			"left-pad",
		},
		{
			"left-pad",
			"1.1.0",
			"left-pad",
		},
		{
			"@acme/ui",
			"2.0.0",
			"@acme%2fui",
		},
	} {
		tgz := npmTarball(t, pkg.name, pkg.version)

		published[pkg.name+"@"+pkg.version] = tgz
		if resp, body := do(
			t,
			srv,
			call{
				method: "PUT",
				path:   base + "/" + pkg.path,
				body:   publishDoc(pkg.name, pkg.version, tgz),
			},
		); resp.StatusCode != 201 {
			t.Fatalf("publish %s@%s = %d %s", pkg.name, pkg.version, resp.StatusCode, body)
		}
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   base + "/left-pad",
			body:   publishDoc("left-pad", "1.0.0", npmTarball(t, "left-pad", "1.0.0")),
		},
	); resp.StatusCode != 403 {
		t.Errorf("republish = %d, want 403", resp.StatusCode)
	}

	wrongIntegrity := strings.Replace(
		publishDoc("right-pad", "1.0.0", npmTarball(t, "right-pad", "1.0.0")),
		`"integrity":"sha512-`,
		`"integrity":"sha512-AAAA`,
		1,
	)
	if resp, _ := do(t, srv, call{
		method: "PUT",
		path:   base + "/right-pad",
		body:   wrongIntegrity,
	}); resp.StatusCode != 400 {
		t.Errorf("publish whose integrity does not match its tarball = %d, want 400", resp.StatusCode)
	}

	var doc packument
	if code := getJSON(t, srv, base+"/left-pad", &doc); code != 200 {
		t.Fatalf("packument = %d", code)
	}

	if doc.DistTags["latest"] != "1.1.0" || len(doc.Versions) != 2 {
		t.Errorf("packument = %+v", doc)
	}

	v := doc.Versions["1.0.0"]
	if !strings.HasSuffix(v.Dist.Tarball, "/artifactory/api/npm/npm-local/left-pad/-/left-pad-1.0.0.tgz") ||
		v.Dist.Shasum == "" ||
		!strings.HasPrefix(v.Dist.Integrity, "sha512-") {
		t.Errorf("dist = %+v", v.Dist)
	}

	resp, body := do(t, srv, call{
		method: "GET",
		path:   base + "/left-pad/-/left-pad-1.0.0.tgz",
	})
	if resp.StatusCode != 200 || !bytes.Equal([]byte(body), published["left-pad@1.0.0"]) {
		t.Errorf("tarball = %d (%d bytes)", resp.StatusCode, len(body))
	}

	sum := sha512.Sum512([]byte(body))
	if "sha512-"+base64.StdEncoding.EncodeToString(sum[:]) != v.Dist.Integrity {
		t.Error("tarball does not match its integrity")
	}

	var scoped packument
	if code := getJSON(t, srv, base+"/@acme%2fui", &scoped); code != 200 ||
		scoped.Versions["2.0.0"].Dist.Tarball == "" {
		t.Errorf("scoped packument = %d %+v", code, scoped)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   base + "/@acme/ui/-/@acme/ui-2.0.0.tgz",
		},
	); resp.StatusCode != 200 {
		t.Errorf("scoped tarball = %d", resp.StatusCode)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/npm-local/plain/-/plain-0.1.0.tgz",
			body:   string(npmTarball(t, "plain", "0.1.0")),
		},
	); resp.StatusCode != 201 {
		t.Fatalf("plain deploy = %d", resp.StatusCode)
	}

	var plain packument
	if code := getJSON(t, srv, base+"/plain", &plain); code != 200 || plain.Versions["0.1.0"].Dist.Integrity == "" {
		t.Errorf("indexed packument = %d %+v", code, plain)
	}

	_, props := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/storage/npm-local/plain/-/plain-0.1.0.tgz?properties",
		},
	)
	if !strings.Contains(props, `"npm.name"`) || !strings.Contains(props, `"npm.version"`) {
		t.Errorf("npm properties not set: %s", props)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   base + "/-/package/left-pad/dist-tags/beta",
			body:   `"1.0.0"`,
		},
	); resp.StatusCode != 201 {
		t.Errorf("set tag = %d", resp.StatusCode)
	}

	var tags map[string]string
	if getJSON(t, srv, base+"/-/package/left-pad/dist-tags", &tags); tags["beta"] != "1.0.0" ||
		tags["latest"] != "1.1.0" {
		t.Errorf("tags = %v", tags)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "DELETE",
			path:   base + "/-/package/left-pad/dist-tags/latest",
		},
	); resp.StatusCode != 400 {
		t.Errorf("delete latest = %d, want 400", resp.StatusCode)
	}

	do(t, srv, call{
		method: "DELETE",
		path:   "/artifactory/npm-local/left-pad/-/left-pad-1.1.0.tgz",
	})

	var after packument
	getJSON(t, srv, base+"/left-pad", &after)

	if _, ok := after.Versions["1.1.0"]; ok || after.DistTags["latest"] != "1.0.0" {
		t.Errorf("after delete = %+v", after)
	}

	var search struct {
		Objects []struct {
			Package struct {
				Name string
			} `json:"package"`
		} `json:"objects"`
	}
	if getJSON(t, srv, base+"/-/v1/search?text=pad", &search); len(search.Objects) != 1 ||
		search.Objects[0].Package.Name != "left-pad" {
		t.Errorf("search = %+v", search)
	}

	if _, body := do(t, srv, call{
		method: "GET",
		path:   base + "/-/whoami",
	}); !strings.Contains(body, `"admin"`) {
		t.Errorf("whoami = %s", body)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "POST",
			path:   base + "/-/npm/v1/security/audits/quick",
			body:   `{}`,
		},
	); resp.StatusCode != 503 {
		t.Errorf("audit = %d, want 503", resp.StatusCode)
	}

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   base + "/nope",
	}); resp.StatusCode != 404 {
		t.Errorf("missing package = %d", resp.StatusCode)
	}

	resp, body = do(
		t,
		srv,
		call{
			method: "PUT",
			path:   base + "/-/user/org.couchdb.user:admin",
			body:   `{"name":"admin","password":"password"}`,
			noAuth: true,
		},
	)

	var login struct {
		Token string
	}
	if err := json.Unmarshal([]byte(body), &login); err != nil || resp.StatusCode != 201 || login.Token == "" {
		t.Errorf("npm login = %d %s", resp.StatusCode, body)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   base + "/-/user/org.couchdb.user:admin",
			body:   `{"name":"admin","password":"wrong"}`,
			noAuth: true,
		},
	); resp.StatusCode != 401 {
		t.Errorf("npm login with a wrong password = %d, want 401", resp.StatusCode)
	}
}

func TestNpmRemoteAndVirtual(t *testing.T) {
	t.Parallel()
	tgz := npmTarball(t, "upstream-pkg", "3.0.0")

	var upstreamURL string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/upstream-pkg":
			_, _ = io.WriteString(
				w,
				`{"name":"upstream-pkg","dist-tags":{"latest":"3.0.0"},"versions":{"3.0.0":{"name":"upstream-pkg","version":"3.0.0","dist":{"tarball":"`+upstreamURL+`/upstream-pkg/-/upstream-pkg-3.0.0.tgz"}}}}`,
			)
		case "/upstream-pkg/-/upstream-pkg-3.0.0.tgz":
			_, _ = w.Write(tgz)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	upstreamURL = upstream.URL

	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/npm-remote",
			body:   `{"rclass":"remote","packageType":"npm","url":"` + upstream.URL + `"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/npm-local",
			body:   `{"rclass":"local","packageType":"npm"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/npm",
			body:   `{"rclass":"virtual","packageType":"npm","repositories":["npm-local","npm-remote"],"defaultDeploymentRepo":"npm-local"}`,
		},
	)

	var doc packument
	if code := getJSON(t, srv, "/artifactory/api/npm/npm-remote/upstream-pkg", &doc); code != 200 {
		t.Fatalf("remote packument = %d", code)
	}

	if tb := doc.Versions["3.0.0"].Dist.Tarball; !strings.Contains(
		tb,
		"/artifactory/api/npm/npm-remote/upstream-pkg/-/upstream-pkg-3.0.0.tgz",
	) {
		t.Errorf("tarball not rewritten: %s", tb)
	}

	if resp, body := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/npm/npm/local-pkg",
			body:   publishDoc("local-pkg", "1.0.0", npmTarball(t, "local-pkg", "1.0.0")),
		},
	); resp.StatusCode != 201 {
		t.Fatalf("publish via virtual = %d %s", resp.StatusCode, body)
	}

	for _, name := range []string{
		"local-pkg",
		"upstream-pkg",
	} {
		var d packument
		if code := getJSON(t, srv, "/artifactory/api/npm/npm/"+name, &d); code != 200 || len(d.Versions) != 1 {
			t.Fatalf("virtual packument %s = %d %+v", name, code, d)
		}

		for _, v := range d.Versions {
			path := v.Dist.Tarball[strings.Index(v.Dist.Tarball, "/artifactory/"):]
			if !strings.HasPrefix(path, "/artifactory/api/npm/npm/") {
				t.Errorf("virtual tarball link %s", path)
			}

			if resp, _ := do(t, srv, call{
				method: "GET",
				path:   path,
			}); resp.StatusCode != 200 {
				t.Errorf("GET %s = %d", path, resp.StatusCode)
			}
		}
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "HEAD",
			path:   "/artifactory/npm-remote/upstream-pkg/-/upstream-pkg-3.0.0.tgz",
		},
	); resp.StatusCode != 200 {
		t.Errorf("cached remote tarball = %d", resp.StatusCode)
	}
}

func TestNpmSigningKeysAndAttestationsComeFromTheUpstream(t *testing.T) {
	t.Parallel()

	keys := `{"keys":[{"keyid":"SHA256:example","keytype":"ecdsa-sha2-nistp256","key":"MFkw"}]}`
	attestations := `{"attestations":[{"predicateType":"https://slsa.dev/provenance/v1"}]}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.EscapedPath() {
		case "/-/npm/v1/keys":
			_, _ = io.WriteString(w, keys)
		case "/-/npm/v1/attestations/@acme%2Fsigned@1.0.0":
			_, _ = io.WriteString(w, attestations)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)

	srv := server(t)
	for _, r := range []struct {
		name, config string
	}{
		{
			name:   "npm-remote",
			config: `{"rclass":"remote","packageType":"npm","url":"` + upstream.URL + `"}`,
		},
		{
			name:   "npm-local",
			config: `{"rclass":"local","packageType":"npm"}`,
		},
		{
			name:   "npm",
			config: `{"rclass":"virtual","packageType":"npm","repositories":["npm-local","npm-remote"]}`,
		},
	} {
		if resp, body := do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/api/repositories/" + r.name,
			body:   r.config,
		}); resp.StatusCode/100 != 2 {
			t.Fatalf("create %s = %d %s", r.name, resp.StatusCode, body)
		}
	}

	tests := []struct {
		name, path string
		status     int
		want       string
	}{
		{
			name:   "keys through the virtual",
			path:   "/artifactory/api/npm/npm/-/npm/v1/keys",
			status: 200,
			want:   keys,
		},
		{
			name:   "keys of the remote",
			path:   "/artifactory/api/npm/npm-remote/-/npm/v1/keys",
			status: 200,
			want:   keys,
		},
		{
			name:   "attestations of a scoped package",
			path:   "/artifactory/api/npm/npm/-/npm/v1/attestations/@acme%2fsigned@1.0.0",
			status: 200,
			want:   attestations,
		},
		{
			name:   "attestations the upstream does not have",
			path:   "/artifactory/api/npm/npm/-/npm/v1/attestations/unsigned@1.0.0",
			status: 404,
		},
		{
			name:   "attestations without a version",
			path:   "/artifactory/api/npm/npm/-/npm/v1/attestations/unsigned",
			status: 400,
		},
		{
			name:   "keys of a local repository",
			path:   "/artifactory/api/npm/npm-local/-/npm/v1/keys",
			status: 404,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp, body := do(t, srv, call{
				method: "GET",
				path:   tt.path,
			})
			if resp.StatusCode != tt.status || tt.want != "" && body != tt.want {
				t.Errorf("GET %s = %d %s, want %d %s", tt.path, resp.StatusCode, body, tt.status, tt.want)
			}
		})
	}
}

func TestRelicAPINpmPackage(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/npm-local",
			body:   `{"rclass":"local","packageType":"npm"}`,
		},
	)

	tgz := npmTarball(t, "left-pad", "1.3.0")
	if resp, body := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/npm-local/left-pad/-/left-pad-1.3.0.tgz",
			body:   string(tgz),
		},
	); resp.StatusCode != 201 {
		t.Fatalf("deploy = %d %s", resp.StatusCode, body)
	}

	pkgs := relicv1connect.NewPackageServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	got, err := pkgs.GetNpmPackage(
		t.Context(),
		&relicv1.GetNpmPackageRequest{
			Repository: "npm-local",
			Path:       "left-pad/-/left-pad-1.3.0.tgz",
		},
	)
	if err != nil || got.GetName() != "left-pad" || got.GetVersion() != "1.3.0" ||
		got.GetDescription() != "test package" {
		t.Errorf("npm info = %v %v", got, err)
	}

	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/npm-local/notes.txt",
		body:   "hi",
	})

	if _, err := pkgs.GetNpmPackage(
		t.Context(),
		&relicv1.GetNpmPackageRequest{
			Repository: "npm-local",
			Path:       "notes.txt",
		},
	); code(err) != connect.CodeNotFound {
		t.Errorf("non-tarball: %v, want not found", err)
	}
}

func TestNpmDeprecate(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/npm-local",
			body:   `{"rclass":"local","packageType":"npm"}`,
		},
	)

	base := "/artifactory/api/npm/npm-local"
	for _, v := range []string{
		"1.0.0",
		"1.1.0",
	} {
		if resp, body := do(
			t,
			srv,
			call{
				method: "PUT",
				path:   base + "/old-pkg",
				body:   publishDoc("old-pkg", v, npmTarball(t, "old-pkg", v)),
			},
		); resp.StatusCode != 201 {
			t.Fatalf("publish %s = %d %s", v, resp.StatusCode, body)
		}
	}

	deprecate := func(message string) {
		t.Helper()

		doc := `{"name":"old-pkg","versions":{` +
			`"1.0.0":{"name":"old-pkg","version":"1.0.0","deprecated":` + strconv.Quote(message) + `},` +
			`"1.1.0":{"name":"old-pkg","version":"1.1.0"}}}`
		if resp, body := do(t, srv, call{
			method: "PUT",
			path:   base + "/old-pkg",
			body:   doc,
		}); resp.StatusCode != 200 {
			t.Fatalf("deprecate = %d %s", resp.StatusCode, body)
		}
	}

	var doc struct {
		Versions map[string]map[string]any `json:"versions"`
	}

	deprecate("use 1.1.0")
	getJSON(t, srv, base+"/old-pkg", &doc)

	if doc.Versions["1.0.0"]["deprecated"] != "use 1.1.0" || doc.Versions["1.1.0"]["deprecated"] != nil {
		t.Errorf("after deprecate: %v / %v", doc.Versions["1.0.0"]["deprecated"], doc.Versions["1.1.0"]["deprecated"])
	}

	deprecate("")

	doc.Versions = nil
	getJSON(t, srv, base+"/old-pkg", &doc)

	if _, ok := doc.Versions["1.0.0"]["deprecated"]; ok {
		t.Errorf("undeprecate left %v", doc.Versions["1.0.0"]["deprecated"])
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   base + "/missing-pkg",
			body:   `{"name":"missing-pkg","versions":{"1.0.0":{"deprecated":"x"}}}`,
		},
	); resp.StatusCode != 404 {
		t.Errorf("deprecate an unknown package = %d, want 404", resp.StatusCode)
	}
}

func TestNpmAudit(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/-/npm/v1/security/advisories/bulk" ||
			r.Header.Get("Content-Encoding") != "gzip" ||
			len(body) == 0 {
			http.Error(w, "unexpected audit request", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"minimist":[{"id":1096465,"severity":"critical","vulnerable_versions":"<1.2.6"}]}`)
	}))
	t.Cleanup(upstream.Close)
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/npm-local",
			body:   `{"rclass":"local","packageType":"npm"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/npm-remote",
			body:   `{"rclass":"remote","packageType":"npm","url":"` + upstream.URL + `"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/npm",
			body:   `{"rclass":"virtual","packageType":"npm","repositories":["npm-local","npm-remote"]}`,
		},
	)

	var gz bytes.Buffer

	zw := gzip.NewWriter(&gz)
	_, _ = io.WriteString(zw, `{"minimist":["1.2.5"]}`)
	_ = zw.Close()

	audit := func(repoKey string) (reply, string) {
		return do(t, srv, call{
			method: "POST",
			path:   "/artifactory/api/npm/" + repoKey + "/-/npm/v1/security/advisories/bulk",
			body:   gz.String(),
			headers: map[string]string{
				"Content-Type":     "application/json",
				"Content-Encoding": "gzip",
			},
		})
	}
	if resp, body := audit("npm"); resp.StatusCode != 200 || !strings.Contains(body, `"severity":"critical"`) {
		t.Errorf("audit through the virtual = %d %s", resp.StatusCode, body)
	}

	if resp, _ := audit("npm-local"); resp.StatusCode != 503 {
		t.Errorf("audit of a local = %d, want 503", resp.StatusCode)
	}

	if resp, _ := do(t, srv, call{
		method: "POST",
		path:   "/artifactory/api/npm/npm/-/npm/v1/security/advisories/bulk",
		body:   strings.Repeat(" ", 32<<20+1),
		headers: map[string]string{
			"Content-Type": "application/json",
		},
	}); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized audit = %d, want 413", resp.StatusCode)
	}
}

func TestNpmCopyIndexes(t *testing.T) {
	t.Parallel()
	srv := server(t)

	for _, body := range []string{
		`{"key":"npm-src","rclass":"local","packageType":"npm"}`,
		`{"key":"npm-dst","rclass":"local","packageType":"npm"}`,
	} {
		key := strings.Split(strings.Split(body, `"key":"`)[1], `"`)[0]
		if resp, b := do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/repositories/" + key,
				body:   body,
			},
		); resp.StatusCode >= 300 {
			t.Fatalf("create %s = %d %s", key, resp.StatusCode, b)
		}
	}

	if resp, b := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/npm-src/copied-pkg/-/copied-pkg-1.0.0.tgz",
			body:   string(npmTarball(t, "copied-pkg", "1.0.0")),
		},
	); resp.StatusCode != 201 {
		t.Fatalf("deploy = %d %s", resp.StatusCode, b)
	}

	if resp, b := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/copy/npm-src/copied-pkg/-/copied-pkg-1.0.0.tgz?to=npm-dst%2Fcopied-pkg%2F-%2Fcopied-pkg-1.0.0.tgz",
		},
	); resp.StatusCode != 200 {
		t.Fatalf("copy = %d %s", resp.StatusCode, b)
	}

	resp, body := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/npm/npm-dst/copied-pkg",
	})
	if resp.StatusCode != 200 || !strings.Contains(body, `"1.0.0"`) {
		t.Errorf("packument after copy = %d %s", resp.StatusCode, body)
	}
}

func TestNpmDistTagPointsAtAPublishedVersion(t *testing.T) {
	t.Parallel()

	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/npm-local",
			body:   `{"rclass":"local","packageType":"npm"}`,
		},
	)

	base := "/artifactory/api/npm/npm-local"
	if resp, body := do(t, srv, call{
		method: "PUT",
		path:   base + "/tagged",
		body:   publishDoc("tagged", "1.0.0", npmTarball(t, "tagged", "1.0.0")),
	}); resp.StatusCode != 201 {
		t.Fatalf("publish = %d %s", resp.StatusCode, body)
	}

	tests := []struct {
		name    string
		pkg     string
		version string
		want    int
	}{
		{
			name:    "a published version",
			pkg:     "tagged",
			version: `"1.0.0"`,
			want:    http.StatusCreated,
		},
		{
			name:    "a version never published",
			pkg:     "tagged",
			version: `"9.9.9"`,
			want:    http.StatusNotFound,
		},
		{
			name:    "a package never published",
			pkg:     "untagged",
			version: `"1.0.0"`,
			want:    http.StatusNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := do(t, srv, call{
				method: "PUT",
				path:   base + "/-/package/" + tt.pkg + "/dist-tags/next",
				body:   tt.version,
			})
			if resp.StatusCode != tt.want {
				t.Errorf("tag %s@%s = %d %s, want %d", tt.pkg, tt.version, resp.StatusCode, body, tt.want)
			}
		})
	}

	var tags map[string]string
	if getJSON(t, srv, base+"/-/package/tagged/dist-tags", &tags); tags["next"] != "1.0.0" {
		t.Errorf("dist-tags = %v, want next at 1.0.0", tags)
	}
}
