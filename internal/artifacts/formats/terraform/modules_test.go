package terraform

import (
	"archive/zip"
	"bytes"
	"errors"
	"slices"
	"testing"
)

func TestParseModuleSource(t *testing.T) {
	t.Parallel()

	const download = "https://registry.example.com/v1/modules/acme/label/null/0.25.0/download"

	for _, tc := range []struct {
		name, location string
		want           moduleSource
		wantErr        error
	}{
		{
			name:     "a GitHub repository at a tag",
			location: "git::https://github.com/cloudposse/terraform-null-label?ref=0.25.0",
			want: moduleSource{
				url:      "https://codeload.github.com/cloudposse/terraform-null-label/zip/0.25.0",
				stripTop: true,
			},
		},
		{
			name:     "a GitHub repository's subdirectory",
			location: "git::https://github.com/acme/modules.git//network/vpc?ref=v1.2.0",
			want: moduleSource{
				url:      "https://codeload.github.com/acme/modules/zip/v1.2.0",
				stripTop: true,
				subdir:   "network/vpc",
			},
		},
		{
			name:     "a tar.gz archive named by its query",
			location: "https://files.example.com/label.tar.gz//mod?archive=tar.gz",
			want: moduleSource{
				url:     "https://files.example.com/label.tar.gz",
				tarGzip: true,
				subdir:  "mod",
			},
		},
		{
			name:     "a relative zip archive",
			location: "/archives/label-0.25.0.zip",
			want: moduleSource{
				url: "https://registry.example.com/archives/label-0.25.0.zip",
			},
		},
		{
			name:     "a git repository elsewhere",
			location: "git::https://gitlab.example.com/acme/label.git?ref=1.0.0",
			wantErr:  ErrUnsupportedSource,
		},
		{
			name:     "a GitHub repository without a ref",
			location: "git::https://github.com/acme/label",
			wantErr:  ErrUnsupportedSource,
		},
		{
			name:     "an S3 bucket",
			location: "s3::https://s3.amazonaws.com/acme/label.zip",
			wantErr:  ErrUnsupportedSource,
		},
		{
			name:     "a subdirectory outside the archive",
			location: "https://files.example.com/label.zip//../etc",
			wantErr:  ErrUnsupportedSource,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseModuleSource(download, tc.location)
			if got != tc.want || !errors.Is(err, tc.wantErr) {
				t.Errorf("parseModuleSource(%q) = %+v, %v; want %+v, %v", tc.location, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}

		_, _ = w.Write([]byte(body))
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func TestRepackModule(t *testing.T) {
	t.Parallel()

	archive := zipOf(t, map[string]string{
		"label-0.25.0/main.tf":               "root",
		"label-0.25.0/modules/null/main.tf":  "null",
		"label-0.25.0/modules/null/../x.tf":  "outside",
		"label-0.25.0/modules/other/main.tf": "other",
	})

	for _, tc := range []struct {
		name    string
		src     moduleSource
		want    []string
		wantErr bool
	}{
		{
			name: "the top directory dropped",
			src: moduleSource{
				stripTop: true,
			},
			want: []string{
				"main.tf",
				"modules/null/main.tf",
				"modules/other/main.tf",
			},
		},
		{
			name: "a subdirectory",
			src: moduleSource{
				stripTop: true,
				subdir:   "modules/null",
			},
			want: []string{
				"main.tf",
			},
		},
		{
			name: "a subdirectory the archive does not have",
			src: moduleSource{
				stripTop: true,
				subdir:   "modules/missing",
			},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repacked, err := repackModule(archive, tc.src)
			if tc.wantErr {
				if err == nil {
					t.Fatal("repackModule succeeded, want an error")
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			zr, err := zip.NewReader(bytes.NewReader(repacked), int64(len(repacked)))
			if err != nil {
				t.Fatal(err)
			}

			var names []string
			for _, f := range zr.File {
				names = append(names, f.Name)
			}

			slices.Sort(names)

			if !slices.Equal(names, tc.want) {
				t.Errorf("repacked files = %q, want %q", names, tc.want)
			}
		})
	}
}
