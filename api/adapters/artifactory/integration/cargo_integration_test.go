//go:build integration

package integration

import (
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func cargoPublishBody(name, version string, crate []byte) string {
	meta := `{"name":"` + name + `","vers":"` + version + `","deps":[],"features":{},"links":null}`

	var b []byte

	b = binary.LittleEndian.AppendUint32(b, uint32(len(meta))) //nolint:gosec // test data
	b = append(b, meta...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(crate))) //nolint:gosec // test data

	return string(append(b, crate...))
}

func TestCargoRegistry(t *testing.T) {
	t.Parallel()
	srv := server(t)

	const api = "/artifactory/api/cargo/cargo-local"

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/cargo-local",
			body:   `{"rclass":"local","packageType":"cargo"}`,
		},
	)

	_, cfg := do(t, srv, call{
		method: "GET",
		path:   api + "/index/config.json",
	})
	if !strings.Contains(cfg, `"auth-required":true`) || !strings.Contains(cfg, `/api/cargo/cargo-local/v1/crates"`) {
		t.Errorf("config.json = %s", cfg)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   api + "/index/config.json",
			noAuth: true,
		},
	); resp.StatusCode != 401 {
		t.Errorf("anonymous config.json = %d, want 401", resp.StatusCode)
	}

	for _, v := range []string{
		"0.1.0",
		"0.2.0",
	} {
		if resp, body := do(
			t,
			srv,
			call{
				method: "PUT",
				path:   api + "/api/v1/crates/new",
				body:   cargoPublishBody("My_Crate", v, []byte("crate "+v)),
			},
		); resp.StatusCode != 200 {
			t.Fatalf("publish %s = %d %s", v, resp.StatusCode, body)
		}
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   api + "/api/v1/crates/new",
			body:   cargoPublishBody("my_crate", "0.1.0", []byte("x")),
		},
	); resp.StatusCode != 409 {
		t.Errorf("republish = %d, want 409", resp.StatusCode)
	}

	if props := storageProps(
		t,
		srv,
		"cargo-local/crates/My_Crate/My_Crate-0.1.0.crate",
	); props["cargo.version"][0] != "0.1.0" {
		t.Errorf("props = %v", props)
	}

	resp, index := do(t, srv, call{
		method: "GET",
		path:   api + "/index/my/_c/my_crate",
	})
	if resp.StatusCode != 200 || strings.Count(index, "\n") != 2 ||
		!strings.Contains(index, `"cksum":"`+sha256Hex("crate 0.1.0")+`"`) {
		t.Fatalf("index = %d %q", resp.StatusCode, index)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   api + "/index/my/_c/my_crate",
			headers: map[string]string{
				"If-None-Match": resp.Header.Get("ETag"),
			},
		},
	); resp.StatusCode != 304 {
		t.Errorf("conditional index = %d, want 304", resp.StatusCode)
	}

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   api + "/index/wr/on/my_crate",
	}); resp.StatusCode != 404 {
		t.Errorf("wrong prefix = %d, want 404", resp.StatusCode)
	}

	if resp, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   api + "/v1/crates/my_crate/0.2.0/download",
		},
	); resp.StatusCode != 200 ||
		got != "crate 0.2.0" || resp.Header.Get("Content-Type") != "application/gzip" {
		t.Errorf("download = %d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), got)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "DELETE",
			path:   api + "/api/v1/crates/My_Crate/0.1.0/yank",
		},
	); resp.StatusCode != 200 {
		t.Errorf("yank = %d", resp.StatusCode)
	}

	if _, index := do(t, srv, call{
		method: "GET",
		path:   api + "/index/my/_c/my_crate",
	}); !strings.Contains(
		index,
		`"vers":"0.1.0","deps":[],"cksum":"`+sha256Hex("crate 0.1.0")+`","features":{},"yanked":true`,
	) {
		t.Errorf("index after yank = %q", index)
	}

	if _, found := do(t, srv, call{
		method: "GET",
		path:   api + "/api/v1/crates?q=crate",
	}); !strings.Contains(
		found,
		`"max_version":"0.2.0"`,
	) {
		t.Errorf("search = %s", found)
	}

	if resp, body := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   api + "/api/v1/crates/new",
			body:   cargoPublishBody("other_crate", "1.0.0", []byte("other crate")),
		},
	); resp.StatusCode != 200 {
		t.Fatalf("publish other_crate = %d %s", resp.StatusCode, body)
	}

	var page struct {
		Crates []json.RawMessage `json:"crates"`
		Meta   struct {
			Total int `json:"total"`
		} `json:"meta"`
	}

	_, found := do(t, srv, call{
		method: "GET",
		path:   api + "/api/v1/crates?q=crate&per_page=1",
	})
	if err := json.Unmarshal([]byte(found), &page); err != nil || len(page.Crates) != 1 || page.Meta.Total != 2 {
		t.Errorf("search page = %s", found)
	}

	var upstream *httptest.Server

	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/config.json":
			_, _ = w.Write([]byte(`{"dl":"` + upstream.URL + `/crates","api":"` + upstream.URL + `"}`))
		case "/se/rd/serde":
			_, _ = w.Write(
				[]byte(
					`{"name":"serde","vers":"1.0.0","deps":[],` +
						`"cksum":"` + sha256Hex("serde crate") + `","features":{},"yanked":false}` + "\n",
				),
			)
		case "/crates/serde/1.0.0/download":
			_, _ = w.Write([]byte("serde crate"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/cargo-remote",
			body:   `{"rclass":"remote","packageType":"cargo","url":"` + upstream.URL + `"}`,
		},
	)

	const rapi = "/artifactory/api/cargo/cargo-remote"
	if resp, index := do(t, srv, call{
		method: "GET",
		path:   rapi + "/index/se/rd/serde",
	}); resp.StatusCode != 200 ||
		!strings.Contains(index, `"name":"serde"`) {
		t.Errorf("remote index = %d %q", resp.StatusCode, index)
	}

	if resp, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   rapi + "/v1/crates/serde/1.0.0/download",
		},
	); resp.StatusCode != 200 ||
		got != "serde crate" {
		t.Errorf("remote download = %d %q", resp.StatusCode, got)
	}

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   rapi + "/index/no/pe/nope",
	}); resp.StatusCode != 404 {
		t.Errorf("remote missing crate = %d, want 404", resp.StatusCode)
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/cargo",
			body:   `{"rclass":"virtual","packageType":"cargo","repositories":["cargo-local","cargo-remote"],"defaultDeploymentRepo":"cargo-local"}`,
		},
	)

	const vapi = "/artifactory/api/cargo/cargo"
	if _, cfg := do(t, srv, call{
		method: "GET",
		path:   vapi + "/index/config.json",
	}); !strings.Contains(
		cfg,
		`/api/cargo/cargo/v1/crates"`,
	) {
		t.Errorf("virtual config.json = %s", cfg)
	}

	for _, c := range []struct {
		index, want string
	}{
		{
			"/index/se/rd/serde",
			`"name":"serde"`,
		},
		{
			"/index/my/_c/my_crate",
			`"vers":"0.2.0"`,
		},
	} {
		if resp, index := do(t, srv, call{
			method: "GET",
			path:   vapi + c.index,
		}); resp.StatusCode != 200 ||
			!strings.Contains(index, c.want) {
			t.Errorf("virtual index %s = %d %q", c.index, resp.StatusCode, index)
		}
	}

	for _, c := range []struct {
		path, want string
	}{
		{
			"/v1/crates/serde/1.0.0/download",
			"serde crate",
		},
		{
			"/v1/crates/my_crate/0.2.0/download",
			"crate 0.2.0",
		},
	} {
		if resp, got := do(t, srv, call{
			method: "GET",
			path:   vapi + c.path,
		}); resp.StatusCode != 200 || got != c.want {
			t.Errorf("virtual download %s = %d %q", c.path, resp.StatusCode, got)
		}
	}

	if resp, body := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   vapi + "/api/v1/crates/new",
			body:   cargoPublishBody("via_virtual", "1.0.0", []byte("v")),
		},
	); resp.StatusCode != 200 {
		t.Errorf("publish through the virtual = %d %s", resp.StatusCode, body)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/storage/cargo-local/crates/via_virtual/via_virtual-1.0.0.crate",
		},
	); resp.StatusCode != 200 {
		t.Errorf("virtual publish landed = %d", resp.StatusCode)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "DELETE",
			path:   vapi + "/api/v1/crates/via_virtual/1.0.0/yank",
		},
	); resp.StatusCode != 200 {
		t.Errorf("yank through the virtual = %d", resp.StatusCode)
	}

	if _, found := do(t, srv, call{
		method: "GET",
		path:   vapi + "/api/v1/crates?q=via",
	}); !strings.Contains(
		found,
		`"via_virtual"`,
	) {
		t.Errorf("virtual search = %s", found)
	}
}
