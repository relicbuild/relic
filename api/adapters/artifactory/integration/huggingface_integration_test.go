//go:build integration

package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHuggingFaceRepository(t *testing.T) {
	t.Parallel()
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

	const EP = "/artifactory/api/huggingfaceml/hf"

	if _, body := do(
		t,
		srv,
		call{
			method: "POST",
			path:   EP + "/api/validate-yaml",
			body:   `{"content":"---\n---","repoType":"model"}`,
		},
	); !strings.Contains(body, `"errors": []`) {
		t.Errorf("validate-yaml = %s", body)
	}

	resp, body := do(t, srv, call{
		method: "GET",
		path:   EP + "/api/models/org/demo/revision/main",
	})
	if resp.StatusCode != 404 || resp.Header.Get("X-Error-Code") != "RepoNotFound" {
		t.Errorf("missing repo = %d %s %s", resp.StatusCode, resp.Header.Get("X-Error-Code"), body)
	}

	resp, body = do(
		t,
		srv,
		call{
			method: "POST",
			path:   EP + "/api/repos/create",
			body:   `{"name":"demo","organization":"org","private":false}`,
		},
	)
	if resp.StatusCode != 200 || !strings.Contains(body, `"url": "http://`) ||
		!strings.Contains(body, `/artifactory/api/huggingfaceml/hf/org/demo"`) {
		t.Errorf("create model repo = %d %s", resp.StatusCode, body)
	}

	if _, body = do(
		t,
		srv,
		call{
			method: "POST",
			path:   EP + "/api/repos/create",
			body:   `{"name":"set","organization":"org","type":"dataset"}`,
		},
	); !strings.Contains(body, "/hf/datasets/org/set") {
		t.Errorf("create dataset repo = %s", body)
	}

	if resp, _ = do(
		t,
		srv,
		call{
			method: "POST",
			path:   EP + "/api/repos/create",
			body:   `{"name":"app","organization":"org","type":"space"}`,
		},
	); resp.StatusCode != 400 {
		t.Errorf("create space = %d, want 400", resp.StatusCode)
	}

	if resp, _ = do(
		t,
		srv,
		call{
			method: "POST",
			path:   EP + "/api/repos/create",
			body:   `{"name":"demo","organization":"org"}`,
			noAuth: true,
		},
	); resp.StatusCode != 401 {
		t.Errorf("anonymous create = %d, want 401", resp.StatusCode)
	}

	big := bytes.Repeat([]byte("w"), 11<<20)
	bigOid := sha256Hex(string(big))

	_, pre := do(t, srv, call{
		method: "POST",
		path:   EP + "/api/models/org/demo/preupload/main",
		body: `{"files":[{"path":"config.json","sample":"","size":20},{"path":"weights.bin","sample":"","size":` + jsonNumber(
			int64(len(big)),
		) + `}]}`,
	})
	if !strings.Contains(pre, `"uploadMode": "regular"`) || !strings.Contains(pre, `"uploadMode": "lfs"`) ||
		!strings.Contains(pre, `"shouldIgnore": false`) {
		t.Fatalf("preupload = %s", pre)
	}

	resp, batch := do(t, srv, call{
		method: "POST",
		path:   EP + "/org/demo.git/info/lfs/objects/batch",
		body: `{"operation":"upload","transfers":["basic","multipart","xet"],"objects":[{"oid":"` + bigOid + `","size":` + jsonNumber(
			int64(len(big)),
		) + `}],"hash_algo":"sha256"}`,
	})

	var b struct {
		Transfer string `json:"transfer"`
		Objects  []struct {
			Actions struct {
				Upload struct {
					Href string
				} `json:"upload"`
			} `json:"actions"`
		} `json:"objects"`
	}
	if err := json.Unmarshal([]byte(batch), &b); err != nil || b.Transfer != "basic" ||
		b.Objects[0].Actions.Upload.Href == "" {
		t.Fatalf("batch = %d %s", resp.StatusCode, batch)
	}

	req, _ := http.NewRequestWithContext(
		t.Context(),
		http.MethodPut,
		b.Objects[0].Actions.Upload.Href,
		bytes.NewReader(big),
	)

	put, err := http.DefaultClient.Do(req)
	if err != nil || put.StatusCode != 200 {
		t.Fatalf("LFS PUT = %v %v", put, err)
	}

	_ = put.Body.Close()
	tampered := strings.Replace(b.Objects[0].Actions.Upload.Href, "sig=", "sig=x", 1)

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPut, tampered, bytes.NewReader(big))
	if bad, err := http.DefaultClient.Do(req); err != nil || bad.StatusCode != 403 {
		t.Errorf("tampered LFS PUT = %v %v", bad, err)
	} else {
		_ = bad.Body.Close()
	}

	if resp, body := do(t, srv, call{
		method: "POST",
		path:   EP + "/org/demo.git/info/lfs/objects/batch",
		body: `{"operation":"upload","objects":[` + strings.Repeat(
			`{"oid":"`+bigOid+`","size":1},`,
			1000,
		) + `{"oid":"` + bigOid + `","size":1}]}`,
	}); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("batch of 1001 objects = %d %s, want 413", resp.StatusCode, body)
	}

	commit := func(lines ...string) (int, string) {
		resp, body := do(
			t,
			srv,
			call{
				method: "POST",
				path:   EP + "/api/models/org/demo/commit/main",
				body:   strings.Join(lines, "\n"),
				headers: map[string]string{
					"Content-Type": "application/x-ndjson",
				},
			},
		)

		return resp.StatusCode, body
	}
	header := `{"key":"header","value":{"summary":"Upload folder using huggingface_hub","description":""}}`
	file := func(p, content string) string {
		return `{"key":"file","value":{"content":"` + base64.StdEncoding.EncodeToString(
			[]byte(content),
		) + `","path":"` + p + `","encoding":"base64"}}`
	}
	code, c1 := commit(
		header,
		file("config.json", `{"hidden": 1}`),
		file("sub/tok.txt", "tok"),
		`{"key":"lfsFile","value":{"path":"weights.bin","algo":"sha256","oid":"`+bigOid+`","size":`+jsonNumber(
			int64(len(big)),
		)+`}}`,
	)

	var cr struct {
		CommitURL, CommitOid string
	}
	if err := json.Unmarshal([]byte(c1), &cr); code != 200 || err != nil || len(cr.CommitOid) != 40 ||
		!strings.HasSuffix(cr.CommitURL, "/org/demo/commit/"+cr.CommitOid) {
		t.Fatalf("commit = %d %s", code, c1)
	}

	if code, body := commit(
		header,
		`{"key":"lfsFile","value":{"path":"x.bin","algo":"sha256","oid":"`+sha256Hex("never")+`","size":5}}`,
	); code != 400 {
		t.Errorf("commit with a missing LFS object = %d %s", code, body)
	}

	_, info := do(t, srv, call{
		method: "GET",
		path:   EP + "/api/models/org/demo/revision/main",
	})
	if !strings.Contains(info, `"sha": "`+cr.CommitOid+`"`) || !strings.Contains(info, `"rfilename": "sub/tok.txt"`) ||
		!strings.Contains(info, `"rfilename": "weights.bin"`) {
		t.Fatalf("revision = %s", info)
	}

	resp, _ = do(t, srv, call{
		method: "HEAD",
		path:   EP + "/org/demo/resolve/" + cr.CommitOid + "/weights.bin",
	})
	if resp.StatusCode != 200 || resp.Header.Get("X-Repo-Commit") != cr.CommitOid ||
		resp.Header.Get("ETag") != `"`+bigOid+`"` ||
		resp.Header.Get("Content-Length") != jsonNumber(int64(len(big))) {
		t.Errorf("HEAD resolve = %d %v", resp.StatusCode, resp.Header)
	}

	if _, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   EP + "/org/demo/resolve/" + cr.CommitOid + "/sub/tok.txt",
		},
	); got != "tok" {
		t.Errorf("GET resolve = %q", got)
	}

	resp, got := do(t, srv, call{
		method: "GET",
		path:   EP + "/org/demo/resolve/" + cr.CommitOid + "/weights.bin",
		headers: map[string]string{
			"Range": "bytes=1048576-",
		},
	})

	wantRange := "bytes 1048576-" + jsonNumber(int64(len(big))-1) + "/" + jsonNumber(int64(len(big)))
	if resp.StatusCode != http.StatusPartialContent || len(got) != len(big)-1048576 ||
		resp.Header.Get("Content-Range") != wantRange {
		t.Errorf("ranged resolve = %d %d bytes %v", resp.StatusCode, len(got), resp.Header)
	}

	_, c2 := commit(header, file("config.json", `{"hidden": 2}`))

	var cr2 struct {
		CommitOid string
	}

	_ = json.Unmarshal([]byte(c2), &cr2)

	_, info = do(t, srv, call{
		method: "GET",
		path:   EP + "/api/models/org/demo/revision/main",
	})
	if !strings.Contains(info, cr2.CommitOid) || !strings.Contains(info, "weights.bin") {
		t.Errorf("revision after second commit = %s", info)
	}

	if _, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   EP + "/org/demo/resolve/" + cr.CommitOid + "/config.json",
		},
	); got != `{"hidden": 1}` {
		t.Errorf("old commit content = %q", got)
	}

	if _, tree := do(
		t,
		srv,
		call{
			method: "GET",
			path:   EP + "/api/models/org/demo/tree/" + cr.CommitOid + "?recursive=true",
		},
	); !strings.Contains(tree, `"path": "sub/tok.txt"`) {
		t.Errorf("tree by commit = %s", tree)
	}

	if _, info := do(
		t,
		srv,
		call{
			method: "GET",
			path:   EP + "/api/models/org/demo/revision/" + cr.CommitOid,
		},
	); !strings.Contains(info, `"sha": "`+cr.CommitOid+`"`) {
		t.Errorf("revision by commit = %s", info)
	}

	resp, got = do(t, srv, call{
		method: "GET",
		path:   EP + "/org/demo/resolve/main/config.json",
	})
	if resp.StatusCode != 200 || got != `{"hidden": 2}` || resp.Header.Get("X-Repo-Commit") != cr2.CommitOid {
		t.Errorf("resolve by branch = %d %q %v", resp.StatusCode, got, resp.Header)
	}

	_, folders := do(t, srv, call{
		method: "POST",
		path:   "/artifactory/api/search/aql",
		body:   `items.find({"repo":"hf-local","type":"folder","path":"models/org/demo","name":{"$match":"main_*"}}).include("name").sort({"$desc":["name"]}).limit(1)`,
	})

	var fr struct {
		Results []struct {
			Name string
		}
	}
	if err := json.Unmarshal([]byte(folders), &fr); err != nil || len(fr.Results) != 1 {
		t.Fatalf("revision folder AQL = %s", folders)
	}

	_, files := do(t, srv, call{
		method: "POST",
		path:   "/artifactory/api/search/aql",
		body:   `items.find({"repo":"hf-local","path":{"$match":"models/org/demo/` + fr.Results[0].Name + `/*"}}).include("repo","path","name","sha256")`,
	})
	if !strings.Contains(files, `"config.json"`) || !strings.Contains(files, `"weights.bin"`) {
		t.Errorf("revision files AQL = %s", files)
	}

	const upSHA = "0123456789abcdef0123456789abcdef01234567"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models/org/up/revision/main":
			_, _ = w.Write([]byte(`{"id":"org/up","sha":"` + upSHA + `","siblings":[{"rfilename":"config.json"}]}`))
		case "/org/up/resolve/" + upSHA + "/config.json":
			_, _ = w.Write([]byte(`{"up": true}`))
		case "/api/models/org/up/tree/" + upSHA:
			_, _ = w.Write([]byte(`[{"type":"file","path":"config.json","size":12,"oid":"abc"}]`))
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
			path:   "/artifactory/api/repositories/hf-remote",
			body:   `{"rclass":"remote","packageType":"huggingfaceml","url":"` + upstream.URL + `"}`,
		},
	)

	const REP = "/artifactory/api/huggingfaceml/hf-remote"
	if _, info := do(t, srv, call{
		method: "GET",
		path:   REP + "/api/models/org/up/revision/main",
	}); !strings.Contains(
		info,
		upSHA,
	) {
		t.Errorf("remote revision = %s", info)
	}

	resp, got = do(t, srv, call{
		method: "GET",
		path:   REP + "/org/up/resolve/" + upSHA + "/config.json",
	})
	if resp.StatusCode != 200 || got != `{"up": true}` || resp.Header.Get("X-Repo-Commit") != upSHA {
		t.Errorf("remote resolve = %d %q %v", resp.StatusCode, got, resp.Header)
	}

	resp, got = do(t, srv, call{
		method: "GET",
		path:   REP + "/org/up/resolve/main/config.json",
	})
	if resp.StatusCode != 200 || got != `{"up": true}` || resp.Header.Get("X-Repo-Commit") != upSHA {
		t.Errorf("remote resolve by branch = %d %q %v", resp.StatusCode, got, resp.Header)
	}

	if resp, tree := do(t, srv, call{
		method: "GET",
		path:   REP + "/api/models/org/up/tree/main?recursive=true",
	}); resp.StatusCode != 200 || !strings.Contains(tree, `"path": "config.json"`) {
		t.Errorf("remote tree = %d %s", resp.StatusCode, tree)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "POST",
			path:   REP + "/api/models/org/up/commit/main",
			body:   header,
		},
	); resp.StatusCode == 200 {
		t.Errorf("commit to a remote succeeded")
	}
}
