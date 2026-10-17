//go:build integration

package repository

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestLocalOnlyNamesNeverReachRemotes(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	srv, hits := countingUpstream(t, func(w http.ResponseWriter, _ int32) {
		_, _ = io.WriteString(w, "public look-alike")
	})
	createRepo(t, s, remoteRepo("public-remote", RemoteConfig{
		URL: srv.URL,
	}))
	createRepo(t, s, virtualRepo("inner-virtual", "", "public-remote"))

	outer := virtualRepo("company-virtual", "generic-local", "generic-local", "inner-virtual")
	outer.Config.Virtual.LocalOnlyPatterns = []string{
		"com/acme",
		"@acme/*",
	}
	createRepo(t, s, outer)

	if _, err := s.Deploy(t.Context(), DeployInput{
		Repository: "generic-local",
		Path:       "com/acme/lib/1.0/lib-1.0.jar",
		Body:       strings.NewReader("internal"),
	}); err != nil {
		t.Fatal(err)
	}

	it, err := s.Resolve(t.Context(), "company-virtual", "com/acme/lib/1.0/lib-1.0.jar")
	if err != nil || readItem(t, s, it) != "internal" {
		t.Fatalf("an internal artifact = %v, %v", it, err)
	}

	for _, p := range []string{
		"com/acme/lib/2.0/lib-2.0.jar",
		"@acme/ui/-/ui-1.0.0.tgz",
	} {
		if _, err := s.Resolve(t.Context(), "company-virtual", p); !errors.Is(err, ErrNotFound) {
			t.Errorf("a local-only name missing locally, %s = %v, want ErrNotFound", p, err)
		}
	}

	if n := hits.Load(); n != 0 {
		t.Errorf("the remote was asked %d times for local-only names", n)
	}

	if _, err := s.Resolve(t.Context(), "company-virtual", "org/other/1.0/other.jar"); err != nil || hits.Load() != 1 {
		t.Errorf("another name = %v with %d upstream requests, want it fetched", err, hits.Load())
	}

	members := s.Flatten(t.Context(), outer, "@acme/ui")

	names := make([]string, 0, len(members))
	for _, m := range members {
		names = append(names, m.Name)
	}

	if strings.Join(names, ",") != "generic-local" {
		t.Errorf("Flatten for a local-only name = %v, want only the local member", names)
	}

	names = nil
	for _, m := range s.Flatten(t.Context(), outer, "lodash") {
		names = append(names, m.Name)
	}

	if strings.Join(names, ",") != "generic-local,public-remote" {
		t.Errorf("Flatten for another name = %v", names)
	}

	pypi := virtualRepo("pypi-virtual", "generic-local", "generic-local", "inner-virtual")
	pypi.Format = FormatPyPI
	pypi.Config.Virtual.LocalOnlyPatterns = []string{
		"Acme_*",
	}

	pypiMembers := s.Flatten(t.Context(), pypi, "acme-tools")
	names = make([]string, 0, len(pypiMembers))

	for _, m := range pypiMembers {
		names = append(names, m.Name)
	}

	if strings.Join(names, ",") != "generic-local" {
		t.Errorf("Flatten of a PyPI virtual for a name its pattern names in another spelling = %v", names)
	}
}
