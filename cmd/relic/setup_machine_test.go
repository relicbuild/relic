package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testMachineSetup(t *testing.T, answers string) (*machineSetup, *[]string, *bytes.Buffer) {
	t.Helper()

	home := t.TempDir()
	ran := []string{}
	out := &bytes.Buffer{}

	return &machineSetup{
		home:       home,
		root:       t.TempDir(),
		shell:      "bash",
		listen:     "127.0.0.1:8090",
		configPath: filepath.Join(home, ".config", "relic", "config.yaml"),
		username:   "dev",
		euid:       1000,
		executable: "/usr/local/bin/relic",
		getenv: func(string) string {
			return ""
		},
		completion: func(shell string) (string, error) {
			return "# " + shell + " completion for relic\n", nil
		},
		run: func(_ context.Context, name string, args ...string) error {
			ran = append(ran, strings.Join(append([]string{
				name,
			}, args...), " "))

			return nil
		},
		query: func(context.Context, string, ...string) (string, error) {
			return "", nil
		},
		proc: filepath.Join(home, "proc"),
		hasUser: func(string) bool {
			return false
		},
		lookPath: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		in:  bufio.NewReader(strings.NewReader(answers)),
		out: out,
	}, &ran, out
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

func TestMachineSetupCompletionIsIdempotent(t *testing.T) {
	t.Parallel()

	m, ran, _ := testMachineSetup(t, "")
	m.yes = true

	bashrc := filepath.Join(m.home, ".bashrc")
	//nolint:gosec // G306: a shell rc file's usual mode, which setup must keep
	if err := os.WriteFile(bashrc, []byte("alias ll='ls -l'"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := m.runSetup(t.Context()); err != nil {
		t.Fatal(err)
	}

	first := readFile(t, bashrc)
	if !strings.HasPrefix(first, "alias ll='ls -l'\n") ||
		!strings.Contains(first, `. "$HOME/.config/relic/completion.bash"`) {
		t.Fatalf(".bashrc = %q", first)
	}

	if got := readFile(
		t,
		filepath.Join(m.home, ".config", "relic", "completion.bash"),
	); got != "# bash completion for relic\n" {
		t.Errorf("completion = %q", got)
	}

	if fi, err := os.Stat(m.configPath); err != nil || fi.Mode().Perm() != 0o600 || fi.Size() != 0 {
		t.Errorf("config = %v, %v", fi, err)
	}

	if fi, _ := os.Stat(bashrc); fi.Mode().Perm() != 0o640 {
		t.Errorf(".bashrc mode = %v, want the user's 0640 kept", fi.Mode().Perm())
	}

	if !strings.Contains(strings.Join(*ran, "\n"), "systemctl --user enable relic.service") {
		t.Errorf("ran %v: the service is always installed", *ran)
	}

	m2, _, out := testMachineSetup(t, "")
	m2.home, m2.configPath, m2.yes = m.home, m.configPath, true

	if err := m2.runSetup(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, bashrc); got != first {
		t.Errorf("second run changed .bashrc:\n%s", got)
	}

	if strings.Contains(out.String(), " create ") || strings.Contains(out.String(), " update ") {
		t.Errorf("second run plan:\n%s", out)
	}
}

func TestMachineSetupKeepsTheConfigFile(t *testing.T) {
	t.Parallel()

	m, _, _ := testMachineSetup(t, "")
	m.yes = true

	if err := os.MkdirAll(filepath.Dir(m.configPath), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(m.configPath, []byte("current: prod\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := m.runSetup(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, m.configPath); got != "current: prod\n" {
		t.Errorf("config = %q", got)
	}
}

func TestMachineSetupShells(t *testing.T) {
	t.Parallel()

	tests := []struct {
		shell string
		file  string
		has   string
	}{
		{
			shell: "zsh",
			file:  ".zshrc",
			has:   "compinit",
		},
		{
			shell: "fish",
			file:  ".config/fish/completions/relic.fish",
			has:   "# fish completion for relic",
		},
	}
	for _, tt := range tests {
		t.Run(tt.shell, func(t *testing.T) {
			t.Parallel()

			m, _, _ := testMachineSetup(t, "")
			m.yes, m.shell = true, tt.shell

			if err := m.runSetup(t.Context()); err != nil {
				t.Fatal(err)
			}

			if got := readFile(t, filepath.Join(m.home, tt.file)); !strings.Contains(got, tt.has) {
				t.Errorf("%s = %q", tt.file, got)
			}

			if _, err := os.Stat(filepath.Join(m.home, ".bashrc")); err == nil {
				t.Error(".bashrc written for " + tt.shell)
			}
		})
	}
}

func TestMachineSetupAsks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		answers string
		wantErr string
		wrote   bool
	}{
		{
			name:    "proceed",
			answers: "\n",
			wrote:   true,
		},
		{
			name:    "cancelled",
			answers: "n\n",
			wantErr: "cancelled",
		},
		{
			name:    "stdin closed",
			answers: "",
			wantErr: "--yes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m, ran, _ := testMachineSetup(t, tt.answers)

			err := m.runSetup(t.Context())
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}

			if _, err := os.Stat(filepath.Join(m.home, ".bashrc")); (err == nil) != tt.wrote {
				t.Errorf(".bashrc written = %v, want %v", err == nil, tt.wrote)
			}

			if (len(*ran) > 0) != tt.wrote {
				t.Errorf("ran %v", *ran)
			}
		})
	}
}

func TestMachineSetupUserService(t *testing.T) {
	t.Parallel()

	m, ran, out := testMachineSetup(t, "")
	m.yes = true

	if err := m.runSetup(t.Context()); err != nil {
		t.Fatal(err)
	}

	settings := filepath.Join(m.home, ".config", "relic", "server.env")
	env := readFile(t, settings)

	for _, want := range []string{
		"RELIC_HTTP_LISTEN_ADDRESS=127.0.0.1:8090\n",
		"RELIC_DATABASE_URL=sqlite://" + filepath.Join(m.home, ".local", "share", "relic", "relic.db") + "\n",
		"RELIC_OBJECT_STORAGE_TYPE=filesystem\n",
		"RELIC_METRICS_LISTEN_ADDRESS=127.0.0.1:8091\n",
		"RELIC_TLS_DIRECTORY=" + filepath.Join(m.home, ".config", "relic", "tls") + "\n",
		"RELIC_ENCRYPTION_KEY=",
		"RELIC_ADMIN_PASSWORD=",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("server.env lacks %q:\n%s", want, env)
		}
	}

	if fi, _ := os.Stat(settings); fi.Mode().Perm() != 0o600 {
		t.Errorf("server.env mode = %v", fi.Mode().Perm())
	}

	if fi, err := os.Stat(filepath.Join(m.home, ".local", "share", "relic")); err != nil || !fi.IsDir() ||
		fi.Mode().Perm() != 0o700 {
		t.Errorf("data directory = %v, %v", fi, err)
	}

	unit := readFile(t, filepath.Join(m.home, ".config", "systemd", "user", "relic.service"))
	for _, want := range []string{
		"ExecStart=/usr/local/bin/relic server start --worker\n",
		"EnvironmentFile=" + settings + "\n",
		"Restart=always\n",
		"WantedBy=default.target\n",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}

	wantRan := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable relic.service",
		"systemctl --user restart relic.service",
		"loginctl enable-linger dev",
	}
	if strings.Join(*ran, "\n") != strings.Join(wantRan, "\n") {
		t.Errorf("ran:\n%s\nwant:\n%s", strings.Join(*ran, "\n"), strings.Join(wantRan, "\n"))
	}

	if !strings.Contains(out.String(), "relic auth login https://127.0.0.1:8090") {
		t.Errorf("output:\n%s", out)
	}

	m2, ran2, _ := testMachineSetup(t, "")
	m2.home, m2.configPath, m2.yes = m.home, m.configPath, true

	if err := m2.runSetup(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, settings); got != env {
		t.Errorf("second run changed server.env:\n%s", got)
	}

	if !strings.Contains(strings.Join(*ran2, "\n"), "systemctl --user start relic.service") {
		t.Errorf("second run ran %v", *ran2)
	}
}

func TestMachineSetupSettingsMerge(t *testing.T) {
	t.Parallel()

	m, ran, _ := testMachineSetup(t, "")
	m.yes = true

	settings := filepath.Join(m.home, ".config", "relic", "server.env")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}

	mine := "# mine\nRELIC_ENCRYPTION_KEY=kept\nRELIC_HTTP_LISTEN_ADDRESS=0.0.0.0:9000\nRELIC_LOG_LEVEL=debug"
	if err := os.WriteFile(settings, []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := m.runSetup(t.Context()); err != nil {
		t.Fatal(err)
	}

	env := readFile(t, settings)
	if !strings.HasPrefix(env, mine+"\n") || strings.Count(env, "RELIC_ENCRYPTION_KEY=") != 1 ||
		!strings.Contains(env, "RELIC_DATABASE_URL=") {
		t.Errorf("server.env:\n%s", env)
	}

	if !strings.Contains(strings.Join(*ran, "\n"), "restart") {
		t.Errorf("added settings without a restart: %v", *ran)
	}

	m2, _, _ := testMachineSetup(t, "")
	m2.home, m2.configPath, m2.yes = m.home, m.configPath, true
	m2.listen, m2.listenSet = "127.0.0.1:9100", true

	if err := m2.runSetup(t.Context()); err != nil {
		t.Fatal(err)
	}

	if env := readFile(t, settings); !strings.Contains(env, "\nRELIC_HTTP_LISTEN_ADDRESS=127.0.0.1:9100\n") ||
		strings.Contains(env, "9000") {
		t.Errorf("server.env after --listen:\n%s", env)
	}
}

func TestMachineSetupSystemService(t *testing.T) {
	t.Parallel()

	t.Run("needs root", func(t *testing.T) {
		t.Parallel()

		m, _, _ := testMachineSetup(t, "")
		m.yes, m.system = true, true

		if err := m.runSetup(t.Context()); err == nil || !strings.Contains(err.Error(), "sudo") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("as root", func(t *testing.T) {
		t.Parallel()

		m, ran, _ := testMachineSetup(t, "")
		m.yes, m.system, m.euid = true, true, 0

		m.executable = filepath.Join(t.TempDir(), "relic")
		//nolint:gosec // G306: an executable
		if err := os.WriteFile(m.executable, []byte("binary"), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := m.runSetup(t.Context()); err != nil {
			t.Fatal(err)
		}

		if got := readFile(t, filepath.Join(m.root, systemBinary)); got != "binary" {
			t.Errorf("binary = %q", got)
		}

		unit := readFile(t, filepath.Join(m.root, systemUnit))
		for _, want := range []string{
			"ExecStart=/usr/local/bin/relic server start --worker\n",
			"EnvironmentFile=/etc/relic/relic.env\n",
			"User=relic\n",
			"ProtectSystem=strict\n",
			"ReadWritePaths=/var/lib/relic /etc/relic/tls\n",
			"WantedBy=multi-user.target\n",
		} {
			if !strings.Contains(unit, want) {
				t.Errorf("unit lacks %q:\n%s", want, unit)
			}
		}

		env := readFile(t, filepath.Join(m.root, systemSettings))
		if !strings.Contains(env, "RELIC_DATABASE_URL=sqlite:///var/lib/relic/relic.db\n") ||
			!strings.Contains(env, "RELIC_TLS_DIRECTORY=/etc/relic/tls\n") {
			t.Errorf("relic.env:\n%s", env)
		}

		if fi, _ := os.Stat(filepath.Join(m.root, systemSettings)); fi.Mode().Perm() != 0o640 {
			t.Errorf("relic.env mode = %v", fi.Mode().Perm())
		}

		if _, err := os.Stat(filepath.Join(m.root, systemCompletion)); err != nil {
			t.Error(err)
		}

		if _, err := os.Stat(m.configPath); err == nil {
			t.Error("--system wrote root's client config")
		}

		got := strings.Join(*ran, "\n")
		for _, want := range []string{
			"useradd --system --user-group --create-home --home-dir /var/lib/relic --shell /usr/sbin/nologin relic",
			"chown root:relic " + filepath.Join(m.root, systemSettings),
			"install -d -o relic -g relic -m 0750 /etc/relic/tls",
			"systemctl enable relic.service",
			"systemctl restart relic.service",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("ran lacks %q:\n%s", want, got)
			}
		}

		if strings.Contains(got, "systemctl --user") {
			t.Errorf("a system setup ran a user command:\n%s", got)
		}
	})

	t.Run("as root with the relic user there", func(t *testing.T) {
		t.Parallel()

		m, ran, _ := testMachineSetup(t, "")
		m.yes, m.system, m.euid = true, true, 0
		m.hasUser = func(string) bool {
			return true
		}

		m.executable = filepath.Join(t.TempDir(), "relic")
		//nolint:gosec // G306: an executable
		if err := os.WriteFile(m.executable, []byte("binary"), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := m.runSetup(t.Context()); err != nil {
			t.Fatal(err)
		}

		got := strings.Join(*ran, "\n")
		if strings.Contains(got, "useradd") ||
			!strings.Contains(got, "install -d -o relic -g relic -m 0700 /var/lib/relic") {
			t.Errorf("ran:\n%s", got)
		}
	})
}

func TestServerConfigSet(t *testing.T) {
	t.Parallel()

	m, ran, _ := testMachineSetup(t, "")
	m.yes = true

	if err := m.runSetup(t.Context()); err != nil {
		t.Fatal(err)
	}

	*ran = nil
	path := filepath.Join(m.configDir(), "server.env")

	for range 2 {
		var out bytes.Buffer
		if err := m.setServiceSetting(t.Context(), &out, path, "RELIC_LOG_LEVEL", "debug"); err != nil {
			t.Fatal(err)
		}
	}

	if !strings.Contains(readFile(t, path), "RELIC_LOG_LEVEL=debug\n") {
		t.Errorf("settings:\n%s", readFile(t, path))
	}

	if n := strings.Count(strings.Join(*ran, "\n"), "systemctl --user restart relic.service"); n != 2 {
		t.Errorf("restarted %d times, want 2 (the second set restarts too): %v", n, *ran)
	}
}

func TestSettingArgument(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		arg        string
		valueStdin bool
		stdin      string
		wantKey    string
		wantValue  string
		wantErr    bool
	}{
		{
			name:      "key and value",
			arg:       "RELIC_LOG_LEVEL=debug",
			wantKey:   "RELIC_LOG_LEVEL",
			wantValue: "debug",
		},
		{
			name:       "value from standard input",
			arg:        "RELIC_ADMIN_PASSWORD",
			valueStdin: true,
			stdin:      "s3cret=x\n",
			wantKey:    "RELIC_ADMIN_PASSWORD",
			wantValue:  "s3cret=x",
		},
		{
			name:       "value in the argument and from standard input",
			arg:        "RELIC_ADMIN_PASSWORD=x",
			valueStdin: true,
			wantErr:    true,
		},
		{
			name:    "no value",
			arg:     "RELIC_LOG_LEVEL",
			wantErr: true,
		},
		{
			name:    "not a Relic setting",
			arg:     "HOME=/root",
			wantErr: true,
		},
		{
			name:    "a misspelt Relic setting",
			arg:     "RELIC_LOG_LEVL=debug",
			wantErr: true,
		},
		{
			name:      "an OpenTelemetry setting",
			arg:       "OTEL_EXPORTER_OTLP_ENDPOINT=http://collector.example.com:4318",
			wantKey:   "OTEL_EXPORTER_OTLP_ENDPOINT",
			wantValue: "http://collector.example.com:4318",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			key, value, err := settingArgument(tt.arg, tt.valueStdin, strings.NewReader(tt.stdin))
			if (err != nil) != tt.wantErr || key != tt.wantKey || value != tt.wantValue {
				t.Errorf("settingArgument = %q, %q, %v", key, value, err)
			}
		})
	}
}

func TestMachineSetupRefusesGoRunBinary(t *testing.T) {
	t.Parallel()

	m, _, _ := testMachineSetup(t, "")
	m.yes = true
	m.executable = "/tmp/go-build123/b001/exe/relic"

	if err := m.runSetup(t.Context()); err == nil || !strings.Contains(err.Error(), "go install") {
		t.Fatalf("err = %v", err)
	}
}

func TestSetupArguments(t *testing.T) {
	t.Parallel()

	root := newRootCmd(func(string) string {
		return ""
	})
	root.SetArgs([]string{
		"package",
		"setup",
		"npm",
	})
	root.SetOut(&bytes.Buffer{})

	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "accepts 2 arg(s), received 1") {
		t.Fatalf("err = %v", err)
	}
}

func TestGeneratedPasswordIsURLSafe(t *testing.T) {
	t.Parallel()

	for range 200 {
		p, err := randomPassword()
		if err != nil {
			t.Fatal(err)
		}

		if strings.Trim(p, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") != "" || len(p) < 24 {
			t.Fatalf("password %q is not URL-safe", p)
		}
	}
}

func TestMachineSetupRestartsAServiceRunningAnotherBinary(t *testing.T) {
	t.Parallel()

	m := installedUserService(t)

	old := filepath.Join(m.home, "old-relic")
	//nolint:gosec // G306: an executable
	if err := os.WriteFile(old, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		exe  string
		want string
	}{
		{
			name: "running the installed binary",
			exe:  m.executable,
			want: "systemctl --user start relic.service",
		},
		{
			name: "running a binary since replaced",
			exe:  old,
			want: "systemctl --user restart relic.service",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, ran, _ := testMachineSetup(t, "")
			u.home, u.configPath, u.executable, u.yes = m.home, m.configPath, m.executable, true
			u.proc = t.TempDir()
			u.query = func(context.Context, string, ...string) (string, error) {
				return "42\n", nil
			}

			if err := os.MkdirAll(filepath.Join(u.proc, "42"), 0o750); err != nil {
				t.Fatal(err)
			}

			if err := os.Symlink(tt.exe, filepath.Join(u.proc, "42", "exe")); err != nil {
				t.Fatal(err)
			}

			if err := u.runSetup(t.Context()); err != nil {
				t.Fatal(err)
			}

			if got := strings.Join(*ran, "\n"); !strings.Contains(got, tt.want) {
				t.Errorf("ran:\n%s\nwant %q", got, tt.want)
			}
		})
	}
}

func TestMachineSetupNeedsAHomeDirectory(t *testing.T) {
	t.Parallel()

	m, _, _ := testMachineSetup(t, "")
	m.yes, m.home = true, ""

	if err := m.runSetup(t.Context()); err == nil {
		t.Error("setup without a home directory went ahead")
	}

	if _, err := os.Stat(".bashrc"); err == nil {
		t.Error("setup without a home directory wrote .bashrc in the working directory")
	}
}

func TestMachineSetupWithoutSystemdChangesNothing(t *testing.T) {
	t.Parallel()

	for _, system := range []bool{
		false,
		true,
	} {
		t.Run(fmt.Sprintf("system=%v", system), func(t *testing.T) {
			t.Parallel()

			m, ran, _ := testMachineSetup(t, "")
			m.yes, m.system = true, system

			if system {
				m.euid = 0

				m.executable = filepath.Join(t.TempDir(), "relic")
				//nolint:gosec // G306: an executable
				if err := os.WriteFile(m.executable, []byte("binary"), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			m.lookPath = func(name string) (string, error) {
				if name == "systemctl" {
					return "", exec.ErrNotFound
				}

				return "/usr/bin/" + name, nil
			}

			err := m.runSetup(t.Context())
			if err == nil || !strings.Contains(err.Error(), "systemctl") ||
				!strings.Contains(err.Error(), "relic server start") {
				t.Fatalf("err = %v, want one that names systemctl and running the server by hand", err)
			}

			for _, dir := range []string{
				m.home,
				m.root,
			} {
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}

				if len(entries) > 0 {
					t.Errorf("setup wrote %s: %v", dir, entries)
				}
			}

			if len(*ran) > 0 {
				t.Errorf("ran %v", *ran)
			}
		})
	}
}

func TestServerConfigSetWithoutSystemdChangesNothing(t *testing.T) {
	t.Parallel()

	m, _, _ := testMachineSetup(t, "")
	m.yes = true

	if err := m.runSetup(t.Context()); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(m.configDir(), "server.env")
	before := readFile(t, path)

	m.lookPath = func(string) (string, error) {
		return "", exec.ErrNotFound
	}

	if err := m.setServiceSetting(t.Context(), &bytes.Buffer{}, path, "RELIC_LOG_LEVEL", "debug"); err == nil {
		t.Fatal("set without systemctl went ahead")
	}

	if got := readFile(t, path); got != before {
		t.Errorf("settings changed:\n%s", got)
	}
}

func TestServerConfigSetRefusesASettingTheServerRejects(t *testing.T) {
	t.Parallel()

	m, ran, _ := testMachineSetup(t, "")
	m.yes = true

	if err := m.runSetup(t.Context()); err != nil {
		t.Fatal(err)
	}

	*ran = nil
	path := filepath.Join(m.configDir(), "server.env")
	before := readFile(t, path)

	for key, value := range map[string]string{
		"RELIC_HTTP_LISTEN_ADDRESS": "8090",
		"RELIC_LOG_LEVEL":           "loud",
		"OTEL_TRACES_SAMPLER":       "sometimes",
	} {
		err := m.setServiceSetting(t.Context(), &bytes.Buffer{}, path, key, value)
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("set %s=%s = %v, want an error that names it", key, value, err)
		}
	}

	if got := readFile(t, path); got != before {
		t.Errorf("settings changed:\n%s", got)
	}

	if len(*ran) > 0 {
		t.Errorf("ran %v", *ran)
	}
}

func TestServerConfigSetFixesOneOfSeveralWrongSettings(t *testing.T) {
	t.Parallel()

	m, _, _ := testMachineSetup(t, "")
	m.yes = true

	if err := m.runSetup(t.Context()); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(m.configDir(), "server.env")
	broken := readFile(t, path) + "RELIC_LOG_LEVEL=loud\nRELIC_MAX_UPLOAD_SIZE=10GB\n"

	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := m.setServiceSetting(t.Context(), &bytes.Buffer{}, path, "RELIC_LOG_LEVEL", "debug"); err != nil {
		t.Fatalf("fixing one wrong setting while another stays wrong = %v", err)
	}
}

func TestMachineSetupRefusesAListenAddressWithoutAPort(t *testing.T) {
	t.Parallel()

	m, ran, _ := testMachineSetup(t, "")
	m.yes, m.listen, m.listenSet = true, "garbage", true

	if err := m.runSetup(t.Context()); err == nil || !strings.Contains(err.Error(), "--listen") {
		t.Fatalf("err = %v, want one about --listen", err)
	}

	if entries, _ := os.ReadDir(m.home); len(entries) > 0 || len(*ran) > 0 {
		t.Errorf("setup went ahead: wrote %v, ran %v", entries, *ran)
	}
}
