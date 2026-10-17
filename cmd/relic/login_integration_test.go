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

	"connectrpc.com/connect"

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

		approve := connect.NewRequest(&relicv1.ApproveLoginRequest{
			Session: m[1],
			Code:    m[2],
		})

		if _, err := relicv1connect.NewSessionServiceClient(&http.Client{
			Transport: basicAuth{
				user:     "admin",
				password: "password",
			},
		}, srv.URL+"/api").ApproveLogin(t.Context(), approve); err != nil {
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
