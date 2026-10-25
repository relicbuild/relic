package relicapi

import (
	"context"
	"errors"
	"slices"
	"testing"
	"uuid"

	"connectrpc.com/connect/v2"

	"relic/internal/delivery/buildinfo"
	"relic/internal/scanning/scan"
)

func TestGraphScanAvailable(t *testing.T) {
	t.Parallel()

	queue := func(context.Context, uuid.UUID) error {
		return nil
	}
	workers := func(running bool, err error) func(context.Context) (bool, error) {
		return func(context.Context) (bool, error) {
			return running, err
		}
	}
	failure := errors.New("database down")

	tests := []struct {
		name     string
		deps     Dependencies
		wantCode connect.Code
		wantErr  error
	}{
		{
			name:     "no scan service",
			deps:     Dependencies{},
			wantCode: connect.CodeUnavailable,
		},
		{
			name: "no worker check",
			deps: Dependencies{
				Scans:          &scan.Service{},
				QueueGraphScan: queue,
			},
			wantCode: connect.CodeUnavailable,
		},
		{
			name: "no worker scans",
			deps: Dependencies{
				Scans:            &scan.Service{},
				QueueGraphScan:   queue,
				GraphScanWorkers: workers(false, nil),
			},
			wantCode: connect.CodeUnavailable,
		},
		{
			name: "the check fails",
			deps: Dependencies{
				Scans:            &scan.Service{},
				QueueGraphScan:   queue,
				GraphScanWorkers: workers(false, failure),
			},
			wantErr: failure,
		},
		{
			name: "a worker scans",
			deps: Dependencies{
				Scans:            &scan.Service{},
				QueueGraphScan:   queue,
				GraphScanWorkers: workers(true, nil),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := graphScanAvailable(t.Context(), tt.deps)

			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("graphScanAvailable = %v, want %v", err, tt.wantErr)
				}
			case tt.wantCode != 0:
				if connect.CodeOf(err) != tt.wantCode {
					t.Errorf("graphScanAvailable = %v, want %v", err, tt.wantCode)
				}
			case err != nil:
				t.Errorf("graphScanAvailable = %v, want nil", err)
			}
		})
	}
}

func TestBuildScanRequestScansEveryDependency(t *testing.T) {
	t.Parallel()

	doc := buildinfo.Document{
		Modules: []buildinfo.Module{
			{
				Type: "npm",
				Dependencies: []buildinfo.Dependency{
					{
						ID:     "lodash:4.17.15",
						SHA256: "1111",
					},
					{
						ID:     "lodash-4.17.15.tgz",
						SHA256: "ABCD",
					},
				},
				Artifacts: []buildinfo.Artifact{
					{
						Name:   "app.tgz",
						SHA256: "ef01",
					},
				},
			},
		},
	}

	req, err := buildScanRequest(t.Context(), Dependencies{}, doc)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(req.PURLs, []string{
		"pkg:npm/lodash@4.17.15",
	}) {
		t.Errorf("purls = %v", req.PURLs)
	}

	if !slices.Equal(req.Artifacts, []string{
		"abcd",
		"ef01",
	}) {
		t.Errorf("content scanned = %v, want the dependency without a package id and the artifact", req.Artifacts)
	}
}
