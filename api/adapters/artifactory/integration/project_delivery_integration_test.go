//go:build integration

package integration

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func TestProjectRolesOnBuildsAndReleases(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()

	const password = "Delivery-pass-12345"

	admin := apiClient(relicHTTP("admin", "password"), srv.URL)
	sec := relicv1connect.NewSecurityServiceClient(admin)

	for _, name := range []string{
		"paula",
		"dave",
		"olga",
		"mallory",
	} {
		if _, err := sec.PutUser(ctx, &relicv1.PutUserRequest{
			User: &relicv1.User{
				Name:     name,
				Email:    name + "@example.com",
				Password: password,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := relicv1connect.NewProjectServiceClient(admin).PutProject(ctx, &relicv1.PutProjectRequest{
		Project: &relicv1.Project{
			Key:  "acme",
			Name: "Acme",
			Members: []*relicv1.ProjectMember{
				{
					Name: "paula",
					Role: relicv1.ProjectRole_PROJECT_ROLE_ADMIN,
				},
				{
					Name: "dave",
					Role: relicv1.ProjectRole_PROJECT_ROLE_DEVELOPER,
				},
				{
					Name: "olga",
					Role: relicv1.ProjectRole_PROJECT_ROLE_VIEWER,
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	for _, repo := range []*relicv1.Repository{
		{
			Name:    "acme-libs",
			Class:   relicv1.RepositoryClass_REPOSITORY_CLASS_LOCAL,
			Format:  relicv1.Format_FORMAT_GENERIC,
			Project: "acme",
		},
		{
			Name:    "acme-prod",
			Class:   relicv1.RepositoryClass_REPOSITORY_CLASS_LOCAL,
			Format:  relicv1.Format_FORMAT_GENERIC,
			Project: "acme",
			Environments: []string{
				"PROD",
			},
		},
	} {
		if _, err := relicv1connect.NewRepositoryServiceClient(admin).CreateRepository(
			ctx,
			&relicv1.CreateRepositoryRequest{
				Repository: repo,
			},
		); err != nil {
			t.Fatal(err)
		}
	}

	if resp, body := do(t, srv, call{
		method: http.MethodPut,
		path:   "/artifactory/acme-libs/app.bin",
		body:   "binary v1",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("deploy = %d %s", resp.StatusCode, body)
	}

	run := func(number string) *relicv1.PublishBuildRunRequest {
		return &relicv1.PublishBuildRunRequest{
			Project: "acme",
			Run: &relicv1.PublishedBuildRun{
				Name:    "acmeb",
				Number:  number,
				Started: timestamppb.New(time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)),
			},
		}
	}
	spec := func(version string) *relicv1.CreateReleaseRequest {
		return &relicv1.CreateReleaseRequest{
			Project: "acme",
			Spec: &relicv1.ReleaseSpec{
				Name:    "rel",
				Version: version,
				Sources: []*relicv1.ReleaseSource{
					{
						Files: []*relicv1.ReleaseFileRef{
							{
								Path: "acme-libs/app.bin",
							},
						},
					},
				},
			},
		}
	}

	if _, err := relicv1connect.NewBuildServiceClient(admin).PublishBuildRun(ctx, run("1")); err != nil {
		t.Fatal(err)
	}

	if _, err := relicv1connect.NewReleaseServiceClient(admin).CreateRelease(ctx, spec("0")); err != nil {
		t.Fatal(err)
	}

	const denied, hidden = connect.CodePermissionDenied, connect.CodeNotFound

	tests := []struct {
		user           string
		lists          bool
		read           connect.Code
		publish        connect.Code
		promoteBuild   connect.Code
		deleteRun      connect.Code
		createRelease  connect.Code
		promoteRelease connect.Code
		deleteRelease  connect.Code
	}{
		{
			user:  "paula",
			lists: true,
		},
		{
			user:           "dave",
			lists:          true,
			promoteRelease: denied,
		},
		{
			user:           "olga",
			lists:          true,
			publish:        denied,
			promoteBuild:   denied,
			deleteRun:      denied,
			createRelease:  denied,
			promoteRelease: denied,
			deleteRelease:  denied,
		},
		{
			user:           "mallory",
			read:           hidden,
			publish:        denied,
			promoteBuild:   hidden,
			deleteRun:      hidden,
			createRelease:  hidden,
			promoteRelease: hidden,
			deleteRelease:  hidden,
		},
	}
	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			client := apiClient(relicHTTP(tt.user, password), srv.URL)
			builds := relicv1connect.NewBuildServiceClient(client)
			releases := relicv1connect.NewReleaseServiceClient(client)

			check := func(action string, err error, want connect.Code) {
				t.Helper()

				if code(err) != want {
					t.Errorf("%s: %v, want code %v", action, err, want)
				}
			}

			listed, err := builds.ListBuilds(ctx, &relicv1.ListBuildsRequest{
				Project: "acme",
			})
			if err != nil {
				t.Fatal(err)
			}

			names := []string{}
			for _, b := range listed.GetBuilds() {
				names = append(names, b.GetName())
			}

			if got := slices.Contains(names, "acmeb"); got != tt.lists {
				t.Errorf("list builds = %v, want acmeb listed %v", names, tt.lists)
			}

			_, err = builds.GetBuildRun(ctx, &relicv1.GetBuildRunRequest{
				Project: "acme",
				Name:    "acmeb",
				Number:  "1",
			})
			check("read a build run", err, tt.read)

			_, err = releases.GetRelease(ctx, &relicv1.GetReleaseRequest{
				Project: "acme",
				Name:    "rel",
				Version: "0",
			})
			check("read a release", err, tt.read)

			_, err = builds.PublishBuildRun(ctx, run(tt.user))
			check("publish a build run", err, tt.publish)

			_, err = builds.PromoteBuildRun(ctx, &relicv1.PromoteBuildRunRequest{
				Project: "acme",
				Name:    "acmeb",
				Number:  "1",
				Status:  "tested by " + tt.user,
			})
			check("promote a build run", err, tt.promoteBuild)

			_, err = builds.DeleteBuildRuns(ctx, &relicv1.DeleteBuildRunsRequest{
				Project: "acme",
				Name:    "acmeb",
				Numbers: []string{
					tt.user,
				},
			})
			check("delete a build run", err, tt.deleteRun)

			_, err = releases.CreateRelease(ctx, spec(tt.user))
			check("create a release version", err, tt.createRelease)

			_, err = releases.PromoteRelease(ctx, &relicv1.PromoteReleaseRequest{
				Project:     "acme",
				Name:        "rel",
				Version:     "0",
				Environment: "PROD",
			})
			check("promote a release version", err, tt.promoteRelease)

			_, err = releases.DeleteRelease(ctx, &relicv1.DeleteReleaseRequest{
				Project: "acme",
				Name:    "rel",
				Version: tt.user,
			})
			check("delete a release version", err, tt.deleteRelease)
		})
	}

	const lifecycle = "/lifecycle/api/v2/"

	jfrogTests := []struct {
		name   string
		user   string
		method string
		path   string
		body   string
		want   int
	}{
		{
			name:   "a developer publishes a build with jf",
			user:   "dave",
			method: http.MethodPut,
			path:   "/artifactory/api/build?project=acme",
			body:   `{"version":"1.0.1","name":"acmeb","number":"jf-dave","started":"2026-10-24T10:00:00.000+0000"}`,
			want:   http.StatusNoContent,
		},
		{
			name:   "a viewer publishes a build with jf",
			user:   "olga",
			method: http.MethodPut,
			path:   "/artifactory/api/build?project=acme",
			body:   `{"version":"1.0.1","name":"acmeb","number":"jf-olga","started":"2026-10-24T10:00:00.000+0000"}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "a developer creates a release bundle",
			user:   "dave",
			method: http.MethodPost,
			path:   lifecycle + "release_bundle?project=acme&async=false",
			body: `{"release_bundle_name":"rel","release_bundle_version":"jf-1","source_type":"artifacts",` +
				`"source":{"artifacts":[{"path":"acme-libs/app.bin"}]}}`,
			want: http.StatusCreated,
		},
		{
			name:   "a developer promotes a release bundle",
			user:   "dave",
			method: http.MethodPost,
			path:   lifecycle + "promotion/records/rel/jf-1?project=acme&async=false",
			body:   `{"environment":"PROD"}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "a project admin promotes a release bundle",
			user:   "paula",
			method: http.MethodPost,
			path:   lifecycle + "promotion/records/rel/jf-1?project=acme&async=false",
			body:   `{"environment":"PROD"}`,
			want:   http.StatusCreated,
		},
	}
	for _, tt := range jfrogTests {
		resp, body := do(t, srv, call{
			method:  tt.method,
			path:    tt.path,
			body:    tt.body,
			noAuth:  true,
			headers: basicAuthHeader(tt.user, password),
		})
		if resp.StatusCode != tt.want {
			t.Errorf("%s = %d %s, want %d", tt.name, resp.StatusCode, body, tt.want)
		}
	}
}
