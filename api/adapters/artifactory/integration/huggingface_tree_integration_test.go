//go:build integration

package integration

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func treeListing(t *testing.T, body string) []string {
	t.Helper()

	var entries []struct {
		Type, Path string
	}
	if err := json.Unmarshal([]byte(body), &entries); err != nil {
		t.Fatalf("tree = %s: %v", body, err)
	}

	listing := make([]string, 0, len(entries))
	for _, e := range entries {
		listing = append(listing, e.Type+" "+e.Path)
	}

	slices.Sort(listing)

	return listing
}

func TestHuggingFaceTreeListsAFolder(t *testing.T) {
	t.Parallel()

	srv := server(t)
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/repositories/hf-tree",
		body:   `{"rclass":"local","packageType":"huggingfaceml"}`,
	})

	const EP = "/artifactory/api/huggingfaceml/hf-tree"

	file := func(p, content string) string {
		return `{"key":"file","value":{"content":"` + base64.StdEncoding.EncodeToString(
			[]byte(content),
		) + `","path":"` + p + `","encoding":"base64"}}`
	}

	if resp, body := do(t, srv, call{
		method: "POST",
		path:   EP + "/api/models/org/demo/commit/main",
		body: strings.Join([]string{
			`{"key":"header","value":{"summary":"upload"}}`,
			file("config.json", "{}"),
			file("sub/vocab.txt", "v"),
			file("sub/deep/x.txt", "x"),
		}, "\n"),
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("commit = %d %s", resp.StatusCode, body)
	}

	upstreamSHA := strings.Repeat("ab", 20)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models/org/up/revision/main":
			_, _ = w.Write([]byte(`{"id":"org/up","sha":"` + upstreamSHA + `","siblings":[]}`))
		case "/api/models/org/up/tree/" + upstreamSHA:
			_, _ = w.Write([]byte(`[{"type":"directory","path":"onnx","oid":"d1","size":0},` +
				`{"type":"file","path":"config.json","oid":"f1","size":2},` +
				`{"type":"file","path":"onnx/model.onnx","oid":"f2","size":4}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/repositories/hf-tree-remote",
		body:   `{"rclass":"remote","packageType":"huggingfaceml","url":"` + upstream.URL + `"}`,
	})

	for _, tc := range []struct {
		name, path string
		want       []string
	}{
		{
			name: "the top level",
			path: EP + "/api/models/org/demo/tree/main",
			want: []string{
				"directory sub",
				"file config.json",
			},
		},
		{
			name: "every level",
			path: EP + "/api/models/org/demo/tree/main?recursive=true",
			want: []string{
				"directory sub",
				"directory sub/deep",
				"file config.json",
				"file sub/deep/x.txt",
				"file sub/vocab.txt",
			},
		},
		{
			name: "a folder",
			path: EP + "/api/models/org/demo/tree/main/sub",
			want: []string{
				"directory sub/deep",
				"file sub/vocab.txt",
			},
		},
		{
			name: "a nested folder, its path escaped as huggingface_hub sends it",
			path: EP + "/api/models/org/demo/tree/main/sub%2Fdeep",
			want: []string{
				"file sub/deep/x.txt",
			},
		},
		{
			name: "a remote's top level",
			path: "/artifactory/api/huggingfaceml/hf-tree-remote/api/models/org/up/tree/main",
			want: []string{
				"directory onnx",
				"file config.json",
			},
		},
		{
			name: "a remote's folder",
			path: "/artifactory/api/huggingfaceml/hf-tree-remote/api/models/org/up/tree/main/onnx",
			want: []string{
				"file onnx/model.onnx",
			},
		},
	} {
		resp, body := do(t, srv, call{
			method: "GET",
			path:   tc.path,
		})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: %d %s", tc.name, resp.StatusCode, body)
			continue
		}

		if got := treeListing(t, body); !slices.Equal(got, tc.want) {
			t.Errorf("%s = %v, want %v", tc.name, got, tc.want)
		}
	}

	resp, body := do(t, srv, call{
		method: "GET",
		path:   EP + "/api/models/org/demo/tree/main/none",
	})
	if resp.StatusCode != http.StatusNotFound || resp.Header.Get("X-Error-Code") != "EntryNotFound" {
		t.Errorf("a missing folder = %d %s %s", resp.StatusCode, resp.Header.Get("X-Error-Code"), body)
	}
}
