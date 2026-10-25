package telemetry

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestDiskCollectorLogsReadErrors(t *testing.T) {
	var logged bytes.Buffer

	previous := diskLog.Swap(slog.New(slog.NewTextHandler(&logged, nil)))

	t.Cleanup(func() {
		diskLog.Store(previous)
	})

	unregister := RegisterDisk(
		"failing",
		"/failing",
		func() (uint64, uint64, error) {
			return 0, 0, errors.New("statfs: input/output error")
		},
		func() (int64, error) {
			return 0, errors.New("stat: permission denied")
		},
	)
	t.Cleanup(unregister)

	if _, err := registry.Gather(); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"input/output error",
		"permission denied",
	} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("the disk collector did not log %q: %s", want, logged.String())
		}
	}
}

func TestDiskCollectorCountsADatabaseOpenedTwiceOnce(t *testing.T) {
	for range 2 {
		unregister := RegisterDisk(
			"opened_twice",
			"/var/lib/relic/data/relic.db",
			func() (uint64, uint64, error) {
				return 100, 1000, nil
			},
			func() (int64, error) {
				return 50, nil
			},
		)
		t.Cleanup(unregister)
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]float64{}

	for _, family := range families {
		for _, metric := range family.GetMetric() {
			labels := metric.GetLabel()
			if len(labels) == 0 || labels[0].GetName() == "volume" && labels[0].GetValue() == "opened_twice" {
				got[family.GetName()] = metric.GetGauge().GetValue()
			}
		}
	}

	if got["relic_disk_free_bytes"] != 100 || got["relic_disk_size_bytes"] != 1000 ||
		got["relic_db_sqlite_file_bytes"] != 50 {
		t.Errorf("a database opened twice reports %v, want free 100, size 1000 and file 50", got)
	}
}
