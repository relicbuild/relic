package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerStop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		system  bool
		answers string
		yes     bool
		state   string
		wantRan []string
		wantOut string
		wantErr string
	}{
		{
			name:    "stops the user service after a yes",
			answers: "y\n",
			state:   "ActiveState=active\nMainPID=4242\n",
			wantRan: []string{
				"systemctl --user stop relic.service",
			},
			wantOut: "pid 4242",
		},
		{
			name:    "an empty answer keeps it running",
			answers: "\n",
			state:   "ActiveState=active\nMainPID=4242\n",
			wantErr: "cancelled",
		},
		{
			name:    "a no keeps it running",
			answers: "n\n",
			state:   "ActiveState=active\nMainPID=4242\n",
			wantErr: "cancelled",
		},
		{
			name:    "no answer on stdin asks for --yes",
			state:   "ActiveState=active\nMainPID=4242\n",
			wantErr: "--yes",
		},
		{
			name:  "--yes does not ask",
			yes:   true,
			state: "ActiveState=active\nMainPID=4242\n",
			wantRan: []string{
				"systemctl --user stop relic.service",
			},
		},
		{
			name:    "a stopped service is left alone",
			state:   "ActiveState=inactive\nMainPID=0\n",
			wantOut: "not running",
		},
		{
			name:    "a failed service is left alone",
			state:   "ActiveState=failed\nMainPID=0\n",
			wantOut: "not running",
		},
		{
			name:    "stops the system service",
			system:  true,
			answers: "yes\n",
			state:   "ActiveState=active\nMainPID=7\n",
			wantRan: []string{
				"systemctl stop relic.service",
			},
			wantOut: "system service",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			installed := installedService(t, tt.system)

			m, ran, out := testMachineSetup(t, tt.answers)
			m.home, m.root, m.yes = installed.home, installed.root, tt.yes
			m.query = func(context.Context, string, ...string) (string, error) {
				return tt.state, nil
			}

			err := m.runStop(t.Context())
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}

			if strings.Join(*ran, "\n") != strings.Join(tt.wantRan, "\n") {
				t.Errorf("ran %q, want %q", *ran, tt.wantRan)
			}

			if !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("output %q, want %q", out, tt.wantOut)
			}

			if asked := strings.Contains(out.String(), "[y/N]"); asked == tt.yes && tt.wantRan != nil {
				t.Errorf("asked = %v with --yes = %v: %q", asked, tt.yes, out)
			}
		})
	}
}

func TestServerStopWithoutAService(t *testing.T) {
	t.Parallel()

	m, ran, _ := testMachineSetup(t, "y\n")

	if err := m.runStop(t.Context()); err == nil || !strings.Contains(err.Error(), "relic server setup") {
		t.Fatalf("err = %v", err)
	}

	if len(*ran) != 0 {
		t.Errorf("ran %q", *ran)
	}
}

func installedService(t *testing.T, system bool) *machineSetup {
	t.Helper()

	if !system {
		return installedUserService(t)
	}

	m, _, _ := testMachineSetup(t, "")
	m.yes, m.system, m.euid = true, true, 0
	m.executable = filepath.Join(t.TempDir(), "relic")

	//nolint:gosec // G306: an executable
	if err := os.WriteFile(m.executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := m.runSetup(t.Context()); err != nil {
		t.Fatal(err)
	}

	return m
}
