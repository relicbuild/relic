package npm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestValidName(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]bool{
		"lodash":     true,
		"@scope/pkg": true,
		"a.b-c_d~e":  true,
		"":           false,
		"Upper":      false,
		"@scope":     false,
		"../x":       false,
		"a/b":        false,
		"@s/a/b":     false,
		".hidden":    false,
	} {
		if got := ValidName(name); got != want {
			t.Errorf("ValidName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestTarballPath(t *testing.T) {
	t.Parallel()

	if got := TarballPath("left-pad", "1.3.0"); got != "left-pad/-/left-pad-1.3.0.tgz" {
		t.Error(got)
	}

	if got := TarballPath("@acme/ui", "2.0.0-rc.1"); got != "@acme/ui/-/@acme/ui-2.0.0-rc.1.tgz" {
		t.Error(got)
	}
}

func Tarball(t testing.TB, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)

	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(
			&tar.Header{
				Name:     name,
				Mode:     0o644,
				Size:     int64(len(content)),
				Typeflag: tar.TypeReg,
			},
		); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func TestReadManifest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		data    []byte
		want    string
		wantErr bool
	}{
		{
			name: "package root",
			data: Tarball(t, map[string]string{
				"package/index.js":     "x",
				"package/package.json": `{"name":"a"}`,
			}),
			want: `{"name":"a"}`,
		},
		{
			name: "other root folder",
			data: Tarball(t, map[string]string{
				"left-pad/package.json": `{"name":"b"}`,
			}),
			want: `{"name":"b"}`,
		},
		{
			name: "nested package.json ignored",
			data: Tarball(t, map[string]string{
				"package/node_modules/x/package.json": `{}`,
			}),
			wantErr: true,
		},
		{
			name:    "not gzip",
			data:    []byte("plain"),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := readManifest(bytes.NewReader(tt.data))
			if tt.wantErr {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("err = %v, want ErrInvalid", err)
				}

				return
			}

			if err != nil || string(got) != tt.want {
				t.Errorf("readManifest = %q, %v", got, err)
			}
		})
	}
}

func TestParseKeywords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "a list",
			raw:  `["http","client"]`,
			want: []string{
				"http",
				"client",
			},
		},
		{
			name: "a string, as old packages have",
			raw:  `"http, client  util"`,
			want: []string{
				"http",
				"client",
				"util",
			},
		},
		{
			name: "missing",
			want: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := parseKeywords(json.RawMessage(tt.raw)); !slices.Equal(got, tt.want) {
				t.Errorf("parseKeywords(%s) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestParsePerson(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want Person
		ok   bool
	}{
		{
			name: "object",
			raw:  `{"name":"alice","email":"alice@example.com"}`,
			want: Person{
				Name:  "alice",
				Email: "alice@example.com",
			},
			ok: true,
		},
		{
			name: "text with an email and a url",
			raw:  `"Alice Doe <alice@example.com> (https://example.com)"`,
			want: Person{
				Name:  "Alice Doe",
				Email: "alice@example.com",
			},
			ok: true,
		},
		{
			name: "text with a name only",
			raw:  `"alice"`,
			want: Person{
				Name: "alice",
			},
			ok: true,
		},
		{
			name: "object without a name",
			raw:  `{"email":"alice@example.com"}`,
		},
		{
			name: "missing",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got, ok := parsePerson(json.RawMessage(tt.raw)); got != tt.want || ok != tt.ok {
				t.Errorf("parsePerson(%s) = %+v, %v; want %+v, %v", tt.raw, got, ok, tt.want, tt.ok)
			}
		})
	}
}
