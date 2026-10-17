//go:build integration

package main

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

func TestMCPOverStdio(t *testing.T) {
	t.Parallel()

	config, _ := adminCLI(t)

	root := newRootCmd(func(string) string {
		return ""
	})
	stdin, input := io.Pipe()
	out := &lockedBuffer{}

	root.SetIn(stdin)
	root.SetOut(out)
	root.SetErr(io.Discard)
	root.SetArgs([]string{
		"--config",
		config,
		"mcp",
	})

	done := make(chan error, 1)
	go func() {
		done <- root.ExecuteContext(t.Context())
	}()

	send := func(line, awaited string) {
		t.Helper()

		if _, err := io.WriteString(input, line+"\n"); err != nil {
			t.Fatal(err)
		}

		if awaited == "" {
			return
		}

		deadline := time.Now().Add(10 * time.Second)
		for !strings.Contains(out.String(), awaited) {
			if time.Now().After(deadline) {
				t.Fatalf("no %s in:\n%s", awaited, out.String())
			}

			time.Sleep(20 * time.Millisecond)
		}
	}

	send(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`"id":1`,
	)
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, "")
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, `"id":2`)
	send(
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_repositories","arguments":{}}}`,
		`"id":3`,
	)

	_ = input.Close()

	if err := <-done; err != nil {
		t.Fatal(err)
	}

	got := out.String()
	for _, want := range []string{
		`"name":"relic"`,
		`"name":"search_artifacts"`,
		`"name":"explain_download"`,
		`"id":3,"result":{"content":[{"type":"text"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the session's output lacks %s:\n%s", want, got)
		}
	}
}
