//go:build integration

package integration

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"go.yaml.in/yaml/v3"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func chartTgz(t *testing.T, name, version string) string {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)

	tw := tar.NewWriter(gz)
	for file, body := range map[string]string{
		name + "/Chart.yaml":  "apiVersion: v2\nname: " + name + "\nversion: " + version + "\ndescription: test chart\nappVersion: \"1.0\"\n",
		name + "/values.yaml": "replicas: 1\n",
	} {
		_ = tw.WriteHeader(&tar.Header{
			Name: file,
			Mode: 0o644,
			Size: int64(len(body)),
		})
		_, _ = io.WriteString(tw, body)
	}

	_ = tw.Close()
	_ = gz.Close()

	return buf.String()
}

type helmIndex struct {
	Entries map[string][]struct {
		Version string
		Digest  string
		URLs    []string `yaml:"urls"`
	}
}

func getIndex(t *testing.T, srv *httptest.Server, path string) helmIndex {
	t.Helper()
	resp, body := do(t, srv, call{
		method: "GET",
		path:   path,
	})

	var idx helmIndex
	if resp.StatusCode != 200 || yaml.Unmarshal([]byte(body), &idx) != nil {
		t.Fatalf("GET %s = %d %s", path, resp.StatusCode, body)
	}

	return idx
}

func TestHelmRepository(t *testing.T) {
	t.Parallel()
	upstreamChart := chartTgz(t, "nginx", "15.0.0")

	var upstream *httptest.Server

	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/charts/index.yaml":
			_, _ = io.WriteString(
				w,
				"apiVersion: v1\nentries:\n  nginx:\n  - name: nginx\n    version: 15.0.0\n    urls:\n    - "+upstream.URL+"/charts/nginx-15.0.0.tgz\n",
			)
		case "/charts/nginx-15.0.0.tgz":
			_, _ = io.WriteString(w, upstreamChart)
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
			path:   "/artifactory/api/repositories/helm-local",
			body:   `{"rclass":"local","packageType":"helm"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/helm-remote",
			body:   `{"rclass":"remote","packageType":"helm","url":"` + upstream.URL + `/charts"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/helm",
			body:   `{"rclass":"virtual","packageType":"helm","repositories":["helm-local","helm-remote"]}`,
		},
	)

	for _, v := range []string{
		"0.1.0",
		"0.2.0",
		"0.10.0",
	} {
		if resp, _ := do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/helm-local/demo-" + v + ".tgz",
				body:   chartTgz(t, "demo", v),
			},
		); resp.StatusCode != 201 {
			t.Fatalf("deploy %s = %d", v, resp.StatusCode)
		}
	}

	if props := storageProps(
		t,
		srv,
		"helm-local/demo-0.2.0.tgz",
	); props["chart.name"][0] != "demo" || props["chart.version"][0] != "0.2.0" ||
		props["appVersion"][0] != "1.0" {
		t.Errorf("chart properties = %v", props)
	}

	idx := getIndex(t, srv, "/artifactory/helm-local/index.yaml")

	demo := idx.Entries["demo"]
	if len(demo) != 3 || demo[0].Version != "0.10.0" || demo[0].URLs[0] != "demo-0.10.0.tgz" ||
		len(demo[0].Digest) != 64 {
		t.Errorf("local index = %+v", idx)
	}

	vidx := getIndex(t, srv, "/artifactory/api/helm/helm/index.yaml")
	if len(vidx.Entries["demo"]) != 3 || len(vidx.Entries["nginx"]) != 1 ||
		vidx.Entries["nginx"][0].URLs[0] != "nginx-15.0.0.tgz" {
		t.Errorf("virtual index = %+v", vidx)
	}

	for p, want := range map[string]string{
		"/artifactory/api/helm/helm/nginx-15.0.0.tgz": "name: nginx\nversion: 15.0.0\n",
		"/artifactory/api/helm/helm/demo-0.1.0.tgz":   "name: demo\nversion: 0.1.0\n",
	} {
		resp, got := do(t, srv, call{
			method: "GET",
			path:   p,
		})
		if chart := chartYAML(t, got); resp.StatusCode != 200 || !strings.Contains(chart, want) {
			t.Errorf("GET %s = %d with Chart.yaml %q, want %q", p, resp.StatusCode, chart, want)
		}
	}
}

func chartYAML(t *testing.T, tgz string) string {
	t.Helper()

	gz, err := gzip.NewReader(strings.NewReader(tgz))
	if err != nil {
		return ""
	}

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err != nil {
			return ""
		}

		if strings.HasSuffix(header.Name, "/Chart.yaml") {
			body, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}

			return string(body)
		}
	}
}

func TestRelicAPIPackageInfo(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/charts",
			body:   `{"rclass":"local","packageType":"helm"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/py-local",
			body:   `{"rclass":"local","packageType":"pypi"}`,
		},
	)
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/charts/web-1.2.0.tgz",
		body:   chartTgz(t, "web", "1.2.0"),
	})

	var whl bytes.Buffer

	zw := zip.NewWriter(&whl)
	f, _ := zw.Create("demo-1.0.dist-info/METADATA")
	_, _ = f.Write(
		[]byte("Metadata-Version: 2.1\nName: demo\nVersion: 1.0\nSummary: A demo\nRequires-Dist: requests>=2\n"),
	)
	_ = zw.Close()

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/py-local/demo/1.0/demo-1.0-py3-none-any.whl",
			body:   whl.String(),
		},
	)

	ctx := t.Context()
	pkgs := relicv1connect.NewPackageServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	chart, err := pkgs.GetHelmChart(
		ctx,
		&relicv1.GetHelmChartRequest{
			Repository: "charts",
			Path:       "web-1.2.0.tgz",
		},
	)
	if err != nil || chart.GetName() != "web" || chart.GetAppVersion() != "1.0" {
		t.Errorf("chart = %v %v", chart, err)
	}

	wheel, err := pkgs.GetPythonPackage(
		ctx,
		&relicv1.GetPythonPackageRequest{
			Repository: "py-local",
			Path:       "demo/1.0/demo-1.0-py3-none-any.whl",
		},
	)
	if err != nil || wheel.GetName() != "demo" || len(wheel.GetRequiresDist()) != 1 {
		t.Errorf("wheel = %v %v", wheel, err)
	}

	if _, err := pkgs.GetHelmChart(
		ctx,
		&relicv1.GetHelmChartRequest{
			Repository: "charts",
		},
	); code(err) != connect.CodeNotFound &&
		code(err) != connect.CodeInvalidArgument {
		t.Errorf("repository root: %v", err)
	}
}
