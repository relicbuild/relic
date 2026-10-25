//go:build integration

package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestProjectCommands(t *testing.T) {
	t.Parallel()

	config, _ := adminCLI(t)

	if _, err := relic(t, config, `apiVersion: relic.build/v1
kind: User
metadata: {name: alice}
spec: {email: alice@example.com, password: Alice-pass-1}
---
apiVersion: relic.build/v1
kind: Group
metadata: {name: qa}
---
apiVersion: relic.build/v1
kind: Project
metadata: {name: acme}
spec:
  name: Acme
  storageQuotaBytes: 1048576
  members:
    - {name: alice, role: admin}
`, "apply", "-f", "-"); err != nil {
		t.Fatal(err)
	}

	if out, err := relic(
		t,
		config,
		"",
		"project",
		"member-add",
		"acme",
		"qa",
		"--group",
		"--role",
		"viewer",
	); err != nil ||
		out != "acme has 2 members\n" {
		t.Fatalf("member-add = %q %v", out, err)
	}

	if _, err := relic(t, config, "", "project", "member-add", "acme", "bob", "--role", "owner"); err == nil {
		t.Error("an unknown role was accepted")
	}

	if _, err := relic(t, config, "", "project", "member-add", "acme", "ghost3", "--role", "viewer"); err == nil {
		t.Error("a user that does not exist was added")
	}

	if out, err := relic(t, config, "", "project", "member-remove", "acme", "alice"); err != nil ||
		out != "acme has 1 members\n" {
		t.Errorf("member-remove = %q %v", out, err)
	}

	out, err := relic(t, config, "", "get", "project", "acme", "-o", "yaml")
	if err != nil || !strings.Contains(out, "name: qa") || !strings.Contains(out, "group: true") ||
		strings.Contains(out, "alice") {
		t.Errorf("get = %q %v", out, err)
	}

	var (
		wg   sync.WaitGroup
		errs = make([]error, 8)
	)

	var developers strings.Builder
	for i := range errs {
		fmt.Fprintf(&developers, "---\napiVersion: relic.build/v1\nkind: User\nmetadata: {name: dev%d}\n"+
			"spec: {email: dev%d@example.com, password: Developer-pass-1}\n", i, i)
	}

	if _, err := relic(t, config, developers.String(), "apply", "-f", "-"); err != nil {
		t.Fatal(err)
	}

	for i := range errs {
		wg.Go(func() {
			_, errs[i] = relic(t, config, "", "project", "member-add", "acme", fmt.Sprintf("dev%d", i))
		})
	}

	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}

	out, err = relic(t, config, "", "get", "project", "acme", "-o", "yaml")
	for i := range errs {
		if err != nil || !strings.Contains(out, fmt.Sprintf("name: dev%d\n", i)) {
			t.Errorf("after concurrent member-adds, dev%d is missing: %q %v", i, out, err)
		}
	}
}
