package lifecycle

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	rb "relic/internal/delivery/release"
)

func TestPageOrder(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	bundles := []rb.Release{
		{
			Name:    "beta",
			Created: start,
		},
		{
			Name:    "alpha",
			Created: start.Add(2 * time.Hour),
		},
		{
			Name:    "gamma",
			Created: start.Add(time.Hour),
		},
	}

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{
			name:  "newest first by default",
			query: "",
			want: []string{
				"alpha",
				"gamma",
				"beta",
			},
		},
		{
			name:  "oldest first when ascending",
			query: "order_asc=true",
			want: []string{
				"beta",
				"gamma",
				"alpha",
			},
		},
		{
			name:  "names descending by default",
			query: "order_by=release_bundle_name",
			want: []string{
				"gamma",
				"beta",
				"alpha",
			},
		},
		{
			name:  "names ascending when ascending",
			query: "order_by=release_bundle_name&order_asc=true",
			want: []string{
				"alpha",
				"beta",
				"gamma",
			},
		},
		{
			name:  "short name key ascending",
			query: "order_by=name&order_asc=true",
			want: []string{
				"alpha",
				"beta",
				"gamma",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := httptest.NewRequestWithContext(
				t.Context(),
				http.MethodGet,
				"/lifecycle/api/v2/release_bundle/groups?"+tt.query,
				nil,
			)

			list, _, _, _ := page(r, slices.Clone(bundles), func(b rb.Release) string {
				return b.Name
			})

			var got []string
			for _, b := range list {
				got = append(got, b.Name)
			}

			if !slices.Equal(got, tt.want) {
				t.Errorf("page(%q) = %v; want %v", tt.query, got, tt.want)
			}
		})
	}
}
