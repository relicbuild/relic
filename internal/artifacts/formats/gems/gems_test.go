package gems

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func TestSpecsIndexAfter(t *testing.T) {
	t.Parallel()

	many := make([]Spec, 200)
	for i := range many {
		many[i] = Spec{
			Name:     "upstream" + strconv.Itoa(i),
			Version:  "1.0.0",
			Platform: "ruby",
		}
	}

	local := []Spec{
		{
			Name:     "relic_local",
			Version:  "2.0.0",
			Platform: "ruby",
		},
	}

	for _, tc := range []struct {
		name     string
		upstream []Spec
	}{
		{
			name:     "a one-byte entry count",
			upstream: many[:1],
		},
		{
			name:     "a multi-byte entry count",
			upstream: many,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			upstream, err := SpecsIndex(tc.upstream)
			if err != nil {
				t.Fatal(err)
			}

			merged, err := SpecsIndexAfter(upstream, local)
			if err != nil {
				t.Fatal(err)
			}

			got := gunzipped(t, merged)
			want := Marshal(specsEntries(append(append([]Spec{}, tc.upstream...), local...)))

			count, _, err := readInt(got[3:])
			if err != nil || count != len(tc.upstream)+1 {
				t.Errorf("entry count = %d %v, want %d", count, err, len(tc.upstream)+1)
			}

			if !bytes.Contains(got, []byte("relic_local")) || !bytes.Contains(got, []byte("upstream0")) {
				t.Errorf("merged index = %q, want the upstream's and the local entries", got)
			}

			assertRubyLoadsSpecsIndex(t, got, want)
		})
	}

	if same, err := SpecsIndexAfter([]byte("not gzip"), nil); err != nil || string(same) != "not gzip" {
		t.Errorf("no local specs = %q %v, want the upstream index unchanged", same, err)
	}

	if _, err := SpecsIndexAfter([]byte("not gzip"), local); !errors.Is(err, ErrInvalidIndex) {
		t.Errorf("an unreadable upstream index = %v, want ErrInvalidIndex", err)
	}
}

func gunzipped(t *testing.T, body []byte) []byte {
	t.Helper()

	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}

	return raw
}

func assertRubyLoadsSpecsIndex(t *testing.T, got, want []byte) {
	t.Helper()

	ruby, err := exec.LookPath("ruby")
	if err != nil {
		return
	}

	dir := t.TempDir()
	for name, body := range map[string][]byte{
		"got":  got,
		"want": want,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	script := `module Gem; class Version; def marshal_load(a); @v = a[0]; end; def to_s; @v; end; end; end
load = ->(f) { Marshal.load(File.binread(f)).map { |n, v, p| [n, v.to_s, p] } }
exit(load.(ARGV[0]) == load.(ARGV[1]) ? 0 : 1)`

	out, err := exec.CommandContext( //nolint:gosec // G204: the test's own script
		t.Context(),
		ruby,
		"-e",
		script,
		filepath.Join(dir, "got"),
		filepath.Join(dir, "want"),
	).CombinedOutput()
	if err != nil {
		t.Errorf("Ruby does not load the merged index as the entries: %v %s", err, out)
	}
}

func TestInfoLineVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		line string
		want string
		ok   bool
	}{
		{
			"1.0.0 rack:>= 2|checksum:abc",
			"1.0.0",
			true,
		},
		{
			"---",
			"",
			false,
		},
		{
			"",
			"",
			false,
		},
		{
			"   ",
			"",
			false,
		},
		{
			"\r",
			"",
			false,
		},
	}
	for _, tt := range tests {
		got, ok := infoLineVersion(tt.line)
		if got != tt.want || ok != tt.ok {
			t.Errorf("infoLineVersion(%q) = %q, %v; want %q, %v", tt.line, got, ok, tt.want, tt.ok)
		}
	}
}
