package relicapi

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const sdkPage = "../../web/layers/site/content/docs/6.sdk/2.relic-api-clients.md"

var goExample = regexp.MustCompile("(?s)```go\n(.*?)\n```")

func moduleVersion(t *testing.T, goMod, module string) string {
	t.Helper()

	raw, err := os.ReadFile(goMod) //nolint:gosec // the repository's own go.mod
	if err != nil {
		t.Fatal(err)
	}

	for line := range strings.Lines(string(raw)) {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == module {
			return fields[1]
		}
	}

	t.Fatalf("%s does not require %s", goMod, module)

	return ""
}

func TestSDKPagePinsThePluginsRelicGeneratesWith(t *testing.T) {
	t.Parallel()

	page, err := os.ReadFile(sdkPage)
	if err != nil {
		t.Fatal(err)
	}

	for plugin, module := range map[string]string{
		"buf.build/protocolbuffers/go": "google.golang.org/protobuf",
		"buf.build/connectrpc/go":      "connectrpc.com/connect/v2",
	} {
		want := "remote: " + plugin + ":" + moduleVersion(t, "../../tools/protogen/go.mod", module) + "\n"
		if !strings.Contains(string(page), want) {
			t.Errorf("the SDK page's buf.gen.yaml does not pin %q", strings.TrimSpace(want))
		}
	}
}

func TestSDKPageGoExampleCompiles(t *testing.T) {
	t.Parallel()

	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not installed")
	}

	page, err := os.ReadFile(sdkPage)
	if err != nil {
		t.Fatal(err)
	}

	example := goExample.FindSubmatch(page)
	if example == nil {
		t.Fatal("the SDK page has no Go example")
	}

	program := strings.Join([]string{
		"package main",
		"",
		"import (",
		`	"context"`,
		`	"net/http"`,
		"",
		`	"connectrpc.com/connect/v2"`,
		`	"connectrpc.com/connect/v2/connecthttp"`,
		"",
		`	relicv1 "relic/api/gen/relic/v1"`,
		`	"relic/api/gen/relic/v1/relicv1connect"`,
		")",
		"",
		"func main() {",
		"	ctx := context.Background()",
		`	token := ""`,
		string(example[1]),
		"	_, _ = resp, err",
		"}",
		"",
	}, "\n")

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")

	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}

	overlay, err := json.Marshal(map[string]map[string]string{
		"Replace": {
			filepath.Join(root, "api", "relicapi", "sdkexample", "main.go"): source,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	overlayFile := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(overlayFile, overlay, 0o600); err != nil {
		t.Fatal(err)
	}

	build := exec.CommandContext( //nolint:gosec // the go tool from PATH, on the docs' own example
		t.Context(),
		goTool,
		"build",
		"-overlay",
		overlayFile,
		"-o",
		filepath.Join(dir, "sdkexample"),
		"./api/relicapi/sdkexample",
	)
	build.Dir = root

	if out, err := build.CombinedOutput(); err != nil {
		t.Errorf("the SDK page's Go example does not compile against api/gen: %v\n%s\n%s", err, out, program)
	}
}
