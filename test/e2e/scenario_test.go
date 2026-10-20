package e2e

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestNames(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, f := range []string{
		"b.sh",
		"a.sh",
		"lib.sh",
		"notes.txt",
	} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name    string
		only    []string
		want    []string
		wantErr bool
	}{
		{
			name: "all, sorted, without lib",
			want: []string{
				"a",
				"b",
			},
		},
		{
			name: "a selection keeps its order",
			only: []string{
				"b",
				"a",
			},
			want: []string{
				"b",
				"a",
			},
		},
		{
			name: "an unknown name fails",
			only: []string{
				"c",
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Names(dir, tt.only)
			if (err != nil) != tt.wantErr || !slices.Equal(got, tt.want) {
				t.Errorf("Names = %v, %v; want %v, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestTier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		script  string
		want    string
		wantErr bool
	}{
		{
			name:   "no tier line is core",
			script: "#!/usr/bin/env bash\n# covers: rt ping\n",
			want:   "core",
		},
		{
			name:   "the first word of the tier line",
			script: "#!/usr/bin/env bash\n# covers: mvn\n# tier: platform (the metadata job runs on the worker)\n",
			want:   "platform",
		},
		{
			name:    "an unknown tier fails",
			script:  "# tier: nightly\n",
			wantErr: true,
		},
		{
			name:    "an empty tier line fails",
			script:  "# tier:\n",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "scenario.sh")
			if err := os.WriteFile(path, []byte(tt.script), 0o600); err != nil {
				t.Fatal(err)
			}

			got, err := Tier(path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Tier() error = %v, wantErr %v", err, tt.wantErr)
			}

			if got != tt.want {
				t.Errorf("Tier() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEveryRequiredScenarioHasATier(t *testing.T) {
	t.Parallel()

	required, err := Required(filepath.Join("../..", requiredList))
	if err != nil {
		t.Fatal(err)
	}

	for name := range required {
		if name == sdkScenario {
			continue
		}

		if _, err := Tier(filepath.Join("../..", scenarioDir, name+".sh")); err != nil {
			t.Errorf("required scenario %s: %v", name, err)
		}
	}
}

func TestRequired(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "required.txt")
	if err := os.WriteFile(path, []byte("# the gate\nnpm\n\n  maven  \n# done\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Required(path)
	if err != nil {
		t.Fatal(err)
	}

	if names := slices.Sorted(func(yield func(string) bool) {
		for n := range got {
			if !yield(n) {
				return
			}
		}
	}); !slices.Equal(names, []string{
		"maven",
		"npm",
	}) {
		t.Errorf("Required = %v, want maven and npm", names)
	}
}
