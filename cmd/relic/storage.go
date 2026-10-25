package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"

	"relic/internal/platform/terminal"
)

// storageReport is a storage check job's output.
type storageReport struct {
	Objects       int   `json:"objects"`
	Adopted       int   `json:"adopted"`
	Missing       int   `json:"missing"`
	WrongSize     int   `json:"wrongSize"`
	Corrupt       int   `json:"corrupt"`
	Verified      int   `json:"verified"`
	VerifiedBytes int64 `json:"verifiedBytes"`
	Problems      []struct {
		SHA256  string `json:"sha256"`
		Problem string `json:"problem"`
	} `json:"problems,omitempty"`
}

const (
	storageCheckPoll         = 2 * time.Second
	storageCheckStartTimeout = 5 * time.Minute
	storageCheckWaitTimeout  = 3 * time.Hour
)

func storageCheckCmd(g *globals) *cobra.Command {
	var (
		output  string
		wait    bool
		timeout time.Duration
	)

	cmd := &cobra.Command{
		Use:   "check",
		Short: "Compare stored content with the database and report missing or damaged files",
		Long: "Queues a storage check on the worker. It hands objects no file uses to garbage collection, " +
			"reports content that is missing or of the wrong size, and re-hashes a share of the stored content. " +
			"The worker also runs it daily. With --wait, prints the report and exits with status 1 when " +
			"content is missing or damaged. It gives up when no worker starts the check within 5 minutes " +
			"or the check has not finished within --timeout.",
		Example: "  relic storage check\n  relic storage check --wait -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			res, err := c.system.CheckStorage(cmd.Context(), &relicv1.CheckStorageRequest{})
			if err != nil {
				return fmt.Errorf("storage check: %w", err)
			}

			id := res.GetJobId()
			if !wait {
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "storage check queued as job %d\n", id)

				return err
			}

			report, err := waitForStorageCheck(cmd, c, id, timeout)
			if err != nil {
				return err
			}

			if err := printMessage(cmd.OutOrStdout(), output, report, func(w io.Writer) error {
				return printStorageReport(w, report)
			}); err != nil {
				return err
			}

			if report.Missing+report.WrongSize+report.Corrupt > 0 {
				return exitCode1
			}

			return nil
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the check and print its report")
	cmd.Flags().DurationVar(&timeout, "timeout", storageCheckWaitTimeout, "how long --wait waits for the report")
	outputFlag(cmd, &output)

	return cmd
}

func waitForStorageCheck(cmd *cobra.Command, c *client, id int64, timeout time.Duration) (*storageReport, error) {
	deadline := time.Now().Add(timeout)
	startDeadline := time.Now().Add(min(timeout, storageCheckStartTimeout))

	for {
		res, err := c.system.ListBackgroundJobs(cmd.Context(), &relicv1.ListBackgroundJobsRequest{
			Kind:  "storage_check",
			Limit: 50,
		})
		if err != nil {
			return nil, fmt.Errorf("storage check job %d: %w", id, err)
		}

		job := storageCheckJob(res.GetJobs(), id)
		if job == nil {
			return nil, fmt.Errorf("storage check job %d: not among the latest storage checks", id)
		}

		now := time.Now()

		switch {
		case job.GetFinalized() != nil:
			return storageCheckResult(job)
		case job.GetAttempted() == nil && now.After(startDeadline):
			return nil, fmt.Errorf(
				"storage check job %d: still queued after %s; no worker has started it "+
					"(relic server worker, or relic server start --worker)",
				id,
				min(timeout, storageCheckStartTimeout),
			)
		case now.After(deadline):
			return nil, fmt.Errorf("storage check job %d: still %s after %s", id, job.GetState(), timeout)
		}

		select {
		case <-cmd.Context().Done():
			return nil, cmd.Context().Err()
		case <-time.After(storageCheckPoll):
		}
	}
}

func storageCheckJob(jobs []*relicv1.BackgroundJob, id int64) *relicv1.BackgroundJob {
	for _, j := range jobs {
		if j.GetId() == id {
			return j
		}
	}

	return nil
}

func storageCheckResult(j *relicv1.BackgroundJob) (*storageReport, error) {
	id := j.GetId()

	if j.GetState() != "completed" {
		msg := j.GetState()
		if errs := j.GetErrors(); len(errs) > 0 {
			msg = errs[len(errs)-1].GetError()
		}

		return nil, fmt.Errorf("storage check job %d failed: %s", id, msg)
	}

	var report storageReport
	if err := json.Unmarshal([]byte(j.GetOutput()), &report); err != nil {
		return nil, fmt.Errorf("storage check job %d: report: %w", id, err)
	}

	return &report, nil
}

func printStorageReport(w io.Writer, r *storageReport) error {
	counts := terminal.Table{
		Headers: []string{
			"OBJECTS",
			"COUNT",
			"NOTE",
		},
		RightAligned: []int{
			1,
		},
	}

	for _, count := range []struct {
		name  string
		count int
		note  string
	}{
		{
			name:  "stored",
			count: r.Objects,
		},
		{
			name:  "adopted",
			count: r.Adopted,
			note:  "handed to garbage collection",
		},
		{
			name:  "missing",
			count: r.Missing,
		},
		{
			name:  "wrong size",
			count: r.WrongSize,
		},
		{
			name:  "corrupt",
			count: r.Corrupt,
		},
		{
			name:  "verified",
			count: r.Verified,
			note:  terminal.ByteSize(r.VerifiedBytes) + " re-hashed",
		},
	} {
		counts.Rows = append(counts.Rows, []string{
			count.name,
			strconv.Itoa(count.count),
			count.note,
		})
	}

	if err := counts.Print(w); err != nil {
		return err
	}

	problems := terminal.Table{
		Headers: []string{
			"SHA-256",
			"PROBLEM",
		},
	}

	for _, p := range r.Problems {
		problems.Rows = append(problems.Rows, []string{
			p.SHA256,
			tableCell(p.Problem),
		})
	}

	if len(problems.Rows) == 0 {
		return nil
	}

	return problems.Print(w)
}
