//go:build integration

package main

import (
	"bufio"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/access/sso/ssotest"
)

type basicAuth struct {
	user, password string
}

func (b basicAuth) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.SetBasicAuth(b.user, b.password)

	return http.DefaultTransport.RoundTrip(r)
}

func TestBrowserLogin(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	config := filepath.Join(t.TempDir(), "config.yaml")

	root := newRootCmd(func(string) string {
		return ""
	})
	stderr, prompts := io.Pipe()

	root.SetOut(io.Discard)
	root.SetErr(prompts)
	root.SetArgs([]string{
		"--config",
		config,
		"auth",
		"login",
		srv.URL,
		"--context",
		"browser",
	})

	done := make(chan error, 1)
	go func() {
		done <- root.ExecuteContext(t.Context())

		_ = prompts.Close()
	}()

	prompt := regexp.MustCompile(`cliSession=([0-9a-f-]+)\S* in your browser, sign in, and approve the code (\w{4})`)

	lines := bufio.NewScanner(stderr)
	for lines.Scan() {
		m := prompt.FindStringSubmatch(lines.Text())
		if m == nil {
			continue
		}

		approve := &relicv1.ApproveLoginRequest{
			Session: m[1],
			Code:    m[2],
		}

		if _, err := relicv1connect.NewSessionServiceClient(connect.NewClient(connecthttp.NewTransport(&http.Client{
			Transport: basicAuth{
				user:     "admin",
				password: "password",
			},
		}, srv.URL+"/api"))).ApproveLogin(t.Context(), approve); err != nil {
			t.Fatal(err)
		}

		break
	}

	go func() {
		_, _ = io.Copy(io.Discard, stderr)
	}()

	if err := <-done; err != nil {
		t.Fatalf("browser login: %v", err)
	}

	if out, err := relic(t, config, "", "auth", "status"); err != nil || !strings.Contains(out, "admin") {
		t.Errorf("status after a browser login = %q %v", out, err)
	}
}

func TestWorkloadLogin(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	dir := t.TempDir()
	admin := filepath.Join(dir, "admin.yaml")
	ci := filepath.Join(dir, "ci.yaml")
	issuer := ssotest.NewOIDC(t, "unused", "unused")

	if _, err := relic(
		t,
		admin,
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

	file := filepath.Join(dir, "workload.yaml")
	if err := os.WriteFile(file, []byte(`apiVersion: relic.build/v1
kind: User
metadata:
  name: ci-bot
spec:
  email: ci@example.com
  password: Ci-bot-pw-12345
---
apiVersion: relic.build/v1
kind: WorkloadIdentity
metadata:
  name: github
spec:
  enabled: true
  issuer: `+issuer.URL+`
  audience: relic
  rules:
    - claims:
        repository: acme/*
      user: ci-bot
`), 0o600); err != nil {
		t.Fatal(err)
	}

	if out, err := relic(
		t,
		admin,
		"",
		"apply",
		"-f",
		file,
	); err != nil ||
		!hasTableRow(out, "workloadidentity/github", "created") {
		t.Fatalf("apply = %q %v", out, err)
	}

	if out, err := relic(t, admin, "", "apply", "-f", file); err != nil || strings.Count(out, "unchanged") != 2 {
		t.Errorf("second apply = %q %v", out, err)
	}

	token := issuer.Mint(t, map[string]any{
		"sub":        "repo:acme/app",
		"aud":        "relic",
		"repository": "acme/app",
	})

	if _, err := relic(
		t,
		ci,
		token+"\n",
		"auth",
		"login",
		srv.URL,
		"--identity",
		"github",
		"--oidc-token",
		"-",
	); err != nil {
		t.Fatal(err)
	}

	if out, err := relic(t, ci, "", "auth", "status"); err != nil || !strings.Contains(out, "ci-bot") {
		t.Errorf("status after an OIDC login = %q %v", out, err)
	}

	if _, err := relic(
		t,
		ci,
		"",
		"auth",
		"login",
		srv.URL,
		"--identity",
		"github",
		"--oidc-token",
		issuer.Mint(t, map[string]any{
			"sub":        "repo:evil/app",
			"aud":        "relic",
			"repository": "evil/app",
		}),
	); err == nil {
		t.Error("a token no rule matches signed in")
	}

	if _, err := relic(
		t,
		ci,
		"",
		"auth",
		"login",
		srv.URL,
		"--identity",
		"github",
		"--oidc-token",
		"-",
	); err == nil || !strings.Contains(err.Error(), "no OIDC token on standard input") {
		t.Errorf("an empty standard input = %v", err)
	}
}

func TestLogoutRevokesTheContextsToken(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	config := filepath.Join(t.TempDir(), "config.yaml")
	g := &globals{
		config: config,
		getenv: func(string) string {
			return ""
		},
	}

	login := func() string {
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
			"--context",
			"work",
		); err != nil {
			t.Fatal(err)
		}

		c, _, err := g.loadConfig()
		if err != nil {
			t.Fatal(err)
		}

		work, _ := c.find("work")

		return work.Token
	}
	first := login()
	second := login()

	if signsIn(t, srv.URL, first) {
		t.Error("signing in again kept the context's previous token")
	}

	if !signsIn(t, srv.URL, second) {
		t.Fatal("the new token does not sign in")
	}

	if _, err := relic(t, config, "", "auth", "logout", "work"); err != nil {
		t.Fatal(err)
	}

	if signsIn(t, srv.URL, second) {
		t.Error("logout kept the context's token")
	}
}

func signsIn(t *testing.T, serverURL, token string) bool {
	t.Helper()

	ctx, info := connect.NewClientContext(t.Context())
	info.RequestHeader().Set("Authorization", "Bearer "+token)

	_, err := relicv1connect.NewSessionServiceClient(connect.NewClient(connecthttp.NewTransport(http.DefaultClient, serverURL+"/api"))).
		GetCurrentUser(ctx, &relicv1.GetCurrentUserRequest{})

	return err == nil
}
