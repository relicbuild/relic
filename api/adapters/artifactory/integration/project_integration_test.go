//go:build integration

package integration

import (
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"

	"connectrpc.com/connect"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func TestProjects(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()

	const password = "Project-pass-12345"

	sec := relicv1connect.NewSecurityServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")
	for _, name := range []string{
		"pa-lead",
		"pa-dev",
		"pa-tester",
		"pa-stranger",
	} {
		if _, err := sec.PutUser(ctx, connect.NewRequest(&relicv1.PutUserRequest{
			User: &relicv1.User{
				Name:     name,
				Email:    name + "@example.com",
				Password: password,
			},
		})); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := sec.PutGroup(ctx, connect.NewRequest(&relicv1.PutGroupRequest{
		Group: &relicv1.Group{
			Name: "pa-qa",
			Members: []string{
				"pa-tester",
			},
		},
	})); err != nil {
		t.Fatal(err)
	}

	projects := func(user string) relicv1connect.ProjectServiceClient {
		pw := password
		if user == "admin" {
			pw = "password"
		}

		return relicv1connect.NewProjectServiceClient(relicHTTP(user, pw), srv.URL+"/api")
	}
	repos := func(user string) relicv1connect.RepositoryServiceClient {
		return relicv1connect.NewRepositoryServiceClient(relicHTTP(user, password), srv.URL+"/api")
	}

	if _, err := projects("admin").PutProject(ctx, connect.NewRequest(&relicv1.PutProjectRequest{
		Project: &relicv1.Project{
			Key: "Bad-Key",
		},
	})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("an invalid key: %v", err)
	}

	if _, err := projects("pa-lead").PutProject(ctx, connect.NewRequest(&relicv1.PutProjectRequest{
		Project: &relicv1.Project{
			Key: "acme",
		},
	})); code(err) != connect.CodePermissionDenied {
		t.Errorf("a user creating a project: %v", err)
	}

	if _, err := projects("admin").PutProject(ctx, connect.NewRequest(&relicv1.PutProjectRequest{
		Project: &relicv1.Project{
			Key:               "acme",
			Name:              "Acme",
			StorageQuotaBytes: 10,
			Members: []*relicv1.ProjectMember{
				{
					Name: "pa-lead",
					Role: relicv1.ProjectRole_PROJECT_ROLE_ADMIN,
				},
				{
					Name: "pa-dev",
					Role: relicv1.ProjectRole_PROJECT_ROLE_DEVELOPER,
				},
				{
					Name:  "pa-qa",
					Group: true,
					Role:  relicv1.ProjectRole_PROJECT_ROLE_VIEWER,
				},
			},
		},
	})); err != nil {
		t.Fatal(err)
	}

	newRepo := func(name, project string) *connect.Request[relicv1.CreateRepositoryRequest] {
		return connect.NewRequest(&relicv1.CreateRepositoryRequest{
			Repository: &relicv1.Repository{
				Name:    name,
				Class:   relicv1.RepositoryClass_REPOSITORY_CLASS_LOCAL,
				Format:  relicv1.Format_FORMAT_GENERIC,
				Project: project,
			},
		})
	}

	if _, err := repos(
		"pa-dev",
	).CreateRepository(ctx, newRepo("acme-dev", "acme")); code(
		err,
	) != connect.CodePermissionDenied {
		t.Errorf("a developer creating a repository: %v", err)
	}

	if _, err := repos(
		"pa-lead",
	).CreateRepository(ctx, newRepo("outside", "")); code(
		err,
	) != connect.CodePermissionDenied {
		t.Errorf("a project admin creating a repository outside the project: %v", err)
	}

	if _, err := repos("pa-lead").CreateRepository(ctx, newRepo("acme-libs", "acme")); err != nil {
		t.Fatalf("a project admin creating a repository in the project: %v", err)
	}

	file := func(user, method, path, body string) int {
		auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
		resp, _ := do(t, srv, call{
			method: method,
			path:   "/api/v1/files/acme-libs/" + path,
			body:   body,
			noAuth: true,
			headers: map[string]string{
				"Authorization": "Basic " + auth,
			},
		})

		return resp.StatusCode
	}

	if got := file("pa-dev", http.MethodPut, "a.txt", "12345"); got != http.StatusCreated {
		t.Errorf("a developer deploying = %d", got)
	}

	if got := file("pa-tester", http.MethodPut, "b.txt", "1"); got != http.StatusForbidden {
		t.Errorf("a viewer (through a group) deploying = %d", got)
	}

	if got := file("pa-tester", http.MethodGet, "a.txt", ""); got != http.StatusOK {
		t.Errorf("a viewer reading = %d", got)
	}

	if got := file("pa-stranger", http.MethodGet, "a.txt", ""); got != http.StatusForbidden &&
		got != http.StatusNotFound {
		t.Errorf("a stranger reading = %d", got)
	}

	if got := file("pa-dev", http.MethodPut, "c.txt", "12345678"); got != http.StatusRequestEntityTooLarge {
		t.Errorf("a deploy past the project's quota = %d, want 413", got)
	}

	if got := file("pa-dev", http.MethodGet, "c.txt", ""); got != http.StatusNotFound {
		t.Errorf("the refused file = %d, want not found", got)
	}

	if _, err := projects("pa-dev").SetProjectMembers(ctx, connect.NewRequest(&relicv1.SetProjectMembersRequest{
		Key: "acme",
	})); code(err) != connect.CodePermissionDenied {
		t.Errorf("a developer changing members: %v", err)
	}

	got, err := projects("pa-lead").SetProjectMembers(ctx, connect.NewRequest(&relicv1.SetProjectMembersRequest{
		Key: "acme",
		Members: []*relicv1.ProjectMember{
			{
				Name: "pa-lead",
				Role: relicv1.ProjectRole_PROJECT_ROLE_ADMIN,
			},
			{
				Name: "pa-dev",
				Role: relicv1.ProjectRole_PROJECT_ROLE_DEVELOPER,
			},
			{
				Name: "pa-stranger",
				Role: relicv1.ProjectRole_PROJECT_ROLE_VIEWER,
			},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}

	p := got.Msg.GetProject()
	if p.GetUsedBytes() != 5 || !slices.Equal(p.GetRepositories(), []string{
		"acme-libs",
	}) || len(p.GetMembers()) != 3 {
		t.Errorf("project = %v", p)
	}

	if _, err := projects("pa-dev").AddProjectMember(ctx, connect.NewRequest(&relicv1.AddProjectMemberRequest{
		Key: "acme",
		Member: &relicv1.ProjectMember{
			Name: "pa-dev",
			Role: relicv1.ProjectRole_PROJECT_ROLE_ADMIN,
		},
	})); code(err) != connect.CodePermissionDenied {
		t.Errorf("a developer adding a member: %v", err)
	}

	if _, err := projects("pa-dev").RemoveProjectMember(ctx, connect.NewRequest(&relicv1.RemoveProjectMemberRequest{
		Key:  "acme",
		Name: "pa-lead",
	})); code(err) != connect.CodePermissionDenied {
		t.Errorf("a developer removing a member: %v", err)
	}

	extras := []string{
		"pa-extra-1",
		"pa-extra-2",
		"pa-extra-3",
		"pa-extra-4",
		"pa-extra-5",
		"pa-extra-6",
		"pa-extra-7",
		"pa-extra-8",
	}
	members := func() []string {
		got, err := projects("pa-lead").GetProject(ctx, connect.NewRequest(&relicv1.GetProjectRequest{
			Key: "acme",
		}))
		if err != nil {
			t.Fatal(err)
		}

		names := make([]string, 0, len(got.Msg.GetProject().GetMembers()))
		for _, m := range got.Msg.GetProject().GetMembers() {
			names = append(names, m.GetName())
		}

		slices.Sort(names)

		return names
	}
	changeConcurrently := func(change func(name string) error) {
		var wg sync.WaitGroup

		errs := make([]error, len(extras))
		for i, name := range extras {
			wg.Go(func() {
				errs[i] = change(name)
			})
		}

		wg.Wait()

		if err := errors.Join(errs...); err != nil {
			t.Fatal(err)
		}
	}

	changeConcurrently(func(name string) error {
		_, err := projects("pa-lead").AddProjectMember(ctx, connect.NewRequest(&relicv1.AddProjectMemberRequest{
			Key: "acme",
			Member: &relicv1.ProjectMember{
				Name: name,
				Role: relicv1.ProjectRole_PROJECT_ROLE_VIEWER,
			},
		}))

		return err
	})

	want := slices.Sorted(slices.Values(append([]string{
		"pa-dev",
		"pa-lead",
		"pa-stranger",
	}, extras...)))
	if got := members(); !slices.Equal(got, want) {
		t.Errorf("members after concurrent adds = %v, want %v", got, want)
	}

	changeConcurrently(func(name string) error {
		_, err := projects("pa-lead").RemoveProjectMember(ctx, connect.NewRequest(&relicv1.RemoveProjectMemberRequest{
			Key:  "acme",
			Name: name,
		}))

		return err
	})

	if got := members(); !slices.Equal(got, []string{
		"pa-dev",
		"pa-lead",
		"pa-stranger",
	}) {
		t.Errorf("members after concurrent removals = %v", got)
	}

	if got := file("pa-stranger", http.MethodGet, "a.txt", ""); got != http.StatusOK {
		t.Errorf("a new viewer reading = %d", got)
	}

	if got := file("pa-tester", http.MethodGet, "a.txt", ""); got == http.StatusOK {
		t.Errorf("a removed member still reads")
	}

	list, err := projects("pa-dev").ListProjects(ctx, connect.NewRequest(&relicv1.ListProjectsRequest{}))
	if err != nil || len(list.Msg.GetProjects()) != 1 {
		t.Errorf("a member's projects = %v, %v", list, err)
	}

	list, err = projects("pa-tester").ListProjects(ctx, connect.NewRequest(&relicv1.ListProjectsRequest{}))
	if err != nil || len(list.Msg.GetProjects()) != 0 {
		t.Errorf("a non-member's projects = %v, %v", list, err)
	}

	if _, err := repos("pa-lead").DeleteRepository(ctx, connect.NewRequest(&relicv1.DeleteRepositoryRequest{
		Name: "acme-libs",
	})); err != nil {
		t.Errorf("a project admin deleting the project's repository: %v", err)
	}
}
