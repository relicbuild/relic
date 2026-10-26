//go:build perf

package perf

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	httpPort    = "8090/tcp"
	metricsPort = "8092/tcp"
)

type server struct {
	container testcontainers.Container
	id        string
	api       string
	metrics   string
	password  string
	http      *http.Client
}

func randomHex(t *testing.T, n int) string {
	t.Helper()

	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}

	return hex.EncodeToString(b)
}

func encryptionKey(t *testing.T) string {
	t.Helper()

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}

	return base64.StdEncoding.EncodeToString(b)
}

func repositoryRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the repository")
	}

	return filepath.Join(filepath.Dir(file), "..", "..")
}

func startServer(ctx context.Context, t *testing.T, memoryLimit int64) server {
	t.Helper()

	password := randomHex(t, 16)
	request := testcontainers.ContainerRequest{
		Name: "relic-perf",
		Cmd: []string{
			"server",
			"start",
			"--worker",
			"--no-tls",
		},
		Env: map[string]string{
			"RELIC_HTTP_LISTEN_ADDRESS":            ":8090",
			"RELIC_METRICS_LISTEN_ADDRESS":         ":8092",
			"RELIC_PROFILING_ENABLED":              "true",
			"RELIC_DATABASE_URL":                   "sqlite:///var/lib/relic/data/relic.db",
			"RELIC_OBJECT_STORAGE_TYPE":            "filesystem",
			"RELIC_OBJECT_STORAGE_PATH":            "/var/lib/relic/data/objects",
			"RELIC_ENCRYPTION_KEY":                 encryptionKey(t),
			"RELIC_ADMIN_PASSWORD":                 password,
			"RELIC_FAILED_SIGN_INS_BEFORE_BACKOFF": "1000000",
		},
		ExposedPorts: []string{
			httpPort,
			metricsPort,
		},
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.Memory = memoryLimit
			hc.MemorySwap = memoryLimit
		},
		WaitingFor: wait.ForHTTP("/readyz").
			WithPort(httpPort).
			WithStartupTimeout(2 * time.Minute),
	}

	request.Image = *imageFlag
	if request.Image == "" {
		request.Image = buildImage(ctx, t)
	}

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: request,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start the server: %v", err)
	}

	t.Cleanup(func() {
		stopServer(context.WithoutCancel(ctx), t, c)
	})

	api, err := c.PortEndpoint(ctx, httpPort, "http")
	if err != nil {
		t.Fatal(err)
	}

	metrics, err := c.PortEndpoint(ctx, metricsPort, "http")
	if err != nil {
		t.Fatal(err)
	}

	return server{
		container: c,
		id:        c.GetContainerID(),
		api:       api,
		metrics:   metrics,
		password:  password,
		http: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

const builtImage = "relic-perf:latest"

func buildImage(ctx context.Context, t *testing.T) string {
	t.Helper()

	build := exec.CommandContext(ctx, "docker", "build",
		"--quiet",
		"--label", "build.relic.e2e=true",
		"--tag", builtImage,
		repositoryRoot(t),
	)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the server image: %v\n%s", err, out)
	}

	return builtImage
}

func stopServer(ctx context.Context, t *testing.T, c testcontainers.Container) {
	t.Helper()

	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	if logs, err := c.Logs(ctx); err == nil {
		body, _ := io.ReadAll(logs)
		_ = logs.Close()

		writeArtifact(t, "server.log", body)
	}

	_ = testcontainers.TerminateContainer(c, testcontainers.StopContext(ctx))
}

func (s server) outOfMemory(ctx context.Context) bool {
	dc, ok := s.container.(*testcontainers.DockerContainer)
	if !ok {
		return false
	}

	info, err := dc.Inspect(ctx)

	return err == nil && info.State != nil && info.State.OOMKilled
}

func (s server) running(ctx context.Context) bool {
	state, err := s.container.State(ctx)

	return err == nil && state.Running
}

func (s server) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, s.api+path, body)
	if err != nil {
		return nil, err
	}

	request.SetBasicAuth("admin", s.password)

	return request, nil
}

func (s server) createRepository(ctx context.Context, t *testing.T, name, format, upstream string) {
	t.Helper()

	repository := map[string]any{
		"name":   name,
		"format": format,
		"class":  "REPOSITORY_CLASS_LOCAL",
	}
	if upstream != "" {
		repository["class"] = "REPOSITORY_CLASS_REMOTE"
		repository["remote"] = map[string]string{
			"url": upstream,
		}
	}

	body, err := json.Marshal(map[string]any{
		"repository": repository,
	})
	if err != nil {
		t.Fatal(err)
	}

	request, err := s.request(ctx, http.MethodPost, "/api/relic.v1.RepositoryService/CreateRepository",
		bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	request.Header.Set("Content-Type", "application/json")

	response, err := s.http.Do(request)
	if err != nil {
		t.Fatalf("create repository %s: %v", name, err)
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(response.Body)
		t.Fatalf("create repository %s: %s: %s", name, response.Status, message)
	}
}

func (s server) fetch(ctx context.Context, method, path string, body io.Reader) result {
	start := time.Now()

	request, err := s.request(ctx, method, path, body)
	if err != nil {
		return result{
			path: path,
			err:  err,
		}
	}

	response, err := s.http.Do(request)
	if err != nil {
		return result{
			path:     path,
			err:      err,
			duration: time.Since(start),
		}
	}

	defer func() {
		_ = response.Body.Close()
	}()

	size, err := io.Copy(io.Discard, response.Body)

	return result{
		path:     path,
		status:   response.StatusCode,
		size:     size,
		duration: time.Since(start),
		err:      err,
	}
}

type result struct {
	path     string
	status   int
	size     int64
	duration time.Duration
	err      error
}

func (r result) failed() bool {
	return r.err != nil || r.status < 200 || r.status > 299
}

func (r result) String() string {
	if r.err != nil {
		return fmt.Sprintf("error %v %s", r.err, r.path)
	}

	return fmt.Sprintf("%d %d %.3fs %s", r.status, r.size, r.duration.Seconds(), r.path)
}
