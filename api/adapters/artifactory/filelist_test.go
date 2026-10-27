package artifactory

import (
	"net/http/httptest"
	"testing"

	"relic/api/adapters/artifactory/wire"
)

func TestFileListWriterMatchesWriteJSON(t *testing.T) {
	t.Parallel()

	entry := func(uri string, folder bool) fileListFile {
		return fileListFile{
			URI:          uri,
			Size:         42,
			LastModified: "2026-10-26T12:00:00.000Z",
			Folder:       folder,
			SHA1:         "da39a3ee5e6b4b0d3255bfef95601890afd80709",
		}
	}

	many := make([]fileListFile, 0, 2*fileListFlushEntries+3)
	for i := range cap(many) {
		many = append(many, entry("/f"+string(rune('a'+i%26)), i%2 == 0))
	}

	for _, tc := range []struct {
		name  string
		files []fileListFile
	}{
		{
			name:  "no files",
			files: []fileListFile{},
		},
		{
			name: "one file",
			files: []fileListFile{
				entry("/a <b> & \"c\".txt", false),
			},
		},
		{
			name: "a folder and a file",
			files: []fileListFile{
				entry("/dir", true),
				entry("/dir/file", false),
			},
		},
		{
			name:  "more entries than one flush",
			files: many,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			const (
				uri     = "https://relic.example.com/artifactory/api/storage/libs-local/a&b"
				created = "2026-10-26T12:00:00.000Z"
			)

			want := httptest.NewRecorder()
			wire.WriteJSON(want, 200, fileListResponse{
				URI:     uri,
				Created: created,
				Files:   tc.files,
			})

			got := httptest.NewRecorder()

			l, err := newFileListWriter(got, uri, created)
			if err != nil {
				t.Fatal(err)
			}

			for _, f := range tc.files {
				if err := l.add(f); err != nil {
					t.Fatal(err)
				}
			}

			if err := l.close(); err != nil {
				t.Fatal(err)
			}

			if got.Body.String() != want.Body.String() ||
				got.Header().Get("Content-Type") != want.Header().Get("Content-Type") || got.Code != want.Code {
				t.Errorf("streamed list =\n%s\nwant\n%s", got.Body, want.Body)
			}
		})
	}
}
