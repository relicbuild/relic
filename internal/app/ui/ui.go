// Package ui serves the embedded Nuxt single-page application at the server root.
package ui

import (
	"crypto/sha256"
	"encoding/base64"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Handler serves the SPA mounted at prefix (the server uses "/").
func Handler(prefix string) http.Handler {
	root, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}

	return handler(prefix, root)
}

func handler(prefix string, root fs.FS) http.Handler {
	files := http.FileServerFS(root)

	return http.StripPrefix(
		strings.TrimSuffix(prefix, "/"),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
			if name == "" || !exists(root, name) {
				index, err := fs.ReadFile(root, "index.html")
				if err != nil {
					http.Error(w, "UI not built into this binary", http.StatusNotFound)
					return
				}

				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Cache-Control", "no-cache")
				_, _ = w.Write(index)

				return
			}

			files.ServeHTTP(w, r)
		}),
	)
}

func exists(root fs.FS, name string) bool {
	st, err := fs.Stat(root, name)
	return err == nil && !st.IsDir()
}

var inlineScript = regexp.MustCompile(`(?s)<script([^>]*)>(.*?)</script>`)

// ScriptHashes returns the CSP sources ('sha256-…') of the inline scripts
// in the embedded HTML files.
func ScriptHashes() []string {
	root, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}

	return scriptHashes(root)
}

func scriptHashes(root fs.FS) []string {
	seen := map[string]bool{}

	var out []string

	_ = fs.WalkDir(root, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(name, ".html") {
			return err
		}

		page, err := fs.ReadFile(root, name)
		if err != nil {
			return err
		}

		for _, m := range inlineScript.FindAllSubmatch(page, -1) {
			attrs := strings.ToLower(string(m[1]))
			if strings.Contains(attrs, "src=") || strings.Contains(attrs, "application/json") {
				continue
			}

			sum := sha256.Sum256(m[2])
			if h := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"; !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}

		return nil
	})

	sort.Strings(out)

	return out
}
