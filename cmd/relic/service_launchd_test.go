package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const launchctlRunning = `gui/1000/build.relic.server = {
	active count = 1
	path = /Users/dev/Library/LaunchAgents/build.relic.server.plist
	state = running

	arguments = {
		/usr/local/bin/relic
		server
	}

	runs = 3
	pid = 4242
	last exit code = (never exited)
}
`

// testLaunchAgent is a macOS machineSetup whose launchctl print answers
// print until a bootout ran, and fails when print is empty, as for an agent
// launchd does not know.
func testLaunchAgent(t *testing.T, answers, print string) (*machineSetup, *[]string, *bytes.Buffer) {
	t.Helper()

	m, ran, out := testMachineSetup(t, answers)
	m.goos = "darwin"
	m.query = func(_ context.Context, name string, args ...string) (string, error) {
		if name == "launchctl" && len(args) > 0 && args[0] == "print" {
			if print == "" || slices.ContainsFunc(*ran, func(command string) bool {
				return strings.HasPrefix(command, "launchctl bootout")
			}) {
				return "", errors.New("exit status 113")
			}

			return print, nil
		}

		return "", errors.New("unexpected " + name)
	}

	return m, ran, out
}

func TestMachineSetupLaunchAgent(t *testing.T) {
	t.Parallel()

	m, ran, out := testLaunchAgent(t, "", "")
	m.yes = true

	if err := m.runSetup(t.Context()); err != nil {
		t.Fatal(err)
	}

	plist := filepath.Join(m.home, "Library", "LaunchAgents", "build.relic.server.plist")
	settings := filepath.Join(m.home, ".config", "relic", "server.env")

	agent := readFile(t, plist)
	for _, want := range []string{
		"<string>/usr/local/bin/relic</string>",
		"<string>--worker</string>",
		"<string>--settings</string>\n\t\t<string>" + settings + "</string>",
		"<key>KeepAlive</key>",
		"<string>/opt/homebrew/bin:/usr/local/bin:",
		"<string>" + filepath.Join(m.home, "Library", "Logs", "relic.log") + "</string>",
	} {
		if !strings.Contains(agent, want) {
			t.Errorf("launch agent lacks %q:\n%s", want, agent)
		}
	}

	env := keyValues(readFile(t, settings), "=")
	for _, secret := range []string{
		"RELIC_ENCRYPTION_KEY",
		"RELIC_ADMIN_PASSWORD",
	} {
		if env[secret] == "" || strings.Contains(agent, env[secret]) {
			t.Errorf("%s = %q: it belongs in the settings file only", secret, env[secret])
		}
	}

	for path, mode := range map[string]os.FileMode{
		plist:    0o644,
		settings: 0o600,
	} {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != mode {
			t.Errorf("%s: %v, %v, want mode %v", path, fi, err, mode)
		}
	}

	want := []string{
		"launchctl enable gui/1000/build.relic.server",
		"launchctl bootstrap gui/1000 " + plist,
		"launchctl kickstart gui/1000/build.relic.server",
	}
	if got := strings.Join(*ran, "\n"); got != strings.Join(want, "\n") {
		t.Errorf("ran:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}

	if !strings.Contains(out.String(), "tail -f ~/Library/Logs/relic.log") ||
		strings.Contains(out.String(), "systemctl") {
		t.Errorf("output:\n%s", out)
	}
}

func TestMachineSetupLaunchAgentAgain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(t *testing.T, m *machineSetup)
		want   string
	}{
		{
			name: "nothing changed starts it if it is not running",
			want: "launchctl kickstart gui/1000/build.relic.server",
		},
		{
			name: "new settings restart it",
			change: func(_ *testing.T, m *machineSetup) {
				m.listen, m.listenSet = "127.0.0.1:9443", true
			},
			want: "launchctl kickstart -k gui/1000/build.relic.server",
		},
		{
			name: "a new agent replaces the loaded one",
			change: func(t *testing.T, m *machineSetup) {
				plist := filepath.Join(m.home, "Library", "LaunchAgents", "build.relic.server.plist")
				//nolint:gosec // G306: a launch agent's mode
				if err := os.WriteFile(plist, []byte("<plist/>\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "launchctl bootout gui/1000/build.relic.server\nlaunchctl bootstrap gui/1000 ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			first, _, _ := testLaunchAgent(t, "", "")
			first.yes = true

			if err := first.runSetup(t.Context()); err != nil {
				t.Fatal(err)
			}

			m, ran, _ := testLaunchAgent(t, "", launchctlRunning)
			m.home, m.configPath, m.yes = first.home, first.configPath, true

			if tt.change != nil {
				tt.change(t, m)
			}

			if err := m.runSetup(t.Context()); err != nil {
				t.Fatal(err)
			}

			got := strings.Join(*ran, "\n")
			if !strings.HasPrefix(got, "launchctl enable gui/1000/build.relic.server\n"+tt.want) {
				t.Errorf("ran:\n%s\nwant after enable:\n%s", got, tt.want)
			}

			if tt.want == "launchctl kickstart gui/1000/build.relic.server" && strings.Count(got, "\n") != 1 {
				t.Errorf("ran:\n%s\nwant enable and kickstart only", got)
			}
		})
	}
}

func TestMachineSetupLaunchAgentRestartsAnOlderProcess(t *testing.T) {
	t.Parallel()

	first, _, _ := testLaunchAgent(t, "", "")
	first.yes = true

	first.executable = filepath.Join(t.TempDir(), "relic")
	//nolint:gosec // G306: an executable
	if err := os.WriteFile(first.executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := first.runSetup(t.Context()); err != nil {
		t.Fatal(err)
	}

	m, ran, _ := testLaunchAgent(t, "", launchctlRunning)
	m.home, m.configPath, m.executable, m.yes = first.home, first.configPath, first.executable, true
	print := m.query
	m.query = func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "ps" {
			return time.Now().Add(-time.Hour).Format(time.ANSIC) + "\n", nil
		}

		return print(ctx, name, args...)
	}

	if err := m.runSetup(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(*ran, "\n"); !strings.Contains(got, "launchctl kickstart -k gui/1000/build.relic.server") {
		t.Errorf("ran:\n%s\nwant a restart: the process predates the binary", got)
	}
}

func TestMachineSetupRefusesALaunchDaemon(t *testing.T) {
	t.Parallel()

	m, ran, _ := testLaunchAgent(t, "", "")
	m.yes, m.system = true, true

	err := m.runSetup(t.Context())
	if err == nil || !strings.Contains(err.Error(), "without --system and sudo") {
		t.Fatalf("err = %v, want the refusal before asking for sudo", err)
	}

	for _, dir := range []string{
		m.home,
		m.root,
	} {
		if entries, _ := os.ReadDir(dir); len(entries) > 0 {
			t.Errorf("setup wrote %s: %v", dir, entries)
		}
	}

	if len(*ran) > 0 {
		t.Errorf("ran %v", *ran)
	}
}

func TestMachineSetupOnAnotherSystemChangesNothing(t *testing.T) {
	t.Parallel()

	m, ran, _ := testMachineSetup(t, "")
	m.goos, m.yes = "windows", true

	err := m.runSetup(t.Context())
	if err == nil || !strings.Contains(err.Error(), "windows") || !strings.Contains(err.Error(), "relic server start") {
		t.Fatalf("err = %v, want one that names the system and running the server by hand", err)
	}

	if entries, _ := os.ReadDir(m.home); len(entries) > 0 {
		t.Errorf("setup wrote %v", entries)
	}

	if len(*ran) > 0 {
		t.Errorf("ran %v", *ran)
	}
}

func installedLaunchAgent(t *testing.T, answers, print string) (*machineSetup, *[]string, *bytes.Buffer) {
	t.Helper()

	m, _, _ := testLaunchAgent(t, "", "")
	m.yes = true

	if err := m.runSetup(t.Context()); err != nil {
		t.Fatal(err)
	}

	installed, ran, out := testLaunchAgent(t, answers, print)
	installed.home, installed.configPath = m.home, m.configPath

	return installed, ran, out
}

func TestMachineStatusLaunchAgent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		print string
		want  machineStatus
	}{
		{
			name:  "running",
			print: launchctlRunning,
			want: machineStatus{
				State:   "active (running)",
				Enabled: "enabled",
				PID:     4242,
			},
		},
		{
			name:  "exited with an error",
			print: "gui/1000/build.relic.server = {\n\tstate = not running\n\truns = 1\n\tlast exit code = 78\n}\n",
			want: machineStatus{
				State:   "failed (exit 78)",
				Enabled: "enabled",
			},
		},
		{
			name: "not loaded",
			want: machineStatus{
				State:   "inactive (not loaded)",
				Enabled: "not loaded",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m, _, _ := installedLaunchAgent(t, "", tt.print)

			s := m.machineStatus(t.Context())
			if s == nil {
				t.Fatal("no status for an installed launch agent")
			}

			if s.Service != "user" || s.Manager != "launchd" || s.Binary != "/usr/local/bin/relic" ||
				s.State != tt.want.State || s.Enabled != tt.want.Enabled || s.PID != tt.want.PID ||
				s.Restarts != tt.want.Restarts {
				t.Errorf("status = %+v, want %+v", s, tt.want)
			}

			if !strings.HasSuffix(s.Unit, "/Library/LaunchAgents/build.relic.server.plist") {
				t.Errorf("unit = %s", s.Unit)
			}
		})
	}
}

func TestServerStopLaunchAgent(t *testing.T) {
	t.Parallel()

	m, ran, out := installedLaunchAgent(t, "y\n", launchctlRunning)

	if err := m.runStop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(*ran, "\n"); got != "launchctl bootout gui/1000/build.relic.server" {
		t.Errorf("ran %q", got)
	}

	if !strings.Contains(out.String(), "pid 4242") ||
		!strings.Contains(out.String(), "launchctl bootstrap gui/1000 ~/Library/LaunchAgents/build.relic.server.plist"+
			" && launchctl kickstart gui/1000/build.relic.server") {
		t.Errorf("output:\n%s", out)
	}

	stopped, ran, out := installedLaunchAgent(t, "", "")
	if err := stopped.runStop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if len(*ran) > 0 || !strings.Contains(out.String(), "not running") {
		t.Errorf("ran %v, output %q", *ran, out)
	}
}

func TestUninstallLaunchAgent(t *testing.T) {
	t.Parallel()

	m, ran, _ := installedLaunchAgent(t, "", launchctlRunning)
	m.yes = true

	plist := filepath.Join(m.home, "Library", "LaunchAgents", "build.relic.server.plist")

	if err := m.runUninstall(t.Context(), false); err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(*ran, "\n"); got != "launchctl bootout gui/1000/build.relic.server" {
		t.Errorf("ran %q", got)
	}

	if exists(plist) {
		t.Error("the launch agent is still there")
	}

	if !exists(filepath.Join(m.home, ".config", "relic", "server.env")) {
		t.Error("uninstall without --purge removed the settings")
	}
}

func TestServerConfigSetRestartsTheLaunchAgent(t *testing.T) {
	t.Parallel()

	m, ran, _ := installedLaunchAgent(t, "", launchctlRunning)

	svc := m.installedService()
	if svc == nil {
		t.Fatal("no installed service")
	}

	path := svc.settingsPath()

	if err := m.setServiceSettings(t.Context(), &bytes.Buffer{}, path, setting{
		key:   "RELIC_LOG_LEVEL",
		value: "debug",
	}); err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(*ran, "\n"); got != "launchctl kickstart -k gui/1000/build.relic.server" {
		t.Errorf("ran %q", got)
	}

	if !strings.Contains(readFile(t, path), "RELIC_LOG_LEVEL=debug\n") {
		t.Errorf("settings:\n%s", readFile(t, path))
	}
}

func TestLaunchAgentBinary(t *testing.T) {
	t.Parallel()

	m, _, _ := testLaunchAgent(t, "", "")
	l := &launchd{
		m: m,
	}

	if err := os.MkdirAll(filepath.Dir(l.definition()), 0o750); err != nil {
		t.Fatal(err)
	}

	executable := "/Users/dev/R&D <tools>/relic"
	//nolint:gosec // G306: a launch agent's mode
	if err := os.WriteFile(l.definition(), []byte(launchdPlist(executable, "/s", "/l")), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := l.binary(); got != executable {
		t.Errorf("binary = %q, want %q", got, executable)
	}
}
