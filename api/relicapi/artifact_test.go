package relicapi

import (
	"testing"

	"relic/internal/artifacts/repository"
)

func TestItemMessagePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		item     repository.Item
		wantPath string
		wantName string
	}{
		{
			name: "repository root",
			item: repository.Item{
				Repository: "r",
				Parent:     repository.RootPath,
				Name:       repository.RootPath,
				Type:       repository.TypeFolder,
			},
			wantPath: "",
			wantName: "",
		},
		{
			name: "nested folder",
			item: repository.Item{
				Repository: "r",
				Parent:     "a",
				Name:       "b",
				Type:       repository.TypeFolder,
			},
			wantPath: "a/b",
			wantName: "b",
		},
		{
			name: "top-level file",
			item: repository.Item{
				Repository: "r",
				Parent:     repository.RootPath,
				Name:       "a.txt",
			},
			wantPath: "a.txt",
			wantName: "a.txt",
		},
		{
			name: "nested file",
			item: repository.Item{
				Repository: "r",
				Parent:     "a/b",
				Name:       "c.txt",
			},
			wantPath: "a/b/c.txt",
			wantName: "c.txt",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := itemMessage(tt.item)
			if m.GetPath() != tt.wantPath || m.GetName() != tt.wantName {
				t.Errorf(
					"itemMessage() path, name = %q, %q; want %q, %q",
					m.GetPath(),
					m.GetName(),
					tt.wantPath,
					tt.wantName,
				)
			}
		})
	}
}
