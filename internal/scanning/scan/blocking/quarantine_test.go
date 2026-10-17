package blocking

import (
	"testing"

	"relic/internal/scanning/scan"
)

func TestHoldState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		scanStatus string
		malicious  bool
		decision   string
		want       QuarantineState
	}{
		{
			name: "not scanned yet",
			want: AwaitingScan,
		},
		{
			name:       "queued",
			scanStatus: scan.StatusQueued,
			want:       AwaitingScan,
		},
		{
			name:       "scan failed",
			scanStatus: scan.StatusFailed,
			want:       ScanFailed,
		},
		{
			name:       "scan skipped",
			scanStatus: scan.StatusSkipped,
			want:       ScanSkipped,
		},
		{
			name:       "malicious",
			scanStatus: scan.StatusDone,
			malicious:  true,
			want:       Malicious,
		},
		{
			name:       "scanned and clean",
			scanStatus: scan.StatusDone,
		},
		{
			name:       "skipped and released",
			scanStatus: scan.StatusSkipped,
			decision:   "released",
		},
		{
			name:       "rejected",
			scanStatus: scan.StatusDone,
			decision:   "rejected",
			want:       Rejected,
		},
		{
			name:       "malicious and released by an administrator",
			scanStatus: scan.StatusDone,
			malicious:  true,
			decision:   "released",
		},
		{
			name:       "malicious and rejected",
			scanStatus: scan.StatusDone,
			malicious:  true,
			decision:   "rejected",
			want:       Rejected,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := holdState(tt.scanStatus, tt.malicious, tt.decision); got != tt.want {
				t.Errorf("holdState = %q, want %q", got, tt.want)
			}
		})
	}
}
