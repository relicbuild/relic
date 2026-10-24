package access

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"relic/internal/access/auth"
	"relic/internal/access/security"
)

func TestRoundTripSDKPermissionTargets(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("testdata/sdk/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("fixtures: %v %v", files, err)
	}

	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			t.Parallel()

			doc, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}

			var want any
			if err := json.Unmarshal(doc, &want); err != nil {
				t.Fatal(err)
			}

			dropAction(want, "managedXrayMeta")

			p, err := PermissionFromArtifactory(want.(map[string]any)["name"].(string), doc)
			if err != nil {
				t.Fatal(err)
			}

			stored, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}

			back := security.Permission{
				Name: p.Name,
			}
			if err := json.Unmarshal(stored, &back); err != nil {
				t.Fatal(err)
			}

			out, err := PermissionToArtifactory(back)
			if err != nil {
				t.Fatal(err)
			}

			var got any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(got, want) {
				t.Errorf("got  %s\nwant %s", out, doc)
			}
		})
	}
}

func TestPermissionFromArtifactory(t *testing.T) {
	t.Parallel()

	got, err := PermissionFromArtifactory(
		"p",
		[]byte(`{"name":"p","repo":{"repositories":["a"," b","a",""],"include-patterns":["**"],
		"actions":{"users":{"u":["read"," write"]},"groups":{"g":["read"]}}},"build":{"repositories":["artifactory-build-info"]}}`),
	)
	if err != nil {
		t.Fatal(err)
	}

	want := security.Permission{
		Name: "p",
		Rules: []security.Rule{
			{
				Resource: security.ResourceRepository,
				Selector: auth.Selector{
					Repositories: []string{
						"a",
						"b",
					},
				},
				Include: []string{
					"**",
				},
				Grants: []security.Grant{
					{
						User: "u",
						Actions: []auth.Action{
							auth.ActionRead,
							auth.ActionWrite,
						},
					},
					{
						Group: "g",
						Actions: []auth.Action{
							auth.ActionRead,
						},
					},
				},
			},
			{
				Resource: security.ResourceBuild,
				Selector: auth.Selector{
					Repositories: []string{
						"artifactory-build-info",
					},
				},
				Grants: []security.Grant{},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}

	for name, doc := range map[string]string{
		"another name":   `{"name":"q"}`,
		"unknown action": `{"repo":{"repositories":["a"],"actions":{"users":{"u":["fly"]}}}}`,
		"not json":       `{`,
	} {
		if _, err := PermissionFromArtifactory("p", []byte(doc)); !errors.Is(err, security.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestPermissionVocabulary(t *testing.T) {
	t.Parallel()

	doc := `{"name":"p","repo":{"repositories":["ANY REMOTE","ANY LOCAL","libs"],"include-patterns":[],"exclude-patterns":[],
		"actions":{"users":{"u":["read","managedXrayMeta"]}}},
		"releaseBundle":{"repositories":["ANY"],"include-patterns":[],"exclude-patterns":[],"actions":{"groups":{"rel":["read","distribute"]}}}}`

	p, err := PermissionFromArtifactory("p", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}

	repoRule, _ := p.Rule(security.ResourceRepository)
	if !reflect.DeepEqual(
		repoRule.Selector,
		auth.Selector{
			Sets: []auth.RepositorySet{
				auth.SetRemote,
				auth.SetLocal,
			},
			Repositories: []string{
				"libs",
			},
		},
	) ||
		!reflect.DeepEqual(repoRule.Grants[0].Actions, []auth.Action{
			auth.ActionRead,
		}) {
		t.Errorf("repository rule = %+v", repoRule)
	}

	release, _ := p.Rule(security.ResourceRelease)
	if !reflect.DeepEqual(release.Selector.Sets, []auth.RepositorySet{
		auth.SetAll,
	}) ||
		!reflect.DeepEqual(
			release.Grants[0],
			security.Grant{
				Group: "rel",
				Actions: []auth.Action{
					auth.ActionRead,
					auth.ActionPromote,
				},
			},
		) {
		t.Errorf("release rule = %+v", release)
	}

	out, err := PermissionToArtifactory(p)
	if err != nil {
		t.Fatal(err)
	}

	var back Target
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(back.Repo.Repositories, []string{
		"ANY REMOTE",
		"ANY LOCAL",
		"libs",
	}) ||
		!reflect.DeepEqual(back.ReleaseBundle.Actions.Groups["rel"], []string{
			"read",
			"distribute",
		}) {
		t.Errorf("rendered = %s", out)
	}
}

func dropAction(doc any, action string) {
	for _, section := range []string{
		"repo",
		"build",
		"releaseBundle",
	} {
		sec, _ := doc.(map[string]any)[section].(map[string]any)

		actions, _ := sec["actions"].(map[string]any)
		for _, kind := range []string{
			"users",
			"groups",
		} {
			grants, _ := actions[kind].(map[string]any)
			for principal, list := range grants {
				kept := []any{}

				for _, a := range list.([]any) {
					if a != action {
						kept = append(kept, a)
					}
				}

				grants[principal] = kept
			}
		}
	}
}
