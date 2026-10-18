//go:build integration

package integration

import (
	"slices"
	"testing"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func TestRelicAPIVirtualTree(t *testing.T) {
	t.Parallel()
	srv := server(t)

	ctx := t.Context()
	for _, r := range []string{
		"one",
		"two",
	} {
		do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/repositories/" + r,
				body:   `{"rclass":"local","packageType":"generic"}`,
			},
		)
		do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/" + r + "/app/shared.txt",
			body:   r,
		})
	}

	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/one/app/a.txt",
		body:   "a",
	})
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/two/app/b.txt",
		body:   "b",
	})
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/inner",
			body:   `{"rclass":"virtual","packageType":"generic","repositories":["two"]}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/all",
			body:   `{"rclass":"virtual","packageType":"generic","repositories":["one","inner"]}`,
		},
	)

	type entry struct {
		name, repository string
	}

	list := func(c relicv1connect.ArtifactServiceClient, path string) ([]entry, error) {
		res, err := c.ListChildren(ctx, &relicv1.ListChildrenRequest{
			Repository: "all",
			Path:       path,
		})
		if err != nil {
			return nil, err
		}

		var out []entry
		for _, it := range res.GetItems() {
			out = append(out, entry{
				it.GetName(),
				it.GetRepository(),
			})
		}

		return out, nil
	}

	admin := relicv1connect.NewArtifactServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	if got, err := list(admin, ""); err != nil || !slices.Equal(got, []entry{
		{
			"app",
			"one",
		},
	}) {
		t.Errorf("root = %v %v", got, err)
	}

	want := []entry{
		{
			"a.txt",
			"one",
		},
		{
			"b.txt",
			"two",
		},
		{
			"shared.txt",
			"one",
		},
	}
	if got, err := list(admin, "app"); err != nil || !slices.Equal(got, want) {
		t.Errorf("app = %v %v, want %v", got, err, want)
	}

	d, err := admin.GetItemDetails(
		ctx,
		&relicv1.GetItemDetailsRequest{
			Repository: "all",
			Path:       "app/b.txt",
		},
	)
	if err != nil || d.GetItem().GetRepository() != "two" || d.GetItem().GetPath() != "app/b.txt" {
		t.Errorf("details through the virtual repository = %v %v", d, err)
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/security/users/vera",
			body:   `{"email":"v@example.com","password":"Vera-pass-1234"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/vera",
			body:   `{"name":"vera","repo":{"repositories":["one","all"],"actions":{"users":{"vera":["read"]}}}}`,
		},
	)
	vera := relicv1connect.NewArtifactServiceClient(apiClient(relicHTTP("vera", "Vera-pass-1234"), srv.URL))

	want = []entry{
		{
			"a.txt",
			"one",
		},
		{
			"shared.txt",
			"one",
		},
	}
	if got, err := list(vera, "app"); err != nil || !slices.Equal(got, want) {
		t.Errorf("reader of one only = %v %v, want %v", got, err, want)
	}

	if _, err := vera.GetItemDetails(
		ctx,
		&relicv1.GetItemDetailsRequest{
			Repository: "all",
			Path:       "app/b.txt",
		},
	); code(err) != connect.CodeNotFound {
		t.Errorf("details of an unreadable member's file = %v, want not_found", err)
	}
}
