package cargo

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"relic/internal/artifacts/repository"
)

func publishBody(meta string, crate []byte) []byte {
	var b []byte

	b = binary.LittleEndian.AppendUint32(b, uint32(len(meta))) //nolint:gosec // test data
	b = append(b, meta...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(crate))) //nolint:gosec // test data

	return append(b, crate...)
}

func TestIndexPrefix(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"a":     "1/",
		"ab":    "2/",
		"abc":   "3/a/",
		"Serde": "se/rd/",
		"cargo": "ca/rg/",
	}
	for in, want := range tests {
		t.Run(in, func(t *testing.T) {
			t.Parallel()

			if got := IndexPrefix(in); got != want {
				t.Errorf("IndexPrefix(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

func TestDownloadURL(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"https://example.com/crates":                             "https://example.com/crates/Serde/1.0.0/download",
		"https://example.com/{prefix}/{crate}/{version}":         "https://example.com/Se/rd/Serde/1.0.0",
		"https://example.com/{lowerprefix}/{crate}-{version}.gz": "https://example.com/se/rd/Serde-1.0.0.gz",
	}
	for dl, want := range tests {
		t.Run(dl, func(t *testing.T) {
			t.Parallel()

			got, err := downloadURL(dl, "Serde", "1.0.0")
			if err != nil || got != want {
				t.Errorf("downloadURL(%q) = %q, %v; want %q", dl, got, err, want)
			}
		})
	}
}

func TestHighestVersion(t *testing.T) {
	t.Parallel()

	published := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	version := func(vers string, age time.Duration) localVersion {
		return localVersion{
			item: repository.Item{
				Created: published.Add(age),
			},
			line: indexLine{
				Vers: vers,
			},
		}
	}

	tests := []struct {
		name     string
		versions []localVersion
		want     string
	}{
		{
			name: "the highest of several",
			versions: []localVersion{
				version("1.0.0", 0),
				version("2.0.0", time.Hour),
				version("1.5.0", 2*time.Hour),
			},
			want: "2.0.0",
		},
		{
			name: "semantic order, not text order",
			versions: []localVersion{
				version("10.0.0", 0),
				version("9.0.0", time.Hour),
			},
			want: "10.0.0",
		},
		{
			name: "versions equal but for build metadata: the newest upload",
			versions: []localVersion{
				version("1.0.0+old", 0),
				version("1.0.0+new", time.Hour),
				version("1.0.0+middle", 30*time.Minute),
			},
			want: "1.0.0+new",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := highestVersion(tt.versions); got != tt.want {
				t.Errorf("highestVersion = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParsePublish(t *testing.T) {
	t.Parallel()

	meta := `{"name":"demo","vers":"1.0.0","deps":[
		{"name":"serde","version_req":"^1","features":["derive"],"optional":false,"default_features":true,"target":null,"kind":"normal","registry":"https://github.com/rust-lang/crates.io-index","explicit_name_in_toml":null},
		{"name":"rand","version_req":"^0.8","features":[],"optional":true,"default_features":true,"target":null,"kind":"normal","registry":null,"explicit_name_in_toml":"rnd"}],
		"features":{"default":["std"],"std":[],"extra":["dep:rnd"]},"links":null}`

	m, crate, err := parsePublish(publishBody(meta, []byte("crate-bytes")))
	if err != nil || string(crate) != "crate-bytes" {
		t.Fatalf("parsePublish = %q, %v", crate, err)
	}

	l := m.line("abc")
	raw, _ := json.Marshal(l)

	var got map[string]any

	_ = json.Unmarshal(raw, &got)
	deps := got["deps"].([]any)

	renamed := deps[1].(map[string]any)
	if renamed["name"] != "rnd" || renamed["package"] != "rand" || renamed["req"] != "^0.8" {
		t.Errorf("renamed dep = %v", renamed)
	}

	if _, ok := deps[0].(map[string]any)["package"]; ok {
		t.Errorf("plain dep has package: %v", deps[0])
	}

	if l.V != 2 || l.Features2["extra"] == nil || l.Features["extra"] != nil || l.Features["std"] == nil {
		t.Errorf("features = %v, features2 = %v, v = %d", l.Features, l.Features2, l.V)
	}

	for name, body := range map[string][]byte{
		"truncated": {
			1,
			0,
		},
		"bad length":   publishBody(meta, nil)[:10],
		"invalid name": publishBody(`{"name":"../x","vers":"1"}`, nil),
	} {
		if _, _, err := parsePublish(body); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}
