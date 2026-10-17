package malicious

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"testing"
)

func TestReadBatches(t *testing.T) {
	t.Parallel()

	var archive bytes.Buffer

	zw := zip.NewWriter(&archive)

	write := func(name, content string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}

	for i := range 5 {
		write(
			fmt.Sprintf("npm/MAL-2025-%d.json", i),
			fmt.Sprintf(
				`{"id": "MAL-2025-%d", "affected": [{"package": {"ecosystem": "npm", "name": "evil-%d"}}]}`,
				i,
				i,
			),
		)
	}

	write("npm/MAL-2025-9.json", `{"id": "MAL-2025-9", "withdrawn": "2025-01-01T00:00:00Z"}`)
	write("npm/GHSA-xxxx.json", `{"id": "GHSA-xxxx"}`)

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	zr, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal(err)
	}

	r := &Refresher{
		log: slog.New(slog.DiscardHandler),
	}

	var sizes []int

	var ids []string

	err = r.readBatches(t.Context(), zr, Ecosystems[0], 2, func(batch []advisory) error {
		sizes = append(sizes, len(batch))
		for _, a := range batch {
			ids = append(ids, a.id)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(sizes, []int{
		2,
		2,
		2,
	}) {
		t.Errorf("batch sizes = %v, want 2, 2, 2", sizes)
	}

	if want := []string{
		"MAL-2025-0",
		"MAL-2025-1",
		"MAL-2025-2",
		"MAL-2025-3",
		"MAL-2025-4",
		"MAL-2025-9",
	}; !slices.Equal(ids, want) {
		t.Errorf("advisories = %v, want %v", ids, want)
	}

	errStore := errors.New("store failed")

	for _, tt := range []struct {
		name      string
		size      int
		wantCalls int
	}{
		{
			name:      "a full batch",
			size:      2,
			wantCalls: 2,
		},
		{
			name:      "the last partial batch",
			size:      4,
			wantCalls: 2,
		},
	} {
		calls := 0

		err := r.readBatches(t.Context(), zr, Ecosystems[0], tt.size, func([]advisory) error {
			calls++
			if calls == 2 {
				return errStore
			}

			return nil
		})
		if !errors.Is(err, errStore) || calls != tt.wantCalls {
			t.Errorf(
				"%s: a failing store = %v after %d calls, want its error after %d",
				tt.name,
				err,
				calls,
				tt.wantCalls,
			)
		}
	}
}
