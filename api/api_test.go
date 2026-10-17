package api_test

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	relicv1 "relic/api/gen/relic/v1"
)

func TestProtoUpToDate(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	cmd := exec.CommandContext(
		t.Context(),
		"go",
		"tool",
		"-modfile=tools/protogen/go.mod",
		"buf",
		"generate",
		"-o",
		out,
	)

	cmd.Dir = ".."
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("buf generate: %v\n%s", err, b)
	}

	want := readTree(t, filepath.Join(out, "api", "gen"))

	got := readTree(t, "gen")
	for name, w := range want {
		if !bytes.Equal(got[name], w) {
			t.Errorf("api/gen/%s is out of date: run go tool -modfile=tools/protogen/go.mod buf generate", name)
		}
	}

	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("api/gen/%s has no proto: delete it", name)
		}
	}

	spec, err := os.ReadFile(filepath.Join(out, "api", "apispec", "relic.openapi.json"))
	if err != nil {
		t.Fatal(err)
	}

	if committed, err := os.ReadFile(filepath.Join("apispec", "relic.openapi.json")); err != nil ||
		!bytes.Equal(committed, spec) {
		t.Error(
			"api/apispec/relic.openapi.json is out of date: run go tool -modfile=tools/protogen/go.mod buf generate",
		)
	}
}

func readTree(t *testing.T, root string) map[string][]byte {
	t.Helper()

	fsys := os.DirFS(root)
	files := map[string][]byte{}

	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		files[p], err = fs.ReadFile(fsys, p)

		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	return files
}

func TestEveryMethodDeclaresAccess(t *testing.T) {
	t.Parallel()

	methods := 0

	protoregistry.GlobalFiles.RangeFilesByPackage("relic.v1", func(f protoreflect.FileDescriptor) bool {
		for i := range f.Services().Len() {
			svc := f.Services().Get(i)
			for j := range svc.Methods().Len() {
				m := svc.Methods().Get(j)
				methods++

				if proto.GetExtension(m.Options(), relicv1.E_Access).(relicv1.Access) == relicv1.Access_ACCESS_UNSPECIFIED {
					t.Errorf("%s has no (relic.v1.access) option", m.FullName())
				}
			}
		}

		return true
	})

	if methods == 0 {
		t.Fatal("no relic.v1 methods registered")
	}
}

// Viewers may call what only reads, so an administrator method is a write and a
// viewer method a read.
func TestAdministrationMethodsSplitByWrite(t *testing.T) {
	t.Parallel()

	readsWithBody := map[protoreflect.FullName]bool{
		"relic.v1.CleanupService.PreviewCleanupPolicy": true,
	}

	protoregistry.GlobalFiles.RangeFilesByPackage("relic.v1", func(f protoreflect.FileDescriptor) bool {
		for i := range f.Services().Len() {
			svc := f.Services().Get(i)
			for j := range svc.Methods().Len() {
				m := svc.Methods().Get(j)
				access := proto.GetExtension(m.Options(), relicv1.E_Access).(relicv1.Access)
				rule, _ := proto.GetExtension(m.Options(), annotations.E_Http).(*annotations.HttpRule)
				_, get := rule.GetPattern().(*annotations.HttpRule_Get)

				switch {
				case access == relicv1.Access_ACCESS_ADMIN && get:
					t.Errorf("%s only reads: make it ACCESS_VIEWER", m.FullName())
				case access == relicv1.Access_ACCESS_VIEWER && !get && !readsWithBody[m.FullName()]:
					t.Errorf("%s changes something: make it ACCESS_ADMIN", m.FullName())
				}
			}
		}

		return true
	})
}

func TestEveryMethodHasRESTRoute(t *testing.T) {
	t.Parallel()

	seen := map[string]protoreflect.FullName{}
	route := func(m protoreflect.MethodDescriptor, r *annotations.HttpRule) {
		var verb, path string

		switch p := r.GetPattern().(type) {
		case *annotations.HttpRule_Get:
			verb, path = "GET", p.Get
		case *annotations.HttpRule_Put:
			verb, path = "PUT", p.Put
		case *annotations.HttpRule_Post:
			verb, path = "POST", p.Post
		case *annotations.HttpRule_Delete:
			verb, path = "DELETE", p.Delete
		case *annotations.HttpRule_Patch:
			verb, path = "PATCH", p.Patch
		default:
			t.Errorf("%s: unsupported HTTP rule %v", m.FullName(), r.GetPattern())
			return
		}

		if !strings.HasPrefix(path, "/api/v1/") {
			t.Errorf("%s: %s %s is not under /api/v1/", m.FullName(), verb, path)
		}

		key := verb + " " + path
		if other, dup := seen[key]; dup {
			t.Errorf("%s and %s share %s", m.FullName(), other, key)
		}

		seen[key] = m.FullName()
	}

	protoregistry.GlobalFiles.RangeFilesByPackage("relic.v1", func(f protoreflect.FileDescriptor) bool {
		for i := range f.Services().Len() {
			svc := f.Services().Get(i)
			for j := range svc.Methods().Len() {
				m := svc.Methods().Get(j)

				r, _ := proto.GetExtension(m.Options(), annotations.E_Http).(*annotations.HttpRule)
				if r == nil || r.GetPattern() == nil {
					t.Errorf("%s has no (google.api.http) route", m.FullName())
					continue
				}

				route(m, r)

				for _, extra := range r.GetAdditionalBindings() {
					route(m, extra)
				}
			}
		}

		return true
	})

	if len(seen) == 0 {
		t.Fatal("no REST routes registered")
	}
}

func TestProtoVocabulary(t *testing.T) {
	t.Parallel()

	banned := regexp.MustCompile(
		`(?i)\bjf\b|jfrog|artifactory|xray|\bany (local|remote)\b|managedxraymeta|project_key|release[ _-]?bundle|\brclass\b|packagetype`,
	)

	files, err := filepath.Glob(filepath.Join("proto", "relic", "v1", "*.proto"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no protos: %v", err)
	}

	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}

		for i, line := range strings.Split(string(b), "\n") {
			if m := banned.FindString(line); m != "" {
				t.Errorf("%s:%d uses %q: %s", f, i+1, m, strings.TrimSpace(line))
			}
		}
	}
}
