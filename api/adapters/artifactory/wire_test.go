package artifactory

import (
	"reflect"
	"strings"
	"testing"

	"relic/api/packages"
	"relic/internal/artifacts/repository"
)

func TestParseTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		escaped string
		want    packages.Target
		wantErr bool
	}{
		{
			name:    "file",
			escaped: "/artifactory/libs/a/b.txt",
			want: packages.Target{
				Repo: "libs",
				Path: "a/b.txt",
			},
		},
		{
			name:    "folder",
			escaped: "/artifactory/libs/dir/sub/",
			want: packages.Target{
				Repo: "libs",
				Path: "dir/sub/",
				Dir:  true,
			},
		},
		{
			name:    "repo root",
			escaped: "/artifactory/libs/",
			want: packages.Target{
				Repo: "libs",
				Path: "",
				Dir:  true,
			},
		},
		{
			name:    "jf matrix params",
			escaped: "/artifactory/generic-local/my%20dir/a+b%3Bc%23d%3Fe%25f.txt;k=v+1;k=v2;u=%C3%BC%2F%C3%A9;x=a%3Db;y=p%2Cq;z=s%7Ct;build.name=my+build;build.number=1;build.timestamp=1700000000000;vcs.url=https%3A%2F%2Fg%2Fx%2Cy",
			want: packages.Target{
				Repo: "generic-local",
				Path: "my dir/a+b;c#d?e%f.txt",
				Props: repository.Properties{
					"k": {
						"v 1",
						"v2",
					},
					"u": {
						"ü/é",
					},
					"x": {
						"a=b",
					},
					"y": {
						"p",
						"q",
					},
					"z": {
						"s|t",
					},
					"build.name": {
						"my build",
					},
					"build.number": {
						"1",
					},
					"build.timestamp": {
						"1700000000000",
					},
					"vcs.url": {
						"https://g/x",
						"y",
					},
				},
			},
		},
		{
			name:    "escaped comma stays literal",
			escaped: "/artifactory/r/f;k=a%5C%2Cb",
			want: packages.Target{
				Repo: "r",
				Path: "f",
				Props: repository.Properties{
					"k": {
						"a,b",
					},
				},
			},
		},
		{
			name:    "no repo",
			escaped: "/artifactory/",
			wantErr: true,
		},
		{
			name:    "bad escape",
			escaped: "/artifactory/r/%zz",
			wantErr: true,
		},
		{
			name:    "empty property key",
			escaped: "/artifactory/r/f;=v",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseTarget(tt.escaped)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseTarget error = %v, wantErr %v", err, tt.wantErr)
			}

			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseTarget(%q)\n got %+v\nwant %+v", tt.escaped, got, tt.want)
			}
		})
	}
}

func FuzzParseTarget(f *testing.F) {
	for _, s := range []string{
		"/artifactory/r/a/b;k=v",
		"/artifactory/r/%3B;a=%2C,b",
		"/artifactory/r/;;;",
		"/artifactory/r/a\\,b",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		got, err := parseTarget(s)
		if err != nil {
			return
		}

		if got.Repo == "" || strings.Contains(got.Repo, "/") {
			t.Fatalf("parseTarget(%q) repo = %q", s, got.Repo)
		}

		for k := range got.Props {
			if k == "" {
				t.Fatalf("parseTarget(%q) produced an empty property key", s)
			}
		}
	})
}

func TestRawQuery(t *testing.T) {
	t.Parallel()

	q := rawQuery("properties=k=v+1%2Cv2;x=a%3Db;y=p%5C%2Cq;z=s%7Ct&recursive=0")

	props, err := parseProps(strings.Split(q["properties"], ";"))
	if err != nil {
		t.Fatal(err)
	}

	want := repository.Properties{
		"k": {
			"v 1",
			"v2",
		},
		"x": {
			"a=b",
		},
		"y": {
			"p,q",
		},
		"z": {
			"s|t",
		},
	}
	if !reflect.DeepEqual(props, want) || q["recursive"] != "0" {
		t.Errorf("props = %v, recursive = %q", props, q["recursive"])
	}

	if got := splitValues(queryValue(rawQuery("properties=k,x+y,z%7Cw"), "properties")); !reflect.DeepEqual(
		got,
		[]string{
			"k",
			"x y",
			"z|w",
		},
	) {
		t.Errorf("delete keys = %q", got)
	}
}

func TestParsePropsRefusesInvalidKeys(t *testing.T) {
	t.Parallel()

	for _, pair := range []string{
		"k%20sp=v",
		"p%7Cq=1",
		"a%2Cb=c",
		"a%3Bb=c",
		strings.Repeat("k", 256) + "=v",
	} {
		if props, err := parseProps([]string{
			pair,
		}); err == nil {
			t.Errorf("parseProps(%q) = %v, want an error", pair, props)
		}
	}

	if _, err := parseProps([]string{
		"build.name=app",
		"docker.manifest-digest=sha256:1",
	}); err != nil {
		t.Errorf("parseProps of valid keys = %v", err)
	}
}

func TestPropertyPairs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want map[string][]string
	}{
		{
			name: "escaped separately",
			raw:  "a=1%2C2;b=x%3By",
			want: map[string][]string{
				"a": {
					"1",
					"2",
				},
				"b": {
					"x;y",
				},
			},
		},
		{
			name: "fully escaped",
			raw:  "team%3Dcore%3Benv%3Dprod%2Cqa",
			want: map[string][]string{
				"team": {
					"core",
				},
				"env": {
					"prod",
					"qa",
				},
			},
		},
		{
			name: "plus kept",
			raw:  "k%3Da%2Bb",
			want: map[string][]string{
				"k": {
					"a+b",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseProps(propertyPairs(tt.raw))
			if err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(map[string][]string(got), tt.want) {
				t.Errorf("props = %v, want %v", got, tt.want)
			}
		})
	}
}
