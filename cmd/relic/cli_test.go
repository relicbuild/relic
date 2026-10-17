package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	relicv1 "relic/api/gen/relic/v1"
)

func TestDecodeDocuments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		wantErr string
		check   func(t *testing.T, docs []document)
	}{
		{
			name: "several kinds, shorthand class, trailing separator",
			in: `apiVersion: relic.build/v1
kind: Repository
metadata: {name: libs}
spec:
  class: local
  format: maven
---
apiVersion: relic.build/v1
kind: Permission
metadata: {name: devs}
spec:
  rules:
    - resource: repository
      selector: {repositories: [libs]}
      grants: [{group: devs, actions: [read, write]}]
---
`,
			check: func(t *testing.T, docs []document) {
				t.Helper()

				if len(docs) != 2 {
					t.Fatalf("docs = %d", len(docs))
				}

				r := docs[0].msg.(*relicv1.Repository)
				if r.GetName() != "libs" || r.GetClass() != relicv1.RepositoryClass_REPOSITORY_CLASS_LOCAL ||
					r.GetFormat() != relicv1.Format_FORMAT_MAVEN {
					t.Errorf("repository = %v", r)
				}

				if !docs[0].owned["class"] || !docs[0].owned["name"] || docs[0].owned["description"] {
					t.Errorf("owned = %v", docs[0].owned)
				}

				p := docs[1].msg.(*relicv1.Permission)
				if g := p.GetRules()[0].GetGrants()[0]; g.GetGroup() != "devs" ||
					g.GetActions()[1] != relicv1.Action_ACTION_WRITE ||
					p.GetRules()[0].GetResource() != relicv1.ResourceKind_RESOURCE_KIND_REPOSITORY ||
					docs[1].String() != "permission/devs" {
					t.Errorf("permission = %v", p)
				}
			},
		},
		{
			name:    "wrong apiVersion",
			in:      "apiVersion: v1\nkind: Group\nmetadata: {name: g}\n",
			wantErr: "apiVersion",
		},
		{
			name:    "unknown kind",
			in:      "apiVersion: relic.build/v1\nkind: Gadget\nmetadata: {name: g}\n",
			wantErr: "unknown kind",
		},
		{
			name:    "aliases are not kinds",
			in:      "apiVersion: relic.build/v1\nkind: group\nmetadata: {name: g}\n",
			wantErr: "unknown kind",
		},
		{
			name:    "no name",
			in:      "apiVersion: relic.build/v1\nkind: Group\nspec: {description: x}\n",
			wantErr: "metadata.name",
		},
		{
			name:    "spec name differs",
			in:      "apiVersion: relic.build/v1\nkind: Group\nmetadata: {name: g}\nspec: {name: h}\n",
			wantErr: "differs",
		},
		{
			name:    "unknown spec field",
			in:      "apiVersion: relic.build/v1\nkind: Group\nmetadata: {name: g}\nspec: {descripton: x}\n",
			wantErr: "descripton",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			docs, err := decodeDocuments(strings.NewReader(tt.in))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			tt.check(t, docs)
		})
	}
}

func TestRestrictAndMerged(t *testing.T) {
	t.Parallel()

	docs, err := decodeDocuments(strings.NewReader(`apiVersion: relic.build/v1
kind: Repository
metadata: {name: central}
spec:
  description: maven central
  remote: {url: "https://repo1.maven.org/maven2", password: s3cret}
`))
	if err != nil {
		t.Fatal(err)
	}

	d := docs[0]

	live := &relicv1.Repository{
		Name:        "central",
		Class:       relicv1.RepositoryClass_REPOSITORY_CLASS_REMOTE,
		Format:      relicv1.Format_FORMAT_MAVEN,
		Description: "maven central",
		Remote: &relicv1.RemoteRepositorySettings{
			Url: "https://repo1.maven.org/maven2",
		},
		Notes: "kept",
	}
	if !proto.Equal(restrict(live, d.owned, d.kind.ignored), restrict(d.msg, d.owned, d.kind.ignored)) {
		t.Error("an unchanged resource compares different (the password is write-only)")
	}

	d.msg.(*relicv1.Repository).Description = "changed"
	if proto.Equal(restrict(live, d.owned, d.kind.ignored), restrict(d.msg, d.owned, d.kind.ignored)) {
		t.Error("a changed description compares equal")
	}

	m := merged(live, d).(*relicv1.Repository)
	if m.GetDescription() != "changed" || m.GetNotes() != "kept" || m.GetFormat() != relicv1.Format_FORMAT_MAVEN ||
		m.GetRemote().GetPassword() != "s3cret" {
		t.Errorf("merged = %v", m)
	}

	if live.GetDescription() != "maven central" {
		t.Error("merged changed the live message")
	}
}

func TestUnifiedDiff(t *testing.T) {
	t.Parallel()

	docs, err := decodeDocuments(
		strings.NewReader(
			"apiVersion: relic.build/v1\nkind: Group\nmetadata: {name: devs}\nspec: {description: new, defaultForNewUsers: true}\n",
		),
	)
	if err != nil {
		t.Fatal(err)
	}

	d := docs[0]
	live := &relicv1.Group{
		Name:        "devs",
		Description: "old",
	}

	got, err := unifiedDiff(d, restrict(live, d.owned, nil), restrict(d.msg, d.owned, nil))
	if err != nil {
		t.Fatal(err)
	}

	want := `--- group/devs (server)
+++ group/devs (file)
@@ -3,5 +3,6 @@
 metadata:
   name: devs
 spec:
-  description: old
+  defaultForNewUsers: true
+  description: new
 
`
	if got != want {
		t.Errorf("diff:\n%s\nwant:\n%s", got, want)
	}

	if same, _ := unifiedDiff(d, d.msg, d.msg); same != "" {
		t.Errorf("no change gives %q", same)
	}
}

func TestWithoutCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, remote, want string
	}{
		//nolint:gosec // G101: a made-up remote
		{
			name:   "user and password",
			remote: "https://ci:secret@git.example/app.git",
			want:   "https://git.example/app.git",
		},
		{
			name:   "token as the user",
			remote: "https://secret@git.example/app.git",
			want:   "https://git.example/app.git",
		},
		{
			name:   "password with a raw '#'",
			remote: "https://ci:se#cret@git.example/app.git",
			want:   "https://git.example/app.git",
		},
		//nolint:gosec // G101: a made-up remote
		{
			name:   "password with an invalid escape",
			remote: "https://ci:se%cret@git.example/app.git",
			want:   "https://git.example/app.git",
		},
		{
			name:   "password with a raw '/'",
			remote: "https://ci:se/cret@git.example/app.git",
			want:   "https://git.example/app.git",
		},
		{
			name:   "no credentials",
			remote: "https://git.example/app.git",
			want:   "https://git.example/app.git",
		},
		{
			name:   "scp-like address",
			remote: "git@git.example:app.git",
			want:   "git@git.example:app.git",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := withoutCredentials(tt.remote); got != tt.want {
				t.Errorf("withoutCredentials(%q) = %q, want %q", tt.remote, got, tt.want)
			}
		})
	}
}

func TestSetupCommandsKeepTheTokenOutOfArguments(t *testing.T) {
	t.Parallel()

	e := setupEnv{
		repo:     "r",
		url:      "https://relic.example.com/api/r",
		files:    "https://relic.example.com/files/r/",
		registry: "relic.example.com/r",
		host:     "relic.example.com",
		user:     "ci",
		token:    "t0ken-secret",
	}

	for _, name := range []string{
		"poetry",
		"conan",
		"docker",
		"helm",
		"nuget",
		"ruby",
	} {
		steps, err := setupClients[name].steps(e)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		passed := false

		for _, s := range steps {
			if strings.Contains(strings.Join(s.command, " "), e.token) {
				t.Errorf("%s runs %v with the token in its arguments", name, s.command)
			}

			passed = passed || strings.Contains(s.stdin+strings.Join(s.env, " "), e.token)

			if s.argumentFile == nil {
				continue
			}

			path, err := writeArgumentFile(s.argumentFile)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}

			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}

			body, err := os.ReadFile(path) //nolint:gosec // the test's own file
			if err != nil {
				t.Fatal(err)
			}

			_ = os.Remove(path)

			if fi.Mode().Perm() != 0o600 {
				t.Errorf("%s argument file mode = %v", name, fi.Mode().Perm())
			}

			passed = passed || strings.Contains(string(body), "\n"+e.token+"\n")
		}

		if !passed {
			t.Errorf("%s never passes the token", name)
		}
	}
}

func TestWithUserinfo(t *testing.T) {
	t.Parallel()

	got, err := withUserinfo("https://relic.example.com/alpine/apk-local/v3.21/main", "ci", "t0ken")
	//nolint:gosec // G101: a made-up token
	if err != nil || got != "https://ci:t0ken@relic.example.com/alpine/apk-local/v3.21/main" {
		t.Errorf("withUserinfo = %q, %v", got, err)
	}

	if got, err := withUserinfo("https://relic.example.com/alpine/apk-local/v3%zz/main", "ci", "t0ken"); err == nil {
		t.Errorf("withUserinfo of an unparsable URL = %q, want an error", got)
	}
}

func TestConcurrentUpdatesAreKept(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	g := &globals{
		getenv: func(k string) string {
			if k == "RELIC_BUILD_DIR" {
				return filepath.Join(dir, "builds")
			}

			return ""
		},
		config: filepath.Join(dir, "config.yaml"),
	}

	const writers = 16

	var wg sync.WaitGroup

	errs := make(chan error, 2*writers)

	for i := range writers {
		name := strconv.Itoa(i)

		wg.Go(func() {
			errs <- g.updateConfig(func(c *cliConfig) error {
				c.set(cliContext{
					Name: name,
					URL:  "https://relic.example.com",
				})

				return nil
			})
		})
		wg.Go(func() {
			errs <- g.updateBuild("app", "1", func(run *relicv1.PublishedBuildRun) error {
				module(run, name, "generic")

				return nil
			})
		})
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	c, _, err := g.loadConfig()
	if err != nil || len(c.Contexts) != writers {
		t.Errorf("config holds %d contexts (%v), want %d", len(c.Contexts), err, writers)
	}

	run, err := g.loadBuild("app", "1")
	if err != nil || len(run.GetModules()) != writers {
		t.Errorf("build holds %d modules (%v), want %d", len(run.GetModules()), err, writers)
	}
}

func TestConfigAndTarget(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "relic", "config.yaml")
	env := map[string]string{}
	g := &globals{
		getenv: func(k string) string {
			return env[k]
		},
		config: path,
	}

	g.machineRoot = t.TempDir()

	if _, err := g.target(); err == nil || !strings.Contains(err.Error(), "relic auth login") {
		t.Errorf("no config: %v", err)
	}

	var c cliConfig
	c.set(cliContext{
		Name:  "a",
		URL:   "https://a.example/",
		Token: "ta",
	})
	c.set(cliContext{
		Name:                 "b",
		URL:                  "https://b.example",
		Token:                "tb",
		CertificateAuthority: "PEM-B",
	})
	c.set(cliContext{
		Name:  "a",
		URL:   "https://a.example",
		Token: "ta2",
	})

	if err := saveConfig(path, c); err != nil {
		t.Fatal(err)
	}

	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v %v, want 0600", st.Mode(), err)
	}

	tests := []struct {
		name                     string
		env                      map[string]string
		context, url, token      string
		wantURL, wantToken       string
		wantCertificateAuthority string
		wantErr                  bool
	}{
		{
			name:      "current context",
			wantURL:   "https://a.example",
			wantToken: "ta2",
		},
		{
			name:                     "--context",
			context:                  "b",
			wantURL:                  "https://b.example",
			wantToken:                "tb",
			wantCertificateAuthority: "PEM-B",
		},
		{
			name:    "a context's certificate authority stays with its server",
			context: "b",
			env: map[string]string{
				"RELIC_URL": "https://e.example",
			},
			wantURL:   "https://e.example",
			wantToken: "tb",
		},
		{
			name: "environment over context",
			env: map[string]string{
				"RELIC_TOKEN": "te",
			},
			wantURL:   "https://a.example",
			wantToken: "te",
		},
		{
			name: "flags over environment",
			env: map[string]string{
				"RELIC_URL": "https://e.example",
			},
			url:       "https://f.example/",
			token:     "tf",
			wantURL:   "https://f.example",
			wantToken: "tf",
		},
		{
			name:    "unknown context",
			context: "nope",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &globals{
				getenv: func(k string) string {
					return tt.env[k]
				},
				config:  path,
				context: tt.context,
				url:     tt.url,
				token:   tt.token,
			}

			got, err := g.target()
			if tt.wantErr != (err != nil) || got.url != tt.wantURL || got.token != tt.wantToken ||
				got.certificateAuthority != tt.wantCertificateAuthority {
				t.Errorf("target = %+v %v", got, err)
			}
		})
	}

	var out bytes.Buffer

	root := newRootCmd(func(string) string {
		return ""
	})
	root.SetOut(&out)
	root.SetArgs([]string{
		"--config",
		path,
		"auth",
		"context",
		"list",
	})

	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "│ *       │ a ") || strings.Contains(out.String(), "ta2") {
		t.Errorf("auth context list = %q", out.String())
	}
}

func TestEveryKindIsComplete(t *testing.T) {
	t.Parallel()

	for _, k := range kinds {
		if k.new == nil || k.list == nil || k.get == nil || k.create == nil || k.update == nil || k.delete == nil ||
			len(k.aliases) < 2 {
			t.Errorf("%s is missing a function or aliases", k.name)
		}

		if k.new().ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(k.nameField)) == nil {
			t.Errorf("%s: no field %s", k.name, k.nameField)
		}

		for _, p := range k.ignored {
			if !hasFieldPath(k.new().ProtoReflect().Descriptor(), strings.Split(p, ".")) {
				t.Errorf("%s: ignored field %s does not exist", k.name, p)
			}
		}

		if got, err := kindNamed(k.aliases[0]); err != nil || got != k {
			t.Errorf("alias %s = %v %v", k.aliases[0], got, err)
		}
	}
}

func hasFieldPath(d protoreflect.MessageDescriptor, path []string) bool {
	f := d.Fields().ByJSONName(path[0])
	if f == nil {
		return false
	}

	if len(path) == 1 {
		return true
	}

	return f.Message() != nil && !f.IsList() && !f.IsMap() && hasFieldPath(f.Message(), path[1:])
}

func TestPrintMessageYAMLKeepsIntegers(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := printMessage(&out, "yaml", map[string]any{
		"bytes": int64(512110190592),
		"files": 1234567,
		"name":  "relic <x>",
		"ratio": 0.5,
	}, nil); err != nil {
		t.Fatal(err)
	}

	want := "bytes: 512110190592\nfiles: 1234567\nname: relic <x>\nratio: 0.5\n"
	if out.String() != want {
		t.Errorf("yaml = %q, want %q", out.String(), want)
	}
}

type chunkWriter chan []byte

func (c chunkWriter) Write(p []byte) (int, error) {
	c <- bytes.Clone(p)

	return len(p), nil
}

func TestWriteBodyStreamsFiles(t *testing.T) {
	t.Parallel()

	body, sender := io.Pipe()
	chunks := make(chunkWriter, 4)
	done := make(chan error, 1)

	go func() {
		done <- writeBody(chunks, &http.Response{
			Header: http.Header{
				"Content-Type": {
					"application/octet-stream",
				},
			},
			Body: body,
		})
	}()

	if _, err := sender.Write([]byte("first chunk")); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-chunks:
		if string(got) != "first chunk" {
			t.Errorf("chunk = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writeBody held the body back until it ended")
	}

	_ = sender.Close()

	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDecodeSources(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		yaml    string
		want    []*relicv1.ReleaseSource
		wantErr bool
	}{
		{
			name: "unquoted number for a string field",
			yaml: "- builds:\n    - {name: app, number: 42, dependencies: true}\n- releases:\n    - {name: lib, version: 1.10}\n",
			want: []*relicv1.ReleaseSource{
				{
					Builds: []*relicv1.ReleaseBuildRef{
						{
							Name:         "app",
							Number:       "42",
							Dependencies: true,
						},
					},
				},
				{
					Releases: []*relicv1.ReleaseRef{
						{
							Name:    "lib",
							Version: "1.10",
						},
					},
				},
			},
		},
		{
			name: "files",
			yaml: "- files:\n    - path: libs-local/docs/guide.pdf\n",
			want: []*relicv1.ReleaseSource{
				{
					Files: []*relicv1.ReleaseFileRef{
						{
							Path: "libs-local/docs/guide.pdf",
						},
					},
				},
			},
		},
		{
			name: "empty",
		},
		{
			name:    "a boolean field stays typed",
			yaml:    "- builds:\n    - {name: app, number: \"1\", dependencies: maybe}\n",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := decodeSources([]byte(tt.yaml))
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, want error %v", err, tt.wantErr)
			}

			if len(got) != len(tt.want) {
				t.Fatalf("sources = %v, want %v", got, tt.want)
			}

			for i := range got {
				if !proto.Equal(got[i], tt.want[i]) {
					t.Errorf("source %d = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestCommandContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		args         []string
		wantDeadline bool
	}{
		{
			name: "completion request",
			args: []string{
				cobra.ShellCompRequestCmd,
				"artifact",
				"download",
				"",
			},
			wantDeadline: true,
		},
		{
			name: "completion request without descriptions",
			args: []string{
				cobra.ShellCompNoDescRequestCmd,
				"artifact",
			},
			wantDeadline: true,
		},
		{
			name: "command",
			args: []string{
				"artifact",
				"download",
			},
		},
		{
			name: "no arguments",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := commandContext(t.Context(), tt.args)
			defer cancel()

			if _, ok := ctx.Deadline(); ok != tt.wantDeadline {
				t.Errorf("deadline set = %v, want %v", ok, tt.wantDeadline)
			}
		})
	}
}

func TestVersionFlag(t *testing.T) {
	t.Parallel()

	root := newRootCmd(func(string) string {
		return ""
	})

	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetArgs([]string{
		"--version",
	})

	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}

	if got := out.String(); got != "relic version dev\n" {
		t.Errorf("--version = %q", got)
	}
}

func TestTargetUsesTheLocalServiceWithoutAContext(t *testing.T) {
	t.Parallel()

	const contexts = "current: a\ncontexts:\n  - name: a\n    url: https://a.example\n    token: ta\n"

	tests := []struct {
		name            string
		userSettings    string
		userAuthority   string
		systemSettings  string
		systemAuthority string
		contexts        string
		env             map[string]string
		context         string
		chosenConfig    bool
		want            serverTarget
		wantErr         bool
	}{
		{
			name:          "the user service's settings",
			userSettings:  "# settings\nRELIC_HTTP_LISTEN_ADDRESS=:8090\nRELIC_ADMIN_PASSWORD=first\n",
			userAuthority: "PEM-USER",
			want: serverTarget{
				url:                  "https://127.0.0.1:8090",
				username:             "admin",
				password:             "first",
				certificateAuthority: "PEM-USER",
			},
		},
		{
			name: "the system service's settings, a listen host and another administrator",
			systemSettings: "RELIC_HTTP_LISTEN_ADDRESS=192.0.2.7:9000\nRELIC_ADMIN_USERNAME=root\nRELIC_ADMIN_PASSWORD=first\n" +
				"RELIC_TLS_DIRECTORY=/etc/relic/tls\n",
			systemAuthority: "PEM-SYSTEM",
			want: serverTarget{
				url:                  "https://192.0.2.7:9000",
				username:             "root",
				password:             "first",
				certificateAuthority: "PEM-SYSTEM",
			},
		},
		{
			name:         "the default listen address",
			userSettings: "RELIC_ADMIN_PASSWORD=first\n",
			want: serverTarget{
				url:      "https://127.0.0.1:8090",
				username: "admin",
				password: "first",
			},
		},
		{
			name:         "settings without an administrator password",
			userSettings: "RELIC_HTTP_LISTEN_ADDRESS=:8090\n",
			wantErr:      true,
		},
		{
			name:    "no service on this machine",
			wantErr: true,
		},
		{
			name:         "a context wins",
			userSettings: "RELIC_ADMIN_PASSWORD=first\n",
			contexts:     contexts,
			want: serverTarget{
				url:   "https://a.example",
				token: "ta",
			},
		},
		{
			name:         "a server named in the environment wins",
			userSettings: "RELIC_ADMIN_PASSWORD=first\n",
			env: map[string]string{
				"RELIC_URL": "https://e.example",
			},
			want: serverTarget{
				url: "https://e.example",
			},
		},
		{
			name:         "a token alone names no server",
			userSettings: "RELIC_ADMIN_PASSWORD=first\n",
			env: map[string]string{
				"RELIC_TOKEN": "te",
			},
			wantErr: true,
		},
		{
			name:         "a config file chosen with --config names no server",
			userSettings: "RELIC_ADMIN_PASSWORD=first\n",
			chosenConfig: true,
			wantErr:      true,
		},
		{
			name:         "a config file chosen with RELIC_CONFIG names no server",
			userSettings: "RELIC_ADMIN_PASSWORD=first\n",
			env: map[string]string{
				"RELIC_CONFIG": "/nonexistent/config.yaml",
			},
			wantErr: true,
		},
		{
			name:         "an unknown context is an error",
			userSettings: "RELIC_ADMIN_PASSWORD=first\n",
			context:      "nope",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			configHome, machineRoot := t.TempDir(), t.TempDir()
			files := map[string]string{
				filepath.Join(configHome, "relic", "server.env"):            tt.userSettings,
				filepath.Join(configHome, "relic", "config.yaml"):           tt.contexts,
				filepath.Join(machineRoot, systemSettings):                  tt.systemSettings,
				filepath.Join(configHome, "relic", "tls", "ca.crt"):         tt.userAuthority,
				filepath.Join(machineRoot, "etc", "relic", "tls", "ca.crt"): tt.systemAuthority,
			}

			for path, content := range files {
				if content == "" {
					continue
				}

				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}

				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			g := &globals{
				getenv: func(k string) string {
					if k == "XDG_CONFIG_HOME" {
						return configHome
					}

					return tt.env[k]
				},
				context:     tt.context,
				machineRoot: machineRoot,
				userConfigDir: func() (string, error) {
					return configHome, nil
				},
			}
			if tt.chosenConfig {
				g.config = filepath.Join(t.TempDir(), "config.yaml")
			}

			got, err := g.target()
			if tt.wantErr != (err != nil) || got != tt.want {
				t.Errorf("target = %+v, %v; want %+v, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

type unreadInput struct {
	t *testing.T
}

func (u unreadInput) Read([]byte) (int, error) {
	u.t.Error("standard input was read before the server was found")

	return 0, io.EOF
}

func TestCommandsWithoutAServerFailBeforeReadingInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "apply",
			args: []string{
				"apply",
				"-f",
				"-",
			},
		},
		{
			name: "diff",
			args: []string{
				"diff",
				"-f",
				"-",
			},
		},
		{
			name: "delete",
			args: []string{
				"delete",
				"-f",
				"-",
			},
		},
		{
			name: "policy preview",
			args: []string{
				"security",
				"policy",
				"preview",
				"-f",
				"-",
			},
		},
		{
			name: "vex import",
			args: []string{
				"security",
				"vex",
				"import",
				"-",
			},
		},
		{
			name: "api with a body",
			args: []string{
				"api",
				"repositories",
				"-d",
				"@-",
			},
		},
		{
			name: "release create",
			args: []string{
				"release",
				"create",
				"app",
				"1.0.0",
				"-f",
				"-",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := rootCmd(&globals{
				getenv: func(string) string {
					return ""
				},
				machineRoot: t.TempDir(),
			})
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetIn(unreadInput{
				t: t,
			})
			root.SetArgs(append([]string{
				"--config",
				filepath.Join(t.TempDir(), "config.yaml"),
			}, tt.args...))

			if err := root.ExecuteContext(
				t.Context(),
			); err == nil ||
				!strings.Contains(err.Error(), "no Relic server") {
				t.Errorf("without a server: %v, want the no-server error", err)
			}
		})
	}
}
