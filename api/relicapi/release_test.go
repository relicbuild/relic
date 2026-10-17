package relicapi

import (
	"errors"
	"reflect"
	"testing"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/artifacts/itemquery"
	"relic/internal/delivery/release"
)

func TestSpecOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		spec    *relicv1.ReleaseSpec
		want    []release.Source
		wantErr bool
	}{
		{
			name:    "no spec",
			spec:    nil,
			wantErr: true,
		},
		{
			name: "query",
			spec: &relicv1.ReleaseSpec{
				Name:    "a",
				Version: "1",
				Sources: []*relicv1.ReleaseSource{
					{
						Query: repoQuery("x"),
					},
				},
			},
			want: []release.Source{
				{
					Kind: release.SourceQuery,
				},
			},
		},
		{
			name: "a query without a value",
			spec: &relicv1.ReleaseSpec{
				Name:    "a",
				Version: "1",
				Sources: []*relicv1.ReleaseSource{
					{
						Query: &relicv1.ItemQuery{
							Criteria: &relicv1.ItemCriterion{
								Criterion: &relicv1.ItemCriterion_Field{
									Field: &relicv1.ItemFieldComparison{
										Field:    relicv1.ItemField_ITEM_FIELD_NAME,
										Operator: relicv1.ComparisonOperator_COMPARISON_OPERATOR_EQUAL,
									},
								},
							},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "files",
			spec: &relicv1.ReleaseSpec{
				Name:    "a",
				Version: "1",
				Sources: []*relicv1.ReleaseSource{
					{
						Files: []*relicv1.ReleaseFileRef{
							{
								Path:   "x/a",
								Sha256: "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
							},
						},
					},
				},
			},
			want: []release.Source{
				{
					Kind: release.SourceArtifacts,
					Artifacts: []release.ArtifactRef{
						{
							Path:   "x/a",
							SHA256: "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
						},
					},
				},
			},
		},
		{
			name: "builds",
			spec: &relicv1.ReleaseSpec{
				Name:    "a",
				Version: "1",
				Sources: []*relicv1.ReleaseSource{
					{
						Builds: []*relicv1.ReleaseBuildRef{
							{
								Name:         "b",
								Number:       "1",
								Project:      "p",
								Dependencies: true,
							},
						},
					},
				},
			},
			want: []release.Source{
				{
					Kind: release.SourceBuilds,
					Builds: []release.BuildRef{
						{
							Name:         "b",
							Number:       "1",
							Project:      "p",
							Dependencies: true,
						},
					},
				},
			},
		},
		{
			name: "releases",
			spec: &relicv1.ReleaseSpec{
				Name:    "a",
				Version: "1",
				Sources: []*relicv1.ReleaseSource{
					{
						Releases: []*relicv1.ReleaseRef{
							{
								Name:    "r",
								Version: "1",
								Project: "p",
							},
						},
					},
				},
			},
			want: []release.Source{
				{
					Kind: release.SourceReleases,
					Releases: []release.Ref{
						{
							Name:    "r",
							Version: "1",
							Project: "p",
						},
					},
				},
			},
		},
		{
			name: "a query and builds",
			spec: &relicv1.ReleaseSpec{
				Name:    "a",
				Version: "1",
				Sources: []*relicv1.ReleaseSource{
					{
						Query: repoQuery("x"),
					},
					{
						Builds: []*relicv1.ReleaseBuildRef{
							{
								Name:   "b",
								Number: "1",
							},
						},
					},
				},
			},
			want: []release.Source{
				{
					Kind: release.SourceQuery,
				},
				{
					Kind: release.SourceBuilds,
					Builds: []release.BuildRef{
						{
							Name:   "b",
							Number: "1",
						},
					},
				},
			},
		},
		{
			name: "two kinds in one source",
			spec: &relicv1.ReleaseSpec{
				Sources: []*relicv1.ReleaseSource{
					{
						Query: repoQuery("x"),
						Files: []*relicv1.ReleaseFileRef{
							{
								Path: "x/a",
							},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "an empty source",
			spec: &relicv1.ReleaseSpec{
				Sources: []*relicv1.ReleaseSource{
					{},
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := specOf(tt.spec)
			if tt.wantErr {
				if !errors.Is(err, release.ErrInvalid) {
					t.Errorf("err = %v, want ErrInvalid", err)
				}

				return
			}

			for i := range got.Sources {
				got.Sources[i].Query = itemquery.Query{}
			}

			if err != nil || !reflect.DeepEqual(got.Sources, tt.want) {
				t.Errorf("specOf = %+v, %v; want sources %+v", got, err, tt.want)
			}
		})
	}
}

func repoQuery(name string) *relicv1.ItemQuery {
	return &relicv1.ItemQuery{
		Criteria: &relicv1.ItemCriterion{
			Criterion: &relicv1.ItemCriterion_Field{
				Field: &relicv1.ItemFieldComparison{
					Field:    relicv1.ItemField_ITEM_FIELD_REPOSITORY,
					Operator: relicv1.ComparisonOperator_COMPARISON_OPERATOR_EQUAL,
					Value: &relicv1.ComparisonValue{
						Value: &relicv1.ComparisonValue_Text{
							Text: name,
						},
					},
				},
			},
		},
	}
}
