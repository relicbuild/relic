package lifecycle

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"relic/internal/artifacts/itemquery"
	rb "relic/internal/delivery/release"
)

func TestCreateBodySpec(t *testing.T) {
	t.Parallel()

	builds := rb.Source{
		Kind: rb.SourceBuilds,
		Builds: []rb.BuildRef{
			{
				Name:         "b",
				Number:       "7",
				Dependencies: true,
			},
		},
	}

	tests := []struct {
		name    string
		body    string
		want    rb.Spec
		wantErr bool
	}{
		{
			name: "single source",
			body: `{"release_bundle_name":"app","release_bundle_version":"1.0.0","source_type":"builds",
				"source":{"builds":[{"build_name":"b","build_number":"7","build_repository":"artifactory-build-info","include_dependencies":true}]},"draft":true}`,
			want: rb.Spec{
				Name:    "app",
				Version: "1.0.0",
				Draft:   true,
				Sources: []rb.Source{
					builds,
				},
			},
		},
		{
			name: "multiple sources",
			body: `{"release_bundle_name":"app","release_bundle_version":"1.0.0","sources":[
				{"source_type":"builds","builds":[{"build_name":"b","build_number":"7","build_repository":"p-build-info"}]},
				{"source_type":"release_bundles","release_bundles":[{"release_bundle_name":"base","release_bundle_version":"2","project":"p","repository_key":"p-release-bundles-v2"}]},
				{"source_type":"release_bundles","release_bundles":[{"release_bundle_name":"lib","release_bundle_version":"3","repository_key":"release-bundles-v2"}]},
				{"source_type":"release_bundles","release_bundles":[{"release_bundle_name":"own","release_bundle_version":"4"}]},
				{"source_type":"artifacts","artifacts":[{"path":"r/a.jar","sha256":"abc"}]},
				{"source_type":"aql","aql":"items.find({})"}]}`,
			want: rb.Spec{
				Name:    "app",
				Version: "1.0.0",
				Sources: []rb.Source{
					{
						Kind: rb.SourceBuilds,
						Builds: []rb.BuildRef{
							{
								Name:    "b",
								Number:  "7",
								Project: "p",
							},
						},
					},
					{
						Kind: rb.SourceReleases,
						Releases: []rb.Ref{
							{
								Name:    "base",
								Version: "2",
								Project: "p",
							},
						},
					},
					{
						Kind: rb.SourceReleases,
						Releases: []rb.Ref{
							{
								Name:    "lib",
								Version: "3",
								Project: rb.DefaultProject,
							},
						},
					},
					{
						Kind: rb.SourceReleases,
						Releases: []rb.Ref{
							{
								Name:    "own",
								Version: "4",
							},
						},
					},
					{
						Kind: rb.SourceArtifacts,
						Artifacts: []rb.ArtifactRef{
							{
								Path:   "r/a.jar",
								SHA256: "abc",
							},
						},
					},
					{
						Kind: rb.SourceQuery,
						Query: itemquery.Query{
							Criteria: itemquery.All{
								itemquery.All{},
								itemquery.Compare{
									Field: itemquery.FieldType,
									Op:    itemquery.OpEqual,
									Value: "file",
								},
							},
						},
					},
				},
			},
		},
		{
			name: "update adds sources",
			body: `{"release_bundle_name":"app","release_bundle_version":"1","add_sources":[{"source_type":"artifacts","artifacts":[{"path":"r/b"}]}]}`,
			want: rb.Spec{
				Name:    "app",
				Version: "1",
				Sources: []rb.Source{
					{
						Kind: rb.SourceArtifacts,
						Artifacts: []rb.ArtifactRef{
							{
								Path: "r/b",
							},
						},
					},
				},
			},
		},
		{
			name:    "packages",
			body:    `{"sources":[{"source_type":"packages","packages":[{}]}]}`,
			wantErr: true,
		},
		{
			name:    "unknown type",
			body:    `{"sources":[{"source_type":"images"}]}`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var body createBody
			if err := json.Unmarshal([]byte(tt.body), &body); err != nil {
				t.Fatal(err)
			}

			got, err := body.spec()
			if tt.wantErr {
				if !errors.Is(err, rb.ErrInvalid) {
					t.Fatalf("err = %v, want ErrInvalid", err)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestPromoteBodySpec(t *testing.T) {
	t.Parallel()

	var body promoteBody
	if err := json.Unmarshal(
		[]byte(`{"environment":"PROD","included_repository_keys":["a"],"excluded_repository_keys":["b"]}`),
		&body,
	); err != nil {
		t.Fatal(err)
	}

	want := rb.PromotionSpec{
		Environment: "PROD",
		Include: []string{
			"a",
		},
		Exclude: []string{
			"b",
		},
		Move: true,
	}
	if got := body.spec("move"); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}

	if body.spec("copy").Move {
		t.Error("copy is a move")
	}
}

func TestStatus(t *testing.T) {
	t.Parallel()

	for s, want := range map[rb.Status]string{
		rb.StatusProcessing: "PROCESSING",
		rb.StatusCompleted:  "COMPLETED",
		rb.StatusDraft:      "DRAFT",
		rb.StatusFailed:     "FAILED",
	} {
		if got := status(s); got != want {
			t.Errorf("status(%q) = %q, want %q", s, got, want)
		}
	}
}
