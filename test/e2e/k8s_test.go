//go:build e2e

package e2e

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// k8sImages are loaded into the kind cluster besides Relic's own image.
var k8sImages = []string{
	"chrislusf/seaweedfs:4.48",
	"curlimages/curl:8.23.0",
	"alpine/k8s:1.37.1",
}

// TestKubernetes runs k8s.sh in the client image against the host's Docker
// daemon: one kind cluster, the Helm chart, Kubernetes pulls, a rolling
// restart and an upgrade, HA, and the restore drill.
func TestKubernetes(t *testing.T) {
	if *tierFlag != "k8s" {
		t.Skip("runs with -tier k8s")
	}

	start := time.Now()
	ctx := t.Context()
	s := stack{
		tier:    "k8s",
		project: "relic-e2e-k8s",
		files: []string{
			"deploy/docker-compose.yml",
		},
		results: filepath.Join(root, "test", "e2e", "results", "k8s"),
	}

	clientImage, relic, err := s.images(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	phase(t, start, "images built")

	if _, err := command(ctx, "docker", "tag", s.project+"-relic-server:latest", "relic:k8s"); err != nil {
		t.Fatal(err)
	}

	archives := t.TempDir()
	if err := saveImages(ctx, archives, append([]string{
		"relic:k8s",
	}, k8sImages...)); err != nil {
		t.Fatal(err)
	}

	phase(t, start, "images saved for kind")

	if err := os.MkdirAll(s.results, 0o750); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{}

	for _, k := range []string{
		"KIND_NODE_IMAGE",
	} {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}

	if *keepFlag {
		env["KEEP"] = "1"
	}

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: clientImage,
			Name:  s.project + "-client",
			Env:   env,
			Entrypoint: []string{
				"bash",
			},
			Cmd: []string{
				"/k8s.sh",
			},
			HostConfigModifier: func(hc *container.HostConfig) {
				hc.NetworkMode = "host"
				hc.Binds = append(
					hc.Binds,
					"/var/run/docker.sock:/var/run/docker.sock",
					relic+":/usr/local/bin/relic:ro",
					filepath.Join(root, "helm", "relic")+":/chart:ro",
					filepath.Join(root, "test", "e2e", "k8s.sh")+":/k8s.sh:ro",
					archives+":/images:ro",
				)
			},
			WaitingFor: wait.ForExit().WithExitTimeout(100 * time.Minute),
		},
		Started: true,
	})

	t.Cleanup(func() {
		stop(t, c)
	})

	if err != nil {
		t.Fatal(err)
	}

	logs, err := c.Logs(ctx)
	if err != nil {
		t.Fatal(err)
	}

	out, _ := io.ReadAll(logs)
	_ = logs.Close()
	_ = os.WriteFile(filepath.Join(s.results, "k8s.log"), out, 0o600)

	state, err := c.State(ctx)
	if err != nil {
		t.Fatal(err)
	}

	phase(t, start, "done")

	if state.ExitCode != 0 {
		t.Fatalf("k8s.sh failed (exit %d):\n%s", state.ExitCode, lastLines(out, 80))
	}
}

// saveImages writes each image, pulled if missing, as an archive of the
// Docker host's own platform into dir: kind imports every platform of an
// index, and a pulled image holds only one.
func saveImages(ctx context.Context, dir string, images []string) error {
	platform, err := hostPlatform(ctx)
	if err != nil {
		return err
	}

	for _, img := range images {
		if _, err := command(ctx, "docker", "image", "inspect", img); err != nil {
			if _, err := command(ctx, "docker", "pull", "-q", img); err != nil {
				return err
			}
		}

		archive := filepath.Join(dir, strings.NewReplacer("/", "_", ":", "_").Replace(img)+".tar")
		if _, err := command(ctx, "docker", "save", "--platform", platform, "-o", archive, img); err != nil {
			return err
		}
	}

	return nil
}
