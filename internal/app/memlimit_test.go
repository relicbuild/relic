package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContainerMemory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		file string // "" leaves no file
		want int64
	}{
		{
			name: "the container's limit",
			file: "1073741824\n",
			want: 1 << 30,
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

			if got := containerMemory(path); got != tt.want {
				t.Errorf("containerMemory = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestScanMemoryBudget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		configured int64
		container  int64
		want       int64
	}{
		{
			name:       "the setting wins",
			configured: 768 << 20,
			container:  4 << 30,
			want:       768 << 20,
		},
		{
			name:      "half of a small container",
			container: 512 << 20,
			want:      256 << 20,
		},
		{
			name:      "at most 1 GiB of a large container",
			container: 8 << 30,
			want:      1 << 30,
		},
		{
			name: "1 GiB without a container limit",
			want: 1 << 30,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := scanMemoryBudget(tt.configured, tt.container); got != tt.want {
				t.Errorf("scanMemoryBudget = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestMemoryLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		container  int64
		scanBudget int64
		goMemLimit string
		want       int64
	}{
		{
			name:      "90% of the container's limit without scanners",
			container: 1 << 30,
			want:      966367638,
		},
		{
			name:       "90% of what the scanners leave",
			container:  512 << 20,
			scanBudget: 256 << 20,
			want:       241591905,
		},
		{
			name:       "at least 128 MiB",
			container:  512 << 20,
			scanBudget: 1 << 30,
			want:       128 << 20,
		},
		{
			name:       "at most 90% of a tiny container",
			container:  100 << 20,
			scanBudget: 1 << 30,
			want:       94371840,
		},
		{
			name: "no container limit",
			want: 0,
		},
		{
			name:       "GOMEMLIMIT wins",
			container:  1 << 30,
			goMemLimit: "512MiB",
			want:       0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			getenv := func(k string) string {
				if k == "GOMEMLIMIT" {
					return tt.goMemLimit
				}

				return ""
			}

			if got := memoryLimit(getenv, tt.container, tt.scanBudget); got != tt.want {
				t.Errorf("memoryLimit = %d, want %d", got, tt.want)
			}
		})
	}
}
