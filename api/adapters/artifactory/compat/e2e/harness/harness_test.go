package harness

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func TestEnv(t *testing.T) {
	t.Parallel()

	got := Env(
		[]string{
			"HOME=/root",
			"PATH=/bin",
			"SCENARIO_URL=http://relic-server:8090",
		},
		"npm",
		"/w/home",
		"/w/jf",
		"/w/tmp",
	)

	want := []string{
		"PATH=/bin",
		"SCENARIO_URL=http://relic-server:8090",
		"HOME=/w/home",
		"JFROG_CLI_HOME_DIR=/w/jf",
		"SCENARIO_NAME=npm",
		"SCENARIO_SUFFIX=-npm",
		"TMPDIR=/w/tmp",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Env =\n%v\nwant\n%v", got, want)
	}

	if strings.Contains(strings.Join(got, " "), "HOME=/root") {
		t.Error("the runner's HOME leaked into the scenario")
	}
}

func TestEnvDaemon(t *testing.T) {
	t.Parallel()

	base := []string{
		"DOCKER_HOST=tcp://relic-e2e-dind:2375",
		"E2E_OWN_DAEMON=docker podman",
		"SCENARIO_DOCKER_HOSTNAME=relic-e2e-dind",
	}

	tests := []struct {
		name, host, hostname string
	}{
		{
			"docker",
			"tcp://relic-e2e-dind-docker:2375",
			"relic-e2e-dind-docker",
		},
		{
			"podman",
			"tcp://relic-e2e-dind-podman:2375",
			"relic-e2e-dind-podman",
		},
		{
			"apk",
			"tcp://relic-e2e-dind:2375",
			"relic-e2e-dind",
		},
		{
			"native-docker",
			"tcp://relic-e2e-dind:2375",
			"relic-e2e-dind",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := Env(base, tt.name, "/h", "/j", "/t")
			for k, want := range map[string]string{
				"DOCKER_HOST":              tt.host,
				"SCENARIO_DOCKER_HOSTNAME": tt.hostname,
			} {
				if n := slices.IndexFunc(env, func(kv string) bool {
					return strings.HasPrefix(kv, k+"=")
				}); n < 0 ||
					env[n] != k+"="+want {
					t.Errorf("%s: got %v, want %s=%s", k, env, k, want)
				}

				if c := len(
					slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
						return !strings.HasPrefix(kv, k+"=")
					}),
				); c != 1 {
					t.Errorf("%s set %d times", k, c)
				}
			}
		})
	}
}

func TestCopyDir(t *testing.T) {
	t.Parallel()

	src, dst := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "security"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(src, "security", "config.yaml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := CopyDir(src, filepath.Join(dst, "jf")); err != nil {
		t.Fatal(err)
	}

	if b, err := os.ReadFile(filepath.Join(dst, "jf", "security", "config.yaml")); err != nil || string(b) != "x" {
		t.Errorf("copied file = %q, %v", b, err)
	}

	if err := CopyDir(filepath.Join(src, "missing"), filepath.Join(dst, "none")); err != nil {
		t.Errorf("missing source: %v", err)
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

	required, err := os.ReadFile("../required.txt")
	if err != nil {
		t.Fatal(err)
	}

	for line := range strings.Lines(string(required)) {
		name := strings.TrimSpace(line)
		if name == "" || strings.HasPrefix(name, "#") || name == "sdk-artifactory-rest" {
			continue
		}

		if _, err := Tier(filepath.Join("../../fixtures/scenarios", name+".sh")); err != nil {
			t.Errorf("required scenario %s: %v", name, err)
		}
	}
}
