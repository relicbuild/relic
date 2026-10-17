//go:build integration

package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"relic/internal/platform/telemetry/telemetrytest"
)

func TestServerWithWorker(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"RELIC_DATABASE_URL":        "sqlite://" + filepath.Join(dir, "relic.db"),
		"RELIC_OBJECT_STORAGE_TYPE": "filesystem",
		"RELIC_OBJECT_STORAGE_PATH": filepath.Join(dir, "objects"),
		"RELIC_ADMIN_PASSWORD":      "password",
		"RELIC_TLS_DIRECTORY":       filepath.Join(dir, "tls"),
	}

	jobs := telemetrytest.Value(t, "relic_jobs_total", map[string]string{
		"kind":   "trash_purge",
		"result": "success",
	})

	ctx, cancel := context.WithCancel(t.Context())

	var done chan error

	for attempt := 1; ; attempt++ {
		addr := freeAddr(t)
		env["RELIC_HTTP_LISTEN_ADDRESS"] = addr
		env["RELIC_METRICS_LISTEN_ADDRESS"] = freeAddr(t)
		done = make(chan error, 1)

		go func() {
			done <- Run(ctx, []string{
				"server",
				"--worker",
			}, func(k string) string {
				return env[k]
			}, io.Discard)
		}()

		err := waitReady(ctx, "https://"+addr, trustingDirectory(t, filepath.Join(dir, "tls")), done)
		if err == nil {
			break
		}

		if !errors.Is(err, syscall.EADDRINUSE) || attempt == 3 {
			t.Fatal(err)
		}
	}

	plainRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"http://"+env["RELIC_HTTP_LISTEN_ADDRESS"]+"/readyz",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	plain, err := http.DefaultClient.Do(plainRequest)
	if err == nil {
		_ = plain.Body.Close()
	}

	if err != nil || plain.StatusCode != http.StatusBadRequest {
		t.Errorf("plain HTTP to the TLS listener: %v %v, want 400", plain, err)
	}

	waitFor(t, "the worker to run a job", func() bool {
		return telemetrytest.Value(t, "relic_jobs_total", map[string]string{
			"kind":   "trash_purge",
			"result": "success",
		}) > jobs
	})

	for _, m := range []struct {
		name   string
		labels map[string]string
	}{
		{
			"relic_disk_size_bytes",
			map[string]string{
				"volume": "database",
			},
		},
		{
			"relic_disk_size_bytes",
			map[string]string{
				"volume": "object_storage",
			},
		},
		{
			"relic_db_sqlite_file_bytes",
			nil,
		},
	} {
		if v := telemetrytest.Value(t, m.name, m.labels); v <= 0 {
			t.Errorf("%s%v = %v", m.name, m.labels, v)
		}
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("relic server start --worker did not stop")
	}
}

func TestServerWithoutTLS(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	env := map[string]string{
		"RELIC_DATABASE_URL":           "sqlite://" + filepath.Join(dir, "relic.db"),
		"RELIC_OBJECT_STORAGE_TYPE":    "filesystem",
		"RELIC_OBJECT_STORAGE_PATH":    filepath.Join(dir, "objects"),
		"RELIC_ADMIN_PASSWORD":         "password",
		"RELIC_TLS_DIRECTORY":          filepath.Join(dir, "tls"),
		"RELIC_HTTP_LISTEN_ADDRESS":    freeAddr(t),
		"RELIC_METRICS_LISTEN_ADDRESS": freeAddr(t),
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)

	go func() {
		done <- Run(ctx, []string{
			"server",
			"--no-tls",
		}, func(k string) string {
			return env[k]
		}, io.Discard)
	}()

	if err := waitReady(ctx, "http://"+env["RELIC_HTTP_LISTEN_ADDRESS"], plainHTTP, done); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "tls")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("--no-tls made a TLS directory: %v", err)
	}
}

func trustingDirectory(t *testing.T, dir string) func() *http.Client {
	t.Helper()

	return func() *http.Client {
		authority, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
		if err != nil {
			return nil
		}

		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(authority) {
			t.Fatalf("%s holds no certificate", filepath.Join(dir, "ca.crt"))
		}

		return &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					MinVersion: tls.VersionTLS13,
					RootCAs:    roots,
				},
			},
		}
	}
}

func plainHTTP() *http.Client {
	return http.DefaultClient
}

func TestMigrateNeedsOnlyTheDatabase(t *testing.T) {
	t.Parallel()

	url := "sqlite://" + filepath.Join(t.TempDir(), "relic.db")

	err := Run(t.Context(), []string{
		"migrate",
	}, func(k string) string {
		if k == "RELIC_DATABASE_URL" {
			return url
		}

		return ""
	}, io.Discard)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig

	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = ln.Close()
	}()

	return ln.Addr().String()
}

func waitReady(ctx context.Context, baseURL string, client func() *http.Client, done <-chan error) error {
	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			return fmt.Errorf("relic server stopped before it was ready: %w", err)
		default:
		}

		if httpClient := client(); httpClient != nil {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/readyz", nil)
			if err != nil {
				return err
			}

			resp, err := httpClient.Do(request)
			if err == nil {
				_ = resp.Body.Close()

				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}

		time.Sleep(100 * time.Millisecond)
	}

	return errors.New("timed out waiting for the server to be ready")
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(100 * time.Millisecond)
	}
}
