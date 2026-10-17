package telemetry

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestReplicationsConfiguredDropsRemovedReplications(t *testing.T) {
	for range 3 {
		ReplicationRun(t.Context(), "gone-replication", errors.New("refused"), time.Second, 0, 0, 0, 0)
		ReplicationRun(t.Context(), "kept-replication", errors.New("refused"), time.Second, 0, 0, 0, 0)
	}

	ReplicationsConfigured([]string{
		"kept-replication",
	})

	if replicationFailures.DeleteLabelValues("gone-replication") {
		t.Error("the failure gauge of a removed replication is still exported")
	}

	if !replicationFailures.DeleteLabelValues("kept-replication") {
		t.Error("the failure gauge of a configured replication was dropped")
	}

	ReplicationsConfigured(nil)
}

func TestSetQueueNeverExportsAPartialQueue(t *testing.T) {
	oldest := time.Now().Add(-time.Hour)
	stats := []QueueStat{
		{
			Kind:              "scan_artifact",
			Available:         5,
			OldestAvailableAt: &oldest,
		},
	}
	kinds := []string{
		"scan_artifact",
	}

	SetQueue(kinds, stats, time.Now())

	stop := make(chan struct{})
	seen := make(chan string, 1)

	go func() {
		defer close(seen)

		for {
			select {
			case <-stop:
				return
			default:
			}

			mfs, err := registry.Gather()
			if err != nil {
				seen <- err.Error()
				return
			}

			backlog := -1.0

			for _, mf := range mfs {
				if mf.GetName() == "relic_scan_backlog" {
					backlog = mf.GetMetric()[0].GetGauge().GetValue()
				}
			}

			if backlog != 5 {
				seen <- fmt.Sprintf("relic_scan_backlog = %v during a refresh, want 5 throughout", backlog)
				return
			}
		}
	}()

	for range 20000 {
		SetQueue(kinds, stats, time.Now())
	}

	close(stop)

	if problem, ok := <-seen; ok {
		t.Error(problem)
	}

	SetQueue(nil, nil, time.Now())
}
