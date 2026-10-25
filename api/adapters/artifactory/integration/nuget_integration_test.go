//go:build integration

package integration

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func nupkg(t *testing.T, id, version string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	w, _ := zw.Create(id + ".nuspec")
	_, _ = io.WriteString(
		w,
		`<?xml version="1.0"?><package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd"><metadata><id>`+id+`</id><version>`+version+`</version><authors>relic</authors><description>test</description></metadata></package>`,
	)
	lib, _ := zw.Create("lib/net8.0/" + id + ".dll")
	_, _ = io.WriteString(lib, "dll")
	_ = zw.Close()

	return buf.Bytes()
}

func nugetPush(t *testing.T, srv *httptest.Server, path string, pkg []byte) int {
	t.Helper()

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("package", "package.nupkg")
	_, _ = fw.Write(pkg)
	_ = mw.Close()
	resp, _ := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   path,
			body:   body.String(),
			headers: map[string]string{
				"Content-Type": mw.FormDataContentType(),
			},
		},
	)

	return resp.StatusCode
}

func TestNuGetFeed(t *testing.T) {
	t.Parallel()
	remotePkg := nupkg(t, "Newtonsoft.Json", "13.0.3")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/flatcontainer/newtonsoft.json/index.json":
			_, _ = io.WriteString(w, `{"versions":["12.0.1","13.0.3"]}`)
		case "/flatcontainer/newtonsoft.json/13.0.3/newtonsoft.json.13.0.3.nupkg":
			_, _ = w.Write(remotePkg)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/nuget-local",
			body:   `{"rclass":"local","packageType":"nuget"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/nuget-remote",
			body:   `{"rclass":"remote","packageType":"nuget","url":"` + upstream.URL + `"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/nuget",
			body:   `{"rclass":"virtual","packageType":"nuget","repositories":["nuget-local","nuget-remote"],"defaultDeploymentRepo":"nuget-local"}`,
		},
	)

	for _, v := range []string{
		"1.0.0",
		"1.10.0",
		"1.2.0-beta",
	} {
		if got := nugetPush(t, srv, "/artifactory/api/nuget/v3/nuget", nupkg(t, "Relic.Demo", v)); got != 201 {
			t.Fatalf("push %s = %d", v, got)
		}
	}

	if got := nugetPush(t, srv, "/artifactory/api/nuget/nuget-local", nupkg(t, "Relic.Demo", "1.0.0")); got != 409 {
		t.Errorf("duplicate push = %d, want 409", got)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/nuget-local/Other.Pkg.2.0.0.nupkg",
			body:   string(nupkg(t, "Other.Pkg", "2.0.0")),
		},
	); resp.StatusCode != 201 {
		t.Fatalf("plain PUT = %d", resp.StatusCode)
	}

	if _, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/nuget/v3/nuget/flatcontainer/other.pkg/index.json",
		},
	); !strings.Contains(got, "2.0.0") {
		t.Errorf("plain PUT package versions = %s", got)
	}

	var keyBody bytes.Buffer

	mw := multipart.NewWriter(&keyBody)
	fw, _ := mw.CreateFormFile("package", "p.nupkg")
	_, _ = fw.Write(nupkg(t, "Key.Pkg", "1.0.0"))
	_ = mw.Close()

	resp, _ := do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/nuget/nuget-local",
		body:   keyBody.String(),
		noAuth: true,
		headers: map[string]string{
			"Content-Type":   mw.FormDataContentType(),
			"X-NuGet-ApiKey": "admin:password",
		},
	})
	if resp.StatusCode != 201 {
		t.Errorf("push with API key = %d", resp.StatusCode)
	}

	if got := nugetPush(t, srv, "/artifactory/api/nuget/nuget-local", []byte("not a zip")); got != 400 {
		t.Errorf("invalid package push = %d", got)
	}

	if props := storageProps(t, srv, "nuget-local/Relic.Demo.1.10.0.nupkg"); props["nuget.id"][0] != "Relic.Demo" ||
		props["nuget.version"][0] != "1.10.0" {
		t.Errorf("properties = %v", props)
	}

	_, body := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/nuget/v3/nuget/index.json",
	})
	if !strings.Contains(body, "PackageBaseAddress/3.0.0") ||
		!strings.Contains(body, "/api/nuget/v3/nuget/flatcontainer/") {
		t.Errorf("service index = %s", body)
	}

	var versions struct {
		Versions []string
	}

	_, body = do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/nuget/v3/nuget/flatcontainer/relic.demo/index.json",
		},
	)
	if json.Unmarshal([]byte(body), &versions) != nil ||
		strings.Join(versions.Versions, ",") != "1.0.0,1.2.0-beta,1.10.0" {
		t.Errorf("versions = %s", body)
	}

	if resp, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/nuget/v3/nuget/flatcontainer/relic.demo/1.10.0/relic.demo.1.10.0.nupkg",
		},
	); resp.StatusCode != 200 ||
		!strings.HasPrefix(got, "PK") {
		t.Errorf("nupkg = %d", resp.StatusCode)
	}

	if _, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/nuget/v3/nuget/flatcontainer/relic.demo/1.0.0/relic.demo.nuspec",
		},
	); !strings.Contains(got, "<id>Relic.Demo</id>") {
		t.Errorf("nuspec = %s", got)
	}

	if _, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/nuget/v3/nuget/registration/relic.demo/index.json",
		},
	); !strings.Contains(got, `"upper": "1.10.0"`) {
		t.Errorf("registration = %s", got)
	}

	for path, want := range map[string]int{
		"relic.demo/1.10.0.json": 200,
		"relic.demo/9.9.9.json":  404,
	} {
		if resp, got := do(t, srv, call{
			method: "GET",
			path:   "/artifactory/api/nuget/v3/nuget/registration/" + path,
		}); resp.StatusCode != want || want == 200 && !strings.Contains(got, `"version": "1.10.0"`) {
			t.Errorf("registration leaf %s = %d %s, want %d", path, resp.StatusCode, got, want)
		}
	}

	if _, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/nuget/v3/nuget/query?q=demo",
		},
	); !strings.Contains(got, `"totalHits": 1`) {
		t.Errorf("query = %s", got)
	}

	_, body = do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/nuget/v3/nuget/flatcontainer/newtonsoft.json/index.json",
		},
	)
	if !strings.Contains(body, "13.0.3") {
		t.Errorf("remote versions = %s", body)
	}

	if resp, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/nuget/v3/nuget/flatcontainer/newtonsoft.json/13.0.3/newtonsoft.json.13.0.3.nupkg",
		},
	); resp.StatusCode != 200 ||
		!strings.HasPrefix(got, "PK") {
		t.Errorf("remote nupkg = %d", resp.StatusCode)
	}

	_, found := do(t, srv, call{
		method: "POST",
		path:   "/artifactory/api/search/aql",
		body:   `items.find({"repo":"nuget-remote-cache","name":{"$match":"*.nupkg"}}).include("repo","path","name")`,
	})
	if !strings.Contains(found, `{"repo":"nuget-remote-cache","path":".","name":"Newtonsoft.Json.13.0.3.nupkg"}`) ||
		strings.Count(found, `"name"`) != 1 {
		t.Errorf("remote cache = %s", found)
	}

	if resp, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/nuget/nuget/Download/Newtonsoft.Json/13.0.3",
		},
	); resp.StatusCode != 200 ||
		!strings.HasPrefix(got, "PK") {
		t.Errorf("cached V2 download = %d", resp.StatusCode)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/nuget/v3/nuget/flatcontainer/missing/index.json",
		},
	); resp.StatusCode != 404 {
		t.Errorf("unknown package = %d", resp.StatusCode)
	}

	_, body = do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/nuget/nuget/FindPackagesById()?id='Relic.Demo'",
	})
	for _, want := range []string{
		"<d:Version>1.10.0</d:Version>",
		"<d:Version>1.2.0-beta</d:Version>",
		`src="http://`,
		"/api/nuget/nuget/Download/Relic.Demo/1.10.0",
		"<d:Authors>relic</d:Authors>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("V2 FindPackagesById lacks %q:\n%s", want, body)
		}
	}

	if _, body = do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/nuget/nuget/FindPackagesById()?id='Newtonsoft.Json'",
		},
	); !strings.Contains(body, "<d:Version>12.0.1</d:Version>") {
		t.Errorf("V2 remote versions = %s", body)
	}

	if _, body = do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/nuget/nuget/FindPackagesById()?id='Missing'",
		},
	); !strings.Contains(body, "<feed") ||
		strings.Contains(body, "<entry") {
		t.Errorf("V2 unknown id = %s", body)
	}

	if resp, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/nuget/nuget/Packages(Id='Relic.Demo',Version='1.0.0')",
		},
	); resp.StatusCode != 200 || !strings.HasPrefix(got, "<?xml") ||
		!strings.Contains(got, "<entry") ||
		!strings.Contains(got, `<d:IsLatestVersion m:type="Edm.Boolean">false<`) ||
		!strings.Contains(got, `<d:IsAbsoluteLatestVersion m:type="Edm.Boolean">false<`) {
		t.Errorf("V2 Packages() of an old version = %d %s", resp.StatusCode, got)
	}

	if _, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/nuget/nuget-local/Search()?searchTerm='e'",
		},
	); strings.Count(got, "<entry>") < 2 ||
		strings.Count(got, `<d:IsAbsoluteLatestVersion m:type="Edm.Boolean">true<`) != strings.Count(got, "<entry>") {
		t.Errorf("V2 search of two packages flags each one's latest version:\n%s", got)
	}

	if resp, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/nuget/nuget/Download/Relic.Demo/1.10.0",
		},
	); resp.StatusCode != 200 ||
		!strings.HasPrefix(got, "PK") {
		t.Errorf("V2 download = %d", resp.StatusCode)
	}

	if _, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/nuget/nuget/Search()/$count?searchTerm='demo'",
		},
	); got != "1" {
		t.Errorf("V2 search count = %q", got)
	}

	if _, got := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/nuget/nuget/$metadata",
	}); !strings.Contains(
		got,
		"FindPackagesById",
	) {
		t.Errorf("V2 metadata = %s", got)
	}
}

func TestNuGetDelete(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/nuget-local",
			body:   `{"rclass":"local","packageType":"nuget"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/nuget",
			body:   `{"rclass":"virtual","packageType":"nuget","repositories":["nuget-local"],"defaultDeploymentRepo":"nuget-local"}`,
		},
	)

	for _, v := range []string{
		"1.0.0",
		"2.0.0",
		"3.0.0",
	} {
		if got := nugetPush(t, srv, "/artifactory/api/nuget/v3/nuget", nupkg(t, "Relic.Demo", v)); got != 201 {
			t.Fatalf("push %s = %d", v, got)
		}
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
			body:   `{"name":"dev","repo":{"repositories":["nuget-local","nuget"],"include-patterns":["**"],"actions":{"users":{"dev":["read","write"]}}}}`,
		},
	)

	//nolint:gosec // G101: a test user's key
	dev := map[string]string{
		"X-NuGet-ApiKey": "dev:Dev-pass-12345",
	}
	if resp, _ := do(
		t,
		srv,
		call{
			method:  "DELETE",
			path:    "/artifactory/api/nuget/v3/nuget/Relic.Demo/1.0.0",
			noAuth:  true,
			headers: dev,
		},
	); resp.StatusCode != 403 {
		t.Errorf("delete without delete permission = %d, want 403", resp.StatusCode)
	}

	for _, p := range []string{
		"/artifactory/api/nuget/v3/nuget/Relic.Demo/1.0.0",
		"/artifactory/api/nuget/nuget/api/v2/package/Relic.Demo/2.0.0",
	} {
		if resp, body := do(t, srv, call{
			method: "DELETE",
			path:   p,
		}); resp.StatusCode != 204 {
			t.Errorf("DELETE %s = %d %s", p, resp.StatusCode, body)
		}
	}

	var versions struct {
		Versions []string
	}

	_, body := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/nuget/v3/nuget/flatcontainer/relic.demo/index.json",
		},
	)
	if json.Unmarshal([]byte(body), &versions) != nil || strings.Join(versions.Versions, ",") != "3.0.0" {
		t.Errorf("versions after delete = %s", body)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "DELETE",
			path:   "/artifactory/api/nuget/v3/nuget/Relic.Demo/9.9.9",
		},
	); resp.StatusCode != 404 {
		t.Errorf("delete of a missing version = %d, want 404", resp.StatusCode)
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/security/users/outsider",
			body:   `{"email":"o@example.com","password":"Outsider-pass-1"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/outsider",
			body:   `{"name":"outsider","repo":{"repositories":["nuget-local","nuget"],"include-patterns":["**"],"exclude-patterns":["Relic.Demo.*"],"actions":{"users":{"outsider":["read","write"]}}}}`,
		},
	)

	outsider := map[string]string{
		"X-NuGet-ApiKey": "outsider:Outsider-pass-1",
	}
	for _, p := range []string{
		"/artifactory/api/nuget/v3/nuget/flatcontainer/relic.demo/3.0.0/relic.demo.3.0.0.nupkg",
		"/artifactory/api/nuget/v3/nuget/flatcontainer/relic.demo/3.0.0/relic.demo.nuspec",
	} {
		if resp, _ := do(t, srv, call{
			method:  "GET",
			path:    p,
			noAuth:  true,
			headers: outsider,
		}); resp.StatusCode != 404 {
			t.Errorf("GET %s of an excluded package file = %d, want 404", p, resp.StatusCode)
		}
	}

	var push bytes.Buffer

	mw := multipart.NewWriter(&push)
	fw, _ := mw.CreateFormFile("package", "package.nupkg")
	_, _ = fw.Write(nupkg(t, "Relic.Demo", "4.0"))

	_ = mw.Close()
	if resp, _ := do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/nuget/v3/nuget",
		body:   push.String(),
		noAuth: true,
		headers: map[string]string{
			"X-NuGet-ApiKey": "outsider:Outsider-pass-1",
			"Content-Type":   mw.FormDataContentType(),
		},
	}); resp.StatusCode != 403 {
		t.Errorf("push of an excluded package file = %d, want 403", resp.StatusCode)
	}
}

func portablePDB(id []byte) []byte {
	const (
		versionLength = 12
		pdbStart      = 48
	)

	pdb := binary.LittleEndian.AppendUint32(nil, 0x424A5342)
	pdb = binary.LittleEndian.AppendUint32(pdb, 0x00010001)
	pdb = binary.LittleEndian.AppendUint32(pdb, 0)
	pdb = binary.LittleEndian.AppendUint32(pdb, versionLength)
	pdb = append(pdb, "PDB v1.0\x00\x00\x00\x00"...)
	pdb = binary.LittleEndian.AppendUint32(pdb, 1<<16)
	pdb = binary.LittleEndian.AppendUint32(pdb, pdbStart)
	pdb = binary.LittleEndian.AppendUint32(pdb, 20)
	pdb = append(pdb, "#Pdb\x00\x00\x00\x00"...)

	return append(pdb, id...)
}

func snupkg(t *testing.T, id, version string, pdb []byte) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	w, _ := zw.Create(id + ".nuspec")
	_, _ = io.WriteString(w, `<?xml version="1.0"?><package><metadata><id>`+id+`</id><version>`+version+
		`</version><packageTypes><packageType name="SymbolsPackage" /></packageTypes></metadata></package>`)
	lib, _ := zw.Create("lib/net8.0/" + id + ".pdb")
	_, _ = lib.Write(pdb)
	_ = zw.Close()

	return buf.Bytes()
}

func TestNuGetSymbolPackages(t *testing.T) {
	t.Parallel()

	srv := server(t)
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/repositories/nuget-local",
		body:   `{"rclass":"local","packageType":"nuget"}`,
	})

	if _, index := do(t, srv, call{
		method: "GET",
		path:   "/nuget/v3/nuget-local/index.json",
	}); !strings.Contains(index, `"SymbolPackagePublish/4.9.0"`) {
		t.Errorf("service index = %s, want a SymbolPackagePublish resource", index)
	}

	pdbID := bytes.Repeat([]byte{
		0xab,
	}, 20)
	pdb := portablePDB(pdbID)
	symbols := snupkg(t, "Acme.Lib", "1.3.0", pdb)
	pkg := nupkg(t, "Acme.Lib", "1.3.0")

	for _, push := range []struct {
		name string
		body []byte
		want int
	}{
		{
			name: "the symbol package first",
			body: symbols,
			want: 201,
		},
		{
			name: "the package after its symbols",
			body: pkg,
			want: 201,
		},
		{
			name: "the symbol package again",
			body: symbols,
			want: 409,
		},
	} {
		if got := nugetPush(t, srv, "/nuget/v3/nuget-local/", push.body); got != push.want {
			t.Errorf("push %s = %d, want %d", push.name, got, push.want)
		}
	}

	if resp, got := do(t, srv, call{
		method: "GET",
		path:   "/nuget/v3/nuget-local/flatcontainer/acme.lib/1.3.0/acme.lib.1.3.0.nupkg",
	}); resp.StatusCode != 200 || got != string(pkg) {
		t.Errorf("package download = %d, want the package, not its symbols", resp.StatusCode)
	}

	key := "acme.lib.pdb/" + strings.Repeat("ab", 16) + "ffffffff"
	for _, tc := range []struct {
		path string
		want int
	}{
		{
			path: "/nuget/nuget-local/symbols/" + key + "/acme.lib.pdb",
			want: 200,
		},
		{
			path: "/nuget/v3/nuget-local/symbols/" + strings.ToUpper(key) + "/Acme.Lib.pdb",
			want: 200,
		},
		{
			path: "/nuget/nuget-local/symbols/acme.lib.pdb/" + strings.Repeat("cd", 16) + "ffffffff/acme.lib.pdb",
			want: 404,
		},
		{
			path: "/nuget/nuget-local/symbols/" + key + "/other.pdb",
			want: 404,
		},
	} {
		resp, got := do(t, srv, call{
			method: "GET",
			path:   tc.path,
		})
		if resp.StatusCode != tc.want || tc.want == 200 && got != string(pdb) {
			t.Errorf("GET %s = %d, want %d with the PDB", tc.path, resp.StatusCode, tc.want)
		}
	}
}
