package auth

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMatchAnt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern, path string
		want          bool
	}{
		{
			"**",
			"",
			true,
		},
		{
			"**",
			"a/b/c.jar",
			true,
		},
		{
			"org/**",
			"org",
			true,
		},
		{
			"org/**",
			"org/acme/lib.jar",
			true,
		},
		{
			"org/**",
			"com/acme",
			false,
		},
		{
			"**/*.jar",
			"a/b/c.jar",
			true,
		},
		{
			"**/*.jar",
			"c.jar",
			true,
		},
		{
			"**/*.jar",
			"a/b/c.pom",
			false,
		},
		{
			"a/*/c",
			"a/b/c",
			true,
		},
		{
			"a/*/c",
			"a/b/x/c",
			false,
		},
		{
			"a/**/c",
			"a/c",
			true,
		},
		{
			"a/**/c",
			"a/b/x/c",
			true,
		},
		{
			"a/?.txt",
			"a/b.txt",
			true,
		},
		{
			"a/?.txt",
			"a/bb.txt",
			false,
		},
		{
			"a/**/**/b",
			"a/b",
			true,
		},
		{
			"/org/**/",
			"org/x",
			true,
		},
		{
			"[",
			"[",
			true,
		},
		{
			"x/[draft]/**",
			"x/[draft]/notes.txt",
			true,
		},
		{
			"x/[draft]/**",
			"x/d/notes.txt",
			false,
		},
		{
			"a\\*",
			"a\\b",
			true,
		},
		{
			"a\\*",
			"a*",
			false,
		},
		{
			"*.tar.*",
			"lib.tar.gz",
			true,
		},
		{
			"*b*c",
			"abxbc",
			true,
		},
		{
			"*b*c",
			"abxbd",
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"~"+tt.path, func(t *testing.T) {
			t.Parallel()

			if got := MatchesAntPattern(tt.pattern, tt.path); got != tt.want {
				t.Errorf("MatchesAntPattern(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
			}
		})
	}
}

func TestMatchesAntPatternTakesPolynomialTime(t *testing.T) {
	t.Parallel()

	pattern := "**/a/**/a/**/a/**/a/**/a/**/b"
	path := strings.Repeat("a/", 3000) + "c"

	matched := make(chan bool, 1)

	go func() {
		matched <- MatchesAntPattern(pattern, path)
	}()

	select {
	case got := <-matched:
		if got {
			t.Errorf("MatchesAntPattern(%q, a/…/c) = true, want false", pattern)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("MatchesAntPattern(%q, 3000 segments) did not finish in 5 s", pattern)
	}
}

func FuzzMatchAnt(f *testing.F) {
	f.Add("**/*.jar", "a/b/c.jar")
	f.Add("a/**/b/**/c", "a/x/b/y/z/c")
	f.Fuzz(func(t *testing.T, pattern, p string) {
		if len(pattern) > 64 || len(p) > 256 {
			return
		}

		_ = MatchesAntPattern(pattern, p)
		if !MatchesAntPattern("**", p) {
			t.Fatalf(`"**" must match %q`, p)
		}
	})
}

func TestPermissions(t *testing.T) {
	t.Parallel()

	perms := Permissions{
		All: []Permission{
			{
				Name: "java",
				Repository: &Section{
					Selector: Selector{
						Repositories: []string{
							"libs-local",
						},
					},
					Include: []string{
						"org/**",
					},
					Exclude: []string{
						"org/secret/**",
					},
					Users: map[string][]Action{
						"bob": {
							ActionRead,
							ActionWrite,
						},
					},
					Groups: map[string][]Action{
						"devs": {
							ActionRead,
							ActionAnnotate,
						},
					},
				},
			},
			{
				Name: "remote-readers",
				Repository: &Section{
					Selector: Selector{
						Sets: []RepositorySet{
							SetRemote,
						},
					},
					Groups: map[string][]Action{
						"readers": {
							ActionRead,
						},
					},
				},
			},
			{
				Name: "ops",
				Repository: &Section{
					Selector: Selector{
						Sets: []RepositorySet{
							SetAll,
						},
					},
					Users: map[string][]Action{
						"ops": {
							ActionManage,
						},
					},
				},
			},
			{
				Name: "releases",
				Release: &Section{
					Selector: Selector{
						Sets: []RepositorySet{
							SetAll,
						},
					},
					Include: []string{
						"web/**",
					},
					Users: map[string][]Action{
						"ci": {
							ActionRead,
							ActionPromote,
						},
					},
				},
			},
			{
				Name: "builds",
				Build: &Section{
					Selector: Selector{
						Repositories: []string{
							"relic-build-info",
						},
					},
					Include: []string{
						"app-*/**",
					},
					Users: map[string][]Action{
						"ci": {
							ActionRead,
							ActionWrite,
						},
					},
				},
			},
		},
	}
	libs := Repository{
		Name:  "libs-local",
		Class: "local",
	}
	other := Repository{
		Name:  "other-local",
		Class: "local",
	}
	remote := Repository{
		Name:  "maven-remote",
		Class: "remote",
	}
	bob := Subject{
		User: "bob",
	}
	dev := Subject{
		User: "carol",
		Groups: []string{
			"devs",
		},
	}
	reader := Subject{
		User: "dan",
		Groups: []string{
			"readers",
		},
	}
	ops := Subject{
		User: "ops",
	}
	ci := Subject{
		User: "ci",
	}
	admin := Subject{
		User:  "root",
		Admin: true,
	}
	nobody := Subject{
		User: "eve",
	}

	repoTests := []struct {
		name string
		s    Subject
		repo Repository
		path string
		a    Action
		want bool
	}{
		{
			"user read in include",
			bob,
			libs,
			"org/a.jar",
			ActionRead,
			true,
		},
		{
			"user write in include",
			bob,
			libs,
			"org/a.jar",
			ActionWrite,
			true,
		},
		{
			"user delete not granted",
			bob,
			libs,
			"org/a.jar",
			ActionDelete,
			false,
		},
		{
			"outside include",
			bob,
			libs,
			"com/a.jar",
			ActionRead,
			false,
		},
		{
			"excluded path",
			bob,
			libs,
			"org/secret/k.txt",
			ActionRead,
			false,
		},
		{
			"empty segment cannot dodge exclude",
			bob,
			libs,
			"org//secret/k.txt",
			ActionRead,
			false,
		},
		{
			"leading slash cannot dodge exclude",
			bob,
			libs,
			"/org/secret/k.txt",
			ActionWrite,
			false,
		},
		{
			"dot segment denied",
			bob,
			libs,
			"org/./secret/k.txt",
			ActionRead,
			false,
		},
		{
			"dot-dot segment denied",
			bob,
			libs,
			"org/x/../secret/k.txt",
			ActionRead,
			false,
		},
		{
			"empty segment inside include still granted",
			bob,
			libs,
			"org//a.jar",
			ActionRead,
			true,
		},
		{
			"other repo",
			bob,
			other,
			"org/a.jar",
			ActionRead,
			false,
		},
		{
			"group grant",
			dev,
			libs,
			"org/a.jar",
			ActionAnnotate,
			true,
		},
		{
			"group lacks write",
			dev,
			libs,
			"org/a.jar",
			ActionWrite,
			false,
		},
		{
			"any remote read",
			reader,
			remote,
			"x",
			ActionRead,
			true,
		},
		{
			"any remote not local",
			reader,
			libs,
			"org/a.jar",
			ActionRead,
			false,
		},
		{
			"manage implies delete",
			ops,
			other,
			"x",
			ActionDelete,
			true,
		},
		{
			"admin bypass",
			admin,
			other,
			"x",
			ActionManage,
			true,
		},
		{
			"default deny",
			nobody,
			libs,
			"org/a.jar",
			ActionRead,
			false,
		},
		{
			"build section does not grant repos",
			ci,
			libs,
			"org/a.jar",
			ActionRead,
			false,
		},
	}
	for _, tt := range repoTests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := perms.CanInRepository(tt.s, tt.repo, tt.path, tt.a); got != tt.want {
				t.Errorf("CanRepo = %v, want %v", got, tt.want)
			}
		})
	}

	if !perms.CanReadRepository(bob, libs) || perms.CanReadRepository(bob, other) ||
		!perms.CanReadRepository(reader, remote) {
		t.Error("CanReadRepo")
	}

	buildTests := []struct {
		name       string
		s          Subject
		repository string
		build      string
		a          Action
		want       bool
	}{
		{
			"matching build",
			ci,
			"relic-build-info",
			"app-web",
			ActionWrite,
			true,
		},
		{
			"non matching build",
			ci,
			"relic-build-info",
			"infra",
			ActionRead,
			false,
		},
		{
			"build in a repository the rule does not select",
			ci,
			"team-relic-build-info",
			"app-web",
			ActionRead,
			false,
		},
		{
			"not granted action",
			ci,
			"relic-build-info",
			"app-web",
			ActionDelete,
			false,
		},
		{
			"repo grant is not a build grant",
			bob,
			"relic-build-info",
			"app-web",
			ActionRead,
			false,
		},
		{
			"admin",
			admin,
			"team-relic-build-info",
			"anything",
			ActionDelete,
			true,
		},
	}
	for _, tt := range buildTests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := perms.CanBuild(tt.s, tt.repository, tt.build, tt.a); got != tt.want {
				t.Errorf("CanBuild = %v, want %v", got, tt.want)
			}
		})
	}

	releaseTests := []struct {
		name          string
		s             Subject
		release, vers string
		a             Action
		want          bool
	}{
		{
			"matching release",
			ci,
			"web",
			"1.0.0",
			ActionPromote,
			true,
		},
		{
			"read",
			ci,
			"web",
			"1.0.0",
			ActionRead,
			true,
		},
		{
			"other release",
			ci,
			"api",
			"1.0.0",
			ActionRead,
			false,
		},
		{
			"not granted action",
			ci,
			"web",
			"1.0.0",
			ActionDelete,
			false,
		},
		{
			"repository grants are not release grants",
			ops,
			"web",
			"1.0.0",
			ActionRead,
			false,
		},
		{
			"admin",
			admin,
			"api",
			"2",
			ActionDelete,
			true,
		},
	}
	for _, tt := range releaseTests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := perms.CanRelease(tt.s, "release-bundles-v2", tt.release, tt.vers, tt.a); got != tt.want {
				t.Errorf("CanRelease = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEffective(t *testing.T) {
	t.Parallel()

	perms := Permissions{
		All: []Permission{
			{
				Name: "readers",
				Repository: &Section{
					Selector: Selector{
						Sets: []RepositorySet{
							SetLocal,
						},
					},
					Users: map[string][]Action{
						"ann": {
							ActionRead,
						},
					},
					Groups: map[string][]Action{
						"devs": {
							ActionRead,
						},
					},
				},
			},
			{
				Name: "deployers",
				Repository: &Section{
					Selector: Selector{
						Repositories: []string{
							"libs",
						},
					},
					Include: []string{
						"org/**",
					},
					Users: map[string][]Action{
						"ann": {
							ActionWrite,
						},
						"bob": {
							ActionManage,
						},
					},
				},
			},
			{
				Name: "elsewhere",
				Repository: &Section{
					Selector: Selector{
						Repositories: []string{
							"other",
						},
					},
					Users: map[string][]Action{
						"cid": {
							ActionRead,
						},
					},
				},
			},
			{
				Name: "no-secrets",
				Repository: &Section{
					Selector: Selector{
						Repositories: []string{
							"libs",
						},
					},
					Exclude: []string{
						"org/**",
					},
					Users: map[string][]Action{
						"dan": {
							ActionRead,
						},
					},
				},
			},
		},
		AdminOnly: []string{
			"trash",
		},
	}
	libs := Repository{
		Name:  "libs",
		Class: "local",
	}

	tests := []struct {
		name string
		repo Repository
		path string
		want []Grant
	}{
		{
			"merged across targets, manage implies all",
			libs,
			"org/app.jar",
			[]Grant{
				{
					Principal: "ann",
					Actions: []Action{
						ActionRead,
						ActionWrite,
					},
					Permissions: []string{
						"readers",
						"deployers",
					},
				},
				{
					Principal: "bob",
					Actions: []Action{
						ActionRead,
						ActionAnnotate,
						ActionWrite,
						ActionDelete,
						ActionManage,
					},
					Permissions: []string{
						"deployers",
					},
				},
				{
					Principal: "devs",
					Group:     true,
					Actions: []Action{
						ActionRead,
					},
					Permissions: []string{
						"readers",
					},
				},
			},
		},
		{
			"excludes apply",
			libs,
			"tools/x.jar",
			[]Grant{
				{
					Principal: "ann",
					Actions: []Action{
						ActionRead,
					},
					Permissions: []string{
						"readers",
					},
				},
				{
					Principal: "dan",
					Actions: []Action{
						ActionRead,
					},
					Permissions: []string{
						"no-secrets",
					},
				},
				{
					Principal: "devs",
					Group:     true,
					Actions: []Action{
						ActionRead,
					},
					Permissions: []string{
						"readers",
					},
				},
			},
		},
		{
			"trash is administrators only",
			Repository{
				Name:  "trash",
				Class: "local",
			},
			"libs/a",
			[]Grant{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := perms.Effective(tt.repo, tt.path); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Effective =\n%+v\nwant\n%+v", got, tt.want)
			}
		})
	}
}
