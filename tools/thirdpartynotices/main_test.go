package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

func TestComponentLicenseTexts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{
			name: "a license and a notice",
			files: map[string]string{
				"LICENSE":    "license text\n",
				"NOTICE.txt": "notice text\n",
				"README.md":  "readme\n",
				"main.go":    "package main\n",
			},
			want: []string{
				"license text",
				"notice text",
			},
		},
		{
			name: "british spelling and a copying file",
			files: map[string]string{
				"COPYING":    "copying text\n",
				"LICENCE.md": "licence text\n",
			},
			want: []string{
				"copying text",
				"licence text",
			},
		},
		{
			name: "no license file",
			files: map[string]string{
				"README.md": "readme\n",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := component{
				Name: "example.com/module",
				Dir:  writeFiles(t, tt.files),
			}.licenseTexts()
			if tt.want == nil {
				if err == nil {
					t.Fatalf("licenseTexts = %q, want an error for a component without a license file", got)
				}

				return
			}

			if err != nil || !slices.Equal(got, tt.want) {
				t.Errorf("licenseTexts = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestSectionRenderGroupsComponentsUnderOneText(t *testing.T) {
	t.Parallel()

	shared := writeFiles(t, map[string]string{
		"LICENSE": "shared text\n",
	})
	own := writeFiles(t, map[string]string{
		"LICENSE": "own text\n",
	})

	got, err := section{
		Title: "Linked into the binaries",
		Components: []component{
			{
				Name:    "example.com/second",
				Version: "v2.0.0",
				Dir:     shared,
			},
			{
				Name:    "example.com/own",
				Version: "v0.1.0",
				Dir:     own,
			},
			{
				Name:    "example.com/first",
				Version: "v1.0.0",
				Dir:     shared,
			},
		},
	}.render()
	if err != nil {
		t.Fatal(err)
	}

	if strings.Count(got, "shared text") != 1 || strings.Count(got, "own text") != 1 {
		t.Errorf("each license text is written once, got:\n%s", got)
	}

	first := strings.Index(got, "example.com/first v1.0.0")
	second := strings.Index(got, "example.com/second v2.0.0")
	text := strings.Index(got, "shared text")

	if first < 0 || second < first || text < second {
		t.Errorf("the components of a text are listed above it in name order, got:\n%s", got)
	}

	if !strings.HasPrefix(got, "Linked into the binaries\n") {
		t.Errorf("the section starts with its title, got:\n%s", got)
	}
}

func TestParseProgram(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		argument string
		want     program
		wantErr  bool
	}{
		{
			name:     "a name, a module and a version",
			argument: "syft=github.com/anchore/syft@v1.52.0",
			want: program{
				Name:    "syft",
				Module:  "github.com/anchore/syft",
				Version: "v1.52.0",
			},
		},
		{
			name:     "no version",
			argument: "syft=github.com/anchore/syft",
			wantErr:  true,
		},
		{
			name:     "no name",
			argument: "github.com/anchore/syft@v1.52.0",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseProgram(tt.argument)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("parseProgram(%q) = %+v, %v; want %+v, error %v", tt.argument, got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestDockerfileNoticesNameTheBundledProgramVersions(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}

	dockerfile := string(raw)
	bundled := []struct {
		name    string
		version *regexp.Regexp
		module  string
	}{
		{
			name:    "syft",
			version: regexp.MustCompile(`FROM anchore/syft:(v[0-9.]+)@`),
			module:  "github.com/anchore/syft",
		},
		{
			name:    "grype",
			version: regexp.MustCompile(`FROM anchore/grype:(v[0-9.]+)@`),
			module:  "github.com/anchore/grype",
		},
		{
			name:    "gitleaks",
			version: regexp.MustCompile(`FROM zricethezav/gitleaks:(v[0-9.]+)@`),
			module:  "github.com/zricethezav/gitleaks/v8",
		},
		{
			name:    "trivy",
			version: regexp.MustCompile(`FROM aquasec/trivy:([0-9.]+)@`),
			module:  "github.com/aquasecurity/trivy",
		},
		{
			name:    "govulncheck",
			version: regexp.MustCompile(`golang.org/x/vuln/cmd/govulncheck@(v[0-9.]+)`),
			module:  "golang.org/x/vuln",
		},
	}

	for _, b := range bundled {
		m := b.version.FindStringSubmatch(dockerfile)
		if m == nil {
			t.Errorf("%s: the Dockerfile does not bundle it", b.name)

			continue
		}

		want := "-program " + b.name + "=" + b.module + "@v" + strings.TrimPrefix(m[1], "v")
		if !strings.Contains(dockerfile, want) {
			t.Errorf("the Dockerfile's notices do not carry %q", want)
		}
	}
}

func TestGoEnvValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{
			name: "a GOROOT with a space",
			raw:  "C:\\Program Files\\Go\r\ngo1.27.0\r\n",
			want: []string{
				"C:\\Program Files\\Go",
				"go1.27.0",
			},
		},
		{
			name: "a plain GOROOT",
			raw:  "/usr/local/go\ngo1.27.0\n",
			want: []string{
				"/usr/local/go",
				"go1.27.0",
			},
		},
		{
			name:    "a value missing",
			raw:     "/usr/local/go\n",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := goEnvValues([]byte(tt.raw), 2)
			if (err != nil) != tt.wantErr || !slices.Equal(got, tt.want) {
				t.Errorf("goEnvValues(%q) = %q, %v; want %q, error %v", tt.raw, got, err, tt.want, tt.wantErr)
			}
		})
	}
}
