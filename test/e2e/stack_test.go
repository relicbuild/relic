//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	tierFlag = flag.String(
		"tier",
		"core",
		"core (the single binary), platform (the Compose stack with PostgreSQL, SeaweedFS and a worker) "+
			"or k8s (a kind cluster and the Helm chart)",
	)
	scenariosFlag = flag.String(
		"scenarios",
		"",
		"space-separated scenarios to run instead of every scenario of the tier",
	)
	keepFlag = flag.Bool("keep", false, "leave the stack running after the tests")
	urlFlag  = flag.String(
		"url",
		"",
		"an https:// address of a running server to run the scenarios and the jfrog-client-go suite against, "+
			"as RELIC_USER (an administrator) with RELIC_PASSWORD or RELIC_TOKEN, instead of starting a stack",
	)
)

// root is the repository, two directories above this package.
var root = func() string {
	dir, err := filepath.Abs("../..")
	if err != nil {
		panic(err)
	}

	return dir
}()

// stack is one Compose project of the shipped Compose files: the single
// binary (core) or the full stack (platform).
type stack struct {
	tier    string
	project string
	files   []string
	results string
}

func newStack(tier string) stack {
	files := []string{
		"deploy/docker-compose.yml",
		"test/compose/plain-http.yml",
	}
	if tier == "core" {
		files = append(files, "deploy/docker-compose.single-node.yml", "test/compose/plain-http.single-node.yml")
	}

	return stack{
		tier:    tier,
		project: "relic-e2e-" + tier,
		files:   files,
		results: filepath.Join(root, "test", "e2e", "results", tier),
	}
}

// network is the Compose project's network, which the test containers join;
// against a remote server, a network of their own.
func (s stack) network() string { return s.project + "_default" }

// remote is the address of a running server the tests use instead of the
// stack, or "".
func (s stack) remote() string { return strings.TrimSuffix(*urlFlag, "/") }

// services are the Relic services the tier runs.
func (s stack) services() []string {
	if s.tier == "core" {
		return []string{
			"relic-server",
		}
	}

	return []string{
		"relic-server",
		"relic-worker",
	}
}

func (s stack) compose(ctx context.Context, args ...string) ([]byte, error) {
	full := []string{
		"compose",
		"-p",
		s.project,
	}
	for _, f := range s.files {
		full = append(full, "-f", filepath.Join(root, f))
	}

	return command(ctx, "docker", append(full, args...)...)
}

// environment configures the Compose files for the tests: every published
// port is a free one, so stacks never collide on the host, and traces go to
// the collector the tests start.
func (s stack) environment() []string {
	ports := publishedPorts()

	return []string{
		"RELIC_PORT=" + ports[0],
		"RELIC_POSTGRESQL_PORT=" + ports[1],
		"RELIC_OBJECT_STORAGE_PORT=" + ports[2],
		"RELIC_OTLP_ENDPOINT=http://relic-e2e-otel:4318",
		"RELIC_FAILED_SIGN_INS_BEFORE_BACKOFF=1000",
	}
}

// publishedPorts are three free ports on 127.0.0.1, chosen once per run by
// the kernel: Docker Engine's own choice for port 0 can be a port in use.
var publishedPorts = sync.OnceValue(func() []string {
	ports := make([]string, 0, 3)

	for range 3 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			panic(fmt.Sprintf("pick a free port: %v", err))
		}

		defer func() {
			_ = ln.Close()
		}()

		ports = append(ports, strconv.Itoa(int(netip.MustParseAddrPort(ln.Addr().String()).Port())))
	}

	return ports
})

// up starts the tier's services from the server image, which images built.
func (s stack) up(ctx context.Context) error {
	if s.tier == "core" {
		if _, err := s.compose(ctx, "rm", "-sf", "relic-worker"); err != nil {
			return err
		}
	}

	_, err := s.compose(ctx, append([]string{
		"up",
		"-d",
		"--wait",
		"--no-build",
		"--remove-orphans",
	}, s.services()...)...)

	return err
}

// down removes the stack and its volumes, except the vulnerability database:
// downloading it again takes about a minute.
func (s stack) down(ctx context.Context) {
	_, _ = s.compose(ctx, "down", "--remove-orphans")

	out, err := command(
		ctx,
		"docker",
		"volume",
		"ls",
		"-q",
		"--filter",
		"label=com.docker.compose.project="+s.project,
	)
	if err != nil {
		return
	}

	for _, v := range strings.Fields(string(out)) {
		if !strings.HasSuffix(v, "_vulnerability-database") {
			_, _ = command(ctx, "docker", "volume", "rm", v)
		}
	}
}

// serverAddress is where the host reaches the server's HTTP port.
func (s stack) serverAddress(ctx context.Context) (string, error) {
	out, err := s.compose(ctx, "port", "relic-server", "8090")
	if err != nil {
		return "", err
	}

	return "http://" + strings.TrimSpace(string(out)), nil
}

// keepOutput writes every service's log into the results, which CI
// uploads, before the stack goes.
func (s stack) keepOutput(ctx context.Context) {
	logs := filepath.Join(s.results, "logs")
	if err := os.MkdirAll(logs, 0o750); err != nil {
		return
	}

	out, err := s.compose(ctx, "ps", "-a", "--services")
	if err != nil {
		return
	}

	for _, service := range strings.Fields(string(out)) {
		out, err := s.compose(ctx, "logs", "--no-color", "--timestamps", service)
		if err == nil {
			_ = os.WriteFile(filepath.Join(logs, service+".log"), out, 0o600)
		}
	}
}

// images builds the server image, tagged for both services, and the client
// image at once; it returns the client image and the path of the relic
// binary taken from the server image, which the client mounts.
// RELIC_E2E_SERVER_IMAGE and RELIC_E2E_CLIENT_IMAGE name images built before
// (CI builds them in their own steps, with a cache).
func (s stack) images(ctx context.Context, dir string) (client, relic string, err error) {
	var clientErr error

	done := make(chan struct{})

	go func() {
		defer close(done)

		client, clientErr = clientImage(ctx)
	}()

	relic, err = s.serverImage(ctx, dir)

	<-done

	return client, relic, errors.Join(err, clientErr)
}

// serverImage builds the server image, or tags RELIC_E2E_SERVER_IMAGE, for
// both services, and copies its relic binary into dir.
func (s stack) serverImage(ctx context.Context, dir string) (string, error) {
	server := s.project + "-relic-server:latest"

	if prebuilt := os.Getenv("RELIC_E2E_SERVER_IMAGE"); prebuilt != "" {
		if _, err := command(ctx, "docker", "tag", prebuilt, server); err != nil {
			return "", err
		}
	} else if _, err := s.compose(ctx, "build", "relic-server"); err != nil {
		// BuildKit fails one of two builds of a Dockerfile that collide on
		// a locked layer instead of waiting: another tier may be building.
		if _, err := s.compose(ctx, "build", "relic-server"); err != nil {
			return "", err
		}
	}

	if _, err := command(ctx, "docker", "tag", server, s.project+"-relic-worker:latest"); err != nil {
		return "", err
	}

	relic := filepath.Join(dir, "relic")

	return relic, copyFromImage(ctx, server, "/relic", relic)
}

// clientImage builds the client image, or names RELIC_E2E_CLIENT_IMAGE.
func clientImage(ctx context.Context) (string, error) {
	if prebuilt := os.Getenv("RELIC_E2E_CLIENT_IMAGE"); prebuilt != "" {
		return prebuilt, nil
	}

	const client = "relic-e2e-client:latest"

	_, err := command(
		ctx,
		"docker",
		"build",
		"-q",
		"-t",
		client,
		"-f",
		filepath.Join(root, "test", "e2e", "client", "Dockerfile"),
		root,
	)

	return client, err
}

// copyFromImage copies the file at src in image to dst on the host.
func copyFromImage(ctx context.Context, image, src, dst string) error {
	out, err := command(ctx, "docker", "create", image)
	if err != nil {
		return err
	}

	id := strings.TrimSpace(string(out))

	defer func() {
		_, _ = command(context.WithoutCancel(ctx), "docker", "rm", id)
	}()

	_, err = command(ctx, "docker", "cp", id+":"+src, dst)

	return err
}

// command runs name with args in the repository, with the stack's
// environment, and returns its output; a failure carries the output.
func command(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // docker with the suite's own arguments
	cmd.Dir = root

	cmd.Env = append(os.Environ(), stack{}.environment()...)

	var out bytes.Buffer

	cmd.Stdout, cmd.Stderr = &out, &out

	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, lastLines(out.Bytes(), 30))
	}

	return out.Bytes(), nil
}

func lastLines(b []byte, n int) string {
	lines := bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n"))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	return string(bytes.Join(lines, []byte("\n")))
}

// phase logs how long each part of the setup took, so slow steps show.
func phase(t testing.TB, start time.Time, what string) {
	t.Helper()
	t.Logf("[%5.1fs] %s", time.Since(start).Seconds(), what)
}
