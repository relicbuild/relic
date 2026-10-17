//go:build integration

package integration

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTerraformProviderMirror(t *testing.T) {
	t.Parallel()

	pkg := func(body string) []byte {
		var buf bytes.Buffer

		zw := zip.NewWriter(&buf)
		f, _ := zw.Create("terraform-provider-random")
		_, _ = io.WriteString(f, body)
		_ = zw.Close()

		return buf.Bytes()
	}
	remoteZip := pkg("remote 3.6.3")
	sum := sha256.Sum256(remoteZip)

	var upstream *httptest.Server

	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/providers/hashicorp/random/versions":
			_, _ = io.WriteString(
				w,
				`{"versions":[{"version":"3.6.3","protocols":["5.0"],"platforms":[{"os":"linux","arch":"amd64"}]}]}`,
			)
		case "/v1/providers/hashicorp/random/3.6.3/download/linux/amd64":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"filename":     "terraform-provider-random_3.6.3_linux_amd64.zip",
				"shasum":       hex.EncodeToString(sum[:]),
				"download_url": upstream.URL + "/releases/terraform-provider-random_3.6.3_linux_amd64.zip",
			})
		case "/releases/terraform-provider-random_3.6.3_linux_amd64.zip":
			_, _ = w.Write(remoteZip)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	host := strings.TrimPrefix(upstream.URL, "http://")
	host = host[:strings.LastIndex(host, ":")]
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/tf-local",
			body:   `{"rclass":"local","packageType":"terraform"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/tf-remote",
			body:   `{"rclass":"remote","packageType":"terraform","url":"` + upstream.URL + `"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/tf",
			body:   `{"rclass":"virtual","packageType":"terraform","repositories":["tf-local","tf-remote"]}`,
		},
	)

	localZip := pkg("local 3.7.0")
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/tf-local/" + host + "/hashicorp/random/terraform-provider-random_3.7.0_linux_amd64.zip",
			body:   string(localZip),
		},
	)

	base := "/artifactory/api/terraform/tf/providers/" + host + "/hashicorp/random/"

	var index struct {
		Versions map[string]any `json:"versions"`
	}
	if code := getJSON(
		t,
		srv,
		base+"index.json",
		&index,
	); code != 200 || len(index.Versions) != 2 || index.Versions["3.6.3"] == nil ||
		index.Versions["3.7.0"] == nil {
		t.Fatalf("index.json = %d %v", code, index)
	}

	type archives struct {
		Archives map[string]struct {
			URL    string   `json:"url"`
			Hashes []string `json:"hashes"`
		} `json:"archives"`
	}

	for _, tt := range []struct {
		version string
		body    []byte
	}{
		{
			"3.6.3",
			remoteZip,
		},
		{
			"3.7.0",
			localZip,
		},
	} {
		var doc archives
		getJSON(t, srv, base+tt.version+".json", &doc)
		a := doc.Archives["linux_amd64"]
		want := sha256.Sum256(tt.body)

		archive := "/artifactory/api/terraform/provider-archive/tf/" + host + "/hashicorp/random/terraform-provider-random_" + tt.version + "_linux_amd64.zip?"
		if !strings.HasPrefix(a.URL, archive) || len(a.Hashes) != 1 ||
			a.Hashes[0] != "zh:"+hex.EncodeToString(want[:]) {
			t.Errorf("%s.json = %+v", tt.version, doc)
		}

		if resp, got := do(t, srv, call{
			method: "GET",
			path:   a.URL,
			noAuth: true,
		}); resp.StatusCode != 200 ||
			got != string(tt.body) {
			t.Errorf("%s archive = %d", tt.version, resp.StatusCode)
		}

		if resp, _ := do(
			t,
			srv,
			call{
				method: "GET",
				path:   strings.Split(a.URL, "?")[0],
				noAuth: true,
			},
		); resp.StatusCode != 403 {
			t.Errorf("%s archive without a signature = %d, want 403", tt.version, resp.StatusCode)
		}
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/storage/tf-remote/" + host + "/hashicorp/random/terraform-provider-random_3.6.3_linux_amd64.zip",
		},
	); resp.StatusCode != 200 {
		t.Errorf("remote cache = %d", resp.StatusCode)
	}

	for _, p := range []string{
		"/artifactory/api/terraform/tf/providers/registry.opentofu.org/hashicorp/random/index.json",
		"/artifactory/api/terraform/tf/providers/" + host + "/hashicorp/random/9.9.9.json",
		"/artifactory/api/terraform/tf/providers/" + host + "/hashicorp/../x/index.json",
	} {
		if resp, _ := do(t, srv, call{
			method: "GET",
			path:   p,
		}); resp.StatusCode != 404 {
			t.Errorf("GET %s = %d, want 404", p, resp.StatusCode)
		}
	}

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   base + "index.json",
		noAuth: true,
	}); resp.StatusCode != 401 {
		t.Errorf("anonymous index = %d, want 401", resp.StatusCode)
	}
}
