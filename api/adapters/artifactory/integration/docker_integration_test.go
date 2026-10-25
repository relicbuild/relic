//go:build integration

package integration

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func pushBlob(t *testing.T, srv *httptest.Server, name, content string) string {
	t.Helper()

	resp, _ := do(t, srv, call{
		method: "POST",
		path:   "/v2/" + name + "/blobs/uploads/",
	})
	if resp.StatusCode != 202 {
		t.Fatalf("start upload = %d", resp.StatusCode)
	}

	half := len(content) / 2

	resp, _ = do(t, srv, call{
		method: "PATCH",
		path:   resp.Header.Get("Location"),
		body:   content[:half],
	})
	if resp.StatusCode != 202 || resp.Header.Get("Range") != fmt.Sprintf("0-%d", half-1) {
		t.Fatalf("patch = %d, Range %q", resp.StatusCode, resp.Header.Get("Range"))
	}

	d := digestOf(content)

	resp, _ = do(t, srv, call{
		method: "PUT",
		path:   resp.Header.Get("Location") + "&digest=" + d,
		body:   content[half:],
	})
	if resp.StatusCode != 201 || resp.Header.Get("Docker-Content-Digest") != d {
		t.Fatalf("finish upload = %d", resp.StatusCode)
	}

	return d
}

func manifestJSON(config, layer string) string {
	return fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json",`+
		`"config":{"mediaType":"application/vnd.docker.container.image.v1+json","digest":%q,"size":2},`+
		`"layers":[{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","digest":%q,"size":5}]}`, config, layer)
}

func TestDockerRegistry(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/docker-local",
			body:   `{"rclass":"local","packageType":"docker"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/docker-prod",
			body:   `{"rclass":"local","packageType":"docker"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/docker",
			body:   `{"rclass":"virtual","packageType":"docker","repositories":["docker-local"],"defaultDeploymentRepo":"docker-local"}`,
		},
	)

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   "/v2/",
		noAuth: true,
	}); resp.StatusCode != 401 ||
		resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("anonymous /v2/ = %d, challenge %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   "/v2/",
	}); resp.StatusCode != 200 {
		t.Fatalf("/v2/ = %d", resp.StatusCode)
	}

	config := pushBlob(t, srv, "docker/org/app", "{}")

	layer := pushBlob(t, srv, "docker/org/app", "layer")
	if resp, _ := do(t, srv, call{
		method: "HEAD",
		path:   "/v2/docker/org/app/blobs/" + layer,
	}); resp.StatusCode != 200 {
		t.Fatalf("HEAD finished blob = %d", resp.StatusCode)
	}

	m := manifestJSON(config, layer)

	resp, body := do(t, srv, call{
		method: "PUT",
		path:   "/v2/docker/org/app/manifests/1.0",
		body:   m,
		headers: map[string]string{
			"Content-Type": "application/vnd.docker.distribution.manifest.v2+json",
		},
	})
	if resp.StatusCode != 201 || resp.Header.Get("Docker-Content-Digest") != digestOf(m) {
		t.Fatalf("put manifest = %d %s", resp.StatusCode, body)
	}

	for _, p := range []string{
		"org/app/1.0/manifest.json",
		"org/app/1.0/" + strings.Replace(config, ":", "__", 1),
		"org/app/1.0/" + strings.Replace(layer, ":", "__", 1),
	} {
		if resp, _ := do(t, srv, call{
			method: "GET",
			path:   "/artifactory/docker-local/" + p,
		}); resp.StatusCode != 200 {
			t.Errorf("%s = %d", p, resp.StatusCode)
		}
	}

	if _, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/docker-local/org/app/1.0/manifest.json",
		},
	); got != m {
		t.Errorf("manifest.json is not the pushed bytes")
	}

	mp := storageProps(t, srv, "docker-local/org/app/1.0/manifest.json")
	if mp["docker.manifest.digest"][0] != digestOf(m) || "sha256:"+mp["sha256"][0] != digestOf(m) {
		t.Errorf("manifest properties = %v", mp)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/docker-local/org/app/_uploads/" + strings.Replace(layer, ":", "__", 1),
		},
	); resp.StatusCode != 404 {
		t.Errorf("finished upload not cleaned up: %d", resp.StatusCode)
	}

	for _, ref := range []string{
		"/v2/docker/org/app/manifests/1.0",
		"/v2/docker-local/org/app/manifests/" + digestOf(m),
	} {
		resp, got := do(t, srv, call{
			method: "GET",
			path:   ref,
		})
		if resp.StatusCode != 200 || got != m ||
			resp.Header.Get("Content-Type") != "application/vnd.docker.distribution.manifest.v2+json" {
			t.Errorf("GET %s = %d %q", ref, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}

	resp, _ = do(t, srv, call{
		method: "HEAD",
		path:   "/v2/docker-local/org/app/manifests/1.0",
	})
	if resp.Header.Get("X-Artifactory-Docker-Registry") != "docker-local" {
		t.Errorf("X-Artifactory-Docker-Registry = %q", resp.Header.Get("X-Artifactory-Docker-Registry"))
	}

	if _, got := do(t, srv, call{
		method: "GET",
		path:   "/v2/docker/org/app/blobs/" + layer,
	}); got != "layer" {
		t.Errorf("blob = %q", got)
	}

	member := "docker-local"

	for level := 7; level >= 1; level-- {
		key := "docker-nested-" + strconv.Itoa(level)
		if resp, body := do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/api/repositories/" + key,
			body:   `{"rclass":"virtual","packageType":"docker","repositories":["` + member + `"]}`,
		}); resp.StatusCode >= 300 {
			t.Fatalf("create %s = %d %s", key, resp.StatusCode, body)
		}

		member = key
	}

	if resp, got := do(t, srv, call{
		method: "GET",
		path:   "/v2/" + member + "/org/app/manifests/1.0",
	}); resp.StatusCode != 200 || got != m {
		t.Errorf("manifest through seven nested virtuals = %d", resp.StatusCode)
	}

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   "/v2/docker/org/app/manifests/2.0",
	}); resp.StatusCode != 404 {
		t.Errorf("unknown tag = %d", resp.StatusCode)
	}

	bad := manifestJSON(config, digestOf("missing"))
	if resp, body := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/v2/docker-local/org/app/manifests/bad",
			body:   bad,
		},
	); resp.StatusCode != 400 ||
		!strings.Contains(body, "MANIFEST_BLOB_UNKNOWN") {
		t.Errorf("unknown blob = %d %s", resp.StatusCode, body)
	}

	d := digestOf("mono")
	if resp, _ := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/v2/docker-local/org/app/blobs/uploads/?digest=" + d,
			body:   "mono",
		},
	); resp.StatusCode != 201 {
		t.Fatalf("monolithic upload = %d", resp.StatusCode)
	}

	do(t, srv, call{
		method: "PUT",
		path:   "/v2/docker-local/org/app/manifests/latest",
		body:   manifestJSON(config, d),
	})
	_, tags := do(t, srv, call{
		method: "GET",
		path:   "/v2/docker-local/org/app/tags/list",
	})

	var tl struct {
		Name string
		Tags []string
	}
	if err := json.Unmarshal([]byte(tags), &tl); err != nil || tl.Name != "docker-local/org/app" ||
		strings.Join(tl.Tags, ",") != "1.0,latest" {
		t.Errorf("tags = %s", tags)
	}

	_, catalog := do(t, srv, call{
		method: "GET",
		path:   "/v2/_catalog",
	})
	if !strings.Contains(catalog, `"docker-local/org/app"`) {
		t.Errorf("catalog = %s", catalog)
	}

	var search struct {
		NumResults int `json:"num_results"`
		Results    []struct {
			Name string `json:"name"`
		} `json:"results"`
	}

	resp, found := do(t, srv, call{
		method: "GET",
		path:   "/v1/search?q=ORG%2Fapp&n=25",
	})
	err := json.Unmarshal([]byte(found), &search)

	names := make([]string, 0, len(search.Results))
	for _, result := range search.Results {
		names = append(names, result.Name)
	}

	if resp.StatusCode != 200 || err != nil || search.NumResults == 0 || !strings.Contains(
		strings.Join(names, " ")+" ",
		"docker-local/org/app ",
	) {
		t.Errorf("docker search = %d %s, want docker-local/org/app as JSON", resp.StatusCode, found)
	}

	if resp, found := do(t, srv, call{
		method: "GET",
		path:   "/v1/search?q=no-such-image",
	}); resp.StatusCode != 200 || !strings.Contains(found, `"results": []`) {
		t.Errorf("docker search without matches = %d %s", resp.StatusCode, found)
	}

	if resp, found := do(t, srv, call{
		method: "GET",
		path:   "/v1/search?q=app",
		noAuth: true,
	}); resp.StatusCode != 200 || strings.Contains(found, "org/app") {
		t.Errorf("anonymous docker search = %d %s, want no image the caller cannot read", resp.StatusCode, found)
	}

	if resp, found := do(t, srv, call{
		method: "GET",
		path:   "/v1/_ping",
	}); resp.StatusCode != 200 || !strings.HasPrefix(found, "{") {
		t.Errorf("v1 ping = %d %q, want JSON", resp.StatusCode, found)
	}

	req := `{"targetRepo":"docker-prod","dockerRepository":"org/app","targetDockerRepository":"prod/app","tag":"1.0","targetTag":"stable","copy":true}`
	if resp, body := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/docker/docker-local/v2/promote",
			body:   req,
		},
	); resp.StatusCode != 200 {
		t.Fatalf("promote = %d %s", resp.StatusCode, body)
	}

	if resp, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/docker/docker-prod/v2/prod/app/manifests/stable",
		},
	); resp.StatusCode != 200 ||
		got != m {
		t.Errorf("promoted manifest = %d", resp.StatusCode)
	}

	if pp := storageProps(
		t,
		srv,
		"docker-prod/prod/app/stable/manifest.json",
	); pp["docker.repoName"][0] != "prod/app" ||
		pp["docker.manifest"][0] != "stable" {
		t.Errorf("promoted properties = %v", pp)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "DELETE",
			path:   "/v2/docker-local/org/app/manifests/latest",
		},
	); resp.StatusCode != 202 {
		t.Errorf("delete tag = %d", resp.StatusCode)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/v2/docker-local/org/app/manifests/latest",
		},
	); resp.StatusCode != 404 {
		t.Errorf("deleted tag = %d", resp.StatusCode)
	}
}

func storageProps(t *testing.T, srv *httptest.Server, path string) map[string][]string {
	t.Helper()
	_, body := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/storage/" + path + "?properties",
	})

	var p struct {
		Properties map[string][]string
	}
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("properties of %s: %v: %s", path, err, body)
	}

	return p.Properties
}

func fakeRegistry(t *testing.T, config, layer, manifest string, manifestHits *atomic.Int32) *httptest.Server {
	t.Helper()

	var srv *httptest.Server

	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			if r.URL.Query().Get("scope") != "repository:org/tool:pull" {
				http.Error(w, "bad scope", http.StatusBadRequest)
				return
			}

			_, _ = io.WriteString(w, `{"token":"tok","expires_in":300}`)

			return
		}

		if r.Header.Get("Authorization") != "Bearer tok" {
			w.Header().
				Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="fake",scope="repository:org/tool:pull"`)
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		switch r.URL.Path {
		case "/v2/org/tool/manifests/1.0":
			manifestHits.Add(1)
			w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
			_, _ = io.WriteString(w, manifest)
		case "/v2/org/tool/blobs/" + config:
			_, _ = io.WriteString(w, "{}")
		case "/v2/org/tool/blobs/" + layer:
			_, _ = io.WriteString(w, "remote layer")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

func TestDockerRemote(t *testing.T) {
	t.Parallel()

	config, layer := digestOf("{}"), digestOf("remote layer")
	m := manifestJSON(config, layer)

	var hits atomic.Int32

	upstream := fakeRegistry(t, config, layer, m, &hits)
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/hub",
			body:   `{"rclass":"remote","packageType":"docker","url":"` + upstream.URL + `"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/docker-local",
			body:   `{"rclass":"local","packageType":"docker"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/docker",
			body:   `{"rclass":"virtual","packageType":"docker","repositories":["docker-local","hub"],"defaultDeploymentRepo":"docker-local"}`,
		},
	)

	for range 2 {
		if resp, got := do(
			t,
			srv,
			call{
				method: "GET",
				path:   "/v2/docker/org/tool/manifests/1.0",
			},
		); resp.StatusCode != 200 ||
			got != m {
			t.Fatalf("remote manifest = %d %s", resp.StatusCode, got)
		}
	}

	if n := hits.Load(); n != 1 {
		t.Errorf("upstream manifest requests = %d, want 1 (cached)", n)
	}

	for d, want := range map[string]string{
		config: "{}",
		layer:  "remote layer",
	} {
		if resp, got := do(
			t,
			srv,
			call{
				method: "GET",
				path:   "/v2/docker/org/tool/blobs/" + d,
			},
		); resp.StatusCode != 200 ||
			got != want {
			t.Errorf("remote blob %s = %d %q", d, resp.StatusCode, got)
		}
	}

	for _, name := range []string{
		"manifest.json",
		strings.Replace(config, ":", "__", 1),
		strings.Replace(layer, ":", "__", 1),
	} {
		if resp, _ := do(
			t,
			srv,
			call{
				method: "GET",
				path:   "/artifactory/api/storage/hub/org/tool/1.0/" + name,
			},
		); resp.StatusCode != 200 {
			t.Errorf("cached %s = %d", name, resp.StatusCode)
		}
	}

	_, found := do(t, srv, call{
		method: "POST",
		path:   "/artifactory/api/search/aql",
		body:   `items.find({"repo":"hub-cache","path":"org/tool/1.0"}).include("name","repo")`,
	})

	var res struct {
		Results []struct {
			Name, Repo string
		}
	}
	if err := json.Unmarshal([]byte(found), &res); err != nil || len(res.Results) != 3 ||
		res.Results[0].Repo != "hub-cache" {
		t.Errorf("AQL on hub-cache = %s", found)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/v2/hub/org/tool/manifests/2.0",
			body:   m,
		},
	); resp.StatusCode == 201 {
		t.Errorf("push to a remote succeeded")
	}
}

func TestUIDockerImage(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/docker-local",
			body:   `{"rclass":"local","packageType":"docker"}`,
		},
	)
	config := pushBlob(
		t,
		srv,
		"docker-local/org/app",
		`{"os":"linux","architecture":"amd64","created":"2026-01-02T03:04:05Z",`+
			`"config":{"Labels":{"org.opencontainers.image.source":"https://example.org/app"}},`+
			`"history":[{"created_by":"/bin/sh -c #(nop) ADD file:abc in / "},{"created_by":"/bin/sh -c #(nop)  CMD [\"sh\"]","empty_layer":true},{"created_by":"/bin/sh -c apk add curl"}]}`,
	)
	l1, l2 := pushBlob(t, srv, "docker-local/org/app", "layer-one"), pushBlob(
		t,
		srv,
		"docker-local/org/app",
		"layer-two",
	)

	m := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json",`+
		`"config":{"mediaType":"application/vnd.docker.container.image.v1+json","digest":%q,"size":100},`+
		`"layers":[{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","digest":%q,"size":10},`+
		`{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","digest":%q,"size":20}]}`, config, l1, l2)
	if resp, body := do(t, srv, call{
		method: "PUT",
		path:   "/v2/docker-local/org/app/manifests/1.0",
		body:   m,
		headers: map[string]string{
			"Content-Type": "application/vnd.docker.distribution.manifest.v2+json",
		},
	}); resp.StatusCode != 201 {
		t.Fatalf("push manifest = %d %s", resp.StatusCode, body)
	}

	pkgs := relicv1connect.NewPackageServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	img, err := pkgs.GetContainerImage(
		t.Context(),
		&relicv1.GetContainerImageRequest{
			Repository: "docker-local",
			Path:       "org/app/1.0",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	got := img
	if got.GetDigest() != digestOf(m) || got.GetOs() != "linux" || got.GetArchitecture() != "amd64" ||
		got.GetTotalSize() != 130 ||
		got.GetLabels()["org.opencontainers.image.source"] != "https://example.org/app" ||
		len(got.GetLayers()) != 2 ||
		got.GetLayers()[0].GetInstruction() != "ADD file:abc in /" ||
		got.GetLayers()[1].GetInstruction() != "RUN apk add curl" ||
		got.GetLayers()[1].GetDigest() != l2 {
		t.Errorf("image = %v", got)
	}

	if _, err := pkgs.GetContainerImage(
		t.Context(),
		&relicv1.GetContainerImageRequest{
			Repository: "docker-local",
			Path:       "org",
		},
	); code(err) != connect.CodeNotFound {
		t.Errorf("non-image folder: %v, want not found", err)
	}
}

func TestDockerPushThroughVirtualReusesMemberLayers(t *testing.T) {
	t.Parallel()

	srv := server(t)
	for _, repo := range []string{
		"base-local",
		"app-local",
	} {
		do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/repositories/" + repo,
				body:   `{"rclass":"local","packageType":"docker"}`,
			},
		)
	}

	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/repositories/apps",
		body:   `{"rclass":"virtual","packageType":"docker","repositories":["app-local","base-local"],"defaultDeploymentRepo":"app-local"}`,
	})
	put := func(path, manifest string) (int, string) {
		resp, body := do(t, srv, call{
			method: "PUT",
			path:   path,
			body:   manifest,
			headers: map[string]string{
				"Content-Type": "application/vnd.docker.distribution.manifest.v2+json",
			},
		})

		return resp.StatusCode, body
	}

	baseConfig, baseLayer := pushBlob(t, srv, "base-local/library/base", "{}"), pushBlob(
		t,
		srv,
		"base-local/library/base",
		"base-layer",
	)
	if code, body := put("/v2/base-local/library/base/manifests/1", manifestJSON(baseConfig, baseLayer)); code != 201 {
		t.Fatalf("push base = %d %s", code, body)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "HEAD",
			path:   "/v2/apps/org/app/blobs/" + baseLayer,
		},
	); resp.StatusCode != 200 {
		t.Fatalf("HEAD base layer through the virtual = %d", resp.StatusCode)
	}

	appConfig := pushBlob(t, srv, "apps/org/app", `{"app":1}`)
	if code, body := put("/v2/apps/org/app/manifests/1.0", manifestJSON(appConfig, baseLayer)); code != 201 {
		t.Fatalf("push through the virtual = %d %s", code, body)
	}

	if _, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/v2/app-local/org/app/blobs/" + baseLayer,
		},
	); got != "base-layer" {
		t.Errorf("base layer from app-local = %q", got)
	}

	baseOnly := pushBlob(t, srv, "base-local/library/base", "base-only-layer")
	if code, body := put("/v2/base-local/library/base/manifests/2", manifestJSON(baseConfig, baseOnly)); code != 201 {
		t.Fatalf("push base 2 = %d %s", code, body)
	}

	other := pushBlob(t, srv, "app-local/org/other", `{"other":1}`)
	if code, _ := put("/v2/app-local/org/other/manifests/1.0", manifestJSON(other, baseOnly)); code != 400 {
		t.Errorf("direct push referencing another repository's layer = %d, want 400", code)
	}
}

func TestDockerRemotePullStorm(t *testing.T) {
	t.Parallel()

	config, layer := digestOf("{}"), digestOf("storm layer")
	m := manifestJSON(config, layer)

	var manifestHits, layerHits atomic.Int32

	manifestArrived, layerArrived := make(chan struct{}, 64), make(chan struct{}, 64)
	releaseManifest, releaseLayer := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/org/storm/manifests/1.0":
			manifestHits.Add(1)

			manifestArrived <- struct{}{}

			<-releaseManifest
			w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
			_, _ = io.WriteString(w, m)
		case "/v2/org/storm/blobs/" + layer:
			layerHits.Add(1)

			layerArrived <- struct{}{}

			<-releaseLayer

			_, _ = io.WriteString(w, "storm layer")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	srv := server(t)
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/repositories/storm-hub",
		body:   `{"rclass":"remote","packageType":"docker","url":"` + upstream.URL + `"}`,
	})

	get := func(path string) (string, error) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
		if err != nil {
			return "", err
		}

		req.SetBasicAuth("admin", "password")

		resp, err := srv.Client().Do(req)
		if err != nil {
			return "", err
		}
		defer func() {
			_ = resp.Body.Close()
		}()

		b, err := io.ReadAll(resp.Body)
		if err == nil && resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("GET %s = %d %s", path, resp.StatusCode, b)
		}

		return string(b), err
	}

	const clients = 30

	errs := make(chan error, clients)
	for range clients {
		go func() {
			if got, err := get("/v2/storm-hub/org/storm/manifests/1.0"); err != nil || got != m {
				errs <- fmt.Errorf("manifest: %q, %w", got, err)
				return
			}

			if got, err := get("/v2/storm-hub/org/storm/blobs/" + layer); err != nil || got != "storm layer" {
				errs <- fmt.Errorf("layer: %q, %w", got, err)
				return
			}

			errs <- nil
		}()
	}

	<-manifestArrived
	time.Sleep(300 * time.Millisecond)
	close(releaseManifest)
	<-layerArrived
	time.Sleep(300 * time.Millisecond)
	close(releaseLayer)

	for range clients {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}

	if n, l := manifestHits.Load(), layerHits.Load(); n != 1 || l != 1 {
		t.Fatalf("upstream requests: manifest %d, layer %d; want 1 each", n, l)
	}
}

func TestDockerRemoteSharedLayer(t *testing.T) {
	t.Parallel()

	config, base := digestOf("{}"), digestOf("base layer")

	var baseHits atomic.Int32

	arrived, release := make(chan struct{}, 64), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/manifests/1.0"):
			w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
			_, _ = io.WriteString(w, manifestJSON(config, base))
		case strings.HasSuffix(r.URL.Path, "/blobs/"+config):
			_, _ = io.WriteString(w, "{}")
		case strings.HasSuffix(r.URL.Path, "/blobs/"+base):
			baseHits.Add(1)

			arrived <- struct{}{}

			<-release

			_, _ = io.WriteString(w, "base layer")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	srv := server(t)
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/repositories/shared-hub",
		body:   `{"rclass":"remote","packageType":"docker","url":"` + upstream.URL + `"}`,
	})

	get := func(path string) (string, error) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
		if err != nil {
			return "", err
		}

		req.SetBasicAuth("admin", "password")

		resp, err := srv.Client().Do(req)
		if err != nil {
			return "", err
		}
		defer func() {
			_ = resp.Body.Close()
		}()

		b, err := io.ReadAll(resp.Body)
		if err == nil && resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("GET %s = %d %s", path, resp.StatusCode, b)
		}

		return string(b), err
	}

	images := []string{
		"org/svc-a",
		"org/svc-b",
		"org/svc-c",
		"org/svc-d",
	}
	for _, image := range images {
		if _, err := get("/v2/shared-hub/" + image + "/manifests/1.0"); err != nil {
			t.Fatal(err)
		}
	}

	errs := make(chan error, len(images))
	for _, image := range images {
		go func() {
			got, err := get("/v2/shared-hub/" + image + "/blobs/" + base)
			if err == nil && got != "base layer" {
				err = fmt.Errorf("%s: base layer = %q", image, got)
			}

			errs <- err
		}()
	}

	<-arrived
	time.Sleep(300 * time.Millisecond)
	close(release)

	for range images {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}

	if n := baseHits.Load(); n != 1 {
		t.Fatalf("upstream requests for the shared layer = %d, want 1", n)
	}
}

func TestDockerOCIDistribution(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/oci-local",
			body:   `{"rclass":"local","packageType":"docker"}`,
		},
	)

	for _, method := range []string{
		"GET",
		"HEAD",
	} {
		if resp, _ := do(t, srv, call{
			method: method,
			path:   "/v2/oci-local/app/manifests/.INVALID_MANIFEST_NAME",
		}); resp.StatusCode != 404 {
			t.Fatalf("%s invalid reference = %d, want 404", method, resp.StatusCode)
		}
	}

	resp, _ := do(t, srv, call{
		method: "POST",
		path:   "/v2/oci-local/app/blobs/uploads/",
	})
	start := resp.Header.Get("Location")
	content := "chunked content"
	half := len(content) / 2

	resp, _ = do(t, srv, call{
		method: "PATCH",
		path:   start,
		body:   content[:half],
		headers: map[string]string{
			"Content-Range": fmt.Sprintf("0-%d", half-1),
		},
	})
	if resp.StatusCode != 202 {
		t.Fatalf("first chunk = %d", resp.StatusCode)
	}

	next := resp.Header.Get("Location")

	wantRange := fmt.Sprintf("0-%d", half-1)
	if resp, _ = do(t, srv, call{
		method: "PATCH",
		path:   next,
		body:   content[:half],
		headers: map[string]string{
			"Content-Range": fmt.Sprintf("0-%d", half-1),
		},
	}); resp.StatusCode != 416 ||
		resp.Header.Get("Range") != wantRange {
		t.Fatalf("chunk sent again = %d, Range %q; want 416, %q", resp.StatusCode, resp.Header.Get("Range"), wantRange)
	}

	if resp, _ = do(t, srv, call{
		method: "GET",
		path:   start,
	}); resp.StatusCode != 204 ||
		resp.Header.Get("Range") != wantRange {
		t.Fatalf(
			"status of a stale URL = %d, Range %q; want 204, %q",
			resp.StatusCode,
			resp.Header.Get("Range"),
			wantRange,
		)
	}

	d := digestOf(content)
	if resp, _ = do(t, srv, call{
		method: "PUT",
		path:   next + "&digest=" + d,
		body:   content[half:],
	}); resp.StatusCode != 201 {
		t.Fatalf("finish upload = %d", resp.StatusCode)
	}

	config := pushBlob(t, srv, "oci-local/app", "{}")
	layer := pushBlob(t, srv, "oci-local/app", "layer")

	image := manifestJSON(config, layer)
	if resp, _ = do(t, srv, call{
		method: "PUT",
		path:   "/v2/oci-local/app/manifests/1.0",
		body:   image,
		headers: map[string]string{
			"Content-Type": "application/vnd.docker.distribution.manifest.v2+json",
		},
	}); resp.StatusCode != 201 {
		t.Fatalf("push image = %d", resp.StatusCode)
	}

	subject := digestOf(image)

	artifact := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json",`+
		`"artifactType":"application/vnd.relic.test","config":{"mediaType":"application/vnd.oci.empty.v1+json",`+
		`"digest":%q,"size":2},"layers":[{"mediaType":"application/octet-stream","digest":%q,"size":5}],`+
		`"subject":{"mediaType":"application/vnd.docker.distribution.manifest.v2+json","digest":%q,"size":%d},`+
		`"annotations":{"org.example":"yes"}}`, config, layer, subject, len(image))
	if resp, _ = do(t, srv, call{
		method: "PUT",
		path:   "/v2/oci-local/app/manifests/" + digestOf(artifact),
		body:   artifact,
		headers: map[string]string{
			"Content-Type": "application/vnd.oci.image.manifest.v1+json",
		},
	}); resp.StatusCode != 201 ||
		resp.Header.Get("OCI-Subject") != subject {
		t.Fatalf("push referrer = %d, OCI-Subject %q", resp.StatusCode, resp.Header.Get("OCI-Subject"))
	}

	type index struct {
		MediaType string `json:"mediaType"`
		Manifests []struct {
			Digest       string            `json:"digest"`
			ArtifactType string            `json:"artifactType"`
			Annotations  map[string]string `json:"annotations"`
		} `json:"manifests"`
	}

	tests := []struct {
		name   string
		path   string
		want   []string
		filter string
	}{
		{
			name: "referrers of the image",
			path: "/v2/oci-local/app/referrers/" + subject,
			want: []string{
				digestOf(artifact),
			},
		},
		{
			name: "filtered by artifact type",
			path: "/v2/oci-local/app/referrers/" + subject + "?artifactType=application/vnd.relic.test",
			want: []string{
				digestOf(artifact),
			},
			filter: "artifactType",
		},
		{
			name:   "filtered by another type",
			path:   "/v2/oci-local/app/referrers/" + subject + "?artifactType=application/vnd.relic.other",
			filter: "artifactType",
		},
		{
			name: "no referrers",
			path: "/v2/oci-local/app/referrers/" + digestOf("nothing refers to this"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := do(t, srv, call{
				method: "GET",
				path:   tt.path,
			})
			if resp.StatusCode != 200 || resp.Header.Get("OCI-Filters-Applied") != tt.filter ||
				resp.Header.Get("Content-Type") != "application/vnd.oci.image.index.v1+json" {
				t.Fatalf(
					"referrers = %d, OCI-Filters-Applied %q, Content-Type %q: %s",
					resp.StatusCode,
					resp.Header.Get("OCI-Filters-Applied"),
					resp.Header.Get("Content-Type"),
					body,
				)
			}

			var got index
			if err := json.Unmarshal([]byte(body), &got); err != nil {
				t.Fatal(err)
			}

			if got.MediaType != "application/vnd.oci.image.index.v1+json" || len(got.Manifests) != len(tt.want) {
				t.Fatalf("referrers = %s, want %v", body, tt.want)
			}

			for i, m := range got.Manifests {
				if m.Digest != tt.want[i] || m.ArtifactType != "application/vnd.relic.test" ||
					m.Annotations["org.example"] != "yes" {
					t.Fatalf("referrer %d = %+v", i, m)
				}
			}
		})
	}
}

func TestDockerPushCannotReachLayersThePusherCannotRead(t *testing.T) {
	t.Parallel()

	srv := server(t)
	admin := func(c call) (reply, string) {
		return do(t, srv, c)
	}
	dev := func(c call) (reply, string) {
		c.noAuth = true
		if c.headers == nil {
			c.headers = map[string]string{}
		}

		c.headers["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte("dev:Dev-pass-12345"))

		return do(t, srv, c)
	}

	admin(call{
		method: "PUT",
		path:   "/artifactory/api/repositories/docker-local",
		body:   `{"rclass":"local","packageType":"docker"}`,
	})
	admin(call{
		method: "PUT",
		path:   "/artifactory/api/security/users/dev",
		body:   `{"email":"d@example.com","password":"Dev-pass-12345"}`,
	})
	admin(call{
		method: "POST",
		path:   "/artifactory/api/v2/security/permissions/dev",
		body: `{"name":"dev","repo":{"repositories":["docker-local"],"include-patterns":["public/**"],` +
			`"actions":{"users":{"dev":["read","write"]}}}}`,
	})

	secret := pushBlob(t, srv, "docker-local/secret/app", "a secret layer")

	config := `{"architecture":"amd64","os":"linux"}`
	if resp, body := dev(call{
		method: "POST",
		path:   "/v2/docker-local/public/app/blobs/uploads/?digest=" + digestOf(config),
		body:   config,
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("dev pushes a config = %d %s", resp.StatusCode, body)
	}

	if resp, body := dev(call{
		method: "PUT",
		path:   "/v2/docker-local/public/app/manifests/1.0",
		body:   manifestJSON(digestOf(config), secret),
		headers: map[string]string{
			"Content-Type": "application/vnd.docker.distribution.manifest.v2+json",
		},
	}); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "MANIFEST_BLOB_UNKNOWN") {
		t.Errorf(
			"a manifest naming a layer dev cannot read = %d %s, want 400 MANIFEST_BLOB_UNKNOWN",
			resp.StatusCode,
			body,
		)
	}

	if resp, _ := dev(call{
		method: "POST",
		path:   "/v2/docker-local/public/app/blobs/uploads/?mount=" + secret + "&from=docker-local/secret/app",
	}); resp.StatusCode == http.StatusCreated {
		t.Error("dev mounted a layer it cannot read")
	}
}

func TestImmutableRegistryRefusesMovingATag(t *testing.T) {
	t.Parallel()

	srv := server(t)
	if resp, body := do(t, srv, call{
		method: http.MethodPost,
		path:   "/api/v1/repositories",
		body:   `{"name":"images","class":"REPOSITORY_CLASS_LOCAL","format":"FORMAT_DOCKER","immutable":true}`,
		headers: map[string]string{
			"Content-Type": "application/json",
		},
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("create = %d %s", resp.StatusCode, body)
	}

	config := pushBlob(t, srv, "images/app", `{"os":"linux"}`)
	first := pushBlob(t, srv, "images/app", "first layer")
	second := pushBlob(t, srv, "images/app", "second layer")

	put := func(layer string) int {
		resp, _ := do(t, srv, call{
			method: "PUT",
			path:   "/v2/images/app/manifests/1.0",
			body:   manifestJSON(config, layer),
			headers: map[string]string{
				"Content-Type": "application/vnd.docker.distribution.manifest.v2+json",
			},
		})

		return resp.StatusCode
	}

	if got := put(first); got != http.StatusCreated {
		t.Fatalf("first push of 1.0 = %d", got)
	}

	if got := put(first); got != http.StatusCreated {
		t.Errorf("pushing the same manifest again = %d, want 201", got)
	}

	if got := put(second); got != http.StatusForbidden {
		t.Errorf("moving tag 1.0 to another manifest = %d, want 403", got)
	}
}

func TestDockerTagList(t *testing.T) {
	t.Parallel()

	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/docker-local",
			body:   `{"rclass":"local","packageType":"docker"}`,
		},
	)

	config := pushBlob(t, srv, "docker-local/app", "{}")
	layer := pushBlob(t, srv, "docker-local/app", "layer")

	if resp, _ := do(t, srv, call{
		method: "PUT",
		path:   "/v2/docker-local/app/manifests/1.0",
		body:   manifestJSON(config, layer),
		headers: map[string]string{
			"Content-Type": "application/vnd.docker.distribution.manifest.v2+json",
		},
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("push = %d", resp.StatusCode)
	}

	tests := []struct {
		name   string
		method string
		path   string
		want   int
		code   string
	}{
		{
			name:   "an image",
			method: "GET",
			path:   "/v2/docker-local/app/tags/list",
			want:   http.StatusOK,
		},
		{
			name:   "an image never pushed",
			method: "GET",
			path:   "/v2/docker-local/nope/tags/list",
			want:   http.StatusNotFound,
			code:   "NAME_UNKNOWN",
		},
		{
			name:   "a delete",
			method: "DELETE",
			path:   "/v2/docker-local/app/tags/list",
			want:   http.StatusMethodNotAllowed,
			code:   "UNSUPPORTED",
		},
		{
			name:   "a post",
			method: "POST",
			path:   "/v2/docker-local/app/tags/list",
			want:   http.StatusMethodNotAllowed,
			code:   "UNSUPPORTED",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := do(t, srv, call{
				method: tt.method,
				path:   tt.path,
			})
			if resp.StatusCode != tt.want || !strings.Contains(body, tt.code) {
				t.Errorf("%s %s = %d %s, want %d %s", tt.method, tt.path, resp.StatusCode, body, tt.want, tt.code)
			}
		})
	}

	if resp, _ := do(t, srv, call{
		method: "DELETE",
		path:   "/v2/docker-local/app/manifests/1.0",
	}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("delete the only tag = %d", resp.StatusCode)
	}

	if resp, body := do(t, srv, call{
		method: "GET",
		path:   "/v2/docker-local/app/tags/list",
	}); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"tags": []`) {
		t.Errorf("tags of an image without tags = %d %s, want 200 and none", resp.StatusCode, body)
	}
}

func TestDockerRegistryRefusesBadCredentialsInItsErrorFormat(t *testing.T) {
	t.Parallel()

	srv := server(t)

	tests := []struct {
		name          string
		authorization string
	}{
		{
			name:          "a wrong password",
			authorization: "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:wrong-password")),
		},
		{
			name:          "a malformed token",
			authorization: "Bearer garbage",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp, body := do(t, srv, call{
				method: "GET",
				path:   "/v2/",
				noAuth: true,
				headers: map[string]string{
					"Authorization": tt.authorization,
				},
			})

			var got struct {
				Errors []struct {
					Code string `json:"code"`
				} `json:"errors"`
			}
			if resp.StatusCode != http.StatusUnauthorized || json.Unmarshal([]byte(body), &got) != nil ||
				len(got.Errors) != 1 || got.Errors[0].Code != "UNAUTHORIZED" {
				t.Errorf("GET /v2/ = %d %s, want 401 UNAUTHORIZED", resp.StatusCode, body)
			}

			if v := resp.Header.Get("Docker-Distribution-API-Version"); v != "registry/2.0" {
				t.Errorf("Docker-Distribution-API-Version = %q, want registry/2.0", v)
			}
		})
	}
}
