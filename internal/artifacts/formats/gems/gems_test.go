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
	"strings"
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

			got := gunzipped(t, specsIndexAfter(t, upstream, local))
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

	if same := specsIndexAfter(t, []byte("not gzip"), nil); string(same) != "not gzip" {
		t.Errorf("no local specs = %q, want the upstream index unchanged", same)
	}

	if _, err := SpecsIndexAfter(bytes.NewReader([]byte("not gzip")), local); !errors.Is(err, ErrInvalidIndex) {
		t.Errorf("an unreadable upstream index = %v, want ErrInvalidIndex", err)
	}
}

func TestSpecsIndexAfterReadsTheUpstreamAsItWrites(t *testing.T) {
	t.Parallel()

	many := make([]Spec, 50000)
	for i := range many {
		many[i] = Spec{
			Name:     "upstream" + strconv.Itoa(i),
			Version:  "1.0.0",
			Platform: "ruby",
		}
	}

	upstream, err := SpecsIndex(many)
	if err != nil {
		t.Fatal(err)
	}

	read := &countingReader{
		r: bytes.NewReader(upstream),
	}

	if _, err := SpecsIndexAfter(read, []Spec{
		{
			Name:     "relic_local",
			Version:  "2.0.0",
			Platform: "ruby",
		},
	}); err != nil {
		t.Fatal(err)
	}

	if read.n >= int64(len(upstream)) {
		t.Errorf("checking the upstream index read all of its %d bytes before writing", len(upstream))
	}
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)

	return n, err
}

func specsIndexAfter(t *testing.T, upstream []byte, specs []Spec) []byte {
	t.Helper()

	index, err := SpecsIndexAfter(bytes.NewReader(upstream), specs)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if _, err := index.WriteTo(&out); err != nil {
		t.Fatal(err)
	}

	return out.Bytes()
}

func TestNewlineTerminated(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, in, want string
		buffer         int
	}{
		{
			name: "an empty file stays empty",
			in:   "",
			want: "",
		},
		{
			name: "a terminated file is unchanged",
			in:   "a 1.0 x\n",
			want: "a 1.0 x\n",
		},
		{
			name: "an unterminated file gets a newline",
			in:   "a 1.0 x",
			want: "a 1.0 x\n",
		},
		{
			name:   "a newline after a full buffer",
			in:     "abcd",
			want:   "abcd\n",
			buffer: 4,
		},
		{
			name:   "a newline after a partly filled buffer",
			in:     "abcd",
			want:   "abcd\n",
			buffer: 64,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := &newlineTerminated{
				r: strings.NewReader(tc.in),
			}

			var out bytes.Buffer

			buf := make([]byte, max(tc.buffer, 1))
			if _, err := io.CopyBuffer(struct {
				io.Writer
			}{
				&out,
			}, struct {
				io.Reader
			}{
				r,
			}, buf); err != nil {
				t.Fatal(err)
			}

			if out.String() != tc.want {
				t.Errorf("got %q, want %q", out.String(), tc.want)
			}
		})
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
