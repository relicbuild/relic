// Package upstream_test checks the upstream suite runner's thresholds.
package upstream_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestThresholds(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("thresholds.yaml")
	if err != nil {
		t.Fatal(err)
	}

	var thresholds map[string]int
	if err := yaml.Unmarshal(raw, &thresholds); err != nil {
		t.Fatalf("thresholds.yaml: %v", err)
	}

	if len(thresholds) == 0 {
		t.Fatal("thresholds.yaml records no suite")
	}

	type testCase struct {
		name  string
		suite string
		pass  int
		fails bool
	}

	tests := []testCase{
		{
			name:  "a suite without a threshold",
			suite: "relic-no-such-suite",
			pass:  0,
		},
	}

	for suite, want := range thresholds {
		if want < 0 {
			t.Fatalf("suite %s: negative threshold %d", suite, want)
		}

		tests = append(
			tests,
			testCase{
				name:  suite + " at its threshold",
				suite: suite,
				pass:  want,
			},
			testCase{
				name:  suite + " above its threshold",
				suite: suite,
				pass:  want + 1,
			},
		)
		if want > 0 {
			tests = append(
				tests,
				testCase{
					name:  suite + " below its threshold",
					suite: suite,
					pass:  want - 1,
					fails: true,
				},
			)
		}
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			results := t.TempDir()
			body := fmt.Sprintf(`{"suite":%q,"pass":%d,"fail":0,"skip":0}`, tt.suite, tt.pass)

			if err := os.WriteFile(filepath.Join(results, tt.suite+".json"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}

			cmd := exec.CommandContext(t.Context(), "bash", "run.sh", "--check", tt.suite)

			cmd.Env = append(os.Environ(), "RELIC_UPSTREAM_RESULTS="+results)
			out, err := cmd.CombinedOutput()

			if failed := err != nil; failed != tt.fails {
				t.Fatalf(
					"run.sh --check %s with %d passes: error %v, want failure %v\n%s",
					tt.suite,
					tt.pass,
					err,
					tt.fails,
					out,
				)
			}
		})
	}
}
