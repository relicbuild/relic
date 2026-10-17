//go:build integration

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupCommands(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")
	home := filepath.Join(dir, "home")
	root := filepath.Join(dir, "root")

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

	if _, err := relic(t, config, `apiVersion: relic.build/v1
kind: Repository
metadata: {name: npm-local}
spec: {class: local, format: npm}
---
apiVersion: relic.build/v1
kind: Repository
metadata: {name: mvn-local}
spec: {class: local, format: maven}
---
apiVersion: relic.build/v1
kind: Repository
metadata: {name: deb-local}
spec: {class: local, format: debian}
---
apiVersion: relic.build/v1
kind: Repository
metadata: {name: rpm-local}
spec: {class: local, format: rpm}
---
apiVersion: relic.build/v1
kind: Repository
metadata: {name: apk-local}
spec: {class: local, format: alpine}
---
apiVersion: relic.build/v1
kind: Repository
metadata: {name: go-local}
spec: {class: local, format: gomod}
`, "apply", "-f", "-"); err != nil {
		t.Fatal(err)
	}

	token := strings.TrimSpace(readToken(t, config))
	setup := func(args ...string) (string, error) {
		return relic(t, config, "", append([]string{
			"package",
			"setup",
		}, append(args, "--home", home, "--root", root)...)...)
	}

	out, err := setup("npm", "npm-local")
	if err != nil || !strings.Contains(out, "registry="+srv.URL+"/") || !strings.Contains(out, ":_authToken=<token>") ||
		strings.Contains(out, token) {
		t.Errorf("print = %q %v", out, err)
	}

	if out, _ := setup("npm", "npm-local", "--show-token"); strings.Contains(out, token) ||
		!signsIn(t, srv.URL, authToken(out)) {
		t.Errorf("--show-token = %q, want a token of its own that signs in", out)
	}

	if _, err := setup("maven", "npm-local"); err == nil || !strings.Contains(err.Error(), "is a npm repository") {
		t.Errorf("wrong format: %v", err)
	}

	if _, err := setup("bower", "npm-local"); err == nil {
		t.Error("an unknown client was accepted")
	}

	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}

	npmrc := filepath.Join(home, ".npmrc")
	if err := os.WriteFile(npmrc, []byte("fund=false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if _, err := setup("npm", "npm-local", "--write"); err != nil {
			t.Fatal(err)
		}
	}

	b, _ := os.ReadFile(npmrc)
	if !strings.HasPrefix(string(b), "fund=false\n# relic setup npm-local begin\n") ||
		strings.Count(string(b), "_authToken=") != 1 || strings.Contains(string(b), token) ||
		!signsIn(t, srv.URL, authToken(string(b))) {
		t.Errorf(".npmrc = %q", b)
	}

	if st, _ := os.Stat(npmrc); st.Mode().Perm() != 0o600 {
		t.Errorf(".npmrc mode = %v", st.Mode())
	}

	if _, err := setup("maven", "mvn-local", "--write"); err != nil {
		t.Fatal(err)
	}

	settings := filepath.Join(home, ".m2", "settings.xml")
	if b, _ := os.ReadFile(settings); !strings.Contains(string(b), "<url>"+srv.URL) {
		t.Errorf("settings.xml = %q", b)
	}

	if err := os.WriteFile(settings, []byte("<settings/>"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := setup("maven", "mvn-local", "--write"); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("replaced settings.xml: %v", err)
	}

	if _, err := setup("maven", "mvn-local", "--write", "--force"); err != nil {
		t.Errorf("--force: %v", err)
	}

	if st, _ := os.Stat(settings); st.Mode().Perm() != 0o600 {
		t.Errorf("settings.xml mode = %v", st.Mode())
	}

	if _, err := setup("apt", "deb-local", "--write"); err == nil {
		t.Error("apt without a distribution succeeded")
	}

	if _, err := setup("apt", "deb-local", "--distribution", "bookworm", "--write"); err != nil {
		t.Fatal(err)
	}

	list, _ := os.ReadFile(filepath.Join(root, "etc/apt/sources.list.d/relic-deb-local.list"))
	authFile := filepath.Join(root, "etc/apt/auth.conf.d/relic-deb-local.conf")
	auth, _ := os.ReadFile(authFile)

	if !strings.Contains(string(list), " bookworm main\n") || strings.Contains(string(list), token) ||
		!strings.HasPrefix(string(auth), "machine "+srv.URL+"/") ||
		!strings.Contains(string(auth), "password ") || strings.Contains(string(auth), token) {
		t.Errorf("sources = %q, auth = %q", list, auth)
	}

	if st, _ := os.Stat(authFile); st.Mode().Perm() != 0o600 {
		t.Errorf("apt auth mode = %v", st.Mode())
	}

	if key, err := os.ReadFile(
		filepath.Join(root, "etc/apt/keyrings/relic-deb-local.asc"),
	); err != nil || !strings.Contains(string(key), "BEGIN PGP PUBLIC KEY BLOCK") ||
		!strings.Contains(string(list), "[signed-by=/etc/apt/keyrings/relic-deb-local.asc] ") {
		t.Errorf("apt signing key of a repository with the default key pair = %q %v; sources = %q", key, err, list)
	}

	if _, err := setup("apk", "apk-local", "--branch", "v3.21", "--write"); err != nil {
		t.Fatal(err)
	}

	apkRepositories, _ := os.ReadFile(filepath.Join(root, "etc/apk/repositories"))
	if !strings.Contains(string(apkRepositories), "/alpine/apk-local/v3.21/main\n") {
		t.Errorf("apk repositories = %q, want the /alpine/ mount, which serves APKINDEX.tar.gz", apkRepositories)
	}

	if key, err := os.ReadFile(
		filepath.Join(root, "etc/apk/keys/relic-rsa.rsa.pub"),
	); err != nil || !strings.Contains(string(key), "BEGIN PUBLIC KEY") {
		t.Errorf("apk key, named as APKINDEX's signature names it = %q %v", key, err)
	}

	if _, err := setup("dnf", "rpm-local", "--write"); err != nil {
		t.Fatal(err)
	}

	repoFile := filepath.Join(root, "etc/yum.repos.d/relic-rpm-local.repo")
	dnfRepo, _ := os.ReadFile(repoFile)

	if !strings.Contains(string(dnfRepo), "baseurl="+srv.URL+"/rpm/rpm-local\n") ||
		!strings.Contains(string(dnfRepo), "password=") || strings.Contains(string(dnfRepo), token) {
		t.Errorf("dnf repository file = %q", dnfRepo)
	}

	if st, _ := os.Stat(repoFile); st.Mode().Perm() != 0o600 {
		t.Errorf("dnf repository file mode = %v", st.Mode())
	}

	if !strings.Contains(string(dnfRepo), "repo_gpgcheck=1\n") {
		t.Errorf("dnf repository file of a repository with the default key pair = %q, want repo_gpgcheck=1", dnfRepo)
	}

	out, err = setup("go", "go-local", "--private", "git.example.com/*")
	if err != nil || !strings.Contains(out, "$ go env -w GOPROXY="+srv.URL) ||
		!strings.Contains(out, "$ go env -w GONOSUMDB='git.example.com/*'") || !strings.Contains(out, ".netrc") {
		t.Errorf("go = %q %v", out, err)
	}
}

func TestSetupTokensOutliveTheContextsToken(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")
	home := filepath.Join(dir, "home")
	login := func() {
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
	}

	login()

	if _, err := relic(t, config, `apiVersion: relic.build/v1
kind: Repository
metadata: {name: npm-local}
spec: {class: local, format: npm}
`, "apply", "-f", "-"); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(t, config, "", "package", "setup", "npm", "npm-local", "--write", "--home", home); err != nil {
		t.Fatal(err)
	}

	npmrc, err := os.ReadFile(filepath.Join(home, ".npmrc"))
	if err != nil {
		t.Fatal(err)
	}

	written := authToken(string(npmrc))
	if written == strings.TrimSpace(readToken(t, config)) {
		t.Fatal("relic package setup gave npm the context's own token")
	}

	if list, err := relic(t, config, "", "auth", "token", "list"); err != nil ||
		!strings.Contains(list, "relic package setup npm npm-local") {
		t.Errorf("token list = %q %v, want the token setup issued, named after the client and repository", list, err)
	}

	login()

	if !signsIn(t, srv.URL, written) {
		t.Error("signing in again revoked the token relic package setup wrote")
	}

	if _, err := relic(t, config, "", "auth", "logout", "work"); err != nil {
		t.Fatal(err)
	}

	if !signsIn(t, srv.URL, written) {
		t.Error("logout revoked the token relic package setup wrote")
	}
}

func authToken(npmrc string) string {
	for line := range strings.Lines(npmrc) {
		if _, v, ok := strings.Cut(line, ":_authToken="); ok {
			return strings.TrimSpace(v)
		}
	}

	return ""
}

func readToken(t *testing.T, config string) string {
	t.Helper()

	b, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}

	for line := range strings.Lines(string(b)) {
		if _, v, ok := strings.Cut(line, "token: "); ok {
			return v
		}
	}

	t.Fatal("no token in the config")

	return ""
}
