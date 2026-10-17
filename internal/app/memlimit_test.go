package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMemoryLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		file       string // "" leaves no file
		goMemLimit string
		want       int64
	}{
		{
			name: "90% of the container's limit",
			file: "1073741824\n",
			want: 966367638,
		},
		{
			name: "no limit",
			file: "max\n",
			want: 0,
		},
		{
			name: "no cgroup v2 file",
			want: 0,
		},
		{
			name:       "GOMEMLIMIT wins",
			file:       "1073741824\n",
			goMemLimit: "512MiB",
			want:       0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "memory.max")
			if tt.file != "" {
				if err := os.WriteFile(path, []byte(tt.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			getenv := func(k string) string {
				if k == "GOMEMLIMIT" {
					return tt.goMemLimit
				}

				return ""
			}

			if got := memoryLimit(getenv, path); got != tt.want {
				t.Errorf("memoryLimit = %d, want %d", got, tt.want)
			}
		})
	}
}
