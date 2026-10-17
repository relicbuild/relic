//go:build e2e

// Package sdk drives every REST endpoint the adapter serves with the client
// SDK, against a running Relic.
package sdk

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	buildinfo "github.com/jfrog/build-info-go/entities"
	"github.com/jfrog/jfrog-client-go/artifactory"
	rtauth "github.com/jfrog/jfrog-client-go/artifactory/auth"
	"github.com/jfrog/jfrog-client-go/artifactory/services"
	"github.com/jfrog/jfrog-client-go/artifactory/services/utils"
	"github.com/jfrog/jfrog-client-go/config"
	"github.com/jfrog/jfrog-client-go/utils/io/httputils"
)

type recorder struct {
	mu   sync.Mutex
	reqs []*http.Request
	next http.RoundTripper
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req.Clone(req.Context()))
	r.mu.Unlock()

	resp, err := r.next.RoundTrip(req)
	if os.Getenv("SDK_TRACE") != "" && err == nil {
		fmt.Fprintf(os.Stderr, "sdk: %s %s -> %d\n", req.Method, req.URL.RequestURI(), resp.StatusCode)
	}

	return resp, err
}

type suite struct {
	t       *testing.T
	sm      artifactory.ArtifactoryServicesManager
	rec     *recorder
	base    string
	details httputils.HttpClientDetails
	user    string
	run     string
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}

	return def
}

func newSuite(t *testing.T) *suite {
	t.Helper()

	base := strings.TrimSuffix(env("RELIC_SDK_URL", "http://127.0.0.1:8090"), "/") + "/artifactory/"
	user := env("RELIC_USER", "admin")
	details := rtauth.NewArtifactoryDetails()
	details.SetUrl(base)
	details.SetUser(user)
	details.SetPassword(env("RELIC_PASSWORD", "password"))

	rec := &recorder{
		next: http.DefaultTransport,
	}

	cfg, err := config.NewConfigBuilder().SetServiceDetails(details).SetHttpClient(&http.Client{
		Transport: rec,
	}).
		SetHttpRetries(0).SetOverallRequestTimeout(2 * time.Minute).Build()
	if err != nil {
		t.Fatal(err)
	}

	sm, err := artifactory.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	return &suite{
		t:       t,
		sm:      sm,
		rec:     rec,
		base:    base,
		details: details.CreateHttpClientDetails(),
		user:    user,
		run:     fmt.Sprintf("%d", time.Now().Unix()%100000),
	}
}

func (s *suite) raw(t *testing.T, method, path string, body []byte, want ...int) []byte {
	t.Helper()
	return s.rawAs(t, s.details.Clone(), method, path, body, want...)
}

func (s *suite) anonymous(t *testing.T, method, path string, body []byte, want ...int) []byte {
	t.Helper()

	d := s.details.Clone()
	d.User, d.Password, d.AccessToken = "", "", ""

	return s.rawAs(t, d, method, path, body, want...)
}

func (s *suite) rawAs(
	t *testing.T,
	d *httputils.HttpClientDetails,
	method, path string,
	body []byte,
	want ...int,
) []byte {
	t.Helper()

	url := s.base + strings.TrimPrefix(path, "/")

	if body != nil && d.Headers["Content-Type"] == "" {
		d.SetContentTypeApplicationJson()
	}

	var (
		resp *http.Response
		out  []byte
		err  error
	)

	c := s.sm.Client()

	switch method {
	case http.MethodGet:
		resp, out, _, err = c.SendGet(url, true, d) //nolint:bodyclose // the SDK reads and closes the body
	case http.MethodPost:
		resp, out, err = c.SendPost(url, body, d) //nolint:bodyclose // the SDK reads and closes the body
	case http.MethodPut:
		resp, out, err = c.SendPut(url, body, d) //nolint:bodyclose // the SDK reads and closes the body
	case http.MethodDelete:
		resp, out, err = c.SendDelete(url, body, d) //nolint:bodyclose // the SDK reads and closes the body
	default:
		t.Fatalf("method %s", method)
	}

	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}

	if !slices.Contains(want, resp.StatusCode) {
		t.Fatalf("%s %s = %d, want %v: %s", method, path, resp.StatusCode, want, out)
	}

	return out
}

func check(t *testing.T, what string, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

func TestArtifactoryREST(t *testing.T) {
	s := newSuite(t)
	local, target, remote, virtual := "sdk-"+s.run+"-local", "sdk-"+s.run+"-target", "sdk-"+s.run+"-remote", "sdk-"+s.run+"-virtual"
	build, user, group, perm := "sdk-build-"+s.run, "sdk-user-"+s.run, "sdk-group-"+s.run, "sdk-perm-"+s.run

	t.Run("system", func(t *testing.T) {
		out, err := s.sm.Ping()
		check(t, "ping", err)

		if strings.TrimSpace(string(out)) != "OK" {
			t.Errorf("ping = %q", out)
		}

		v, err := s.sm.GetVersion()
		check(t, "version", err)

		if v == "" {
			t.Error("empty version")
		}

		id, err := s.sm.GetServiceId()
		check(t, "service id", err)

		if !strings.HasPrefix(id, "jfrt@") {
			t.Errorf("service id = %q", id)
		}

		s.raw(
			t,
			http.MethodPost,
			"api/system/usage",
			[]byte(`{"productId":"sdk-test/1.0","features":[{"featureId":"test"}]}`),
			200,
			202,
		)
		s.raw(t, http.MethodGet, "api/v1/uploads/config", nil, 200)
	})

	t.Run("repositories", func(t *testing.T) {
		lp := services.NewGenericLocalRepositoryParams()
		lp.Key, lp.Description = local, "sdk local"
		check(t, "create local", s.sm.CreateLocalRepository().Generic(lp))
		lp.Key = target
		check(t, "create target", s.sm.CreateLocalRepository().Generic(lp))

		rp := services.NewGenericRemoteRepositoryParams()
		rp.Key, rp.Url = remote, "https://example.invalid/files"
		check(t, "create remote", s.sm.CreateRemoteRepository().Generic(rp))

		vp := services.NewGenericVirtualRepositoryParams()
		vp.Key, vp.Repositories, vp.DefaultDeploymentRepo = virtual, []string{
			local,
			remote,
		}, local
		check(t, "create virtual", s.sm.CreateVirtualRepository().Generic(vp))

		lp.Key, lp.Description = local, "sdk local, updated"
		check(t, "update local", s.sm.UpdateLocalRepository().Generic(lp))

		var got services.LocalRepositoryBaseParams
		check(t, "get local", s.sm.GetRepository(local, &got))

		if got.Key != local || got.Description != "sdk local, updated" || got.Rclass != "local" {
			t.Errorf("local = %+v", got)
		}

		var gotVirtual services.VirtualRepositoryBaseParams
		check(t, "get virtual", s.sm.GetRepository(virtual, &gotVirtual))

		if !slices.Equal(gotVirtual.Repositories, []string{
			local,
			remote,
		}) ||
			gotVirtual.DefaultDeploymentRepo != local {
			t.Errorf("virtual = %+v", gotVirtual)
		}

		all, err := s.sm.GetAllRepositories()
		check(t, "list", err)

		if !slices.ContainsFunc(
			*all,
			func(r services.RepositoryDetails) bool {
				return r.Key == remote && r.Type == "REMOTE"
			},
		) {
			t.Errorf("list misses %s", remote)
		}

		filtered, err := s.sm.GetAllRepositoriesFiltered(
			services.RepositoriesFilterParams{
				RepoType:    "local",
				PackageType: "generic",
			},
		)
		check(t, "list filtered", err)

		if slices.ContainsFunc(*filtered, func(r services.RepositoryDetails) bool {
			return r.Key == remote
		}) {
			t.Error("the local filter lists a remote")
		}

		exists, err := s.sm.IsRepoExists(virtual)
		check(t, "exists", err)

		if !exists {
			t.Error("virtual does not exist")
		}

		s.raw(t, http.MethodGet, "api/repositories/", nil, 200)

		batch := fmt.Sprintf(`[{"key":"sdk-%s-batch","rclass":"local","packageType":"generic"}]`, s.run)
		check(t, "batch create", s.sm.CreateUpdateRepositoriesInBatch([]byte(batch), false))
		batch = fmt.Sprintf(
			`[{"key":"sdk-%s-batch","rclass":"local","packageType":"generic","description":"batched"}]`,
			s.run,
		)
		check(t, "batch update", s.sm.CreateUpdateRepositoriesInBatch([]byte(batch), true))
		check(t, "delete batch repo", s.sm.DeleteRepository("sdk-"+s.run+"-batch"))
	})

	dir := t.TempDir()
	for name, content := range map[string]string{
		"a.txt":   "alpha",
		"b.txt":   "bravo",
		"big.bin": strings.Repeat("x", 20000),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("files", func(t *testing.T) {
		up := services.NewUploadParams()
		up.Pattern, up.Target, up.Flat = filepath.Join(dir, "*"), local+"/app/1.0/", true
		props := utils.NewProperties()
		props.AddProperty("stage", "dev")
		up.TargetProps = props
		up.MinChecksumDeploy = 10000
		n, failed, err := s.sm.UploadFiles(artifactory.UploadServiceOptions{
			FailFast: true,
		}, up)
		check(t, "upload", err)

		if n != 3 || failed != 0 {
			t.Fatalf("uploaded %d, failed %d", n, failed)
		}

		up.Target = target + "/copy-of-big/"

		up.Pattern = filepath.Join(dir, "big.bin")
		if n, _, err := s.sm.UploadFiles(artifactory.UploadServiceOptions{
			FailFast: true,
		}, up); err != nil || n != 1 {
			t.Fatalf("checksum deploy: %d %v", n, err)
		}

		info, err := s.sm.FileInfo(local + "/app/1.0/a.txt")
		check(t, "file info", err)

		if info.Checksums.Sha256 == "" || info.Size != "5" {
			t.Errorf("file info = %+v", info)
		}

		folder, err := s.sm.FolderInfo(local + "/app")
		check(t, "folder info", err)

		if len(folder.Children) != 1 {
			t.Errorf("folder = %+v", folder)
		}

		list, err := s.sm.FileList(local+"/app", utils.FileListParams{
			Deep:            true,
			IncludeRootPath: false,
		})
		check(t, "file list", err)

		if len(list.Files) != 3 {
			t.Errorf("list = %+v", list.Files)
		}

		sp := services.NewSearchParams()
		sp.Pattern = local + "/app/1.0/*.txt"
		reader, err := s.sm.SearchFiles(sp)
		check(t, "search", err)

		defer func() {
			_ = reader.Close()
		}()

		if l, _ := reader.Length(); l != 2 {
			t.Errorf("search found %d, want 2", l)
		}

		pp := services.NewPropsParams()

		pp.Reader, pp.Props = reader, "qa=passed;owner=sdk"
		if n, err := s.sm.SetProps(pp); err != nil || n != 2 {
			t.Fatalf("set props: %d %v", n, err)
		}

		props2, err := s.sm.GetItemProps(local + "/app/1.0/a.txt")
		check(t, "get props", err)

		if !slices.Equal(props2.Properties["qa"], []string{
			"passed",
		}) ||
			!slices.Equal(props2.Properties["stage"], []string{
				"dev",
			}) {
			t.Errorf("props = %v", props2.Properties)
		}

		reader.Reset()

		pp.Props = "owner"
		if n, err := s.sm.DeleteProps(pp); err != nil || n != 2 {
			t.Fatalf("delete props: %d %v", n, err)
		}

		rc, err := s.sm.Aql(
			fmt.Sprintf(`items.find({"repo":%q,"name":{"$match":"*.txt"}}).include("name","sha256")`, local),
		)
		check(t, "aql", err)

		body, _ := io.ReadAll(rc)
		_ = rc.Close()

		var aql struct {
			Results []struct {
				Name string
			} `json:"results"`
		}
		if json.Unmarshal(body, &aql) != nil || len(aql.Results) != 2 {
			t.Errorf("aql = %s", body)
		}

		dl := services.NewDownloadParams()

		dl.Pattern, dl.Target, dl.Flat = virtual+"/app/1.0/a.txt", filepath.Join(t.TempDir(), "dl")+"/", true
		if n, failed, err := s.sm.DownloadFiles(dl); err != nil || n != 1 || failed != 0 {
			t.Fatalf("download through the virtual: %d %d %v", n, failed, err)
		}

		if b, _ := os.ReadFile(filepath.Join(strings.TrimSuffix(dl.Target, "/"), "a.txt")); string(b) != "alpha" {
			t.Errorf("downloaded %q", b)
		}

		mc := services.NewMoveCopyParams()

		mc.Pattern, mc.Target, mc.Flat = local+"/app/1.0/a.txt", target+"/copied/", true
		if ok, failed, err := s.sm.Copy(mc); err != nil || ok != 1 || failed != 0 {
			t.Fatalf("copy: %d %d %v", ok, failed, err)
		}
		// A fresh value, so no query of the previous call is reused.
		mc = services.NewMoveCopyParams()

		mc.Pattern, mc.Target, mc.Flat = local+"/app/1.0/b.txt", target+"/moved/", true
		if ok, failed, err := s.sm.Move(mc); err != nil || ok != 1 || failed != 0 {
			t.Fatalf("move: %d %d %v", ok, failed, err)
		}

		dp := services.NewDeleteParams()
		dp.Pattern = target + "/copied/*"
		paths, err := s.sm.GetPathsToDelete(dp)
		check(t, "paths to delete", err)

		defer func() {
			_ = paths.Close()
		}()

		if n, err := s.sm.DeleteFiles(paths); err != nil || n != 1 {
			t.Fatalf("delete: %d %v", n, err)
		}

		var st struct {
			DownloadCount *int64 `json:"downloadCount"`
		}
		for range 40 {
			if json.Unmarshal(
				s.raw(t, http.MethodGet, "api/storage/"+local+"/app/1.0/a.txt?stats", nil, 200),
				&st,
			) == nil &&
				st.DownloadCount != nil &&
				*st.DownloadCount >= 1 {
				break
			}

			time.Sleep(500 * time.Millisecond)
		}

		if st.DownloadCount == nil || *st.DownloadCount < 1 {
			t.Errorf("stats after one download = %+v", st)
		}

		s.raw(t, http.MethodPut, "api/storage/"+local+"/app/1.0/a.txt?properties=note=raw", nil, 204)
		s.raw(t, http.MethodDelete, "api/storage/"+local+"/app/1.0/a.txt?properties=note", nil, 204)
		s.raw(t, http.MethodPost, "api/copy/"+local+"/app/1.0/a.txt?to=/"+target+"/raw/a.txt", nil, 200)
		s.raw(t, http.MethodPost, "api/move/"+target+"/raw/a.txt?to=/"+target+"/raw/moved.txt", nil, 200)
	})

	t.Run("trash", func(t *testing.T) {
		s.raw(t, http.MethodDelete, target+"/moved/b.txt", nil, 204)
		s.raw(t, http.MethodPost, "api/trash/restore/"+target+"/moved/b.txt", nil, 200, 202)
		s.raw(t, http.MethodDelete, target+"/moved/b.txt", nil, 204)
		s.raw(t, http.MethodDelete, "api/trash/clean/"+target+"/moved/b.txt", nil, 204, 200)
		// Emptying the trash is global, and the e2e scenarios running beside
		// this suite keep items there.
		s.anonymous(t, http.MethodPost, "api/trash/empty", nil, 401)
	})

	t.Run("builds", func(t *testing.T) {
		big, err := s.sm.FileInfo(local + "/app/1.0/big.bin")
		check(t, "big.bin info", err)

		for i, number := range []string{
			"1",
			"2",
			"3",
		} {
			started := time.Now().UTC().Add(time.Duration(i-3) * time.Minute).Format(buildinfo.TimeFormat)

			bi := &buildinfo.BuildInfo{
				Name:    build,
				Number:  number,
				Started: started,
				Agent: &buildinfo.Agent{
					Name:    "sdk",
					Version: "1",
				},
				Modules: []buildinfo.Module{
					{
						Id:   "app",
						Type: "generic",
						Artifacts: []buildinfo.Artifact{
							{
								Name:                   "big.bin",
								Path:                   "app/1.0/big.bin",
								OriginalDeploymentRepo: local,
								Checksum: buildinfo.Checksum{
									Sha1:   big.Checksums.Sha1,
									Md5:    big.Checksums.Md5,
									Sha256: big.Checksums.Sha256,
								},
							},
						},
					},
				},
			}
			if _, err := s.sm.PublishBuildInfo(bi, ""); err != nil {
				t.Fatalf("publish %s: %v", number, err)
			}
		}

		got, found, err := s.sm.GetBuildInfo(services.BuildInfoParams{
			BuildName:   build,
			BuildNumber: "2",
		})
		check(t, "get build", err)

		if !found || got.BuildInfo.Number != "2" {
			t.Errorf("build = %+v %v", got, found)
		}

		runs, found, err := s.sm.GetBuildRuns(services.BuildInfoParams{
			BuildName: build,
		})
		check(t, "build runs", err)

		if !found || len(runs.BuildsNumbers) != 3 {
			t.Errorf("runs = %+v", runs)
		}

		s.raw(t, http.MethodGet, "api/build", nil, 200)

		if out := s.raw(
			t,
			http.MethodGet,
			"api/builds/buildArtifacts/"+build+"/2/artifactory-build-info",
			nil,
			200,
		); !strings.Contains(string(out), "big.bin") {
			t.Errorf("build artifacts = %s", out)
		}

		check(
			t,
			"promote",
			s.sm.PromoteBuild(
				services.PromotionParams{
					BuildName:   build,
					BuildNumber: "2",
					TargetRepo:  target,
					Status:      "released",
					Copy:        true,
				},
			),
		)
		check(t, "discard", s.sm.DiscardBuilds(services.DiscardBuildsParams{
			BuildName: build,
			MaxBuilds: "2",
		}))

		if _, found, err := s.sm.GetBuildInfo(
			services.BuildInfoParams{
				BuildName:   build,
				BuildNumber: "1",
			},
		); err != nil ||
			found {
			t.Errorf("run 1 after keeping the newest 2: found %v, %v", found, err)
		}

		for _, kept := range []string{
			"2",
			"3",
		} {
			if _, found, err := s.sm.GetBuildInfo(
				services.BuildInfoParams{
					BuildName:   build,
					BuildNumber: kept,
				},
			); err != nil ||
				!found {
				t.Errorf("run %s after keeping the newest 2: found %v, %v", kept, found, err)
			}
		}

		check(t, "delete build", s.sm.DeleteBuildInfo(&buildinfo.BuildInfo{
			Name:   build,
			Number: "3",
		}, "", 1))
	})

	t.Run("security", func(t *testing.T) {
		yes := true
		up := services.NewUserParams()
		up.UserDetails = services.User{
			Name:             user,
			Email:            user + "@example.com",
			Password:         "Sdk-pass-12345",
			ProfileUpdatable: &yes,
		}
		check(t, "create user", s.sm.CreateUser(up))
		up.UserDetails.Email = user + "@example.org"
		check(t, "update user", s.sm.UpdateUser(up))
		u, err := s.sm.GetUser(services.UserParams{
			UserDetails: services.User{
				Name: user,
			},
		})
		check(t, "get user", err)

		if u == nil || u.Email != user+"@example.org" {
			t.Errorf("user = %+v", u)
		}

		users, err := s.sm.GetAllUsers()
		check(t, "list users", err)

		if !slices.ContainsFunc(users, func(x *services.User) bool {
			return x.Name == user
		}) {
			t.Error("user not listed")
		}

		gp := services.NewGroupParams()
		gp.GroupDetails = services.Group{
			Name:        group,
			Description: "sdk",
			UsersNames: []string{
				user,
			},
		}
		check(t, "create group", s.sm.CreateGroup(gp))
		gp.GroupDetails.Description = "sdk, updated"
		check(t, "update group", s.sm.UpdateGroup(gp))
		g, err := s.sm.GetGroup(services.GroupParams{
			GroupDetails: services.Group{
				Name: group,
			},
			IncludeUsers: true,
		})
		check(t, "get group", err)

		if g == nil || g.Description != "sdk, updated" || !slices.Contains(g.UsersNames, user) {
			t.Errorf("group = %+v", g)
		}

		groups, err := s.sm.GetAllGroups()
		check(t, "list groups", err)

		if !slices.Contains(*groups, group) {
			t.Error("group not listed")
		}

		pt := services.PermissionTargetParams{
			Name: perm,
			Repo: &services.PermissionTargetSection{
				Repositories: []string{
					local,
				},
				IncludePatterns: []string{
					"**",
				},
				Actions: &services.Actions{
					Groups: map[string][]string{
						group: {
							"read",
							"annotate",
						},
					},
				},
			},
		}
		check(t, "create permission", s.sm.CreatePermissionTarget(pt))
		pt.Repo.Actions.Users = map[string][]string{
			user: {
				"read",
				"write",
			},
		}
		check(t, "update permission", s.sm.UpdatePermissionTarget(pt))
		got, err := s.sm.GetPermissionTarget(perm)
		check(t, "get permission", err)

		if got == nil || !slices.Equal(got.Repo.Actions.Users[user], []string{
			"read",
			"write",
		}) {
			t.Errorf("permission = %+v", got)
		}

		all, err := s.sm.GetAllPermissionTargets()
		check(t, "list permissions", err)

		if !slices.ContainsFunc(*all, func(p services.PermissionTargetParams) bool {
			return p.Name == perm
		}) {
			t.Error("permission not listed")
		}

		s.raw(
			t,
			http.MethodPost,
			"api/v2/security/permissions/"+perm+"-raw",
			[]byte(`{"name":"`+perm+`-raw","repo":{"repositories":["ANY"],"actions":{"users":{"`+user+`":["read"]}}}}`),
			200,
			201,
		)
		s.raw(t, http.MethodDelete, "api/v2/security/permissions/"+perm+"-raw", nil, 200, 204)

		tok, err := s.sm.CreateToken(
			services.CreateTokenParams{
				Username:  user,
				Scope:     "member-of-groups:" + group,
				ExpiresIn: 600,
			},
		)
		check(t, "create token", err)

		if tok.AccessToken == "" {
			t.Error("no access token")
		}

		s.raw(t, http.MethodGet, "api/security/encryptedPassword", nil, 200)

		s.raw(t, http.MethodGet, "api/gpg/key/public", nil, 200)
		s.raw(t, http.MethodGet, "api/security/keypair/public/repositories/"+local, nil, 200, 404)
		s.raw(t, http.MethodGet, "api/security/keypair/relic-default-rsa/public", nil, 200, 404)
		s.raw(t, http.MethodGet, "api/security/keys/trusted", nil, 200)
		s.raw(
			t,
			http.MethodPost,
			"api/security/keys/trusted",
			[]byte(`{"alias":"sdk-`+s.run+`","public_key":"not a key"}`),
			400,
		)
		s.raw(t, http.MethodDelete, "api/security/keys/trusted/sdk-missing-"+s.run, nil, 400, 404)

		check(t, "delete permission", s.sm.DeletePermissionTarget(perm))
		check(t, "delete group", s.sm.DeleteGroup(group))
		check(t, "delete user", s.sm.DeleteUser(user))
	})

	t.Run("replication", func(t *testing.T) {
		p := utils.ReplicationParams{
			RepoKey:        local,
			Url:            "http://127.0.0.1:1/artifactory/" + target,
			Username:       "admin",
			Password:       "x",
			CronExp:        "0 0 12 * * ?",
			Enabled:        false,
			SyncProperties: true,
		}
		check(t, "create replication", s.sm.CreateReplication(services.CreateReplicationParams{
			ReplicationParams: p,
		}))
		p.SyncDeletes = true
		check(t, "update replication", s.sm.UpdateReplication(services.UpdateReplicationParams{
			ReplicationParams: p,
		}))
		got, err := s.sm.GetReplication(local)
		check(t, "get replication", err)

		if len(got) != 1 || !got[0].SyncDeletes {
			t.Errorf("replication = %+v", got)
		}

		s.raw(t, http.MethodGet, "api/replication/"+local, nil, 200)
		s.raw(t, http.MethodPost, "api/replication/execute/"+local, nil, 200, 202, 400)
		check(t, "delete replication", s.sm.DeleteReplication(local))
	})

	t.Run("package endpoints", func(t *testing.T) {
		s.raw(t, http.MethodGet, "api/npm/auth", nil, 200)
		s.raw(
			t,
			http.MethodPut,
			"api/npm/"+local+"/-/user/org.couchdb.user:"+s.user,
			[]byte(`{"name":"`+s.user+`","password":"`+env("RELIC_PASSWORD", "password")+`"}`),
			200,
			201,
			400,
		)
		s.raw(t, http.MethodGet, "api/conan/"+local+"/v1/ping", nil, 200, 404)
		s.raw(t, http.MethodGet, "api/terraform/"+local+"/", nil, 200, 400, 404)
		s.raw(t, http.MethodGet, "api/terraform/v1/modules/"+local+"/", nil, 200, 400, 404)
		s.raw(t, http.MethodGet, "api/terraform/archive/"+local+"/x", nil, 403)
		s.raw(t, http.MethodGet, "api/terraform/provider-archive/"+local+"/x", nil, 403)
		s.raw(t, http.MethodPut, "api/huggingfaceml/"+local+"/lfs/"+strings.Repeat("0", 64), []byte("x"), 403)
		s.raw(t, http.MethodPost, "api/release/import/", []byte("not a bundle"), 400, 415)
	})

	t.Run("cleanup", func(t *testing.T) {
		for _, k := range []string{
			virtual,
			remote,
			target,
			local,
		} {
			check(t, "delete "+k, s.sm.DeleteRepository(k))
		}

		if ok, err := s.sm.IsRepoExists(local); err != nil || ok {
			t.Errorf("%s still exists: %v", local, err)
		}
	})

	t.Run("coverage", func(t *testing.T) {
		s.checkCoverage(t)
	})
}

func (s *suite) checkCoverage(t *testing.T) {
	routes, err := artifactoryRoutes()
	if err != nil {
		t.Fatal(err)
	}

	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()

	missing := uncovered(routes, s.rec.reqs)
	for _, r := range missing {
		t.Errorf("not exercised: %s", r)
	}

	t.Logf(
		"%d Artifactory REST routes, %d requests, %d not exercised, %d excluded",
		len(routes),
		len(s.rec.reqs),
		len(missing),
		len(excluded),
	)
}
