package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/platform/servertls"
)

type versionServer struct {
	relicv1connect.UnimplementedSystemServiceHandler
}

func (versionServer) GetVersion(
	context.Context,
	*connect.Request[relicv1.GetVersionRequest],
) (*connect.Response[relicv1.GetVersionResponse], error) {
	return connect.NewResponse(&relicv1.GetVersionResponse{
		Version: "v9.9.9",
	}), nil
}

func runningServer(t *testing.T) (string, []byte) {
	t.Helper()

	path, h := relicv1connect.NewSystemServiceHandler(versionServer{})
	mux := http.NewServeMux()
	mux.Handle("/api"+path, http.StripPrefix("/api", h))

	directory, err := servertls.OpenDirectory(t.Context(), t.TempDir(), []string{
		"127.0.0.1",
	}, time.Now)
	if err != nil {
		t.Fatal(err)
	}

	certificate, err := directory.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = servertls.Config(tls.VersionTLS12, directory.GetCertificate)
	srv.TLS.Certificates = []tls.Certificate{
		*certificate,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	return strings.TrimPrefix(srv.URL, "https://"), directory.Authority().CertificatePEM()
}

func statusOf(t *testing.T, listen string, authority []byte, systemctl string) (*machineSetup, *machineStatus) {
	t.Helper()

	m, _, _ := testMachineSetup(t, "")
	m.yes, m.listen = true, listen

	if err := m.runSetup(t.Context()); err != nil {
		t.Fatal(err)
	}

	if authority != nil {
		tlsDirectory := filepath.Join(m.configDir(), "tls")
		if err := os.MkdirAll(tlsDirectory, 0o700); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(
			filepath.Join(tlsDirectory, servertls.AuthorityCertificateFile),
			authority,
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}

	data := m.dataDir()
	for name, content := range map[string]string{
		"relic.db":         "0123456789",
		"objects/a/1":      "abc",
		"objects/b/2":      "defg",
		"grype/vulnerable": "x",
	} {
		p := filepath.Join(data, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	m.query = func(context.Context, string, ...string) (string, error) {
		return systemctl, nil
	}

	return m, m.machineStatus(t.Context())
}

func TestMachineStatusRunning(t *testing.T) {
	t.Parallel()

	listen, authority := runningServer(t)
	m, s := statusOf(t, listen, authority,
		"ActiveState=active\nSubState=running\nUnitFileState=enabled\nMainPID=42\nNRestarts=0\n"+
			"ActiveEnterTimestamp=Sat 2026-10-03 06:00:00 +04\n")

	if s == nil {
		t.Fatal("no status for an installed service")
	}

	if !s.Healthy || !*s.Reachable || s.ServerVersion != "v9.9.9" || s.PID != 42 {
		t.Errorf("status = %+v", s)
	}

	if s.Database.Kind != "sqlite" || s.Database.Bytes != 10 {
		t.Errorf("database = %+v", s.Database)
	}

	if s.Storage.Kind != "filesystem" || s.Storage.Files != 2 || s.Storage.Bytes != 7 {
		t.Errorf("storage = %+v", s.Storage)
	}

	if !*s.EncryptionKey || s.ScannerDatabase == nil || s.ScannerDatabase.Bytes != 1 {
		t.Errorf("status = %+v", s)
	}

	var out bytes.Buffer
	m.printMachineStatus(&out, s)

	for _, want := range []string{
		"This machine  user service · ~/.config/systemd/user/relic.service",
		"● active (running)  since Sat 2026-10-03 06:00:00 +04 · pid 42 · enabled",
		"✓ Relic v9.9.9",
		"sqlite ~/.local/share/relic/relic.db  10 B",
		"filesystem ~/.local/share/relic/objects  7 B in 2 files",
		"✓ encryption key set",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestMachineStatusStopped(t *testing.T) {
	t.Parallel()

	_, s := statusOf(t, "127.0.0.1:1", nil, "ActiveState=failed\nSubState=failed\nUnitFileState=enabled\nNRestarts=5\n")

	if s.Healthy || *s.Reachable || s.Restarts != 5 {
		t.Errorf("status = %+v", s)
	}
}

func TestMachineStatusNotInstalled(t *testing.T) {
	t.Parallel()

	m, _, _ := testMachineSetup(t, "")

	if s := m.machineStatus(t.Context()); s != nil {
		t.Errorf("status = %+v", s)
	}
}

func TestMachineStatusUnreadableSettings(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}

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

	if err := os.Chmod(filepath.Join(m.root, systemSettings), 0); err != nil {
		t.Fatal(err)
	}

	m.query = func(context.Context, string, ...string) (string, error) {
		return "ActiveState=active\n", nil
	}

	s := m.machineStatus(t.Context())
	if s == nil || s.Service != "system" || s.SettingsError == "" || s.Healthy {
		t.Fatalf("status = %+v", s)
	}

	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}

	for _, unknown := range []string{
		`"encryptionKey"`,
		`"reachable"`,
		`"database"`,
		`"storage"`,
	} {
		if strings.Contains(string(b), unknown) {
			t.Errorf("JSON states %s without the settings: %s", unknown, b)
		}
	}

	var out bytes.Buffer
	m.printMachineStatus(&out, s)

	if !strings.Contains(out.String(), "run with sudo") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestPostgreSQLStatusHidesThePassword(t *testing.T) {
	t.Parallel()

	st := databaseStatus("postgres://relic:secret@db:5432/relic?sslmode=require")
	if st.Kind != "postgresql" || strings.Contains(st.Location, "secret") || st.Location != "db:5432/relic" {
		t.Errorf("status = %+v", st)
	}
}

func TestMachineStatusProbesTheDefaultAddress(t *testing.T) {
	t.Parallel()

	_, s := statusOf(t, "", nil, "ActiveState=active\nSubState=running\n")

	if s == nil || s.Address != ":8090" {
		t.Errorf(
			"status of a server.env without a listen address probes %q, want the server's default :8090",
			s.Address,
		)
	}
}
