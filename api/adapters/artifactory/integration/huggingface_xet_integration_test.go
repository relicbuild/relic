//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"net/http/httptest"
	"strings"
	"testing"

	"relic/internal/artifacts/formats/huggingface/xet/xettest"
)

type xetToken struct {
	AccessToken string `json:"accessToken"`
	CasURL      string `json:"casUrl"`
	Exp         int64  `json:"exp"`
}

func issueXetToken(t *testing.T, srv *httptest.Server, path string) xetToken {
	t.Helper()

	resp, body := do(t, srv, call{
		method: "GET",
		path:   path,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s = %d %s", path, resp.StatusCode, body)
	}

	var token xetToken
	if err := json.Unmarshal([]byte(body), &token); err != nil {
		t.Fatal(err)
	}

	if resp.Header.Get("X-Xet-Access-Token") != token.AccessToken ||
		resp.Header.Get("X-Xet-Cas-Url") != token.CasURL ||
		resp.Header.Get("X-Xet-Token-Expiration") == "" ||
		!strings.HasPrefix(token.AccessToken, "xet_") {
		t.Fatalf("token headers %v do not match the body %s", resp.Header, body)
	}

	return token
}

func casCall(t *testing.T, srv *httptest.Server, token xetToken, method, path, body string) (reply, string) {
	t.Helper()

	return do(t, srv, call{
		method: method,
		path:   strings.TrimPrefix(token.CasURL, srv.URL) + path,
		body:   body,
		headers: map[string]string{
			"Authorization": "Bearer " + token.AccessToken,
		},
	})
}

func TestHuggingFaceXetUpload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		mount string
	}{
		{
			"compatible mount",
			"/artifactory/api/huggingfaceml/",
		},
		{
			"native mount",
			"/huggingface/",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			testXetUpload(t, tt.mount)
		})
	}
}

func testXetUpload(t *testing.T, mount string) {
	t.Helper()

	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/hf-local",
			body:   `{"rclass":"local","packageType":"huggingfaceml"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/hf",
			body:   `{"rclass":"virtual","packageType":"huggingfaceml","repositories":["hf-local"],"defaultDeploymentRepo":"hf-local"}`,
		},
	)

	ep := mount + "hf"

	write := issueXetToken(t, srv, ep+"/api/models/org/demo/xet-write-token/main")
	if !strings.HasSuffix(write.CasURL, mount+"hf-local/api/xet") {
		t.Fatalf("write casUrl = %s, want the deployment repository's", write.CasURL)
	}

	read := issueXetToken(t, srv, ep+"/api/models/org/demo/xet-read-token/main")

	weights := make([]byte, 300_000)
	_, _ = rand.NewChaCha8([32]byte{
		7,
	}).Read(weights)

	upload := xettest.Build(t, weights)
	xorbPath := "/v1/xorbs/default/" + upload.XorbHash.String()

	if resp, body := casCall(t, srv, read, "POST", xorbPath, string(upload.Xorb)); resp.StatusCode != 403 {
		t.Errorf("xorb upload with a read token = %d %s, want 403", resp.StatusCode, body)
	}

	if resp, body := do(t, srv, call{
		method: "POST",
		path:   strings.TrimPrefix(write.CasURL, srv.URL) + xorbPath,
		body:   string(upload.Xorb),
		noAuth: true,
	}); resp.StatusCode != 401 {
		t.Errorf("xorb upload without a token = %d %s, want 401", resp.StatusCode, body)
	}

	forged := write
	forged.AccessToken += "x"

	if resp, body := casCall(t, srv, forged, "POST", xorbPath, string(upload.Xorb)); resp.StatusCode != 401 {
		t.Errorf("xorb upload with a forged token = %d %s, want 401", resp.StatusCode, body)
	}

	otherRepo := write
	otherRepo.CasURL = strings.Replace(write.CasURL, "/hf-local/", "/hf/", 1)

	if resp, body := casCall(t, srv, otherRepo, "POST", xorbPath, string(upload.Xorb)); resp.StatusCode != 401 {
		t.Errorf("xorb upload to another repository = %d %s, want 401", resp.StatusCode, body)
	}

	corrupt := bytes.Clone(upload.Xorb)
	corrupt[len(corrupt)-1] ^= 0xff

	if resp, body := casCall(t, srv, write, "POST", xorbPath, string(corrupt)); resp.StatusCode != 400 {
		t.Errorf("corrupt xorb = %d %s, want 400", resp.StatusCode, body)
	}

	if resp, body := casCall(
		t,
		srv,
		write,
		"POST",
		"/v1/xorbs/default/nothex",
		string(upload.Xorb),
	); resp.StatusCode != 400 {
		t.Errorf("malformed xorb hash = %d %s, want 400", resp.StatusCode, body)
	}

	if resp, body := casCall(
		t,
		srv,
		write,
		"POST",
		"/v1/shards",
		string(upload.ShardBytes(t)),
	); resp.StatusCode != 400 {
		t.Errorf("shard before its xorb = %d %s, want 400", resp.StatusCode, body)
	}

	if resp, body := casCall(t, srv, write, "POST", xorbPath, string(upload.Xorb)); resp.StatusCode != 200 ||
		!strings.Contains(body, `"was_inserted": true`) {
		t.Fatalf("xorb upload = %d %s", resp.StatusCode, body)
	}

	if resp, body := casCall(t, srv, write, "POST", xorbPath, string(upload.Xorb)); !strings.Contains(
		body,
		`"was_inserted": false`,
	) {
		t.Errorf("xorb upload again = %d %s", resp.StatusCode, body)
	}

	if resp, _ := casCall(t, srv, write, "HEAD", xorbPath, ""); resp.StatusCode != 200 {
		t.Errorf("HEAD xorb = %d, want 200", resp.StatusCode)
	}

	for _, path := range []string{
		"/v1/chunks/default-merkledb/" + upload.XorbHash.String(),
		"/v2/shards",
		"/v1/reconstructions/" + upload.Shard.Files[0].Hash.String(),
	} {
		if resp, body := casCall(t, srv, write, "POST", path, ""); resp.StatusCode != 404 {
			t.Errorf("%s = %d %s, want 404", path, resp.StatusCode, body)
		}
	}

	if resp, body := casCall(
		t,
		srv,
		write,
		"POST",
		"/v1/shards",
		string(upload.ShardBytes(t)),
	); resp.StatusCode != 200 ||
		!strings.Contains(body, `"result": 1`) {
		t.Fatalf("shard upload = %d %s", resp.StatusCode, body)
	}

	oid := upload.Shard.Files[0].SHA256
	commit := `{"key":"header","value":{"summary":"xet"}}` + "\n" +
		`{"key":"lfsFile","value":{"path":"weights.bin","algo":"sha256","oid":"` + oid + `","size":` +
		jsonNumber(int64(len(weights))) + `}}` + "\n"

	if resp, body := do(t, srv, call{
		method: "POST",
		path:   ep + "/api/models/org/demo/commit/main",
		body:   commit,
	}); resp.StatusCode != 200 {
		t.Fatalf("commit = %d %s", resp.StatusCode, body)
	}

	resp, body := do(t, srv, call{
		method: "GET",
		path:   ep + "/org/demo/resolve/main/weights.bin",
	})
	if resp.StatusCode != 200 || body != string(weights) {
		t.Fatalf("resolve = %d, %d bytes; want the %d bytes uploaded", resp.StatusCode, len(body), len(weights))
	}
}
