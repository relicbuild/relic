//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestStack runs the tier's scenarios, and on core the jfrog-client-go
// suite and the UI tests, against one stack, then checks its storage.
func TestStack(t *testing.T) {
	if *tierFlag == "k8s" {
		t.Skip("-tier k8s runs TestKubernetes only")
	}

	if !slices.Contains(Tiers, *tierFlag) {
		t.Fatalf("-tier %q: core, platform or k8s", *tierFlag)
	}

	start := time.Now()
	s := newStack(*tierFlag)
	ctx := t.Context()

	if r := s.remote(); r != "" && !strings.HasPrefix(r, "https://") {
		t.Fatalf("-url %s: an https:// address", r)
	}

	names, err := s.scenarios()
	if err != nil {
		t.Fatal(err)
	}

	required, err := Required(filepath.Join(root, requiredList))
	if err != nil {
		t.Fatal(err)
	}

	s.down(ctx)

	if err := os.RemoveAll(s.results); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(s.results, 0o750); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		s.keepOutput(ctx)

		if !*keepFlag {
			s.down(ctx)
		}
	})

	if _, err := command(
		ctx,
		"docker",
		"network",
		"create",
		"--label",
		"com.docker.compose.network=default",
		"--label",
		"com.docker.compose.project="+s.project,
		s.network(),
	); err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Fatal(err)
	}

	containers := s.startContainers(t, names)

	if s.remote() == "" {
		if err := s.serveVulnerabilityDatabases(t); err != nil {
			t.Fatal(err)
		}

		phase(t, start, "vulnerability database served")
	}

	clientImage, relic, err := s.images(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	phase(t, start, "images built")

	var (
		uiContainer testcontainers.Container
		uiErr       error
	)

	if s.remote() == "" {
		if err := s.up(ctx); err != nil {
			t.Fatal(err)
		}

		phase(t, start, "stack up")

		// The UI tests start while the daemons and the client get ready;
		// Lighthouse, which measures speed, runs alone after the scenarios.
		if s.tier == "core" && *scenariosFlag == "" {
			if err := os.RemoveAll(filepath.Join(root, "web", "e2e", "results")); err != nil {
				t.Fatal(err)
			}

			uiContainer, uiErr = startUI(ctx, s, "ui", "--grep-invert", "Lighthouse")

			t.Cleanup(func() {
				stop(uiContainer)
			})
		}
	}

	if err := containers.wait(); err != nil {
		t.Fatal(err)
	}

	if containers.tlsCA != nil {
		if err := os.WriteFile(filepath.Join(s.results, "tls-ca.crt"), containers.tlsCA, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cl, err := client(ctx, s, clientImage, relic, s.clientEnvironment(names))

	t.Cleanup(func() {
		stop(cl)
	})

	if err != nil {
		t.Fatal(err)
	}

	phase(t, start, "client ready")

	address := s.remote()
	if address == "" {
		if address, err = s.serverAddress(ctx); err != nil {
			t.Fatal(err)
		}
	}

	results := scenarioResults{
		m: map[string]string{},
	}

	t.Cleanup(func() {
		results.write(t, s.results)

		// Files the client wrote as root belong to whoever runs the tests.
		_, _ = run(context.Background(), cl, []string{
			"chown",
			"-R",
			hostUser(),
			"/results",
		})
	})

	t.Run("suites", func(t *testing.T) {
		t.Run("scenarios", func(t *testing.T) {
			t.Parallel()

			for _, n := range names {
				if _, serial := Serial[n]; !serial {
					t.Run(n, func(t *testing.T) {
						t.Parallel()
						scenario(t, cl, n, required[n], &results)
					})
				}
			}
		})

		if s.tier != "core" {
			return
		}

		if *scenariosFlag == "" || slices.Contains(strings.Fields(*scenariosFlag), sdkScenario) {
			t.Run("sdk", func(t *testing.T) {
				t.Parallel()
				sdk(t, address, &results)
			})
		}

		if *scenariosFlag == "" && s.remote() == "" {
			t.Run("ui", func(t *testing.T) {
				t.Parallel()
				ui(t, s, "ui", uiContainer, uiErr)
			})
		}
	})

	t.Run("serial scenarios", func(t *testing.T) {
		for _, n := range names {
			if _, serial := Serial[n]; serial {
				t.Run(n, func(t *testing.T) {
					scenario(t, cl, n, required[n], &results)
				})
			}
		}
	})

	if s.tier == "core" && *scenariosFlag == "" && s.remote() == "" {
		t.Run("lighthouse", func(t *testing.T) {
			c, err := startUI(
				t.Context(),
				s,
				"lighthouse",
				"--output",
				"e2e/results/lighthouse",
				"e2e/lighthouse.spec.ts",
			)

			t.Cleanup(func() {
				stop(c)
			})

			ui(t, s, "lighthouse", c, err)
		})
	}

	t.Run("storage check", func(t *testing.T) {
		if s.remote() != "" {
			t.Skip("checks only the stack's own storage")
		}

		out, err := run(ctx, cl, []string{
			"sh",
			"-c",
			`printf '%s\n' "$RELIC_PASSWORD" | relic --config /tmp/storage-check.yaml auth login "$RELIC_URL" ` +
				`--user "$RELIC_USER" --password-stdin >/dev/null && ` +
				`relic --config /tmp/storage-check.yaml storage check --wait -o json`,
		})
		if err != nil {
			t.Fatal(err)
		}

		t.Logf("storage check: %s", trimmed(out))
	})

	phase(t, start, "done")
}

// scenarios lists the tier's scenarios, or those -scenarios names that
// belong to it.
func (s stack) scenarios() ([]string, error) {
	dir := filepath.Join(root, scenarioDir)

	only := slices.DeleteFunc(strings.Fields(*scenariosFlag), func(n string) bool {
		return n == sdkScenario
	})
	if len(only) == 0 && *scenariosFlag != "" {
		return nil, nil
	}

	all, err := Names(dir, only)
	if err != nil {
		return nil, err
	}

	var out []string

	for _, n := range all {
		tier, err := Tier(filepath.Join(dir, n+".sh"))
		if err != nil {
			return nil, err
		}

		if tier == s.tier {
			out = append(out, n)
		}
	}

	return out, nil
}

// clientEnvironment is what the scenarios read: the server's addresses on
// the stack's network and the daemons of those with one of their own.
func (s stack) clientEnvironment(names []string) map[string]string {
	var own []string

	for _, n := range names {
		if _, ok := OwnDaemon[n]; ok {
			own = append(own, n)
		}
	}

	if r := s.remote(); r != "" {
		return map[string]string{
			"RELIC_URL":                r,
			"RELIC_USER":               os.Getenv("RELIC_USER"),
			"RELIC_PASSWORD":           os.Getenv("RELIC_PASSWORD"),
			"RELIC_TOKEN":              os.Getenv("RELIC_TOKEN"),
			"SCENARIO_REGISTRY":        strings.TrimPrefix(r, "https://"),
			"SCENARIO_REGISTRY_TLS":    "true",
			"SCENARIO_TLS_URL":         r,
			"SCENARIO_REMOTE_SERVER":   "true",
			"DOCKER_HOST":              "tcp://relic-e2e-dind:2375",
			"SCENARIO_DOCKER_HOSTNAME": "relic-e2e-dind",
			"E2E_OWN_DAEMON":           strings.Join(own, " "),
		}
	}

	return map[string]string{
		"RELIC_URL":                "http://relic-server:8090",
		"RELIC_USER":               "admin",
		"RELIC_PASSWORD":           "password",
		"SCENARIO_REGISTRY":        "relic-server:8090",
		"SCENARIO_TLS_URL":         "https://tls.relic.test",
		"SCENARIO_METRICS_URL":     "http://relic-server:8091",
		"SCENARIO_TRACES_FILE":     "/results/otel/traces.json",
		"DOCKER_HOST":              "tcp://relic-e2e-dind:2375",
		"SCENARIO_DOCKER_HOSTNAME": "relic-e2e-dind",
		"E2E_OWN_DAEMON":           strings.Join(own, " "),
		"TLS_CA":                   "/results/tls-ca.crt",
	}
}

// testContainers are the containers the scenarios need beside the stack,
// started while the stack comes up.
type testContainers struct {
	done  chan struct{}
	err   error
	tlsCA []byte
}

func (c *testContainers) wait() error {
	<-c.done
	return c.err
}

// startContainers starts the Docker daemons, the collector and the TLS
// proxy in the background; wait returns when all are ready. Each is stopped
// when t ends.
func (s stack) startContainers(t *testing.T, names []string) *testContainers {
	ctx := t.Context()
	out := &testContainers{
		done: make(chan struct{}),
	}

	var (
		mu      sync.Mutex
		started []testcontainers.Container
		wg      sync.WaitGroup
		errs    []error
	)

	keep := func(c testcontainers.Container, err error) {
		mu.Lock()
		defer mu.Unlock()

		if c != nil {
			started = append(started, c)
		}

		if err != nil {
			errs = append(errs, err)
		}
	}

	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()

		for _, c := range started {
			stop(c)
		}
	})

	aliases := map[string][]string{
		"relic-e2e-dind": clientImages,
	}

	for _, n := range names {
		if _, ok := OwnDaemon[n]; ok {
			aliases["relic-e2e-dind-"+n] = nil
		}
	}

	for alias, images := range aliases {
		wg.Go(func() {
			keep(daemon(ctx, s, alias, images))
		})
	}

	if s.remote() == "" {
		wg.Go(func() {
			keep(collector(ctx, s))
		})

		wg.Go(func() {
			c, ca, err := tlsProxy(ctx, s)
			keep(c, err)

			mu.Lock()
			defer mu.Unlock()

			out.tlsCA = ca
		})
	}

	go func() {
		defer close(out.done)

		wg.Wait()

		mu.Lock()
		defer mu.Unlock()

		if len(errs) > 0 {
			out.err = errors.Join(errs...)
		}
	}()

	return out
}

// scenarioResults records each scenario's outcome for results.json.
type scenarioResults struct {
	sync.Mutex
	m map[string]string
}

func (r *scenarioResults) set(name, outcome string) {
	r.Lock()
	defer r.Unlock()

	r.m[name] = outcome
}

func (r *scenarioResults) write(t *testing.T, dir string) {
	r.Lock()
	defer r.Unlock()

	b, err := json.MarshalIndent(r.m, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "results.json"), b, 0o600)
	}

	if err != nil {
		t.Errorf("write results: %v", err)
	}
}

// scenario runs one scenario in the client container. A required scenario
// that fails fails the test; any other is reported and skipped.
func scenario(t *testing.T, cl testcontainers.Container, name string, required bool, results *scenarioResults) {
	start := time.Now()
	_, err := run(t.Context(), cl, []string{
		"/e2e/scenario.sh",
		name,
	})
	took := time.Since(start).Round(time.Second)

	outcome := "pass"
	if err != nil {
		outcome = "fail"
	}

	results.set(name, outcome)

	if err == nil {
		t.Logf("pass  %s  (%s)", name, took)
		return
	}

	log := filepath.Join(newStack(*tierFlag).results, "logs", name+".log")
	tail := ""

	if b, rerr := os.ReadFile(log); rerr == nil { //nolint:gosec // the scenario's own log
		tail = lastLines(b, 25)
	}

	if !required {
		t.Skipf("fail  %s  (%s), not required; log %s:\n%s", name, took, log, tail)
	}

	t.Errorf("fail  %s  (%s); log %s, last lines:\n%s", name, took, log, tail)
}

// sdk runs the jfrog-client-go suite, its own module, against the server.
func sdk(t *testing.T, address string, results *scenarioResults) {
	cmd := exec.CommandContext( //nolint:gosec // the suite's own module
		t.Context(),
		"go",
		"test",
		"-tags=e2e",
		"-count=1",
		"-v",
		"./...",
	)
	cmd.Dir = filepath.Join(root, "api", "adapters", "artifactory", "compat", "sdk")

	cmd.Env = append(os.Environ(), "RELIC_SDK_URL="+address)

	out, err := cmd.CombinedOutput()
	log := filepath.Join(newStack(*tierFlag).results, "logs", sdkScenario+".log")
	_ = os.WriteFile(log, out, 0o600)

	if err != nil {
		results.set(sdkScenario, "fail")
		t.Fatalf("the jfrog-client-go suite failed; log %s:\n%s", log, lastLines(out, 40))
	}

	results.set(sdkScenario, "pass")
}

// startUI starts Playwright tests of web/e2e, chosen by args, in
// Playwright's image against the server on the stack's network, without
// waiting for them. web/node_modules must be installed.
func startUI(ctx context.Context, s stack, name string, args ...string) (testcontainers.Container, error) {
	web := filepath.Join(root, "web")

	b, err := os.ReadFile(filepath.Join(web, "node_modules", "@playwright", "test", "package.json"))
	if err != nil {
		return nil, fmt.Errorf("playwright is not installed: run pnpm install in web: %w", err)
	}

	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return nil, err
	}

	return testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "mcr.microsoft.com/playwright:v" + pkg.Version + "-noble",
			Name:  s.project + "-" + name,
			Env: map[string]string{
				"HOME":         "/tmp",
				"RELIC_UI_URL": "http://relic-server:8090",
			},
			Cmd: append([]string{
				"node",
				"node_modules/@playwright/test/cli.js",
				"test",
				"-c",
				"e2e/playwright.config.ts",
			}, args...),
			Networks: []string{
				s.network(),
			},
			ConfigModifier: func(cfg *container.Config) {
				cfg.User = hostUser()
				cfg.WorkingDir = "/web"
			},
			HostConfigModifier: func(hc *container.HostConfig) {
				hc.Binds = append(hc.Binds, web+":/web")
			},
		},
		Started: true,
	})
}

// ui waits for the UI tests startUI started, keeps their output as
// logs/<name>.log and fails t when they failed.
func ui(t *testing.T, s stack, name string, c testcontainers.Container, startErr error) {
	if startErr != nil {
		t.Fatal(startErr)
	}

	if err := wait.ForExit().WithExitTimeout(30*time.Minute).WaitUntilReady(t.Context(), c); err != nil {
		t.Fatal(err)
	}

	logs, err := c.Logs(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	out, _ := io.ReadAll(logs)
	_ = logs.Close()
	_ = os.WriteFile(filepath.Join(s.results, "logs", name+".log"), out, 0o600)

	state, err := c.State(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if state.ExitCode != 0 {
		t.Fatalf("the UI tests failed (exit %d):\n%s", state.ExitCode, lastLines(out, 60))
	}

	t.Logf("UI tests: %s", lastLines(out, 3))
}
