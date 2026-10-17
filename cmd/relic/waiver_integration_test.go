//go:build integration

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const oneRowTableLines = 5

func TestWaiverCommands(t *testing.T) {
	t.Parallel()

	config, _ := adminCLI(t)

	out, err := relic(
		t,
		config,
		"",
		"security",
		"waiver",
		"request",
		"--package",
		"pkg:npm/lodash@4.17.15",
		"--vulnerability",
		"CVE-2020-8203",
		"--reason",
		"not\treachable\nfrom the app",
		"-o",
		"json",
	)
	if err != nil {
		t.Fatalf("request = %q %v", out, err)
	}

	var requested struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &requested); err != nil || requested.ID == "" {
		t.Fatalf("request output %q: %v", out, err)
	}

	out, err = relic(t, config, "", "security", "waiver", "list", "--status", "pending")
	if err != nil || !strings.Contains(out, requested.ID) ||
		!strings.Contains(out, "pkg:npm/lodash@4.17.15,CVE-2020-8203") ||
		!strings.Contains(out, "not reachable from the app") || strings.Count(out, "\n") != oneRowTableLines {
		t.Errorf("list = %q %v", out, err)
	}

	if _, err := relic(t, config, "", "security", "waiver", "list", "--status", "open"); err == nil {
		t.Error("an unknown status was accepted")
	}

	expiresBefore := time.Now().AddDate(0, 0, 14).Local().Format(time.DateOnly)
	out, err = relic(t, config, "", "security", "waiver", "approve", requested.ID, "--days", "14")
	expiresAfter := time.Now().AddDate(0, 0, 14).Local().Format(time.DateOnly)

	if err != nil || !strings.HasPrefix(out, "approved "+requested.ID+": ignore rule ") ||
		(!strings.HasSuffix(out, " expires "+expiresBefore+"\n") && !strings.HasSuffix(out, " expires "+expiresAfter+"\n")) {
		t.Errorf("approve = %q %v, want it to expire in 14 days", out, err)
	}

	if _, err := relic(t, config, "", "security", "waiver", "reject", requested.ID); err == nil ||
		!strings.Contains(err.Error(), "already decided") {
		t.Errorf("rejecting an approved waiver: %v", err)
	}

	out, err = relic(t, config, "", "security", "waiver", "list", "--status", "approved")
	if err != nil || !strings.Contains(out, "approved") || !strings.Contains(out, requested.ID) {
		t.Errorf("approved list = %q %v", out, err)
	}

	out, err = relic(
		t,
		config,
		"",
		"security",
		"waiver",
		"request",
		"--package",
		"pkg:npm/minimist@1.2.0",
		"--reason",
		"development only",
		"-o",
		"json",
	)
	if err != nil {
		t.Fatalf("second request = %q %v", out, err)
	}

	var newer struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &newer); err != nil || newer.ID == "" {
		t.Fatalf("second request output %q: %v", out, err)
	}

	for page, want := range map[string]string{
		"1": newer.ID,
		"2": requested.ID,
	} {
		out, err = relic(t, config, "", "security", "waiver", "list", "--page", page, "--page-size", "1")
		if err != nil || !strings.Contains(out, want) || strings.Count(out, "\n") != oneRowTableLines {
			t.Errorf("page %s of one waiver = %q %v, want %s alone", page, out, err, want)
		}
	}

	if _, err := relic(t, config, "", "security", "waiver", "list", "--page-size", "500"); err == nil {
		t.Error("a page of 500 waivers was accepted")
	}
}
