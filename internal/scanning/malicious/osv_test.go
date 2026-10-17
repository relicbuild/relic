package malicious

import (
	"slices"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()

	npm := Ecosystems[0]

	pkgs, id, withdrawn, err := parse([]byte(`{
  "id": "MAL-2025-100",
  "summary": "Malicious code in evil-pkg (npm)",
  "modified": "2025-03-01T00:00:00Z",
  "affected": [
    {"package": {"ecosystem": "npm", "name": "evil-pkg"}, "ranges": [{"type": "SEMVER", "events": [{"introduced": "0"}]}]},
    {"package": {"ecosystem": "npm", "name": "@scope/typo"}, "versions": ["1.0.1", "1.0.2"]},
    {"package": {"ecosystem": "npm", "name": "late"}, "ranges": [{"type": "SEMVER", "events": [{"introduced": "2.0.0"}, {"fixed": "2.0.3"}]}]},
    {"package": {"ecosystem": "PyPI", "name": "other"}}
  ]
}`), npm)
	if err != nil || id != "MAL-2025-100" || withdrawn || len(pkgs) != 3 {
		t.Fatalf("parse = %+v %q %v %v", pkgs, id, withdrawn, err)
	}

	if !pkgs[0].AllVersions || pkgs[0].Name != "evil-pkg" || pkgs[0].Summary != "Malicious code in evil-pkg (npm)" {
		t.Errorf("all versions: %+v", pkgs[0])
	}

	if pkgs[1].AllVersions || !slices.Equal(pkgs[1].Versions, []string{
		"1.0.1",
		"1.0.2",
	}) || pkgs[1].Name != "@scope/typo" {
		t.Errorf("listed versions: %+v", pkgs[1])
	}

	if pkgs[2].AllVersions || len(pkgs[2].Ranges) != 1 {
		t.Errorf("range: %+v", pkgs[2])
	}

	if pkgs, _, _, _ := parse(
		[]byte(`{"id": "GHSA-xxxx", "affected": [{"package": {"ecosystem": "npm", "name": "a"}}]}`),
		npm,
	); pkgs != nil {
		t.Errorf("GHSA parsed: %+v", pkgs)
	}

	if _, _, withdrawn, _ := parse([]byte(`{"id": "MAL-1", "withdrawn": "2025-01-01T00:00:00Z"}`), npm); !withdrawn {
		t.Error("withdrawn not reported")
	}
}

func TestKey(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		purlType, name, want string
	}{
		{
			"maven",
			"org.evil:lib",
			"org.evil/lib",
		},
		{
			"maven",
			"org.evil/lib",
			"org.evil/lib",
		},
		{
			"pypi",
			"Evil_Pkg.Name",
			"evil-pkg-name",
		},
		{
			"nuget",
			"Evil.Package",
			"evil.package",
		},
		{
			"npm",
			"@Scope/pkg",
			"@Scope/pkg",
		},
	} {
		if got := Key(tt.purlType, tt.name); got != tt.want {
			t.Errorf("Key(%s, %s) = %s, want %s", tt.purlType, tt.name, got, tt.want)
		}
	}
}

func TestAffects(t *testing.T) {
	t.Parallel()

	ranges := []Range{
		{
			Type: "SEMVER",
			Events: []Event{
				{
					Introduced: "2.0.0",
				},
				{
					Fixed: "2.0.3",
				},
				{
					Introduced: "3.1.0",
				},
			},
		},
	}

	for version, want := range map[string]bool{
		"1.9.9":  false,
		"2.0.0":  true,
		"2.0.2":  true,
		"2.0.3":  false,
		"3.0.0":  false,
		"3.1.0":  true,
		"9.0.0":  true,
		"listed": true,
		"x.y":    false,
	} {
		if got := affects(false, []string{
			"listed",
		}, ranges, version); got != want {
			t.Errorf("affects(%s) = %v, want %v", version, got, want)
		}
	}

	if !affects(true, nil, nil, "anything") {
		t.Error("all versions: not affected")
	}
}
