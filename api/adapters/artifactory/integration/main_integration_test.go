//go:build integration

package integration

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"relic/api/adapters/artifactory/access"
	"relic/internal/access/auth"
	"relic/internal/app"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
	"relic/internal/scanning/scan"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func server(t *testing.T) *httptest.Server {
	t.Helper()
	srv, _ := serverDB(t)

	return srv
}

func serverDB(t *testing.T) (*httptest.Server, *database.DB) {
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

	srv := httptest.NewServer(
		app.NewHandler(db, objectstoretest.NewMemory(), tokens, nil, app.Options{
			AllowPrivateUpstreams: true,
		}, log),
	)
	t.Cleanup(srv.Close)

	return srv, db
}

type call struct {
	method, path string
	body         string
	headers      map[string]string
	noAuth       bool
}

type reply struct {
	StatusCode int
	Header     http.Header
}

func do(t *testing.T, srv *httptest.Server, c call) (reply, string) {
	t.Helper()

	var body io.Reader
	if c.body != "" {
		body = strings.NewReader(c.body)
	}

	req, err := http.NewRequestWithContext(t.Context(), c.method, srv.URL+c.path, body)
	if err != nil {
		t.Fatal(err)
	}

	if !c.noAuth {
		req.SetBasicAuth("admin", "password")
	}

	for k, v := range c.headers {
		req.Header.Set(k, v)
	}

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	return reply{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
	}, string(b)
}

func hexSum(sum []byte) string { return hex.EncodeToString(sum) }

func importMatchedSBOM(t *testing.T, db *database.DB, sha, fileName, sbom string) {
	t.Helper()

	ctx := t.Context()
	if err := scan.NewResults(db).ImportSBOM(ctx, sha, fileName, []byte(sbom)); err != nil {
		t.Fatal(err)
	}

	grype := filepath.Join(t.TempDir(), "grype")

	//nolint:gosec // G306: an executable test script
	if err := os.WriteFile(grype, []byte("#!/bin/sh\necho '{\"matches\":[]}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	svc := scan.NewService(db, nil, scan.Syft{}, scan.Grype{
		Exec: scan.Exec{
			Path: grype,
		},
	}, scan.Limits{}, slog.New(slog.DiscardHandler))
	if err := svc.Rematch(ctx, sha); err != nil {
		t.Fatal(err)
	}
}

type asUser struct {
	user, password string
}

func (a asUser) RoundTrip(r *http.Request) (*http.Response, error) {
	if a.user != "" {
		r = r.Clone(r.Context())
		r.SetBasicAuth(a.user, a.password)
	}

	return http.DefaultTransport.RoundTrip(r)
}

func relicHTTP(user, password string) *http.Client {
	return &http.Client{
		Transport: asUser{
			user,
			password,
		},
	}
}

// apiClient calls the Relic API of the server at serverURL with hc.
func apiClient(hc connecthttp.HTTPClient, serverURL string) *connect.Client {
	return connect.NewClient(
		connecthttp.NewTransport(
			hc,
			serverURL+"/api",
		),
	)
}

// withRequestHeader returns ctx carrying a client call's request header.
func withRequestHeader(ctx context.Context, key, value string) context.Context {
	ctx, info := connect.NewClientContext(ctx)
	info.RequestHeader().Set(key, value)

	return ctx
}

func code(err error) connect.Code {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code()
	}

	return 0
}
