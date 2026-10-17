package pypi

import (
	"archive/zip"
	"bytes"
	"reflect"
	"testing"
)

const sampleMetadata = "Metadata-Version: 2.1\nName: requests\nVersion: 2.32.3\nSummary: Python HTTP for Humans.\n" +
	"Home-page: https://requests.readthedocs.io\nAuthor: Kenneth Reitz\nLicense: Apache-2.0\n" +
	"Project-URL: Source, https://github.com/psf/requests\nClassifier: Programming Language :: Python :: 3\n" +
	"Classifier: License :: OSI Approved :: Apache Software License\nRequires-Python: >=3.8\n" +
	"Requires-Dist: charset-normalizer<4,>=2\nRequires-Dist: idna<4,>=2.5\n\n# Requests\n\nThe description body.\n"

func TestParseMetadata(t *testing.T) {
	t.Parallel()

	got, err := ParseMetadata([]byte(sampleMetadata))
	if err != nil {
		t.Fatal(err)
	}

	want := Metadata{
		Name:           "requests",
		Version:        "2.32.3",
		Summary:        "Python HTTP for Humans.",
		License:        "Apache-2.0",
		RequiresPython: ">=3.8",
		Author:         "Kenneth Reitz",
		HomePage:       "https://requests.readthedocs.io",
		ProjectURLs: []string{
			"Source, https://github.com/psf/requests",
		},
		RequiresDist: []string{
			"charset-normalizer<4,>=2",
			"idna<4,>=2.5",
		},
		Classifiers: []string{
			"Programming Language :: Python :: 3",
			"License :: OSI Approved :: Apache Software License",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseMetadata =\n%+v\nwant\n%+v", got, want)
	}

	if _, err := ParseMetadata([]byte("Version: 1\n")); err == nil {
		t.Error("metadata without a name parsed")
	}
}

func TestWheelMetadata(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	for _, entry := range []struct {
		name string
		body string
	}{
		{
			name: "vendored/x.dist-info/METADATA",
			body: "Name: wrong\n",
		},
		{
			name: "requests/__init__.py",
		},
		{
			name: "requests-2.32.3.dist-info/METADATA",
			body: sampleMetadata,
		},
	} {
		w, _ := zw.Create(entry.name)
		_, _ = w.Write([]byte(entry.body))
	}

	_ = zw.Close()

	raw, err := wheelMetadata(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil || string(raw) != sampleMetadata {
		t.Errorf("wheelMetadata = %q %v, want the top-level dist-info's METADATA", raw, err)
	}
}
