//go:build integration

package main

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"relic/api/adapters/artifactory/access"

	"relic/internal/access/auth"
	"relic/internal/app"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
	"relic/internal/platform/servertls"
)

func testServer(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(testHandler(t))
	t.Cleanup(srv.Close)

	return srv
}

func testTLSServer(t *testing.T) (*httptest.Server, *servertls.Directory) {
	t.Helper()

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

	srv := httptest.NewUnstartedServer(testHandler(t))
	srv.TLS = servertls.Config(tls.VersionTLS12, directory.GetCertificate)
	srv.TLS.Certificates = []tls.Certificate{
		*certificate,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	return srv, directory
}

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	db := databasetest.NewDB(t)

	log := slog.New(slog.DiscardHandler)
	if err := app.BootstrapAdmin(t.Context(), db, "admin", "password", log); err != nil {
		t.Fatal(err)
	}

	tokens, err := auth.NewTokens(t.Context(), db, nil, access.Format{})
	if err != nil {
		t.Fatal(err)
	}

	return app.NewHandler(db, objectstoretest.NewMemory(), tokens, nil, app.Options{
		AllowPrivateUpstreams: true,
	}, log)
}

func relic(t *testing.T, config, stdin string, args ...string) (string, error) {
	t.Helper()

	root := newRootCmd(func(string) string {
		return ""
	})

	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{
		"--config",
		config,
	}, args...))
	err := root.ExecuteContext(t.Context())

	return out.String(), err
}

const resources = `apiVersion: relic.build/v1
kind: Repository
metadata: {name: cli-local}
spec: {class: local, format: generic, description: CLI test}
---
apiVersion: relic.build/v1
kind: Group
metadata: {name: cli-devs}
spec: {description: developers}
---
apiVersion: relic.build/v1
kind: User
metadata: {name: cli-user}
spec: {email: cli@example.com, password: Cli-pass-12345, groups: [cli-devs]}
---
apiVersion: relic.build/v1
kind: Permission
metadata: {name: cli-devs}
spec:
  rules:
    - resource: repository
      selector: {repositories: [cli-local]}
      include: ["**"]
      grants: [{group: cli-devs, actions: [read, write]}]
---
apiVersion: relic.build/v1
kind: Policy
metadata: {name: cli-critical}
spec:
  kind: security
  rules: [{name: crit, priority: 1, minSeverity: critical, failBuild: true}]
---
apiVersion: relic.build/v1
kind: Watch
metadata: {name: cli-watch}
spec:
  active: true
  scopes: [{kind: repository, names: [cli-local]}]
  policies: [cli-critical]
---
apiVersion: relic.build/v1
kind: Replication
metadata: {name: cli-local}
spec: {targetUrl: "https://peer.example/cli-local", username: push, password: Push-pass-1, schedule: "0 2 * * *", enabled: true}
`

func TestCLI(t *testing.T) {
	t.Parallel()
	srv, tlsDirectory := testTLSServer(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")

	authorityFile := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(authorityFile, tlsDirectory.Authority().CertificatePEM(), 0o600); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(dir, "resources.yaml")
	if err := os.WriteFile(file, []byte(resources), 0o600); err != nil {
		t.Fatal(err)
	}

	serviceConfigHome := t.TempDir()
	serviceSettings := "RELIC_HTTP_LISTEN_ADDRESS=" + strings.TrimPrefix(srv.URL, "https://") +
		"\nRELIC_ADMIN_PASSWORD=password\n"

	if err := os.MkdirAll(filepath.Join(serviceConfigHome, "relic", "tls"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(
		filepath.Join(serviceConfigHome, "relic", "tls", servertls.AuthorityCertificateFile),
		tlsDirectory.Authority().CertificatePEM(),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(
		filepath.Join(serviceConfigHome, "relic", "server.env"),
		[]byte(serviceSettings),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	onServiceMachine := rootCmd(&globals{
		getenv: func(k string) string {
			if k == "XDG_CONFIG_HOME" {
				return serviceConfigHome
			}

			return ""
		},
		userConfigDir: func() (string, error) {
			return serviceConfigHome, nil
		},
	})

	var administrators bytes.Buffer

	onServiceMachine.SetOut(&administrators)
	onServiceMachine.SetErr(io.Discard)
	onServiceMachine.SetArgs([]string{
		"get",
		"users",
	})

	if err := onServiceMachine.ExecuteContext(
		t.Context(),
	); err != nil ||
		!strings.Contains(administrators.String(), "admin") {
		t.Fatalf("without a context on the service's machine, get users = %q, %v", administrators.String(), err)
	}

	if _, err := relic(
		t,
		config,
		"wrong\n",
		"auth",
		"login",
		srv.URL,
		"--user",
		"admin",
		"--password-stdin",
		"--ca-file",
		authorityFile,
	); err == nil {
		t.Fatal("login with a wrong password succeeded")
	}

	if _, err := relic(
		t,
		config,
		"password\n",
		"auth", "login",
		srv.URL,
		"--user",
		"admin",
		"--password-stdin",
	); err == nil ||
		!strings.Contains(err.Error(), tlsDirectory.Authority().Fingerprint()) ||
		!strings.Contains(err.Error(), "--ca-file") {
		t.Fatalf("login to a server whose authority is not trusted: %v", err)
	}

	if _, err := relic(
		t,
		config,
		"password\n",
		"auth", "login",
		srv.URL,
		"--user",
		"admin",
		"--password-stdin",
		"--context",
		"test",
		"--ca-file",
		authorityFile,
	); err != nil {
		t.Fatal(err)
	}

	if b, _ := os.ReadFile(config); strings.Contains(string(b), "password") || !strings.Contains(string(b), "token:") ||
		!strings.Contains(string(b), "certificateAuthority:") {
		t.Errorf("config = %s", b)
	}

	out, err := relic(t, config, "", "apply", "-f", file)

	wantCreated := [][]string{
		{
			"RESOURCE",
			"RESULT",
		},
		{
			"repository/cli-local",
			"created",
		},
		{
			"group/cli-devs",
			"created",
		},
		{
			"user/cli-user",
			"created",
		},
		{
			"permission/cli-devs",
			"created",
		},
		{
			"policy/cli-critical",
			"created",
		},
		{
			"watch/cli-watch",
			"created",
		},
		{
			"replication/cli-local",
			"created",
		},
	}
	if err != nil || !slices.EqualFunc(tableRows(out), wantCreated, slices.Equal[[]string]) {
		t.Fatalf("first apply = %q %v", out, err)
	}

	out, err = relic(t, config, "", "apply", "-f", file)
	if err != nil || strings.Count(out, "unchanged") != 7 {
		t.Errorf("second apply = %q %v", out, err)
	}

	if _, err := relic(t, config, "", "diff", "-f", file, "--exit-code"); err != nil {
		t.Errorf("diff after apply: %v", err)
	}

	changed := strings.Replace(resources, "description: CLI test", "description: CLI test changed", 1)
	out, err = relic(t, config, changed, "diff", "-f", "-", "--exit-code")

	var ec exitCode
	if !errors.As(err, &ec) || ec != 1 ||
		!strings.Contains(out, "-  description: CLI test\n+  description: CLI test changed\n") {
		t.Errorf("diff = %q %v", out, err)
	}

	if out, err := relic(t, config, changed, "apply", "-f", "-"); err != nil ||
		!hasTableRow(out, "repository/cli-local", "configured") || !hasTableRow(out, "group/cli-devs", "unchanged") {
		t.Errorf("apply changed = %q %v", out, err)
	}

	yml, err := relic(t, config, "", "get", "repository", "cli-local", "-o", "yaml")
	if err != nil || !strings.Contains(yml, "kind: Repository") || !strings.Contains(yml, "name: cli-local") {
		t.Fatalf("get = %q %v", yml, err)
	}

	if out, err := relic(
		t,
		config,
		yml,
		"apply",
		"-f",
		"-",
	); err != nil ||
		!hasTableRow(out, "repository/cli-local", "unchanged") {
		t.Errorf("apply of get = %q %v", out, err)
	}

	if out, err := relic(t, config, "", "get", "users"); err != nil || !hasTableRow(out, "cli-user") {
		t.Errorf("get users = %q %v", out, err)
	}

	payload := filepath.Join(dir, "app.txt")
	if err := os.WriteFile(payload, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	userConfig := filepath.Join(dir, "user.yaml")
	if _, err := relic(
		t,
		userConfig,
		"Cli-pass-12345\n",
		"auth", "login",
		srv.URL,
		"-u",
		"cli-user",
		"--password-stdin",
		"--ca-file",
		authorityFile,
	); err != nil {
		t.Fatal(err)
	}

	if out, err := relic(
		t,
		userConfig,
		"",
		"artifact",
		"upload",
		payload,
		"cli-local/org/app/1.0/app.txt",
	); err != nil ||
		!strings.HasPrefix(out, "cli-local/org/app/1.0/app.txt ") {
		t.Errorf("upload = %q %v", out, err)
	}

	if out, err := relic(t, userConfig, "", "artifact", "search", "app", "--repository", "cli-local"); err != nil ||
		!hasTableRowWith(out, "cli-local/org/app/1.0/app.txt", "file") {
		t.Errorf("search = %q %v", out, err)
	}

	if out, err := relic(
		t,
		userConfig,
		"",
		"artifact",
		"download",
		"cli-local/org/app/1.0/app.txt",
		"-o",
		"-",
	); err != nil ||
		out != "hello" {
		t.Errorf("download = %q %v", out, err)
	}

	saved := filepath.Join(dir, "saved.txt")
	if _, err := relic(
		t,
		userConfig,
		"",
		"artifact",
		"download",
		"cli-local/org/app/1.0/app.txt",
		"-o",
		saved,
	); err != nil {
		t.Fatal(err)
	}

	if b, _ := os.ReadFile(saved); string(b) != "hello" {
		t.Errorf("saved = %q", b)
	}

	if _, err := relic(t, userConfig, "", "artifact", "download", "cli-local/nope.txt", "-o", "-"); err == nil ||
		!strings.Contains(err.Error(), "404") {
		t.Errorf("missing download: %v", err)
	}

	if _, err := relic(t, userConfig, "", "apply", "-f", file); err == nil {
		t.Error("a non-admin applied resources")
	}

	out, err = relic(t, config, "", "delete", "-f", file)

	deleted := tableRows(out)
	if err != nil || len(deleted) != 8 || !slices.Equal(deleted[1], []string{
		"replication/cli-local",
		"deleted",
	}) || !slices.Equal(deleted[2], []string{
		"watch/cli-watch",
		"deleted",
	}) || !slices.Equal(deleted[7], []string{
		"repository/cli-local",
		"deleted",
	}) {
		t.Errorf("delete = %q %v", out, err)
	}

	if _, err := relic(t, config, "", "delete", "-f", file); err == nil {
		t.Error("second delete succeeded without --ignore-not-found")
	}

	if _, err := relic(t, config, "", "delete", "-f", file, "--ignore-not-found"); err != nil {
		t.Errorf("delete --ignore-not-found: %v", err)
	}
}

func TestApplyRepositoryLimits(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")
	file := filepath.Join(dir, "limits.yaml")

	if err := os.WriteFile(file, []byte(`apiVersion: relic.build/v1
kind: Repository
metadata:
  name: libs-local
spec:
  class: local
  format: maven
  maxUploadSize: 1073741824
  maxStorage: 536870912000
`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(
		t,
		config,
		"password\n",
		"auth",
		"login",
		srv.URL,
		"--user",
		"admin",
		"--password-stdin",
	); err != nil {
		t.Fatal(err)
	}

	if out, err := relic(t, config, "", "apply", "-f", file); err != nil || !strings.Contains(out, "created") {
		t.Fatalf("apply = %q %v", out, err)
	}

	if out, err := relic(t, config, "", "apply", "-f", file); err != nil || !strings.Contains(out, "unchanged") {
		t.Errorf("second apply = %q %v; want unchanged, the used bytes ignored", out, err)
	}

	out, err := relic(t, config, "", "get", "repository", "libs-local", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}

	compact := strings.Join(strings.Fields(out), "")
	for _, want := range []string{
		`"maxStorage":"536870912000"`,
		`"maxUploadSize":"1073741824"`,
	} {
		if !strings.Contains(compact, want) {
			t.Errorf("get = %s, want %s", out, want)
		}
	}
}

func TestCleanupPolicyCommands(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")
	file := filepath.Join(dir, "cleanup.yaml")
	jar := filepath.Join(dir, "app.jar")

	if err := os.WriteFile(jar, []byte("jar"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(file, []byte(`apiVersion: relic.build/v1
kind: Repository
metadata:
  name: libs-local
spec:
  class: local
  format: maven
---
apiVersion: relic.build/v1
kind: CleanupPolicy
metadata:
  name: keep-one
spec:
  enabled: true
  repositories: [libs-local]
  pathPattern: org/app/*
  keepLatest: 1
`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(
		t,
		config,
		"password\n",
		"auth",
		"login",
		srv.URL,
		"--user",
		"admin",
		"--password-stdin",
	); err != nil {
		t.Fatal(err)
	}

	if out, err := relic(t, config, "", "apply", "-f", file); err != nil ||
		!hasTableRow(out, "cleanuppolicy/keep-one", "created") {
		t.Fatalf("apply = %q %v", out, err)
	}

	if out, err := relic(t, config, "", "apply", "-f", file); err != nil || strings.Count(out, "unchanged") != 2 {
		t.Errorf("second apply = %q %v", out, err)
	}

	for _, v := range []string{
		"1.0",
		"2.0",
	} {
		if _, err := relic(t, config, "", "artifact", "upload", jar, "libs-local/org/app/"+v+"/app.jar"); err != nil {
			t.Fatal(err)
		}

		time.Sleep(10 * time.Millisecond)
	}

	out, err := relic(t, config, "", "cleanup", "preview", "keep-one")
	if err != nil || !strings.Contains(out, "org/app/1.0") || strings.Contains(out, "org/app/2.0") {
		t.Fatalf("preview = %q %v; want 1.0 only", out, err)
	}

	if out, err := relic(
		t,
		config,
		"",
		"cleanup",
		"run",
		"keep-one",
	); err != nil ||
		!strings.Contains(out, "moved 1 items") {
		t.Errorf("run = %q %v", out, err)
	}
}

func TestHoldCommands(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")
	file := filepath.Join(dir, "f.txt")

	if err := os.WriteFile(file, []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(
		t,
		config,
		"password\n",
		"auth",
		"login",
		srv.URL,
		"--user",
		"admin",
		"--password-stdin",
	); err != nil {
		t.Fatal(err)
	}

	repo := filepath.Join(dir, "repo.yaml")
	if err := os.WriteFile(repo, []byte("apiVersion: relic.build/v1\nkind: Repository\nmetadata:\n  name: case-files\n"+
		"spec:\n  class: local\n  format: generic\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(t, config, "", "apply", "-f", repo); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(t, config, "", "artifact", "upload", file, "case-files/2026/f.txt"); err != nil {
		t.Fatal(err)
	}

	if out, err := relic(t, config, "", "hold", "add", "case-files/2026", "--reason", "litigation"); err != nil ||
		!strings.Contains(out, "held case-files/2026") {
		t.Fatalf("hold add = %q %v", out, err)
	}

	if _, err := relic(t, config, "", "artifact", "rm", "case-files/2026/f.txt"); err == nil {
		t.Error("removing a held file succeeded")
	}

	if out, err := relic(t, config, "", "hold", "list"); err != nil || !strings.Contains(out, "case-files/2026") ||
		!strings.Contains(out, "litigation") {
		t.Errorf("hold list = %q %v", out, err)
	}

	if _, err := relic(t, config, "", "hold", "remove", "case-files/2026"); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(t, config, "", "artifact", "rm", "case-files/2026/f.txt"); err != nil {
		t.Errorf("removing the file after the hold was released: %v", err)
	}

	out, err := relic(
		t,
		config,
		"",
		"audit",
		"list",
		"--action",
		"Hold",
		"--since",
		"1h",
		"--page-size",
		"1",
		"-o",
		"json",
	)
	if err != nil || !strings.Contains(out, `"relic.v1.HoldService/DeleteHold"`) ||
		strings.Contains(out, "PutHold") || !strings.Contains(out, `"total": "2"`) {
		t.Errorf("audit list --page-size 1 = %q %v", out, err)
	}

	out, err = relic(t, config, "", "audit", "list", "--actor", "admin", "--outcome", "success")
	if err != nil || !strings.Contains(out, "relic.v1.HoldService/PutHold") ||
		!strings.Contains(out, "case-files/2026") {
		t.Errorf("audit list = %q %v", out, err)
	}
}

func TestIdentityProviderKind(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")

	if _, err := relic(
		t,
		config,
		"password\n",
		"auth",
		"login",
		srv.URL,
		"--user",
		"admin",
		"--password-stdin",
	); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(dir, "idp.yaml")
	if err := os.WriteFile(file, []byte(`apiVersion: relic.build/v1
kind: IdentityProvider
metadata:
  name: corp
spec:
  displayName: Corp SSO
  enabled: true
  groupsClaim: groups
  oidc:
    issuer: https://login.example.com
    clientId: relic
    clientSecret: client-secret-3
`), 0o600); err != nil {
		t.Fatal(err)
	}

	if out, err := relic(t, config, "", "apply", "-f", file); err != nil || !strings.Contains(out, "created") {
		t.Fatalf("first apply = %q %v", out, err)
	}

	if out, err := relic(t, config, "", "apply", "-f", file); err != nil || !strings.Contains(out, "unchanged") {
		t.Errorf("second apply = %q %v; want unchanged, the write-only secret ignored", out, err)
	}

	out, err := relic(t, config, "", "get", "identityproviders", "corp", "-o", "yaml")
	if err != nil || !strings.Contains(out, "issuer: https://login.example.com") ||
		strings.Contains(out, "client-secret-3") || !strings.Contains(out, "/api/v1/sso/corp/callback") {
		t.Errorf("get = %q %v", out, err)
	}
}

func TestSBOMCommands(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")

	if _, err := relic(
		t,
		config,
		"password\n",
		"auth",
		"login",
		srv.URL,
		"--user",
		"admin",
		"--password-stdin",
	); err != nil {
		t.Fatal(err)
	}

	repo := filepath.Join(dir, "repo.yaml")
	if err := os.WriteFile(repo, []byte("apiVersion: relic.build/v1\nkind: Repository\nmetadata:\n  name: sbom-cli\n"+
		"spec:\n  class: local\n  format: generic\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(dir, "app.jar")
	bom := filepath.Join(dir, "bom.json")

	if err := os.WriteFile(file, []byte("jar"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(bom, []byte(`{"bomFormat": "CycloneDX", "specVersion": "1.5", "components": [
	  {"type": "library", "name": "guava", "version": "31.0", "purl": "pkg:maven/com.google.guava/guava@31.0"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{
			"apply",
			"-f",
			repo,
		},
		{
			"artifact",
			"upload",
			file,
			"sbom-cli/app/app.jar",
		},
		{
			"security",
			"sbom",
			"import",
			"sbom-cli/app/app.jar",
			bom,
		},
	} {
		if _, err := relic(t, config, "", args...); err != nil {
			t.Fatalf("relic %v: %v", args, err)
		}
	}

	out, err := relic(t, config, "", "security", "sbom", "import", "sbom-cli/app/app.jar", bom)
	if err != nil || !strings.Contains(out, "imported the SBOM of sbom-cli/app/app.jar") {
		t.Errorf("import = %q %v; want the result on standard output", out, err)
	}

	out, err = relic(t, config, "", "security", "report", "sbom-cli/app/app.jar")
	if err != nil || !strings.Contains(out, "queued (SBOM imported, not matched yet)") {
		t.Errorf("report right after the import = %q %v; want it queued until matched", out, err)
	}

	out, err = relic(t, config, "", "security", "sbom", "export", "sbom-cli/app/app.jar", "--format", "spdx")
	if err != nil || !strings.Contains(out, `"spdxVersion": "SPDX-2.3"`) ||
		!strings.Contains(out, "pkg:maven/com.google.guava/guava@31.0") {
		t.Errorf("export as SPDX = %q %v", out, err)
	}

	written := filepath.Join(dir, "out.cdx.json")
	if _, err := relic(
		t,
		config,
		"",
		"security",
		"sbom",
		"export",
		"sbom-cli/app/app.jar",
		"--file",
		written,
	); err != nil {
		t.Fatal(err)
	}

	if got, err := os.ReadFile(written); err != nil || !strings.Contains(string(got), "guava") {
		t.Errorf("exported file = %q %v", got, err)
	}

	if _, err := relic(t, config, "", "security", "sbom", "import", "sbom-cli/app/app.jar", file); err == nil {
		t.Error("a file that is no SBOM was imported")
	}
}
