//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
)

// clientImages are the images scenarios run inside the shared Docker
// daemon; its volume keeps them between runs.
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

// daemon starts a Docker daemon on the stack's network under alias, its
// storage in a named volume, cleaned of what an earlier run left and, for
// the shared daemon, holding the client images.
func daemon(ctx context.Context, s stack, alias string, images []string) (testcontainers.Container, error) {
	name := s.project + "-" + alias

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "docker:29-dind",
			Name:  name,
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
				// A named volume Docker owns, not testcontainers: it keeps the
				// pulled images for the next run.
				hc.Binds = append(hc.Binds, name+":/var/lib/docker")
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

	script := `docker rm -f $(docker ps -aq) >/dev/null 2>&1; docker volume prune -af >/dev/null 2>&1
printf "%s\n" "$@" >/tmp/kept
docker images --format "{{.Repository}}:{{.Tag}}" | grep -v "<none>" | grep -vxFf /tmp/kept | xargs -r docker rmi >/dev/null 2>&1
for i in "$@"; do docker images -q "$i"; done >/tmp/kept-ids
docker images -qa | sort -u | grep -vxFf /tmp/kept-ids | xargs -r docker rmi -f >/dev/null 2>&1
docker builder prune -af >/dev/null 2>&1
for i in "$@"; do docker image inspect "$i" >/dev/null 2>&1 || docker pull -q "$i" >/dev/null 2>&1 & done
wait; true`

	if _, err := run(ctx, c, append([]string{
		"sh",
		"-c",
		script,
		"sh",
	}, images...)); err != nil {
		return c, fmt.Errorf("prepare docker daemon %s: %w", alias, err)
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
