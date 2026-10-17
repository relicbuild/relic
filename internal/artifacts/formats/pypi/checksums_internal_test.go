package pypi

import (
	"net/url"
	"slices"
	"testing"

	"relic/internal/artifacts/repository"
)

func pypiRemote(upstream string) repository.Repository {
	return repository.Repository{
		Name:   "pypi-remote",
		Class:  repository.ClassRemote,
		Format: Format,
		Config: repository.Config{
			Remote: &repository.RemoteConfig{
				URL: upstream,
			},
		},
	}
}

func TestRemoteOnTheRegistryHostServesFiles(t *testing.T) {
	t.Parallel()

	for upstream, want := range map[string]string{
		"https://pypi.org":                 "https://files.pythonhosted.org",
		"https://pypi.org/":                "https://files.pythonhosted.org",
		"https://files.pythonhosted.org":   "https://files.pythonhosted.org",
		"https://mirror.example.com/pypi/": "https://mirror.example.com/pypi",
	} {
		if got := filesURL(pypiRemote(upstream)); got != want {
			t.Errorf("filesURL(%s) = %s, want %s", upstream, got, want)
		}
	}

	base, _ := url.Parse("https://pypi.org/simple/six/")
	files, _ := url.Parse(filesURL(pypiRemote("https://pypi.org")))

	links := parseSimple(
		`<a href="https://files.pythonhosted.org/packages/d9/5a/six-1.16.0-py2.py3-none-any.whl#sha256=ab">six</a>`,
		base,
		files,
	)
	if len(links) != 1 || links[0].Href != "packages/packages/d9/5a/six-1.16.0-py2.py3-none-any.whl" {
		t.Errorf("links = %+v, want a link through Relic", links)
	}
}

func TestProjectOf(t *testing.T) {
	t.Parallel()

	for file, want := range map[string]string{
		"requests-2.31.0-py3-none-any.whl":           "requests",
		"python_dateutil-2.8.2-py2.py3-none-any.whl": "python-dateutil",
		"python-dateutil-2.8.2.tar.gz":               "python-dateutil",
		"Zope.Interface-6.0.zip":                     "zope-interface",
		"mypackage.tar.gz":                           "mypackage",
	} {
		if got := projectOf(file); got != want {
			t.Errorf("projectOf(%q) = %q, want %q", file, got, want)
		}
	}
}

func TestProjectCandidates(t *testing.T) {
	t.Parallel()

	got := projectCandidates("python-3parclient-4.2.12.tar.gz")
	if !slices.Equal(got, []string{
		"python",
		"python-3parclient",
	}) {
		t.Errorf("projectCandidates = %v, want python then python-3parclient", got)
	}
}

func TestVersionOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		project, file, want string
	}{
		{
			project: "requests",
			file:    "requests-2.31.0-py3-none-any.whl",
			want:    "2.31.0",
		},
		{
			project: "python-dateutil",
			file:    "python-dateutil-2.8.2.tar.gz",
			want:    "2.8.2",
		},
		{
			project: "demo",
			file:    "demo-1.0.zip",
			want:    "1.0",
		},
		{
			project: "python-3parclient",
			file:    "python-3parclient-4.2.12.tar.gz",
			want:    "4.2.12",
		},
		{
			project: "zope-interface",
			file:    "zope.interface-6.0.tar.gz",
			want:    "6.0",
		},
	}
	for _, tt := range tests {
		if got := versionOf(tt.project, tt.file); got != tt.want {
			t.Errorf("versionOf(%q, %q) = %q, want %q", tt.project, tt.file, got, tt.want)
		}
	}
}
