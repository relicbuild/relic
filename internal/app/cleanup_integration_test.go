//go:build integration

package app

import (
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func TestCleanupPreviewShowsViewersOnlyWhatTheyMayRead(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	srv := server(t)

	createRepository(t, srv, "preview-secrets", "LOCAL", "GENERIC", "")
	upload(t, srv, "preview-secrets", "c/a.txt", "first")
	time.Sleep(5 * time.Millisecond)
	upload(t, srv, "preview-secrets", "c/b.txt", "second")
	upload(t, srv, "preview-secrets", "d/gone.txt", "deleted")

	artifacts := relicv1connect.NewArtifactServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	if _, err := artifacts.DeleteItem(ctx, &relicv1.DeleteItemRequest{
		Repository: "preview-secrets",
		Path:       "d/gone.txt",
	}); err != nil {
		t.Fatal(err)
	}

	security := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	if _, err := security.PutUser(ctx, &relicv1.PutUserRequest{
		User: &relicv1.User{
			Name:     "vera",
			Email:    "vera@example.com",
			Password: "Vera-pass-1",
			Viewer:   true,
		},
	}); err != nil {
		t.Fatal(err)
	}

	admin := relicv1connect.NewCleanupServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	viewer := relicv1connect.NewCleanupServiceClient(apiClient(relicHTTP("vera", "Vera-pass-1"), srv.URL))

	policy := &relicv1.CleanupPolicy{
		Name: "keep-one",
		Repositories: []string{
			"preview-secrets",
		},
		PathPattern: "**/*.txt",
		KeepLatest:  1,
	}
	if _, err := admin.PutCleanupPolicy(ctx, &relicv1.PutCleanupPolicyRequest{
		Policy: policy,
	}); err != nil {
		t.Fatal(err)
	}

	byName := &relicv1.PreviewCleanupPolicyRequest{
		Name: "keep-one",
	}
	inline := &relicv1.PreviewCleanupPolicyRequest{
		Policy: policy,
	}

	for _, tc := range []struct {
		name   string
		client relicv1connect.CleanupServiceClient
		req    *relicv1.PreviewCleanupPolicyRequest
		want   int
	}{
		{
			name:   "admin, stored policy",
			client: admin,
			req:    byName,
			want:   1,
		},
		{
			name:   "admin, inline policy",
			client: admin,
			req:    inline,
			want:   1,
		},
		{
			name:   "viewer, stored policy",
			client: viewer,
			req:    byName,
			want:   0,
		},
		{
			name:   "viewer, inline policy",
			client: viewer,
			req:    inline,
			want:   0,
		},
	} {
		res, err := tc.client.PreviewCleanupPolicy(ctx, tc.req)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		if got := len(res.GetCandidates()); got != tc.want {
			t.Errorf("%s: %d candidates %v, want %d", tc.name, got, res.GetCandidates(), tc.want)
		}
	}

	for name, rejected := range map[string]*relicv1.CleanupPolicy{
		"the trash": {
			Repositories: []string{
				"auto-trashcan",
			},
			KeepLatest: 1,
		},
		"no criteria": {
			Repositories: []string{
				"preview-secrets",
			},
			PathPattern: "**/*.txt",
		},
	} {
		_, err := viewer.PreviewCleanupPolicy(ctx, &relicv1.PreviewCleanupPolicyRequest{
			Policy: rejected,
		})
		if code(err) != connect.CodeInvalidArgument {
			t.Errorf("preview of %s = %v, want invalid_argument", name, err)
		}
	}
}
