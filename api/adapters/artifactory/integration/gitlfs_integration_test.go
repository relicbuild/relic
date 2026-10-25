//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type gitLFSAnswer struct {
	Transfer string `json:"transfer"`
	Objects  []struct {
		Oid     string `json:"oid"`
		Size    int64  `json:"size"`
		Actions map[string]struct {
			Href string `json:"href"`
		} `json:"actions"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	} `json:"objects"`
}

func TestGitLFSRepository(t *testing.T) {
	t.Parallel()

	srv := server(t)
	if resp, body := do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/repositories/lfs-local",
		body:   `{"rclass":"local","packageType":"gitlfs"}`,
	}); resp.StatusCode >= 300 {
		t.Fatalf("create lfs-local = %d %s", resp.StatusCode, body)
	}

	content := "large file content"
	oid := sha256Hex(content)
	object := `{"oid":"` + oid + `","size":` + jsonNumber(int64(len(content))) + `}`

	batch := func(endpoint, operation string, c call) (int, gitLFSAnswer) {
		c.method, c.path = "POST", endpoint+"/objects/batch"
		c.body = `{"operation":"` + operation + `","transfers":["basic"],"objects":[` + object + `]}`
		c.headers = map[string]string{
			"Accept":       "application/vnd.git-lfs+json",
			"Content-Type": "application/vnd.git-lfs+json",
		}

		resp, body := do(t, srv, c)

		var answer gitLFSAnswer
		if resp.StatusCode == 200 {
			if err := json.Unmarshal([]byte(body), &answer); err != nil {
				t.Fatalf("batch answer %s: %v", body, err)
			}
		}

		return resp.StatusCode, answer
	}

	transfer := func(method, href, body string) (int, string) {
		req, err := http.NewRequestWithContext(t.Context(), method, href, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		defer func() {
			_ = resp.Body.Close()
		}()

		got, _ := io.ReadAll(resp.Body)

		return resp.StatusCode, string(got)
	}

	const endpoint = "/lfs/lfs-local"

	if status, answer := batch(endpoint, "download", call{}); status != 200 || answer.Objects[0].Error == nil ||
		answer.Objects[0].Error.Code != 404 {
		t.Errorf("download of a missing object = %d %+v, want a 404 object error", status, answer)
	}

	status, answer := batch(endpoint, "upload", call{})
	if status != 200 || answer.Transfer != "basic" || answer.Objects[0].Actions["upload"].Href == "" {
		t.Fatalf("upload batch = %d %+v", status, answer)
	}

	href := answer.Objects[0].Actions["upload"].Href
	if status, body := transfer("PUT", href, "other content"); status != 400 {
		t.Errorf("upload of content that does not match the oid = %d %s, want 400", status, body)
	}

	if status, body := transfer("PUT", strings.Replace(href, "sig=", "sig=x", 1), content); status != 403 {
		t.Errorf("upload with a tampered link = %d %s, want 403", status, body)
	}

	if status, body := transfer("PUT", href, content); status != 200 {
		t.Fatalf("upload = %d %s", status, body)
	}

	if status, answer := batch(endpoint, "upload", call{}); status != 200 || len(answer.Objects[0].Actions) != 0 {
		t.Errorf("upload batch of a stored object = %d %+v, want no actions", status, answer)
	}

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/storage/lfs-local/objects/" + oid[0:2] + "/" + oid[2:4] + "/" + oid,
	}); resp.StatusCode != 200 {
		t.Errorf("stored object = %d, want it at objects/<oid[0:2]>/<oid[2:4]>/<oid>", resp.StatusCode)
	}

	for _, ep := range []string{
		endpoint,
		"/artifactory/api/lfs/lfs-local",
	} {
		status, answer := batch(ep, "download", call{})
		if status != 200 || answer.Objects[0].Actions["download"].Href == "" {
			t.Fatalf("%s download batch = %d %+v", ep, status, answer)
		}

		if status, body := transfer("GET", answer.Objects[0].Actions["download"].Href, ""); status != 200 ||
			body != content {
			t.Errorf("%s download = %d %q", ep, status, body)
		}
	}

	if status, _ := batch(endpoint, "upload", call{
		noAuth: true,
	}); status != 401 {
		t.Errorf("anonymous upload batch = %d, want 401", status)
	}

	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/security/users/reader",
		body:   `{"email":"r@example.com","password":"Reader-pass-1"}`,
	})
	do(t, srv, call{
		method: "POST",
		path:   "/artifactory/api/v2/security/permissions/reader",
		body:   `{"name":"reader","repo":{"repositories":["lfs-local"],"include-patterns":["**"],"actions":{"users":{"reader":["read"]}}}}`,
	})

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+endpoint+"/objects/batch",
		bytes.NewReader([]byte(`{"operation":"upload","objects":[`+object+`]}`)))
	req.SetBasicAuth("reader", "Reader-pass-1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("upload batch without write permission = %d, want 403", resp.StatusCode)
	}

	if resp, body := do(t, srv, call{
		method: "POST",
		path:   endpoint + "/locks/verify",
		body:   `{}`,
	}); resp.StatusCode != 200 || !strings.Contains(body, `"ours":[]`) {
		t.Errorf("locks verify = %d %s", resp.StatusCode, body)
	}

	if resp, body := do(t, srv, call{
		method: "POST",
		path:   endpoint + "/locks",
		body:   `{"path":"a.bin"}`,
	}); resp.StatusCode != 501 {
		t.Errorf("lock = %d %s, want 501", resp.StatusCode, body)
	}

	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/repositories/generic-local",
		body:   `{"rclass":"local","packageType":"generic"}`,
	})

	if status, _ := batch("/lfs/generic-local", "download", call{}); status != 404 {
		t.Errorf("batch on a generic repository = %d, want 404", status)
	}
}
