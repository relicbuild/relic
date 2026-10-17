package artifactory

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"relic/internal/delivery/replication"
)

func TestReplicationTargetURL(t *testing.T) {
	t.Parallel()

	dial := ReplicationTargets(nil)
	for raw, valid := range map[string]bool{
		"https://peer.example/artifactory/libs":  true,
		"https://peer.example/artifactory/libs/": true,
		"http://peer.example:8081":               false,
		"http://peer.example:8081/":              false,
		"libs":                                   false,
	} {
		_, err := dial(replication.Config{
			Repository: "libs",
			TargetURL:  raw,
		})
		if (err == nil) != valid {
			t.Errorf("dial %q: %v, want valid %v", raw, err, valid)
		}

		_, err = replicationFromArtifactory("libs", replicationConfig{
			URL: raw,
		})
		if valid == errors.Is(err, replication.ErrInvalid) {
			t.Errorf("save %q: %v, want valid %v", raw, err, valid)
		}
	}
}

func TestMatrixParams(t *testing.T) {
	t.Parallel()

	got := matrixParams(map[string][]string{
		"b": {
			"x,y",
		},
		"a": {
			"1",
			"2 3",
		},
	})
	if want := ";a=1;a=2+3;b=x%5C%2Cy"; got != want {
		t.Errorf("matrixParams = %q, want %q", got, want)
	}

	if got := matrixParams(nil); got != "" {
		t.Errorf("matrixParams(nil) = %q", got)
	}
}

func TestReplicationTargetStat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    int
		wantFound bool
		wantErr   bool
	}{
		{
			name:      "the target has the file",
			status:    http.StatusOK,
			wantFound: true,
		},
		{
			name:   "the target lacks the file",
			status: http.StatusNotFound,
		},
		{
			name:    "the target is unavailable",
			status:  http.StatusServiceUnavailable,
			wantErr: true,
		},
		{
			name:    "the target refuses the credentials",
			status:  http.StatusUnauthorized,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			t.Cleanup(srv.Close)

			target := &replicationTarget{
				client:  srv.Client(),
				base:    srv.URL,
				repo:    "libs",
				timeout: time.Second,
			}

			remote, err := target.Stat(t.Context(), "app.bin")
			if (err != nil) != tt.wantErr || remote.Found != tt.wantFound {
				t.Errorf(
					"Stat = found %v, error %v; want found %v, error %v",
					remote.Found,
					err,
					tt.wantFound,
					tt.wantErr,
				)
			}
		})
	}
}
