package packages

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestCopyBody(t *testing.T) {
	t.Parallel()

	for _, size := range []int{
		0,
		1,
		256 << 10,
		256<<10 + 1,
		3 << 20,
	} {
		want := make([]byte, size)
		_, _ = rand.Read(want)

		var got bytes.Buffer

		n, err := copyBody(&got, bytes.NewReader(want))
		if err != nil || n != int64(size) || !bytes.Equal(got.Bytes(), want) {
			t.Errorf("size %d: copied %d, err %v, equal %v", size, n, err, bytes.Equal(got.Bytes(), want))
		}
	}
}

func BenchmarkCopyBody(b *testing.B) {
	src := make([]byte, 100<<20)

	b.SetBytes(int64(len(src)))

	for b.Loop() {
		_, _ = copyBody(discard{}, bytes.NewReader(src))
	}
}

// discard, unlike io.Discard, is not a ReaderFrom.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
