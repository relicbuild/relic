//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
)

// clientImages are the images scenarios run inside the shared Docker
// daemon; the host's Docker keeps them, and each run loads them into the
// daemon.
var clientImages = []string{
	"alpine:3.24",
	"debian:trixie-slim",
	"mono:6.12",
	"nixos/nix:2.35.3",
	"osixia/openldap:1.5.0",
	"quay.io/podman/stable:v5",
	"rockylinux/rockylinux:10.2",
	"ghcr.io/opencontainers/distribution-spec/conformance:v1.1.1",
}

// daemon starts a Docker daemon on the stack's network under alias, holding
// images; its storage is an anonymous volume, removed with it.
func daemon(ctx context.Context, s stack, alias string, images []string) (testcontainers.Container, error) {
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "docker:29-dind",
			Name:  s.project + "-" + alias,
			Cmd: []string{
				"--insecure-registry",
				"relic-server:8090",
			},
			Env: map[string]string{
				"DOCKER_TLS_CERTDIR": "",
			},
			Networks: []string{
				s.network(),
			},
			NetworkAliases: map[string][]string{
				s.network(): {
					alias,
				},
			},
			HostConfigModifier: func(hc *container.HostConfig) {
				hc.Privileged = true
			},
			WaitingFor: wait.ForExec([]string{
				"docker",
				"info",
			}).WithStartupTimeout(3 * time.Minute),
		},
		Started: true,
		Reuse:   false,
	})
	if err != nil {
		return c, fmt.Errorf("docker daemon %s: %w", alias, err)
	}

	if err := loadImages(ctx, c, images); err != nil {
		return c, fmt.Errorf("docker daemon %s: %w", alias, err)
	}

	return c, nil
}

func loadImages(ctx context.Context, c testcontainers.Container, images []string) error {
	var (
		mu    sync.Mutex
		errs  []error
		pulls sync.WaitGroup
	)

	for _, image := range images {
		pulls.Go(func() {
			if _, err := command(ctx, "docker", "image", "inspect", image); err == nil {
				return
			}

			if err := pull(ctx, image); err != nil {
				mu.Lock()
				defer mu.Unlock()

				errs = append(errs, err)
			}
		})
	}

	pulls.Wait()

	if err := errors.Join(errs...); err != nil {
		return err
	}

	for _, image := range images {
		if err := copyImage(ctx, c, image); err != nil {
			return err
		}
	}

	return nil
}

func copyImage(ctx context.Context, c testcontainers.Container, image string) error {
	platform, err := command(ctx, "docker", "image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", image)
	if err != nil {
		return err
	}

	//nolint:gosec // docker with the suite's own arguments
	save := exec.CommandContext(ctx, "docker", "save", "--platform", strings.TrimSpace(string(platform)), image)
	load := exec.CommandContext(ctx, "docker", "exec", "-i", c.GetContainerID(), "docker", "load", "-q")

	var out bytes.Buffer

	save.Stderr = &out
	load.Stdout, load.Stderr = &out, &out

	if load.Stdin, err = save.StdoutPipe(); err != nil {
		return err
	}

	if err := save.Start(); err != nil {
		return err
	}

	loadErr := load.Run()
	if err := errors.Join(save.Wait(), loadErr); err != nil {
		return fmt.Errorf("load %s: %w\n%s", image, err, lastLines(out.Bytes(), 30))
	}

	return nil
}

const (
	pullAttempts = 3
	pullTimeout  = 10 * time.Minute
)

func pull(ctx context.Context, image string) error {
	var err error

	for range pullAttempts {
		attempt, cancel := context.WithTimeout(ctx, pullTimeout)
		_, err = command(attempt, "docker", "pull", "-q", image)

		cancel()

		if err == nil || ctx.Err() != nil {
			return err
		}
	}

	return err
}

func hostPlatform(ctx context.Context) (string, error) {
	arch, err := command(ctx, "docker", "version", "--format", "{{.Server.Arch}}")
	if err != nil {
		return "", err
	}

	return "linux/" + strings.TrimSpace(string(arch)), nil
}

const vulnerabilityListing = "https://grype.anchore.io/databases/v6/latest.json"

func vulnerabilityDatabases(ctx context.Context) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}

	dir := filepath.Join(cache, "relic-e2e", "grype")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}

	var listing []byte

	body, err := downloaded(ctx, vulnerabilityListing)
	if err == nil {
		listing, err = readSmall(body)
	}

	if err != nil {
		if _, cached := os.Stat(filepath.Join(dir, "latest.json")); cached == nil {
			return dir, nil
		}

		return "", err
	}

	var newest struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(listing, &newest); err != nil {
		return "", fmt.Errorf("%s: %w", vulnerabilityListing, err)
	}

	if newest.Path == "" || filepath.Base(newest.Path) != newest.Path {
		return "", fmt.Errorf("%s: unexpected archive %q", vulnerabilityListing, newest.Path)
	}

	archive := filepath.Join(dir, newest.Path)
	if _, err := os.Stat(archive); errors.Is(err, fs.ErrNotExist) {
		if err := downloadTo(
			ctx,
			strings.TrimSuffix(vulnerabilityListing, "latest.json")+newest.Path,
			archive,
		); err != nil {
			return "", err
		}
	}

	if err := writeAtomically(filepath.Join(dir, "latest.json"), bytes.NewReader(listing)); err != nil {
		return "", err
	}

	older, err := filepath.Glob(filepath.Join(dir, "*.tar.zst"))
	if err != nil {
		return "", err
	}

	for _, f := range older {
		if f != archive {
			_ = os.Remove(f)
		}
	}

	return dir, nil
}

func downloaded(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}

	return resp.Body, nil
}

func readSmall(body io.ReadCloser) ([]byte, error) {
	defer func() {
		_ = body.Close()
	}()

	return io.ReadAll(io.LimitReader(body, 1<<20))
}

func downloadTo(ctx context.Context, url, path string) error {
	body, err := downloaded(ctx, url)
	if err != nil {
		return err
	}

	defer func() {
		_ = body.Close()
	}()

	return writeAtomically(path, body)
}

func writeAtomically(path string, body io.Reader) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.part")
	if err != nil {
		return err
	}

	defer func() {
		_ = os.Remove(tmp.Name())
	}()

	if _, err := io.Copy(tmp, body); err != nil {
		_ = tmp.Close()
		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), path)
}

func vulnerabilityMirror(ctx context.Context, s stack, dir string) (testcontainers.Container, error) {
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "caddy:2",
			Name:  s.project + "-grype-mirror",
			Cmd: []string{
				"caddy",
				"file-server",
				"--root",
				"/srv",
				"--listen",
				":80",
			},
			Networks: []string{
				s.network(),
			},
			NetworkAliases: map[string][]string{
				s.network(): {
					"grype-mirror",
				},
			},
			HostConfigModifier: func(hc *container.HostConfig) {
				hc.Binds = append(hc.Binds, dir+":/srv:ro")
			},
			WaitingFor: wait.ForExec([]string{
				"wget",
				"-q",
				"-O",
				"/dev/null",
				"http://127.0.0.1/latest.json",
			}).WithStartupTimeout(30 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		return c, fmt.Errorf("vulnerability database mirror: %w", err)
	}

	return c, nil
}

// tlsProxy puts HTTPS with Caddy's internal certificate authority in front
// of the server, at tls.relic.test (terraform refuses registry hostnames
// without a dot), and returns that authority's certificate.
func tlsProxy(ctx context.Context, s stack) (testcontainers.Container, []byte, error) {
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "caddy:2",
			Name:  s.project + "-tls",
			Cmd: []string{
				"caddy",
				"reverse-proxy",
				"--from",
				"https://tls.relic.test",
				"--to",
				"relic-server:8090",
				"--internal-certs",
			},
			Networks: []string{
				s.network(),
			},
			NetworkAliases: map[string][]string{
				s.network(): {
					"tls.relic.test",
				},
			},
			WaitingFor: wait.ForExec([]string{
				"test",
				"-f",
				"/data/caddy/pki/authorities/local/root.crt",
			}).WithStartupTimeout(30 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		return c, nil, fmt.Errorf("TLS proxy: %w", err)
	}

	ca, err := run(ctx, c, []string{
		"cat",
		"/data/caddy/pki/authorities/local/root.crt",
	})

	return c, ca, err
}

// collector starts an OpenTelemetry collector that writes the server's
// traces to the results, where the telemetry scenario reads them.
func collector(ctx context.Context, s stack) (testcontainers.Container, error) {
	out := filepath.Join(s.results, "otel")
	if err := os.MkdirAll(out, 0o750); err != nil {
		return nil, err
	}

	return testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "otel/opentelemetry-collector-contrib:0.163.1",
			Name:  s.project + "-otel",
			Cmd: []string{
				"--config=/etc/otelcol/config.yaml",
			},
			Networks: []string{
				s.network(),
			},
			NetworkAliases: map[string][]string{
				s.network(): {
					"relic-e2e-otel",
				},
			},
			ConfigModifier: func(cfg *container.Config) {
				cfg.User = hostUser()
			},
			HostConfigModifier: func(hc *container.HostConfig) {
				hc.Binds = append(
					hc.Binds,
					filepath.Join(root, "test", "e2e", "otel-collector.yaml")+":/etc/otelcol/config.yaml:ro",
					out+":/out",
				)
			},
		},
		Started: true,
	})
}

// client starts the client container, with every package manager the
// scenarios drive, idle until the tests run scenarios in it.
func client(
	ctx context.Context,
	s stack,
	image, relic string,
	env map[string]string,
) (testcontainers.Container, error) {
	if err := os.MkdirAll(filepath.Join(s.results, "logs"), 0o750); err != nil {
		return nil, err
	}

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: image,
			Name:  s.project + "-client",
			Env:   env,
			Networks: []string{
				s.network(),
			},
			HostConfigModifier: func(hc *container.HostConfig) {
				hc.Binds = append(
					hc.Binds,
					filepath.Join(
						root,
						"api",
						"adapters",
						"artifactory",
						"compat",
						"fixtures",
						"scenarios",
					)+":/scenarios:ro",
					s.results+":/results",
					relic+":/usr/local/bin/relic:ro",
				)
			},
		},
		Started: true,
	})
	if err != nil {
		return c, fmt.Errorf("client container: %w", err)
	}

	if _, err := run(ctx, c, []string{
		"/e2e/setup.sh",
	}); err != nil {
		return c, fmt.Errorf("client setup: %w", err)
	}

	return c, nil
}

// run executes cmd in c and returns its output; a non-zero exit is an
// error that carries the output's last lines.
func run(ctx context.Context, c testcontainers.Container, cmd []string) ([]byte, error) {
	code, r, err := c.Exec(ctx, cmd, tcexec.Multiplexed())
	if err != nil {
		return nil, err
	}

	out, err := io.ReadAll(r)
	if err != nil {
		return out, err
	}

	if code != 0 {
		return out, fmt.Errorf("%s exited with %d:\n%s", strings.Join(cmd, " "), code, lastLines(out, 25))
	}

	return out, nil
}

// hostUser is uid:gid of the user running the tests, so files written into
// the results belong to them.
func hostUser() string {
	return strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
}

// stop terminates c, if a start left one, even a typed nil.
func stop(c testcontainers.Container) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	_ = testcontainers.TerminateContainer(c, testcontainers.StopContext(ctx))
}

// trimmed is b without surrounding white space, as a string.
func trimmed(b []byte) string { return string(bytes.TrimSpace(b)) }
