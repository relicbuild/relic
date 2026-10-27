package scan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"relic/internal/platform/scripttest"
)

func TestParseGrype(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("testdata/grype.json")
	if err != nil {
		t.Fatal(err)
	}

	rep, err := ParseGrype(data)
	if err != nil {
		t.Fatal(err)
	}

	if len(rep.Findings) != 3 || rep.DBBuilt.IsZero() || !strings.HasPrefix(rep.Scanner, "grype ") {
		t.Fatalf("report = %+v", rep)
	}

	for _, f := range rep.Findings {
		if f.ID == "" || f.Package.PURL == "" || f.Summary == "" {
			t.Errorf("incomplete finding %+v", f)
		}

		switch f.Severity {
		case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow, SeverityUnknown:
		default:
			t.Errorf("severity %q not normalized", f.Severity)
		}

		if len(f.CVEs()) == 0 {
			t.Errorf("finding %s has no CVE", f.ID)
		}

		if len(f.Aliases) > 0 && !strings.HasPrefix(f.Aliases[0], "CVE-") && len(f.CVEs()) > 0 &&
			!strings.HasPrefix(f.ID, "CVE-") {
			t.Errorf("aliases not CVE-first: %v", f.Aliases)
		}

		if f.ID == "GHSA-35jh-r3h4-6jhm" && !slices.Contains(f.AliasCVSS["CVE-2021-23337"], CVSS{
			Version: "2.0",
			Vector:  "AV:N/AC:L/Au:S/C:P/I:P/A:P",
			Score:   6.5,
		}) {
			t.Errorf("CVE-2021-23337 keeps no CVSS of its own: %v", f.AliasCVSS)
		}
	}
}

func TestExecLimitsAndEnvironment(t *testing.T) { //nolint:paralleltest // uses t.Setenv
	dir := t.TempDir()

	script := filepath.Join(dir, "fake")
	scripttest.Write(t, script, "#!/bin/sh\necho \"$SECRET|$1\"\nyes | head -c 5000\n")

	t.Setenv("SECRET", "leaked")

	spool := t.TempDir()
	t.Setenv("TMPDIR", spool)

	out, err := Exec{
		Path:      script,
		MaxOutput: 1 << 20,
	}.run(context.Background(), "arg")
	if err != nil || !strings.HasPrefix(string(out), "|arg\n") || len(out) != len("|arg\n")+5000 {
		t.Errorf("run = %q, %v (environment must not be inherited)", out[:min(len(out), 20)], err)
	}

	if spooled, err := os.ReadDir(spool); err != nil || len(spooled) != 0 {
		t.Errorf("output files left after a run = %v, %v", spooled, err)
	}

	if _, err := (Exec{
		Path:      script,
		MaxOutput: 100,
	}).run(context.Background()); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized output error = %v", err)
	}

	slow := filepath.Join(dir, "slow")
	scripttest.Write(t, slow, "#!/bin/sh\nsleep 30\n")

	start := time.Now()
	if _, err := (Exec{
		Path:    slow,
		Timeout: 100 * time.Millisecond,
	}).run(context.Background()); err == nil ||
		time.Since(start) > 15*time.Second {
		t.Errorf("timeout not enforced: %v after %v", err, time.Since(start))
	}

	if spooled, err := os.ReadDir(spool); err != nil || len(spooled) != 0 {
		t.Errorf("output files left after failed runs = %v, %v", spooled, err)
	}
}

func TestExecWritesItsOutputToAFile(t *testing.T) { //nolint:paralleltest // uses t.Setenv
	spool := t.TempDir()
	t.Setenv("TMPDIR", spool)

	script := filepath.Join(t.TempDir(), "scanner")
	scripttest.Write(t, script, "#!/bin/sh\nprintf 'spooled'\nls \"$TMPDIR\"\n")

	out, err := Exec{
		Path: script,
		Env: []string{
			"TMPDIR=" + spool,
		},
	}.run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(string(out), "spooled") || !strings.Contains(string(out), "relic-scanner-") {
		t.Errorf("run = %q, want the output spooled to a file in the temporary directory", out)
	}
}

func TestExecErrorsCarryTheScannersOwnError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		script string
		want   string
	}{
		{
			name:   "an error on stderr",
			script: "#!/bin/sh\necho 'ERROR failed to load vulnerability db' >&2\nexit 1\n",
			want:   "grype: exit status 1: ERROR failed to load vulnerability db",
		},
		{
			name:   "nothing on stderr",
			script: "#!/bin/sh\nexit 1\n",
			want:   "grype: exit status 1",
		},
		{
			name:   "grype told to keep its errors to itself",
			script: "#!/bin/sh\nfor a; do [ \"$a\" = -q ] && exit 1; done\necho 'ERROR no database' >&2\nexit 1\n",
			want:   "grype: exit status 1: ERROR no database",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			script := filepath.Join(t.TempDir(), "grype")
			scripttest.Write(t, script, tt.script)

			_, err := Grype{
				Exec: Exec{
					Path: script,
				},
				DatabaseDir: t.TempDir(),
			}.Match(t.Context(), "sbom.json")
			if err == nil || err.Error() != tt.want {
				t.Errorf("Match = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSyftCatalogsPackageManifestsOfExtractedArchives(t *testing.T) {
	t.Parallel()

	script := filepath.Join(t.TempDir(), "syft")
	scripttest.Write(t, script, "#!/bin/sh\necho \"$@\"\n")

	syft := Syft{
		Exec: Exec{
			Path: script,
		},
	}

	for source, want := range map[string]bool{
		"dir:/scan/extracted": true,
		"file:/scan/a.jar":    false,
	} {
		out, err := syft.Catalog(t.Context(), source)
		if err != nil {
			t.Fatal(err)
		}

		if got := strings.Contains(string(out), "--select-catalogers +javascript-package-cataloger"); got != want {
			t.Errorf("%s: syft %s, want the npm package.json cataloger %v", source, out, want)
		}
	}
}

func TestSyftSkipsTheUpdateCheck(t *testing.T) {
	t.Parallel()

	script := filepath.Join(t.TempDir(), "syft")
	scripttest.Write(t, script, "#!/bin/sh\nenv\n")

	syft := Syft{
		Exec: Exec{
			Path: script,
			Env: []string{
				"SYFT_PARALLELISM=1",
			},
		},
	}

	out, err := syft.Catalog(t.Context(), "file:/scan/a.jar")
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"SYFT_CHECK_FOR_APP_UPDATE=false",
		"SYFT_PARALLELISM=1",
	} {
		if !strings.Contains(string(out), want+"\n") {
			t.Errorf("syft environment %q, want %s", out, want)
		}
	}
}

func TestCycloneDX(t *testing.T) {
	t.Parallel()

	b, err := CycloneDX([]string{
		"pkg:npm/lodash@4.17.15",
	})
	if err != nil || !strings.Contains(string(b), `"purl":"pkg:npm/lodash@4.17.15"`) ||
		!strings.Contains(string(b), `"bomFormat":"CycloneDX"`) {
		t.Errorf("CycloneDX = %s, %v", b, err)
	}
}

func TestGrypeHasDB(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	g := Grype{
		DatabaseDir: dir,
	}
	if g.HasDB() {
		t.Fatal("empty directory has a database")
	}

	if err := os.MkdirAll(filepath.Join(dir, "6"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "6", "vulnerability.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if g.HasDB() {
		t.Fatal("database without import.json counts as installed")
	}

	if err := os.WriteFile(filepath.Join(dir, "6", "import.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	if !g.HasDB() {
		t.Fatal("installed database not found")
	}

	partial := t.TempDir()

	download := filepath.Join(partial, "grype-db-download3750882657")
	if err := os.MkdirAll(download, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(download, "import.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if (Grype{
		DatabaseDir: partial,
	}).HasDB() {
		t.Error("an abandoned download counts as an installed database")
	}
}

func TestGrypeDatabaseCommandsOutlastTheDefaultTimeout(t *testing.T) { //nolint:paralleltest // sets defaultExecTimeout
	saved := defaultExecTimeout
	defaultExecTimeout = 200 * time.Millisecond

	t.Cleanup(func() {
		defaultExecTimeout = saved
	})

	script := filepath.Join(t.TempDir(), "grype")
	scripttest.Write(t, script, "#!/bin/sh\nsleep 0.6\n")

	g := Grype{
		Exec: Exec{
			Path: script,
		},
		DatabaseDir: t.TempDir(),
	}

	if err := g.UpdateDB(t.Context()); err != nil {
		t.Errorf("UpdateDB = %v, want it to run past the default timeout", err)
	}

	if err := g.ImportDB(t.Context(), "archive.tar.zst"); err != nil {
		t.Errorf("ImportDB = %v, want it to run past the default timeout", err)
	}

	if _, err := g.Match(t.Context(), "sbom.json"); err == nil {
		t.Error("Match ran past the default timeout")
	}
}

func TestGrypeUpdateDBRemovesAbandonedDownloads(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, sub := range []string{
		"grype-db-download891423730",
		"6",
	} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, sub, "import.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	script := filepath.Join(t.TempDir(), "grype")
	scripttest.Write(t, script, "#!/bin/sh\nmkdir \"$GRYPE_DB_CACHE_DIR/grype-db-download42\"\nexit 1\n")

	err := Grype{
		Exec: Exec{
			Path: script,
		},
		DatabaseDir: dir,
	}.UpdateDB(t.Context())
	if err == nil {
		t.Fatal("UpdateDB of a failing grype succeeded")
	}

	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(left) != 1 || filepath.Base(left[0]) != "6" {
		t.Errorf("database directory holds %v, want only the installed database", left)
	}
}

func TestTruncateKeepsValidUTF8(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		max  int
		want string
	}{
		{
			name: "short text",
			text: "main ← parse",
			max:  100,
			want: "main ← parse",
		},
		{
			name: "cut inside an arrow",
			text: "main ← parse",
			max:  6,
			want: "main ",
		},
		{
			name: "cut after an arrow",
			text: "main ← parse",
			max:  8,
			want: "main ←",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := truncate(tt.text, tt.max)
			if got != tt.want || !utf8.ValidString(got) {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.text, tt.max, got, tt.want)
			}
		})
	}
}

func TestGrypeDBBuilt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  string
		want    time.Time
		wantErr string
	}{
		{
			name:   "a built database",
			status: `{"built":"2026-09-29T00:00:00Z"}`,
			want:   time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC),
		},
		{
			name:    "no build time",
			status:  `{}`,
			wantErr: "grype db status: no build time in its output",
		},
		{
			name:    "not JSON",
			status:  `not json`,
			wantErr: "grype db status: unexpected output: invalid character",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			script := filepath.Join(t.TempDir(), "grype")
			scripttest.Write(t, script, "#!/bin/sh\necho '"+tt.status+"'\n")

			got, err := Grype{
				Exec: Exec{
					Path: script,
				},
				DatabaseDir: t.TempDir(),
			}.DBBuilt(t.Context())
			if tt.wantErr != "" {
				if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) {
					t.Errorf("DBBuilt = %v, want an error starting %q", err, tt.wantErr)
				}

				return
			}

			if err != nil || !got.Equal(tt.want) {
				t.Errorf("DBBuilt = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}
