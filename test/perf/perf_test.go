//go:build perf

package perf

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/tabwriter"
	"time"

	"github.com/docker/go-units"
)

var (
	memoryFlag  = flag.String("memory", "512m", "the server container's memory limit")
	phasesFlag  = flag.String("phases", "", "the workloads to run, space separated (default all)")
	imageFlag   = flag.String("image", "", "an existing server image to use instead of building one")
	startupFlag = flag.Duration("startup", 3*time.Minute, "how long the startup phase lasts")
	loadFlag    = flag.Duration("load", 2*time.Minute, "how long the upload and download load lasts")
	idleFlag    = flag.Duration("idle", 2*time.Minute, "how long the idle phase lasts")
)

const phaseTimeout = 15 * time.Minute

type phaseReport struct {
	name     string
	duration time.Duration
	peaks    peaks
	requests int
	failed   []result
}

func TestMemory(t *testing.T) {
	limit, err := units.RAMInBytes(*memoryFlag)
	if err != nil {
		t.Fatalf("-memory: %v", err)
	}

	selected, err := selectWorkloads(*phasesFlag)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	s := startServer(ctx, t, limit)
	recorder := startSampler(ctx, t, s)
	t.Logf("server up with %s of memory; artifacts in %s", *memoryFlag, t.ArtifactDir())

	var reports []phaseReport

	for _, w := range selected {
		if !s.running(ctx) {
			t.Errorf("%s: skipped, the server is not running", w.name)

			continue
		}

		reports = append(reports, runPhase(ctx, t, s, recorder, w))
	}

	report := formatReport(reports)
	writeArtifact(t, "report.txt", []byte(report))
	t.Log("\n" + report)

	if s.outOfMemory(ctx) {
		t.Errorf("the server ran out of memory with %s", *memoryFlag)
	}

	for _, r := range reports {
		if len(r.failed) > 0 {
			t.Errorf("%s: %d of %d requests failed, first: %v", r.name, len(r.failed), r.requests, r.failed[0])
		}
	}
}

func selectWorkloads(names string) ([]workload, error) {
	if strings.TrimSpace(names) == "" {
		return workloads, nil
	}

	var selected []workload

	for name := range strings.FieldsSeq(names) {
		found := false

		for _, w := range workloads {
			if w.name == name {
				selected = append(selected, w)
				found = true
			}
		}

		if !found {
			return nil, fmt.Errorf("-phases: unknown workload %q", name)
		}
	}

	return selected, nil
}

func runPhase(ctx context.Context, t *testing.T, s server, recorder *sampler, w workload) phaseReport {
	t.Helper()

	recorder.begin(ctx, w.name)

	start := time.Now()

	t.Logf("%s: start", w.name)

	phaseCtx, cancel := context.WithTimeout(ctx, phaseTimeout)
	results := w.run(phaseCtx, t, s)

	cancel()

	end := time.Now()

	if s.running(ctx) {
		recorder.end(ctx, w.name)
	}

	var lines strings.Builder

	report := phaseReport{
		name:     w.name,
		duration: end.Sub(start),
		peaks:    recorder.peaksBetween(start, end),
		requests: len(results),
	}

	for _, r := range results {
		fmt.Fprintln(&lines, r)

		if r.failed() {
			report.failed = append(report.failed, r)
		}
	}

	writeArtifact(t, w.name+".txt", []byte(lines.String()))
	t.Logf("%s: done in %v", w.name, report.duration.Round(time.Second))

	return report
}

func formatReport(reports []phaseReport) string {
	var b strings.Builder

	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', tabwriter.AlignRight)
	_, _ = fmt.Fprintln(
		w,
		"phase\tseconds\tcgroup\tanon\trelic rss\trelic heap\trequests\tfailed\tlargest other process\t",
	)

	for _, r := range reports {
		_, _ = fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\t%d\t%d\t%s %s\t\n",
			r.name,
			int(r.duration.Seconds()),
			mebibytes(r.peaks.cgroup),
			mebibytes(r.peaks.anon),
			mebibytes(r.peaks.relicRSS),
			mebibytes(r.peaks.relicHeap),
			r.requests,
			len(r.failed),
			mebibytes(r.peaks.otherRSS),
			truncate(r.peaks.otherProcess, 50),
		)
	}

	_ = w.Flush()

	return b.String()
}

func mebibytes(n uint64) string {
	return fmt.Sprintf("%dM", n>>20)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n]
}

func writeArtifact(t *testing.T, name string, body []byte) {
	if err := os.WriteFile(filepath.Join(t.ArtifactDir(), name), body, 0o600); err != nil {
		t.Logf("write %s: %v", name, err)
	}
}

func TestParseRSS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  uint64
		ok    bool
	}{
		{
			name:  "kibibytes without a suffix",
			value: "844",
			want:  844 << 10,
			ok:    true,
		},
		{
			name:  "mebibytes",
			value: "90m",
			want:  90 << 20,
			ok:    true,
		},
		{
			name:  "fractional gibibytes",
			value: "1.5g",
			want:  3 << 29,
			ok:    true,
		},
		{
			name:  "not a size",
			value: "RSS",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := parseRSS(tt.value)
			if got != tt.want || ok != tt.ok {
				t.Errorf("parseRSS(%q) = %d, %v; want %d, %v", tt.value, got, ok, tt.want, tt.ok)
			}
		})
	}
}
