//go:build integration

package integration

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func TestTrashCan(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/libs",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)

	for _, f := range []string{
		"org/a.txt",
		"org/b.txt",
		"tmp/c.txt",
	} {
		do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/libs/" + f,
			body:   "content of " + f,
		})
	}

	status := func(c call) int {
		t.Helper()
		resp, _ := do(t, srv, c)

		return resp.StatusCode
	}

	if got := status(call{
		method: "DELETE",
		path:   "/artifactory/libs/org/a.txt",
	}); got != 204 {
		t.Fatalf("delete = %d", got)
	}

	if got := status(call{
		method: "GET",
		path:   "/artifactory/libs/org/a.txt",
	}); got != 404 {
		t.Errorf("deleted file = %d, want 404", got)
	}

	resp, body := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/auto-trashcan/libs/org/a.txt",
	})
	if resp.StatusCode != 200 || body != "content of org/a.txt" {
		t.Errorf("trashed file = %d %q", resp.StatusCode, body)
	}

	_, props := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/storage/auto-trashcan/libs/org/a.txt?properties",
		},
	)
	for _, want := range []string{
		`"trash.originalRepository"`,
		`"trash.originalPath"`,
		`"trash.deletedBy"`,
		`"trash.time"`,
	} {
		if !strings.Contains(props, want) {
			t.Errorf("trash properties lack %s: %s", want, props)
		}
	}

	if _, repos := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/repositories",
	}); strings.Contains(
		repos,
		"auto-trashcan",
	) {
		t.Errorf("repositories list the trash: %s", repos)
	}

	if _, found := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/search/aql",
			body:   `items.find({"name":"a.txt"})`,
		},
	); strings.Contains(found, "auto-trashcan") {
		t.Errorf("AQL returned trash items: %s", found)
	}

	if _, found := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/search/aql",
			body:   `items.find({"repo":"auto-trashcan"})`,
		},
	); !strings.Contains(found, `"a.txt"`) {
		t.Errorf("AQL naming the trash = %s", found)
	}

	if _, found := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/search/aql",
			body:   `items.find({"name":"a.txt","path":{"$ne":"auto-trashcan"}})`,
		},
	); strings.Contains(found, "auto-trashcan\"") {
		t.Errorf("AQL with the trash name as a path value returned trash items: %s", found)
	}

	if resp, found := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/search/aql",
			body:   `items.find({"name":"a\u0000"})`,
		},
	); resp.StatusCode != 500 && !json.Valid([]byte(found)) {
		t.Errorf("AQL failing before its first row = %d %q, want 500 or a valid result", resp.StatusCode, found)
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/security/users/tess",
			body:   `{"email":"t@example.com","password":"Tess-pass-123"}`,
		},
	)

	perm := `{"name":"tess","repo":{"repositories":["ANY LOCAL"],"include-patterns":["**"],"actions":{"users":{"tess":["read","write","delete"]}}}}`
	do(t, srv, call{
		method: "POST",
		path:   "/artifactory/api/v2/security/permissions/tess",
		body:   perm,
	})

	tess := map[string]string{
		"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("tess:Tess-pass-123")),
	}
	if got := status(
		call{
			method:  "GET",
			path:    "/artifactory/auto-trashcan/libs/org/a.txt",
			headers: tess,
			noAuth:  true,
		},
	); got != 403 &&
		got != 404 {
		t.Errorf("non-admin reads the trash = %d", got)
	}

	if got := status(
		call{
			method:  "POST",
			path:    "/artifactory/api/trash/empty",
			headers: tess,
			noAuth:  true,
		},
	); got != 403 {
		t.Errorf("non-admin empties the trash = %d, want 403", got)
	}

	if got := status(call{
		method: "POST",
		path:   "/artifactory/api/trash/restore/libs/org/a.txt",
	}); got != 200 {
		t.Fatalf("restore = %d", got)
	}

	if resp, body := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/libs/org/a.txt",
	}); resp.StatusCode != 200 ||
		body != "content of org/a.txt" {
		t.Errorf("restored file = %d %q", resp.StatusCode, body)
	}

	if _, props := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/storage/libs/org/a.txt?properties",
		},
	); strings.Contains(props, "trash.") {
		t.Errorf("restored file keeps trash properties: %s", props)
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/other",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)
	status(call{
		method: "DELETE",
		path:   "/artifactory/libs/org",
	})

	if got := status(
		call{
			method: "POST",
			path:   "/artifactory/api/trash/restore/libs/org?to=other/restored",
		},
	); got != 200 {
		t.Fatalf("restore elsewhere = %d", got)
	}

	for _, f := range []string{
		"other/restored/a.txt",
		"other/restored/b.txt",
	} {
		if got := status(call{
			method: "GET",
			path:   "/artifactory/" + f,
		}); got != 200 {
			t.Errorf("%s = %d after restoring the folder", f, got)
		}
	}

	status(call{
		method: "DELETE",
		path:   "/artifactory/libs/tmp/c.txt",
	})

	if got := status(call{
		method: "POST",
		path:   "/artifactory/api/trash/restore/libs?to=other",
	}); got != 400 {
		t.Errorf("restore of a whole repository's entry into another repository = %d, want 400", got)
	}

	if got := status(call{
		method: "GET",
		path:   "/artifactory/other/libs/tmp/c.txt",
	}); got != 404 {
		t.Errorf("other/libs/tmp/c.txt = %d after a refused restore, want 404", got)
	}

	if got := status(call{
		method: "DELETE",
		path:   "/artifactory/api/trash/clean/libs/tmp/c.txt",
	}); got != 204 {
		t.Errorf("clean = %d", got)
	}

	status(call{
		method: "DELETE",
		path:   "/artifactory/other/restored/a.txt",
	})

	if got := status(call{
		method: "POST",
		path:   "/artifactory/api/trash/empty",
	}); got != 200 {
		t.Errorf("empty = %d", got)
	}

	if got := status(call{
		method: "GET",
		path:   "/artifactory/auto-trashcan/other/restored/a.txt",
	}); got != 404 {
		t.Errorf("after empty = %d, want 404", got)
	}

	status(call{
		method: "DELETE",
		path:   "/artifactory/other/restored/b.txt",
	})
	status(call{
		method: "DELETE",
		path:   "/artifactory/auto-trashcan/other/restored/b.txt",
	})

	if got := status(call{
		method: "GET",
		path:   "/artifactory/auto-trashcan/other/restored/b.txt",
	}); got != 404 {
		t.Errorf("deleted from the trash = %d, want 404", got)
	}
}

func TestRelicAPITrash(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/libs",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/libs/org/a.txt",
		body:   "aaa",
	})
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/libs/org/b.txt",
		body:   "bb",
	})

	admin := relicHTTP("admin", "password")

	arts := relicv1connect.NewArtifactServiceClient(admin, srv.URL+"/api")
	if _, err := arts.DeleteItem(
		ctx,
		connect.NewRequest(&relicv1.DeleteItemRequest{
			Repository: "libs",
			Path:       "org/a.txt",
		}),
	); err != nil {
		t.Fatal(err)
	}

	do(t, srv, call{
		method: "DELETE",
		path:   "/artifactory/libs/org/b.txt",
	})

	trash := relicv1connect.NewTrashServiceClient(admin, srv.URL+"/api")

	list, err := trash.ListTrash(ctx, connect.NewRequest(&relicv1.ListTrashRequest{}))
	if err != nil || len(list.Msg.GetEntries()) != 2 {
		t.Fatalf("trash = %v %v", list, err)
	}

	for _, e := range list.Msg.GetEntries() {
		if e.GetOriginalRepository() != "libs" || e.GetDeletedBy() != "admin" ||
			e.GetPath() != "libs/"+e.GetOriginalPath() {
			t.Errorf("entry = %v", e)
		}
	}

	listPage := func(r *relicv1.ListTrashRequest) (*relicv1.ListTrashResponse, error) {
		resp, err := trash.ListTrash(ctx, connect.NewRequest(r))
		if err != nil {
			return nil, err
		}

		return resp.Msg, nil
	}

	first, err := listPage(&relicv1.ListTrashRequest{
		PageSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	second, err := listPage(&relicv1.ListTrashRequest{
		Page:     2,
		PageSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	if first.GetTotal() != 2 || len(first.GetEntries()) != 1 || len(second.GetEntries()) != 1 ||
		first.GetEntries()[0].GetPath() == second.GetEntries()[0].GetPath() {
		t.Errorf("pages of one entry = %v and %v, want the two entries one by one", first, second)
	}

	if !slices.Equal(first.GetOriginalRepositories(), []string{
		"libs",
	}) || !slices.Equal(first.GetDeletedByUsers(), []string{
		"admin",
	}) {
		t.Errorf("filter values = %v and %v", first.GetOriginalRepositories(), first.GetDeletedByUsers())
	}

	filters := []struct {
		name  string
		req   *relicv1.ListTrashRequest
		paths []string
	}{
		{
			name: "query on the original path, in any case",
			req: &relicv1.ListTrashRequest{
				Query: "ORG/B.TXT",
			},
			paths: []string{
				"libs/org/b.txt",
			},
		},
		{
			name: "query on the deleter",
			req: &relicv1.ListTrashRequest{
				Query: "admi",
			},
			paths: []string{
				"libs/org/a.txt",
				"libs/org/b.txt",
			},
		},
		{
			name: "original repository",
			req: &relicv1.ListTrashRequest{
				OriginalRepository: "elsewhere",
			},
		},
		{
			name: "deleter",
			req: &relicv1.ListTrashRequest{
				DeletedBy: "admin",
			},
			paths: []string{
				"libs/org/a.txt",
				"libs/org/b.txt",
			},
		},
		{
			name: "folders",
			req: &relicv1.ListTrashRequest{
				Kind: "folder",
			},
		},
	}

	for _, tc := range filters {
		got, err := listPage(tc.req)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)

			continue
		}

		paths := make([]string, 0, len(got.GetEntries()))
		for _, e := range got.GetEntries() {
			paths = append(paths, e.GetPath())
		}

		slices.Sort(paths)

		if !slices.Equal(paths, tc.paths) || got.GetTotal() != int64(len(tc.paths)) {
			t.Errorf("%s: %v (total %d), want %v", tc.name, paths, got.GetTotal(), tc.paths)
		}
	}

	if _, err := listPage(&relicv1.ListTrashRequest{
		Kind: "link",
	}); code(err) != connect.CodeInvalidArgument {
		t.Errorf("an unknown kind: %v", err)
	}

	restore := func(r *relicv1.RestoreTrashRequest) error {
		_, err := trash.RestoreTrash(ctx, connect.NewRequest(r))
		return err
	}

	tests := []struct {
		name string
		err  error
		want connect.Code
	}{
		{
			"restore",
			restore(&relicv1.RestoreTrashRequest{
				Path: "libs/org/a.txt",
			}),
			0,
		},
		{
			"restore into a missing repository",
			restore(&relicv1.RestoreTrashRequest{
				Path:         "libs/org/b.txt",
				ToRepository: "nope",
				ToPath:       "x.txt",
			}),
			connect.CodeNotFound,
		},
		{
			"restore onto a repository root",
			restore(&relicv1.RestoreTrashRequest{
				Path:         "libs/org/b.txt",
				ToRepository: "libs",
			}),
			connect.CodeInvalidArgument,
		},
		{
			"a destination path without a repository",
			restore(&relicv1.RestoreTrashRequest{
				Path:   "libs/org/b.txt",
				ToPath: "x.txt",
			}),
			connect.CodeInvalidArgument,
		},
		{
			"delete for good",
			func() error {
				_, err := trash.DeleteTrashEntry(
					ctx,
					connect.NewRequest(&relicv1.DeleteTrashEntryRequest{
						Path: "libs/org/b.txt",
					}),
				)

				return err
			}(),
			0,
		},
		{
			"empty",
			func() error {
				_, err := trash.EmptyTrash(ctx, connect.NewRequest(&relicv1.EmptyTrashRequest{}))
				return err
			}(),
			0,
		},
	}
	for _, tc := range tests {
		if code(tc.err) != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, tc.err, tc.want)
		}
	}

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/libs/org/a.txt",
	}); resp.StatusCode != 200 {
		t.Errorf("restored file = %d", resp.StatusCode)
	}

	if list, err := trash.ListTrash(ctx, connect.NewRequest(&relicv1.ListTrashRequest{})); err != nil ||
		len(list.Msg.GetEntries()) != 0 {
		t.Errorf("trash after empty = %v %v", list, err)
	}
}
