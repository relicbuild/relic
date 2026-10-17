//go:build e2e

package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var (
	scenarioDir = flag.String("scenarios", "/scenarios", "directory of scenario scripts")
	resultsDir  = flag.String("results", "/results", "where results.json and logs/<scenario>.log go")
	jfBase      = flag.String("jf-home", "/root/.jfrog", "the jf configuration each scenario starts from")
	timeout     = flag.Duration("scenario-timeout", 20*time.Minute, "longest a scenario may run")
)

var results = struct {
	sync.Mutex
	m map[string]string
}{
	m: map[string]string{},
}

func TestMain(m *testing.M) {
	flag.Parse()

	code := m.Run()

	results.Lock()
	b, err := json.MarshalIndent(results.m, "", "  ")
	results.Unlock()

	if err == nil {
		err = os.WriteFile(filepath.Join(*resultsDir, "results.json"), b, 0o600)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "write results:", err)

		code = 1
	}

	os.Exit(code)
}

func TestScenarios(t *testing.T) {
	names, err := Names(*scenarioDir, strings.Fields(os.Getenv("SCENARIOS")))
	if err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(*resultsDir, "logs"), 0o750); err != nil {
		t.Fatal(err)
	}

	t.Run("parallel", func(t *testing.T) {
		for _, n := range names {
			if _, serial := Serial[n]; serial {
				continue
			}

			t.Run(n, func(t *testing.T) {
				t.Parallel()
				run(t, n)
			})
		}
	})
	t.Run("serial", func(t *testing.T) {
		for _, n := range names {
			if _, serial := Serial[n]; serial {
				t.Run(n, func(t *testing.T) {
					run(t, n)
				})
			}
		}
	})
}

func run(t *testing.T, name string) {
	work := t.TempDir()

	home, jfHome, tmp := filepath.Join(work, "home"), filepath.Join(work, "home", ".jfrog"), filepath.Join(work, "tmp")
	for _, d := range []string{
		home,
		tmp,
	} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	if err := CopyDir(*jfBase, jfHome); err != nil {
		t.Fatalf("jf configuration: %v", err)
	}

	logPath := filepath.Join(*resultsDir, "logs", name+".log")

	logFile, err := os.Create(logPath) //nolint:gosec // the results directory
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = logFile.Close()
	}()

	ctx, cancel := context.WithTimeout(t.Context(), *timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, filepath.Join(*scenarioDir, name+".sh")) //nolint:gosec // a scenario from the suite
	cmd.Dir = work
	cmd.Env = Env(os.Environ(), name, home, jfHome, tmp)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 10 * time.Second

	start := time.Now()
	err = cmd.Run()
	took := time.Since(start).Round(time.Second)

	outcome := "pass"
	if err != nil {
		outcome = "fail"
	}

	results.Lock()
	results.m[name] = outcome
	results.Unlock()
	t.Logf("%s  %s  (%s)", outcome, name, took)

	if err != nil {
		t.Errorf("%s failed after %s: %v; log %s, last lines:\n%s", name, took, err, logPath, tail(logPath, 25))
	}
}

func tail(p string, n int) string {
	b, err := os.ReadFile(p) //nolint:gosec // the scenario's own log
	if err != nil {
		return err.Error()
	}

	lines := bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n"))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	return string(bytes.Join(lines, []byte("\n")))
}
