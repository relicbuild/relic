package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installedUserService(t *testing.T) *machineSetup {
	t.Helper()

	m, _, _ := testMachineSetup(t, "")
	m.yes = true
	m.executable = filepath.Join(m.home, ".local", "bin", "relic")

	if err := os.MkdirAll(filepath.Dir(m.executable), 0o750); err != nil {
		t.Fatal(err)
	}

	//nolint:gosec // G306: an executable
	if err := os.WriteFile(m.executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(m.home, ".bashrc"), []byte("export A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := m.runSetup(t.Context()); err != nil {
		t.Fatal(err)
	}

	return m
}

func TestUninstallUserKeepsData(t *testing.T) {
	t.Parallel()

	m := installedUserService(t)
	if err := os.WriteFile(filepath.Join(m.dataDir(), "relic.db"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	u, ran, out := testMachineSetup(t, "")
	u.home, u.configPath, u.executable, u.yes = m.home, m.configPath, m.executable, true

	if err := u.runUninstall(t.Context(), false); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{
		filepath.Join(m.home, ".config", "systemd", "user", "relic.service"),
		filepath.Join(m.configDir(), "completion.bash"),
		m.executable,
	} {
		if exists(p) {
			t.Errorf("%s is still there", p)
		}
	}

	if got := readFile(t, filepath.Join(m.home, ".bashrc")); got != "export A=1\n" {
		t.Errorf(".bashrc = %q", got)
	}

	for _, p := range []string{
		m.configPath,
		filepath.Join(m.configDir(), "server.env"),
		filepath.Join(m.dataDir(), "relic.db"),
	} {
		if !exists(p) {
			t.Errorf("%s was removed", p)
		}
	}

	if got := strings.Join(*ran, "\n"); !strings.Contains(got, "systemctl --user disable --now relic.service") {
		t.Errorf("ran:\n%s", got)
	}

	if !strings.Contains(out.String(), "Kept on this machine") ||
		!strings.Contains(out.String(), "~/.local/share/relic") {
		t.Errorf("output:\n%s", out)
	}

	out.Reset()

	if err := u.runUninstall(t.Context(), false); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "installed nothing here") || !strings.Contains(out.String(), "Kept") {
		t.Errorf("second run:\n%s", out)
	}
}

func TestUninstallSaysNothingOfAnEmptyDataDir(t *testing.T) {
	t.Parallel()

	m := installedUserService(t)

	u, _, out := testMachineSetup(t, "")
	u.home, u.configPath, u.executable, u.yes = m.home, m.configPath, m.executable, true

	if err := u.runUninstall(t.Context(), false); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(out.String(), "database and files") {
		t.Errorf("an empty data directory was reported:\n%s", out)
	}
}

func TestUninstallPurge(t *testing.T) {
	t.Parallel()

	m := installedUserService(t)

	writeCertificateAuthority(t, filepath.Join(m.configDir(), "tls"))

	run := filepath.Join(filepath.Dir(m.configPath), "builds", "app", "1.json")
	for _, p := range []string{
		run,
		m.configPath + ".lock",
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	u, _, _ := testMachineSetup(t, "")
	u.home, u.configPath, u.executable, u.yes = m.home, m.configPath, m.executable, true

	if err := u.runUninstall(t.Context(), true); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{
		m.configPath,
		filepath.Join(m.configDir(), "server.env"),
		filepath.Join(m.configDir(), "tls"),
		m.dataDir(),
		run,
		m.configPath + ".lock",
		m.configDir(),
		filepath.Dir(m.configPath),
	} {
		if exists(p) {
			t.Errorf("%s is still there", p)
		}
	}
}

func writeCertificateAuthority(t *testing.T, dir string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "ca.key"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUninstallPurgeAnswersNoByDefault(t *testing.T) {
	t.Parallel()

	m := installedUserService(t)

	u, ran, out := testMachineSetup(t, "\n")
	u.home, u.configPath, u.executable = m.home, m.configPath, m.executable

	if err := u.runUninstall(t.Context(), true); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err = %v", err)
	}

	if !strings.Contains(out.String(), "[y/N]") {
		t.Errorf("output:\n%s", out)
	}

	if len(*ran) != 0 || !exists(m.dataDir()) {
		t.Errorf("a purge answered with Enter changed something: %v", *ran)
	}
}

func TestUninstallAsks(t *testing.T) {
	t.Parallel()

	m := installedUserService(t)

	u, ran, _ := testMachineSetup(t, "n\n")
	u.home, u.configPath, u.executable = m.home, m.configPath, m.executable

	if err := u.runUninstall(t.Context(), false); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err = %v", err)
	}

	if len(*ran) != 0 || !exists(m.executable) {
		t.Errorf("a cancelled uninstall changed something: %v", *ran)
	}
}

func TestUninstallSystem(t *testing.T) {
	t.Parallel()

	m, _, _ := testMachineSetup(t, "")
	m.yes, m.system, m.euid = true, true, 0
	m.executable = filepath.Join(t.TempDir(), "relic")

	//nolint:gosec // G306: an executable
	if err := os.WriteFile(m.executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := m.runSetup(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(m.root, systemDataDir), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(m.root, systemDataDir, "relic.db"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	writeCertificateAuthority(t, filepath.Join(m.root, systemTLSDir))

	m.euid = 1000
	if err := m.runUninstall(t.Context(), false); err == nil || !strings.Contains(err.Error(), "sudo") {
		t.Fatalf("err = %v", err)
	}

	u, ran, out := testMachineSetup(t, "")
	u.root, u.yes, u.system, u.euid = m.root, true, true, 0
	u.hasUser = func(string) bool {
		return true
	}

	if err := u.runUninstall(t.Context(), false); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{
		systemUnit,
		systemBinary,
		systemCompletion,
	} {
		if exists(filepath.Join(m.root, p)) {
			t.Errorf("%s is still there", p)
		}
	}

	if !exists(filepath.Join(m.root, systemSettings)) || !exists(filepath.Join(m.root, systemDataDir, "relic.db")) ||
		!exists(filepath.Join(m.root, systemTLSDir, "ca.key")) {
		t.Error("the settings, the data or the certificate authority were removed")
	}

	if strings.Contains(strings.Join(*ran, "\n"), "userdel") {
		t.Errorf("ran userdel without --purge: %v", *ran)
	}

	if !strings.Contains(out.String(), "user relic") {
		t.Errorf("output:\n%s", out)
	}

	if err := u.runUninstall(t.Context(), true); err != nil {
		t.Fatal(err)
	}

	if exists(filepath.Join(m.root, systemDataDir)) || !strings.Contains(strings.Join(*ran, "\n"), "userdel relic") {
		t.Errorf("--purge left the data or the user: %v", *ran)
	}

	if exists(filepath.Join(m.root, systemTLSDir)) {
		t.Error("--purge left the certificate authority")
	}

	if exists(filepath.Join(m.root, "etc", "relic")) {
		t.Error("--purge left an empty /etc/relic")
	}
}
