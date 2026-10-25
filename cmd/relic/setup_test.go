package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func setupTestCommand(t *testing.T) (*cobra.Command, *bytes.Buffer) {
	t.Helper()

	var out bytes.Buffer

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	return cmd, &out
}

func applySetupSteps(t *testing.T, client string, e setupEnv) error {
	t.Helper()

	cmd, _ := setupTestCommand(t)

	steps, err := setupClients[client].steps(e)
	if err != nil {
		return err
	}

	for _, s := range steps {
		if err := applySetupStep(cmd, e, setupClients[client], s, false); err != nil {
			return err
		}
	}

	return nil
}

func TestPrintedSetupCommandsAreShell(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("checks the commands with sh")
	}

	e := setupEnv{
		repo:  "gems-local",
		url:   "https://relic.example.com/gems/gems-local/",
		user:  "ci",
		token: "<token>",
		home:  t.TempDir(),
		flags: map[string]string{
			"private": "git.example.com/*",
		},
	}

	for _, client := range []string{
		"ruby",
		"go",
		"conan",
		"nuget",
	} {
		steps, err := setupClients[client].steps(e)
		if err != nil {
			t.Fatal(err)
		}

		var out bytes.Buffer
		for _, s := range steps {
			printSetupStep(&out, e, setupClients[client], s)
		}

		for line := range strings.Lines(out.String()) {
			command, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), "$ ")
			if !ok {
				continue
			}

			if err := exec.CommandContext(t.Context(), "sh", "-n", "-c", command).Run(); err != nil {
				t.Errorf("%s: %q is not a shell command: %v", client, command, err)
				continue
			}

			_, last, _ := strings.Cut(command, "| ")
			if client != "ruby" || !strings.HasPrefix(last, "ruby ") {
				continue
			}

			words, err := exec.CommandContext(t.Context(), "sh", "-c", `set -f; printf '%s\n' `+last).Output()
			if err != nil ||
				!strings.Contains(string(words), "\nBundler.settings.set_global(ARGV[0], $stdin.read.chomp)\n") {
				t.Errorf("ruby: %q does not pass the script as one word: %q %v", command, words, err)
			}
		}
	}

	if note := setupClients["ruby"].notes(e); !strings.Contains(note, "https://ci:<token>@relic.example.com/") {
		t.Errorf("gem note = %q", note)
	}
}

func TestSetupWritesWhereTheClientReads(t *testing.T) {
	t.Parallel()

	withPipDirectory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(withPipDirectory, "Library", "Application Support", "pip"), 0o700); err != nil {
		t.Fatal(err)
	}

	own, configHome, appData := userHome(), t.TempDir(), t.TempDir()
	for _, tc := range []struct {
		client, goos, home, want string
		env                      map[string]string
	}{
		{
			client: "pip",
			goos:   "linux",
			home:   t.TempDir(),
			want:   ".config/pip/pip.conf",
		},
		{
			client: "pip",
			goos:   "linux",
			home:   own,
			env: map[string]string{
				"XDG_CONFIG_HOME": configHome,
				"APPDATA":         appData,
			},
			want: filepath.Join(configHome, "pip", "pip.conf"),
		},
		{
			client: "pip",
			goos:   "linux",
			home:   own,
			env: map[string]string{
				"XDG_CONFIG_HOME": "relative/config",
			},
			want: ".config/pip/pip.conf",
		},
		{
			client: "pip",
			goos:   "linux",
			home:   t.TempDir(),
			env: map[string]string{
				"XDG_CONFIG_HOME": configHome,
			},
			want: ".config/pip/pip.conf",
		},
		{
			client: "pip",
			goos:   "windows",
			home:   own,
			env: map[string]string{
				"APPDATA":         appData,
				"XDG_CONFIG_HOME": configHome,
			},
			want: filepath.Join(appData, "pip", "pip.ini"),
		},
		{
			client: "pip",
			goos:   "windows",
			home:   t.TempDir(),
			env: map[string]string{
				"APPDATA": appData,
			},
			want: "AppData/Roaming/pip/pip.ini",
		},
		{
			client: "terraform",
			goos:   "windows",
			home:   own,
			env: map[string]string{
				"APPDATA": appData,
			},
			want: filepath.Join(appData, "terraform.rc"),
		},
		{
			client: "terraform",
			goos:   "linux",
			home:   own,
			env: map[string]string{
				"APPDATA":         appData,
				"XDG_CONFIG_HOME": configHome,
			},
			want: ".terraformrc",
		},
		{
			client: "terraform",
			goos:   "linux",
			home:   own,
			env: map[string]string{
				"TF_CLI_CONFIG_FILE": filepath.Join(configHome, "relic.tfrc"),
			},
			want: filepath.Join(configHome, "relic.tfrc"),
		},
		{
			client: "pip",
			goos:   "darwin",
			home:   t.TempDir(),
			want:   ".config/pip/pip.conf",
		},
		{
			client: "pip",
			goos:   "darwin",
			home:   withPipDirectory,
			want:   "Library/Application Support/pip/pip.conf",
		},
		{
			client: "pip",
			goos:   "windows",
			home:   t.TempDir(),
			want:   "AppData/Roaming/pip/pip.ini",
		},
		{
			client: "terraform",
			goos:   "linux",
			home:   t.TempDir(),
			want:   ".terraformrc",
		},
		{
			client: "terraform",
			goos:   "windows",
			home:   t.TempDir(),
			want:   "AppData/Roaming/terraform.rc",
		},
	} {
		steps, err := setupClients[tc.client].steps(setupEnv{
			goos: tc.goos,
			home: tc.home,
			getenv: func(name string) string {
				return tc.env[name]
			},
		})
		if err != nil {
			t.Fatal(err)
		}

		if steps[0].file != tc.want {
			t.Errorf("%s on %s with %v writes %s, want %s", tc.client, tc.goos, tc.env, steps[0].file, tc.want)
		}
	}
}

func TestHuggingFaceTokenGoesWhereHuggingFaceHubReadsIt(t *testing.T) {
	t.Parallel()

	own, other := userHome(), t.TempDir()
	for _, tc := range []struct {
		name string
		home string
		env  map[string]string
		want string
	}{
		{
			name: "the default",
			home: own,
			want: filepath.Join(own, ".cache", "huggingface", "token"),
		},
		{
			name: "HF_TOKEN_PATH",
			home: own,
			//nolint:gosec // G101: file paths, no credentials
			env: map[string]string{
				"HF_TOKEN_PATH":  "/srv/hf/token",
				"HF_HOME":        "/srv/hf-home",
				"XDG_CACHE_HOME": "/srv/cache",
			},
			want: "/srv/hf/token",
		},
		{
			name: "HF_HOME",
			home: own,
			env: map[string]string{
				"HF_HOME":        "/srv/hf-home",
				"XDG_CACHE_HOME": "/srv/cache",
			},
			want: "/srv/hf-home/token",
		},
		{
			name: "XDG_CACHE_HOME",
			home: own,
			env: map[string]string{
				"XDG_CACHE_HOME": "/srv/cache",
			},
			want: "/srv/cache/huggingface/token",
		},
		{
			name: "another home than the user's",
			home: other,
			env: map[string]string{
				"HF_HOME": "/srv/hf-home",
			},
			want: filepath.Join(other, ".cache", "huggingface", "token"),
		},
	} {
		e := setupEnv{
			home: tc.home,
			getenv: func(name string) string {
				return tc.env[name]
			},
		}

		steps, err := setupClients["huggingface"].steps(e)
		if err != nil {
			t.Fatal(err)
		}

		if got := stepPath(e, setupClients["huggingface"], steps[0].file); got != filepath.FromSlash(tc.want) {
			t.Errorf("%s: token at %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestSetupCommandsRunUnderHome(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("the command is a shell script")
	}

	home, seen := t.TempDir(), t.TempDir()
	cmd, _ := setupTestCommand(t)

	err := applySetupStep(cmd, setupEnv{
		home: home,
	}, setupClient{}, setupStep{
		command: []string{
			"sh",
			"-c",
			`printf %s "$HOME" > "$0/home"; printf %s "${XDG_CONFIG_HOME-unset}" > "$0/config"`,
			seen,
		},
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	for file, want := range map[string]string{
		"home":   home,
		"config": "unset",
	} {
		if b, err := os.ReadFile(filepath.Join(seen, file)); err != nil || string(b) != want {
			t.Errorf("%s = %q %v, want %q", file, b, err, want)
		}
	}
}

func TestNuGetSetupRunsAgain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in dotnet is a shell script")
	}

	bin := t.TempDir()
	sources := filepath.Join(t.TempDir(), "sources")
	dotnet := `#!/bin/sh
for last; do :; done
case "$1 $2" in
"nuget remove")
	grep -qx "$4" "` + sources + `" 2>/dev/null || { echo "no such source" >&2; exit 1; }
	grep -vx "$4" "` + sources + `" > "` + sources + `.new"; mv "` + sources + `.new" "` + sources + `" ;;
*)
	name=$(sed -n '/^--name$/{n;p;}' "${last#@}")
	grep -qx "$name" "` + sources + `" 2>/dev/null && { echo "The name specified has already been added" >&2; exit 1; }
	echo "$name" >> "` + sources + `" ;;
esac
`

	//nolint:gosec // a stand-in command must be executable
	if err := os.WriteFile(filepath.Join(bin, "dotnet"), []byte(dotnet), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	e := setupEnv{
		repo:  "nuget-local",
		url:   "https://relic.example.com/nuget/nuget-local/index.json",
		user:  "ci",
		token: "secret",
		home:  t.TempDir(),
	}

	for run := range 2 {
		if err := applySetupSteps(t, "nuget", e); err != nil {
			t.Fatalf("run %d: %v", run+1, err)
		}
	}

	if b, _ := os.ReadFile(sources); strings.Count(string(b), "nuget-local\n") != 1 {
		t.Errorf("sources = %q", b)
	}
}

func TestYarnSetupAuthenticatesUnscopedPackages(t *testing.T) {
	t.Parallel()

	home := t.TempDir()

	err := applySetupSteps(t, "yarn", setupEnv{
		repo:  "npm",
		url:   "https://relic.example.com/npm/npm/",
		token: "secret",
		home:  home,
	})
	if err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(filepath.Join(home, ".npmrc"))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(b), "\n//relic.example.com/npm/npm/:always-auth=true\n") {
		t.Errorf(".npmrc = %q, want always-auth for the registry", b)
	}
}

func TestYarnSetupKeepsYarnrcValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		old     string
		repos   []string
		want    string
		wantErr bool
	}{
		{
			name: "a second repository replaces the first",
			old:  "enableTelemetry: false\n",
			repos: []string{
				"npm",
				"npm-local",
			},
			want: "enableTelemetry: false\n" +
				"# relic setup npm-local begin\n" +
				"npmRegistryServer: \"https://relic.example.com/npm/npm-local/\"\n" +
				"npmAlwaysAuth: true\n" +
				"npmAuthToken: \"secret\"\n" +
				"# relic setup npm-local end\n",
		},
		{
			name: "the same repository again",
			repos: []string{
				"npm",
				"npm",
			},
			want: "# relic setup npm begin\n" +
				"npmRegistryServer: \"https://relic.example.com/npm/npm/\"\n" +
				"npmAlwaysAuth: true\n" +
				"npmAuthToken: \"secret\"\n" +
				"# relic setup npm end\n",
		},
		{
			name: "a registry set outside the blocks",
			old:  "npmRegistryServer: \"https://registry.example.com\"\n",
			repos: []string{
				"npm",
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			yarnrc := filepath.Join(home, ".yarnrc.yml")

			if tt.old != "" {
				if err := os.WriteFile(yarnrc, []byte(tt.old), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			var err error

			for _, repo := range tt.repos {
				err = applySetupSteps(t, "yarn", setupEnv{
					repo:  repo,
					url:   "https://relic.example.com/npm/" + repo + "/",
					token: "secret",
					home:  home,
				})
				if err != nil {
					break
				}
			}

			b, _ := os.ReadFile(yarnrc)

			if (err != nil) != tt.wantErr {
				t.Fatalf("setup yarn: %v, want an error: %v", err, tt.wantErr)
			}

			if tt.wantErr {
				if string(b) != tt.old {
					t.Errorf(".yarnrc.yml = %q, want it unchanged", b)
				}

				return
			}

			if string(b) != tt.want {
				t.Errorf(".yarnrc.yml = %q, want %q", b, tt.want)
			}
		})
	}
}
