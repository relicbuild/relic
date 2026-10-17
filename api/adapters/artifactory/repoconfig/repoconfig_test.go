package repoconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"relic/internal/artifacts/repository"
)

var refusedConfigurations = map[string]string{
	"federated": "federated repositories are not supported",
}

func TestRoundTripSDKConfigurations(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("testdata/sdk/*.json")
	if err != nil || len(files) < 100 {
		t.Fatalf("fixtures: %v %v", files, err)
	}

	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			t.Parallel()

			doc, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}

			var want map[string]any
			if err := json.Unmarshal(doc, &want); err != nil {
				t.Fatal(err)
			}

			for _, attribute := range []string{
				"rclass",
				"packageType",
			} {
				if reason, refused := refusedConfigurations[want[attribute].(string)]; refused {
					if _, err := FromArtifactory(
						want["key"].(string),
						doc,
					); err == nil ||
						!strings.Contains(err.Error(), reason) {
						t.Errorf("FromArtifactory = %v, want an error containing %q", err, reason)
					}

					return
				}
			}

			r, err := FromArtifactory(want["key"].(string), doc)
			if err != nil {
				t.Fatal(err)
			}

			stored, err := json.Marshal(r.Config)
			if err != nil {
				t.Fatal(err)
			}

			r.Config = repository.Config{}
			if err := json.Unmarshal(stored, &r.Config); err != nil {
				t.Fatal(err)
			}

			out, err := ToArtifactory(r)
			if err != nil {
				t.Fatal(err)
			}

			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}

			for k, w := range want {
				if !reflect.DeepEqual(got[k], w) {
					t.Errorf("%s = %#v, want %#v", k, got[k], w)
				}
			}
		})
	}
}

func TestFromArtifactoryTypedSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		doc  string
		want repository.Repository
	}{
		{
			name: "remote",
			doc: `{"rclass":"remote","packageType":"pypi","url":"https://files.pythonhosted.org","username":"u","password":"p",
				"offline":true,"storeArtifactsLocally":false,"retrievalCachePeriodSecs":60,"socketTimeoutMillis":500,
				"pyPIRegistryUrl":"https://pypi.org","description":"d","environments":["DEV"],"xrayIndex":true}`,
			want: repository.Repository{
				Name:   "k",
				Class:  "remote",
				Format: "pypi",
				Config: repository.Config{
					Description: "d",
					Environments: []string{
						"DEV",
					},
					Remote: &repository.RemoteConfig{
						URL:                 "https://files.pythonhosted.org",
						Username:            "u",
						Password:            "p",
						Offline:             true,
						DisableCache:        true,
						CachePeriodSeconds:  60,
						TimeoutMilliseconds: 500,
						IndexURL:            "https://pypi.org",
					},
					Extensions: map[string]json.RawMessage{
						Extension: json.RawMessage(`{"xrayIndex":true}`),
					},
				},
			},
		},
		{
			name: "a jf template's strings",
			doc:  `{"rclass":"virtual","packageType":"debian","repositories":"a, b,,c","environments":"PROD","defaultDeploymentRepo":"a","debianArchitectures":"amd64"}`,
			want: repository.Repository{
				Name:   "k",
				Class:  "virtual",
				Format: "debian",
				Config: repository.Config{
					Environments: []string{
						"PROD",
					},
					Architectures: []string{
						"amd64",
					},
					Virtual: &repository.VirtualConfig{
						Members: []string{
							"a",
							"b",
							"c",
						},
						DeployTo: "a",
					},
				},
			},
		},
		{
			name: "default architectures as a list",
			doc:  `{"rclass":"local","packageType":"debian","debianDefaultArchitectures":["amd64","arm64"]}`,
			want: repository.Repository{
				Name:   "k",
				Class:  "local",
				Format: "debian",
				Config: repository.Config{
					Architectures: []string{
						"amd64",
						"arm64",
					},
				},
			},
		},
		{
			name: "remote defaults",
			doc:  `{"rclass":"remote","url":"https://registry.npmjs.org","offline":"false"}`,
			want: repository.Repository{
				Name:   "k",
				Class:  "remote",
				Format: "generic",
				Config: repository.Config{
					Remote: &repository.RemoteConfig{
						URL:                 "https://registry.npmjs.org",
						CachePeriodSeconds:  7200,
						TimeoutMilliseconds: 15000,
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := FromArtifactory("k", []byte(tt.doc))
			if err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestFromArtifactoryRefuses(t *testing.T) {
	t.Parallel()

	for name, doc := range map[string]string{
		"another key":   `{"key":"other","rclass":"local"}`,
		"bad class":     `{"rclass":"cloud"}`,
		"federated":     `{"rclass":"federated"}`,
		"bad key":       ``,
		"bad boolean":   `{"rclass":"remote","offline":"maybe"}`,
		"bad number":    `{"rclass":"remote","socketTimeoutMillis":"soon"}`,
		"not an object": `[]`,
	} {
		key := "k"
		if name == "bad key" {
			key, doc = "api", `{"rclass":"local"}`
		}

		if _, err := FromArtifactory(key, []byte(doc)); !errors.Is(err, repository.ErrInvalidRepository) {
			t.Errorf("%s: err = %v, want ErrInvalidRepo", name, err)
		}
	}
}

func TestToArtifactory(t *testing.T) {
	t.Parallel()

	r := repository.Repository{
		Name:   "v",
		Class:  "virtual",
		Format: "npm",
		Config: repository.Config{
			Virtual: &repository.VirtualConfig{
				Members: []string{
					"a",
					"b",
				},
			},
		},
	}

	out, err := ToArtifactory(r)
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"key":          "v",
		"rclass":       "virtual",
		"packageType":  "npm",
		"description":  "",
		"notes":        "",
		"environments": []any{},
		"repositories": []any{
			"a",
			"b",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("= %v, want %v", got, want)
	}

	remote := repository.Repository{
		Name:   "r",
		Class:  "remote",
		Format: "generic",
		Config: repository.Config{
			Remote: &repository.RemoteConfig{
				URL:      "https://x",
				Password: "secret",
			},
		},
	}
	if out, _ := ToArtifactory(remote); strings.Contains(string(out), "secret") ||
		!strings.Contains(string(out), `"password":""`) {
		t.Errorf("password not blanked: %s", out)
	}
}

func TestMerge(t *testing.T) {
	t.Parallel()

	existing, err := FromArtifactory(
		"r",
		[]byte(
			`{"rclass":"remote","packageType":"npm","url":"https://a","password":"p","description":"old","xrayIndex":true}`,
		),
	)
	if err != nil {
		t.Fatal(err)
	}

	got, err := Merge(existing, []byte(`{"url":"","password":"","description":"new","offline":true}`))
	if err != nil {
		t.Fatal(err)
	}

	if rc := got.Config.Remote; rc.URL != "https://a" || rc.Password != "p" || !rc.Offline ||
		got.Config.Description != "new" {
		t.Errorf("merged = %+v %+v", got.Config, rc)
	}

	if string(got.Config.Extensions[Extension]) != `{"xrayIndex":true}` {
		t.Errorf("extension = %s", got.Config.Extensions[Extension])
	}

	nulled, err := Merge(existing, []byte(`{"url":null,"password":null}`))
	if err != nil {
		t.Fatal(err)
	}

	if rc := nulled.Config.Remote; rc.URL != "https://a" || rc.Password != "p" {
		t.Errorf("merged with null url and password = %+v, want both kept", rc)
	}

	if _, err := Merge(existing, []byte(`{"rclass":"local"}`)); !errors.Is(err, repository.ErrInvalidRepository) {
		t.Errorf("class change: err = %v", err)
	}
}

func TestFormatOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		packageType string
		want        repository.Format
		ok          bool
	}{
		{
			name:        "same name",
			packageType: "npm",
			want:        repository.FormatNpm,
			ok:          true,
		},
		{
			name:        "any case",
			packageType: "Docker",
			want:        repository.FormatDocker,
			ok:          true,
		},
		{
			name:        "gems",
			packageType: "gems",
			want:        repository.FormatRubyGems,
			ok:          true,
		},
		{
			name:        "go",
			packageType: "go",
			want:        repository.FormatGoModules,
			ok:          true,
		},
		{
			name:        "huggingfaceml",
			packageType: "huggingfaceml",
			want:        repository.FormatHuggingFace,
			ok:          true,
		},
		{
			name:        "release bundles",
			packageType: "releasebundles",
			want:        repository.FormatRelease,
			ok:          true,
		},
		{
			name:        "build info",
			packageType: "buildinfo",
			want:        repository.FormatBuild,
			ok:          true,
		},
		{
			name:        "Relic's own name is not a package type",
			packageType: "rubygems",
		},
		{
			name:        "yum is an alias of rpm",
			packageType: "yum",
			want:        repository.FormatRPM,
			ok:          true,
		},
		{
			name:        "unknown",
			packageType: "rpm-legacy",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := FormatOf(tt.packageType)
			if got != tt.want || ok != tt.ok {
				t.Errorf("FormatOf(%q) = %q, %v; want %q, %v", tt.packageType, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestPackageTypeRoundTrip(t *testing.T) {
	t.Parallel()

	for _, f := range repository.Formats {
		if got, ok := FormatOf(PackageType(f)); !ok || got != f {
			t.Errorf("FormatOf(PackageType(%q)) = %q, %v", f, got, ok)
		}
	}
}

func TestMissedRetrievalPeriod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "default",
			doc:  `{"rclass":"remote","packageType":"maven","url":"https://repo.example.com"}`,
			want: `"missedRetrievalCachePeriodSecs":1800`,
		},
		{
			name: "off",
			doc:  `{"rclass":"remote","packageType":"maven","url":"https://repo.example.com","missedRetrievalCachePeriodSecs":0}`,
			want: `"missedRetrievalCachePeriodSecs":0`,
		},
		{
			name: "set",
			doc:  `{"rclass":"remote","packageType":"maven","url":"https://repo.example.com","missedRetrievalCachePeriodSecs":60}`,
			want: `"missedRetrievalCachePeriodSecs":60`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, err := FromArtifactory("k", []byte(tt.doc))
			if err != nil {
				t.Fatal(err)
			}

			out, err := ToArtifactory(r)
			if err != nil || !strings.Contains(string(out), tt.want) {
				t.Errorf("ToArtifactory = %s, %v; want %s", out, err, tt.want)
			}
		})
	}
}
