package api_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var jfrogAllowed = []string{
	"api/adapters/artifactory/",
	"test/e2e/",
	"web/layers/site/content/",
	"docs/",
	"README.md",
	"AGENTS.md",
	"web/layers/site/app/pages/index.vue",
	"go.mod",
	"go.sum",
	".github/dependabot.yml",
	"web/pnpm-lock.yaml",
	".golangci.yml",
	"api/api_test.go",
	"api/vocabulary_test.go",
}

var (
	jfrogWords   = regexp.MustCompile(`(?i)\bjf\b|jfrog|artifactory|xray`)
	adapterPaths = regexp.MustCompile(`(relic/)?api/adapters/artifactory(/[\w./-]*)?`)
)

func TestJFrogStaysInTheAdapter(t *testing.T) {
	t.Parallel()
	cmd := exec.CommandContext(t.Context(), "git", "ls-files")
	cmd.Dir = ".."

	out, err := cmd.Output()
	if err != nil {
		t.Skipf("git ls-files: %v", err)
	}

	for f := range strings.FieldsSeq(string(out)) {
		if allowed(f) {
			continue
		}

		b, err := os.ReadFile(filepath.Join("..", f))
		if err != nil {
			t.Fatal(err)
		}

		if bytes.IndexByte(b, 0) >= 0 {
			continue
		}

		for i, line := range strings.Split(string(b), "\n") {
			if m := jfrogWord(line); m != "" {
				t.Errorf("%s:%d names %q outside the adapter: %s", f, i+1, m, strings.TrimSpace(line))
			}
		}
	}
}

func jfrogWord(line string) string {
	return jfrogWords.FindString(adapterPaths.ReplaceAllString(line, ""))
}

func TestJFrogWord(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "adapter path",
			line: "see api/adapters/artifactory/compat/jf.sh",
			want: "",
		},
		{
			name: "module path",
			line: "relic/api/adapters/artifactory/xray",
			want: "",
		},
		{
			name: "word glued to the adapter path",
			line: "api/adapters/artifactory-jfrog-client",
			want: "jfrog",
		},
		{
			name: "word outside a path",
			line: "the Xray API",
			want: "Xray",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := jfrogWord(tt.line); got != tt.want {
				t.Errorf("jfrogWord(%q) = %q; want %q", tt.line, got, tt.want)
			}
		})
	}
}

func allowed(f string) bool {
	for _, a := range jfrogAllowed {
		if f == a || (strings.HasSuffix(a, "/") && strings.HasPrefix(f, a)) {
			return true
		}
	}

	return false
}
