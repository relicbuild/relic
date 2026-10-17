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
