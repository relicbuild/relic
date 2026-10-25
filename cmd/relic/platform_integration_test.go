//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func TestStatus(t *testing.T) {
	t.Parallel()

	config, dir := adminCLI(t)

	out, err := relic(t, config, "", "auth", "status")
	if !strings.Contains(out, "admin (administrator)") || !strings.Contains(out, "database") {
		t.Errorf("status = %q %v", out, err)
	}

	out, _ = relic(t, config, "", "auth", "status", "-o", "json")

	var got statusResult
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Version == "" || !got.Admin || len(got.Checks) == 0 {
		t.Errorf("status -o json = %q %v", out, err)
	}

	url := got.URL
	anon := filepath.Join(dir, "anon.yaml")

	out, err = relic(t, anon, "", "--url", url, "auth", "status")
	if err != nil || !strings.Contains(out, "anonymous") || strings.Contains(out, "database") {
		t.Errorf("anonymous status = %q %v", out, err)
	}

	if _, err := relic(t, anon, "", "--url", "http://127.0.0.1:1", "auth", "status"); err == nil {
		t.Error("status of an unreachable server succeeded")
	}
}

type databaseDownSystem struct {
	relicv1connect.UnimplementedSystemServiceHandler
}

func (databaseDownSystem) GetVersion(context.Context, *relicv1.GetVersionRequest) (*relicv1.GetVersionResponse, error) {
	return &relicv1.GetVersionResponse{
		Version: "v1.2.3",
	}, nil
}

type databaseDownSessions struct {
	relicv1connect.UnimplementedSessionServiceHandler
}

func (databaseDownSessions) GetCurrentUser(
	context.Context,
	*relicv1.GetCurrentUserRequest,
) (*relicv1.GetCurrentUserResponse, error) {
	return nil, connect.NewError(connect.CodeUnavailable, "database unavailable")
}

func TestStatusShowsADatabaseThatIsDown(t *testing.T) {
	t.Parallel()

	server := connect.NewServer()
	relicv1connect.RegisterSystemServiceHandler(server, databaseDownSystem{})
	relicv1connect.RegisterSessionServiceHandler(server, databaseDownSessions{})

	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)

	srv := httptest.NewServer(http.StripPrefix("/api", mux))
	t.Cleanup(srv.Close)

	config := filepath.Join(t.TempDir(), "config.yaml")

	out, err := relic(t, config, "", "--url", srv.URL, "auth", "status", "-o", "json")
	if !errors.Is(err, exitCode1) {
		t.Errorf("status with the database down = %v, want exit status 1", err)
	}

	var got statusResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("status -o json = %q: %v", out, err)
	}

	if got.Healthy || len(got.Checks) != 1 || got.Checks[0].GetName() != "database" || got.Checks[0].GetOk() {
		t.Errorf("status with the database down = %+v, want an unhealthy database check", got)
	}
}

func TestAPI(t *testing.T) {
	t.Parallel()

	config, dir := adminCLI(t, "api-local")

	out, err := relic(t, config, "", "api", "version")
	if err != nil || !strings.Contains(out, "\"version\":") || !strings.Contains(out, "\n  ") {
		t.Errorf("api version = %q %v", out, err)
	}

	body := filepath.Join(dir, "body.json")
	if err := os.WriteFile(body, []byte(`{"name": "api-local"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err = relic(t, config, "", "api", "/api/relic.v1.RepositoryService/GetRepository", "-d", "@"+body)
	if err != nil || !strings.Contains(out, `"name": "api-local"`) {
		t.Errorf("api Connect = %q %v", out, err)
	}

	out, err = relic(
		t,
		config,
		`{"name": "api-local"}`,
		"api",
		"/api/relic.v1.RepositoryService/GetRepository",
		"-d",
		"@-",
		"-i",
	)
	if err != nil || !strings.HasPrefix(out, "HTTP/1.1 200 OK\n") {
		t.Errorf("api -i = %q %v", out, err)
	}

	out, err = relic(
		t,
		config,
		"",
		"api",
		"/api/relic.v1.RepositoryService/GetRepository",
		"-d",
		"@"+body,
		"-H",
		"Content-Type: application/proto",
		"-i",
	)
	if err == nil || !strings.Contains(out, "invalid wire-format data") {
		t.Errorf("api -H Content-Type = %q %v", out, err)
	}

	out, err = relic(t, config, "", "api", "repositories/nope")

	var ec exitCode
	if !errors.As(err, &ec) || ec != 1 || !strings.Contains(out, "repository not found") {
		t.Errorf("api missing = %q %v", out, err)
	}

	out, err = relic(t, config, "", "api", "docs", "token")
	if err != nil || !strings.Contains(out, "/api/v1/tokens") || strings.Contains(out, "/api/v1/repositories ") {
		t.Errorf("api docs token = %q %v", out, err)
	}

	out, err = relic(t, config, "", "api", "docs", "-o", "json")
	if err != nil {
		t.Errorf("api docs -o json: %v", err)
	}

	var ops []struct {
		ID string `json:"operationId"`
	}
	if err := json.Unmarshal([]byte(out), &ops); err != nil || len(ops) < 50 {
		t.Errorf("api docs -o json: %d operations %v", len(ops), err)
	}

	out, err = relic(t, config, "", "api", "docs", "describe", "SecurityService_CreateToken")
	if err != nil || !strings.Contains(out, "POST /api/v1/tokens") || !strings.Contains(out, "Schema ") {
		t.Errorf("api docs describe = %q %v", out, err)
	}

	if _, err := relic(t, config, "", "api", "docs", "describe", "NoSuchOperation"); err == nil {
		t.Error("describe of an unknown operation succeeded")
	}
}

func TestTokenCommands(t *testing.T) {
	t.Parallel()

	config, dir := adminCLI(t)

	token, err := relic(t, config, "", "auth", "token", "create", "--description", "cli-test", "--expires-days", "7")
	if err != nil || strings.Count(token, "\n") != 1 || len(token) < 20 {
		t.Fatalf("token create = %q %v", token, err)
	}

	out, err := relic(
		t,
		filepath.Join(dir, "none.yaml"),
		"",
		"--url",
		serverURL(t, config),
		"--token",
		strings.TrimSpace(token),
		"auth",
		"status",
	)
	if err != nil || !strings.Contains(out, "admin (administrator)") {
		t.Errorf("status with the new token = %q %v", out, err)
	}

	out, err = relic(t, config, "", "auth", "token", "create", "--scope", "admin", "-o", "json")
	if err != nil || !strings.Contains(out, `"accessToken"`) || !strings.Contains(out, "TOKEN_SCOPE_KIND_ADMIN") {
		t.Errorf("token create -o json = %q %v", out, err)
	}

	out, err = relic(
		t,
		config,
		"",
		"auth",
		"token",
		"create",
		"--user",
		"admin",
		"--expires-in",
		"10m",
		"--refreshable",
		"-o",
		"json",
	)
	if err != nil || !strings.Contains(out, `"expiresIn": "600"`) || !strings.Contains(out, `"refreshToken"`) {
		t.Errorf("token create --expires-in 10m --refreshable = %q %v", out, err)
	}

	if _, err := relic(
		t,
		config,
		"",
		"auth",
		"token",
		"create",
		"--expires-in",
		"10m",
		"--expires-days",
		"1",
	); err == nil {
		t.Error("--expires-in and --expires-days were accepted together")
	}

	if _, err := relic(t, config, "", "auth", "token", "create", "--scope", "root"); err == nil {
		t.Error("an unknown scope was accepted")
	}

	out, err = relic(t, config, "", "auth", "token", "list")
	if err != nil || !strings.Contains(out, "cli-test") {
		t.Fatalf("token list = %q %v", out, err)
	}

	var id string

	for _, row := range tableRows(out) {
		if slices.Contains(row, "cli-test") {
			id = row[0]
		}
	}

	if out, err := relic(t, config, "", "auth", "token", "revoke", id); err != nil || out != id+" revoked\n" {
		t.Errorf("token revoke = %q %v", out, err)
	}

	if _, err := relic(
		t,
		filepath.Join(dir, "none.yaml"),
		"",
		"--url",
		serverURL(t, config),
		"--token",
		strings.TrimSpace(token),
		"api",
		"repositories",
	); err == nil {
		t.Error("a revoked token still works")
	}
}

func serverURL(t *testing.T, config string) string {
	t.Helper()

	out, err := relic(t, config, "", "auth", "status", "-o", "json")

	var s statusResult
	if jerr := json.Unmarshal([]byte(out), &s); jerr != nil {
		t.Fatalf("status: %v %v", err, jerr)
	}

	return s.URL
}
