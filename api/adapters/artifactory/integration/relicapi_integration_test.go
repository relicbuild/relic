//go:build integration

package integration

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestRelicAPISystem(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/security/users/dev",
			body:   `{"email":"dev@example.com","password":"Dev-pass-12345"}`,
		},
	)
	anon := relicv1connect.NewSystemServiceClient(relicHTTP("", ""), srv.URL+"/api")
	admin := relicv1connect.NewSystemServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")
	dev := relicv1connect.NewSystemServiceClient(relicHTTP("dev", "Dev-pass-12345"), srv.URL+"/api")
	ctx := t.Context()

	v, err := anon.GetVersion(ctx, connect.NewRequest(&relicv1.GetVersionRequest{}))
	if err != nil || v.Msg.GetVersion() == "" {
		t.Fatalf("version = %v %v", v, err)
	}

	if _, err := anon.GetSystemStatus(
		ctx,
		connect.NewRequest(&relicv1.GetSystemStatusRequest{}),
	); code(err) != connect.CodeUnauthenticated {
		t.Errorf("anonymous status: %v", err)
	}

	if _, err := dev.GetSystemStatus(
		ctx,
		connect.NewRequest(&relicv1.GetSystemStatusRequest{}),
	); code(err) != connect.CodePermissionDenied {
		t.Errorf("non-admin status: %v", err)
	}

	st, err := admin.GetSystemStatus(ctx, connect.NewRequest(&relicv1.GetSystemStatusRequest{}))
	if err != nil || st.Msg.GetStorage() == nil || len(st.Msg.GetChecks()) == 0 || st.Msg.GetScanner() == nil {
		t.Fatalf("status = %v %v", st, err)
	}

	bad := relicv1connect.NewSystemServiceClient(relicHTTP("admin", "wrong"), srv.URL+"/api")
	if _, err := bad.GetVersion(
		ctx,
		connect.NewRequest(&relicv1.GetVersionRequest{}),
	); code(err) != connect.CodeUnauthenticated {
		t.Errorf("bad credentials: %v", err)
	}

	resp, body := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/api/relic.v1.SystemService/GetVersion",
			body:   `{}`,
			headers: map[string]string{
				"Content-Type": "application/json",
			},
		},
	)
	if resp.StatusCode != 200 || !strings.Contains(body, `"version"`) {
		t.Errorf("JSON GetVersion = %d %s", resp.StatusCode, body)
	}
}

func TestRelicAPISession(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()
	jar := &cookieJar{}
	client := &http.Client{
		Transport: jar,
	}
	sessions := relicv1connect.NewSessionServiceClient(client, srv.URL+"/api")

	login := connect.NewRequest(&relicv1.LoginRequest{
		Username: "admin",
		Password: "password",
	})
	if _, err := sessions.Login(ctx, login); code(err) != connect.CodePermissionDenied {
		t.Fatalf("login without the UI header: %v", err)
	}

	login.Header().Set("X-Relic-UI", "1")

	if _, err := sessions.Login(
		ctx,
		connect.NewRequest(&relicv1.LoginRequest{
			Username: "admin",
			Password: "nope",
		}),
	); code(err) != connect.CodePermissionDenied {
		t.Fatalf("bad password without header: %v", err)
	}

	bad := connect.NewRequest(&relicv1.LoginRequest{
		Username: "admin",
		Password: "nope",
	})
	bad.Header().Set("X-Relic-UI", "1")

	if _, err := sessions.Login(ctx, bad); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("bad password: %v", err)
	}

	resp, err := sessions.Login(ctx, login)
	if err != nil || resp.Msg.GetPrincipal().GetUsername() != "admin" || jar.cookie == "" {
		t.Fatalf("login = %v %v, cookie %q", resp, err, jar.cookie)
	}

	me := func(header bool) (*connect.Response[relicv1.GetCurrentUserResponse], error) {
		req := connect.NewRequest(&relicv1.GetCurrentUserRequest{})
		if header {
			req.Header().Set("X-Relic-UI", "1")
		}

		return sessions.GetCurrentUser(ctx, req)
	}
	if got, err := me(true); err != nil || !got.Msg.GetPrincipal().GetAdmin() {
		t.Errorf("me = %v %v", got, err)
	}

	if _, err := me(false); code(err) != connect.CodeUnauthenticated {
		t.Errorf("cookie without the UI header: %v", err)
	}

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
		path:   "/artifactory/libs/a.txt",
		body:   "alpha",
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/files/libs/a.txt", nil)
	//nolint:gosec // G124: a request cookie, which carries only its name and value
	req.AddCookie(&http.Cookie{
		Name:  "relic_session",
		Value: jar.cookie,
	})

	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
		t.Errorf("download with the session cookie = %v %v", resp, err)
	} else {
		_ = resp.Body.Close()
	}

	upload := func(header bool) int {
		req, _ := http.NewRequestWithContext(
			ctx,
			http.MethodPut,
			srv.URL+"/api/v1/files/libs/dropped/b.txt",
			strings.NewReader("bravo"),
		)
		//nolint:gosec // G124: a request cookie, which carries only its name and value
		req.AddCookie(&http.Cookie{
			Name:  "relic_session",
			Value: jar.cookie,
		})

		if header {
			req.Header.Set("X-Relic-UI", "1")
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		_ = resp.Body.Close()

		return resp.StatusCode
	}
	if got := upload(false); got != http.StatusUnauthorized {
		t.Errorf("cookie upload without the UI header = %d, want 401", got)
	}

	if got := upload(true); got != http.StatusCreated {
		t.Errorf("cookie upload = %d, want 201", got)
	}

	if _, body := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/libs/dropped/b.txt",
	}); body != "bravo" {
		t.Errorf("uploaded file = %q", body)
	}

	out := connect.NewRequest(&relicv1.LogoutRequest{})
	out.Header().Set("X-Relic-UI", "1")

	cookie := jar.cookie

	if _, err := sessions.Logout(ctx, out); err != nil {
		t.Fatal(err)
	}

	jar.cookie = cookie

	if _, err := me(true); code(err) != connect.CodeUnauthenticated {
		t.Errorf("after logout: %v", err)
	}
}

type cookieJar struct {
	cookie string
}

func (j *cookieJar) RoundTrip(r *http.Request) (*http.Response, error) {
	if j.cookie != "" {
		r = r.Clone(r.Context())
		//nolint:gosec // G124: a request cookie, which carries only its name and value
		r.AddCookie(&http.Cookie{
			Name:  "relic_session",
			Value: j.cookie,
		})
	}

	resp, err := http.DefaultTransport.RoundTrip(r)
	if err == nil {
		for _, c := range resp.Cookies() {
			if c.Name == "relic_session" {
				j.cookie = c.Value
			}
		}
	}

	return resp, err
}

func TestRelicAPIRepositories(t *testing.T) {
	t.Parallel()
	srv, db := serverDB(t)
	ctx := t.Context()
	repos := relicv1connect.NewRepositoryServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")
	create := func(r *relicv1.Repository) error {
		_, err := repos.CreateRepository(ctx, connect.NewRequest(&relicv1.CreateRepositoryRequest{
			Repository: r,
		}))

		return err
	}
	local := &relicv1.Repository{
		Name:        "libs",
		Class:       relicv1.RepositoryClass_REPOSITORY_CLASS_LOCAL,
		Format:      relicv1.Format_FORMAT_MAVEN,
		Description: "libraries",
	}
	remote := &relicv1.Repository{
		Name:   "central",
		Class:  relicv1.RepositoryClass_REPOSITORY_CLASS_REMOTE,
		Format: relicv1.Format_FORMAT_MAVEN,
		Remote: &relicv1.RemoteRepositorySettings{
			Url:      "https://repo1.maven.org/maven2",
			Username: "u",
			Password: "s3cret",
		},
	}

	virtual := &relicv1.Repository{
		Name:   "maven",
		Class:  relicv1.RepositoryClass_REPOSITORY_CLASS_VIRTUAL,
		Format: relicv1.Format_FORMAT_MAVEN,
		Virtual: &relicv1.VirtualRepositorySettings{
			Members: []string{
				"libs",
				"central",
			},
			DeployTo: "libs",
		},
	}
	for _, r := range []*relicv1.Repository{
		local,
		remote,
		virtual,
	} {
		if err := create(r); err != nil {
			t.Fatalf("create %s: %v", r.GetName(), err)
		}
	}

	if err := create(local); code(err) != connect.CodeAlreadyExists {
		t.Errorf("duplicate: %v", err)
	}

	if err := create(&relicv1.Repository{
		Name: "x",
	}); code(err) != connect.CodeInvalidArgument {
		t.Errorf("no class: %v", err)
	}

	gems := &relicv1.Repository{
		Name:   "ruby",
		Class:  relicv1.RepositoryClass_REPOSITORY_CLASS_LOCAL,
		Format: relicv1.Format_FORMAT_RUBYGEMS,
	}
	if err := create(gems); err != nil {
		t.Fatal(err)
	}

	if _, b := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/repositories/ruby",
	}); !strings.Contains(
		b,
		`"packageType":"gems"`,
	) {
		t.Errorf("artifactory view of rubygems = %s", b)
	}

	var listed []struct {
		Key, PackageType string
	}
	if _, b := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/repositories?packageType=gems",
		},
	); json.Unmarshal([]byte(b), &listed) != nil ||
		len(listed) != 1 || listed[0].Key != "ruby" ||
		listed[0].PackageType != "Gems" {
		t.Errorf("artifactory list by gems = %s", b)
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/golibs",
			body:   `{"rclass":"local","packageType":"go"}`,
		},
	)

	gomod, err := repos.GetRepository(ctx, connect.NewRequest(&relicv1.GetRepositoryRequest{
		Name: "golibs",
	}))
	if err != nil || gomod.Msg.GetRepository().GetFormat() != relicv1.Format_FORMAT_GOMOD {
		t.Errorf("go repository = %v %v", gomod, err)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/bad",
			body:   `{"rclass":"local","packageType":"rubygems"}`,
		},
	); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Relic's own format name through the Artifactory API: %d", resp.StatusCode)
	}

	got, err := repos.GetRepository(ctx, connect.NewRequest(&relicv1.GetRepositoryRequest{
		Name: "central",
	}))
	if err != nil || got.Msg.GetRepository().GetRemote().GetUrl() != "https://repo1.maven.org/maven2" ||
		got.Msg.GetRepository().GetRemote().GetPassword() != "" {
		t.Fatalf("get central = %v %v", got, err)
	}

	if _, b := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/repositories/maven",
	}); !strings.Contains(
		b,
		`"defaultDeploymentRepo":"libs"`,
	) {
		t.Errorf("artifactory view = %s", b)
	}

	do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/repositories/central",
			body:   `{"rclass":"remote","xrayIndex":true}`,
		},
	)

	upd := got.Msg.GetRepository()

	upd.Description, upd.Remote.Offline = "maven central", true
	if _, err := repos.UpdateRepository(
		ctx,
		connect.NewRequest(&relicv1.UpdateRepositoryRequest{
			Repository: upd,
		}),
	); err != nil {
		t.Fatal(err)
	}

	if _, b := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/repositories/central",
		},
	); !strings.Contains(b, `"xrayIndex":true`) ||
		!strings.Contains(b, `"offline":true`) {
		t.Errorf("after update = %s", b)
	}

	stored, err := repository.NewService(db, objectstoretest.NewMemory(), slog.New(slog.DiscardHandler)).
		GetRepository(ctx, "central")
	if err != nil || stored.Upstream().Password != "s3cret" || stored.Config.Description != "maven central" {
		t.Errorf("stored = %+v %v", stored.Config, err)
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/security/users/dev",
			body:   `{"email":"dev@example.com","password":"Dev-pass-12345"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/dev-libs",
			body:   `{"name":"dev-libs","repo":{"repositories":["libs"],"actions":{"users":{"dev":["read"]}}}}`,
		},
	)
	dev := relicv1connect.NewRepositoryServiceClient(relicHTTP("dev", "Dev-pass-12345"), srv.URL+"/api")

	list, err := dev.ListRepositories(ctx, connect.NewRequest(&relicv1.ListRepositoriesRequest{}))
	if err != nil || len(list.Msg.GetRepositories()) != 1 || list.Msg.GetRepositories()[0].GetName() != "libs" {
		t.Errorf("dev list = %v %v", list, err)
	}

	if _, err := dev.GetRepository(
		ctx,
		connect.NewRequest(&relicv1.GetRepositoryRequest{
			Name: "central",
		}),
	); code(err) != connect.CodeNotFound {
		t.Errorf("dev get unreadable: %v", err)
	}

	if _, err := dev.DeleteRepository(
		ctx,
		connect.NewRequest(&relicv1.DeleteRepositoryRequest{
			Name: "libs",
		}),
	); code(err) != connect.CodePermissionDenied {
		t.Errorf("dev delete: %v", err)
	}

	filtered, err := repos.ListRepositories(
		ctx,
		connect.NewRequest(&relicv1.ListRepositoriesRequest{
			Class: relicv1.RepositoryClass_REPOSITORY_CLASS_REMOTE,
		}),
	)
	if err != nil || len(filtered.Msg.GetRepositories()) != 1 {
		t.Errorf("remote filter = %v %v", filtered, err)
	}

	if _, err := repos.DeleteRepository(
		ctx,
		connect.NewRequest(&relicv1.DeleteRepositoryRequest{
			Name: "maven",
		}),
	); err != nil {
		t.Fatal(err)
	}

	if _, err := repos.GetRepository(
		ctx,
		connect.NewRequest(&relicv1.GetRepositoryRequest{
			Name: "maven",
		}),
	); code(err) != connect.CodeNotFound {
		t.Errorf("after delete: %v", err)
	}
}

func TestRelicAPIArtifacts(t *testing.T) {
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
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/other",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/security/users/dev",
			body:   `{"email":"dev@example.com","password":"Dev-pass-12345"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/dev",
			body: `{"name":"dev","repo":{"repositories":["libs"],
		"include-patterns":["app/**"],"actions":{"users":{"dev":["read","write"]}}}}`,
		},
	)

	dev := asUser{
		"dev",
		"Dev-pass-12345",
	}

	put := func(u asUser, path, body string) (int, string) {
		t.Helper()

		req, _ := http.NewRequestWithContext(
			ctx,
			http.MethodPut,
			srv.URL+"/api/v1/files/"+path,
			strings.NewReader(body),
		)

		resp, err := (&http.Client{
			Transport: u,
		}).Do(req)
		if err != nil {
			t.Fatal(err)
		}

		defer func() {
			_ = resp.Body.Close()
		}()

		b, _ := io.ReadAll(resp.Body)

		return resp.StatusCode, string(b)
	}
	get := func(u asUser, path string, headers map[string]string) (int, string) {
		t.Helper()

		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/files/"+path, nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := (&http.Client{
			Transport: u,
		}).Do(req)
		if err != nil {
			t.Fatal(err)
		}

		defer func() {
			_ = resp.Body.Close()
		}()

		b, _ := io.ReadAll(resp.Body)

		return resp.StatusCode, string(b)
	}
	admin := asUser{
		"admin",
		"password",
	}

	if code, body := put(dev, "libs/app/1.0/app.txt", "hello"); code != 201 || !strings.Contains(body, `"sha256"`) {
		t.Fatalf("upload = %d %s", code, body)
	}

	if code, _ := put(dev, "libs/app/1.0/app.txt", "again"); code != 403 {
		t.Errorf("overwrite without delete = %d, want 403", code)
	}

	if code, _ := put(dev, "libs/secret/x.txt", "x"); code != 404 {
		t.Errorf("upload outside the include pattern = %d, want 404", code)
	}

	if code, _ := put(asUser{}, "libs/app/y.txt", "x"); code != 401 {
		t.Errorf("anonymous upload = %d", code)
	}

	if code, body := get(dev, "libs/app/1.0/app.txt", nil); code != 200 || body != "hello" {
		t.Errorf("download = %d %q", code, body)
	}

	if code, body := get(dev, "libs/app/1.0/app.txt", map[string]string{
		"Range": "bytes=1-3",
	}); code != 206 ||
		body != "ell" {
		t.Errorf("range = %d %q", code, body)
	}

	if code, _ := get(dev, "other/app/1.0/app.txt", nil); code != 404 {
		t.Errorf("unreadable repository = %d, want 404", code)
	}

	put(admin, "libs/secret/key.txt", "s")

	arts := relicv1connect.NewArtifactServiceClient(&http.Client{
		Transport: dev,
	}, srv.URL+"/api")
	adminArts := relicv1connect.NewArtifactServiceClient(&http.Client{
		Transport: admin,
	}, srv.URL+"/api")

	st, err := arts.StatItem(
		ctx,
		connect.NewRequest(&relicv1.StatItemRequest{
			Repository: "libs",
			Path:       "app/1.0/app.txt",
		}),
	)
	if err != nil || st.Msg.GetItem().GetSize() != 5 || st.Msg.GetItem().GetChecksums().GetSha256() == "" {
		t.Fatalf("stat = %v %v", st, err)
	}

	kids, err := arts.ListChildren(ctx, connect.NewRequest(&relicv1.ListChildrenRequest{
		Repository: "libs",
	}))
	if err != nil || len(kids.Msg.GetItems()) != 1 || kids.Msg.GetItems()[0].GetName() != "app" {
		t.Errorf("children = %v %v", kids, err)
	}

	found, err := arts.SearchItems(ctx, connect.NewRequest(&relicv1.SearchItemsRequest{
		Name: ".txt",
	}))
	if err != nil || len(found.Msg.GetItems()) != 1 {
		t.Errorf("dev search = %v %v", found, err)
	}

	props := map[string]*relicv1.PropertyValues{
		"qa": {
			Values: []string{
				"passed",
			},
		},
	}
	if _, err := arts.SetProperties(
		ctx,
		connect.NewRequest(
			&relicv1.SetPropertiesRequest{
				Repository: "libs",
				Path:       "app/1.0/app.txt",
				Properties: props,
			},
		),
	); code(err) != connect.CodePermissionDenied {
		t.Errorf("set properties without annotate: %v", err)
	}

	if _, err := adminArts.SetProperties(
		ctx,
		connect.NewRequest(
			&relicv1.SetPropertiesRequest{
				Repository: "libs",
				Path:       "app/1.0/app.txt",
				Properties: props,
			},
		),
	); err != nil {
		t.Fatal(err)
	}

	got, err := arts.GetProperties(
		ctx,
		connect.NewRequest(&relicv1.GetPropertiesRequest{
			Repository: "libs",
			Path:       "app/1.0/app.txt",
		}),
	)
	if err != nil || got.Msg.GetProperties()["qa"].GetValues()[0] != "passed" {
		t.Errorf("properties = %v %v", got, err)
	}

	if _, err := adminArts.DeleteProperties(
		ctx,
		connect.NewRequest(
			&relicv1.DeletePropertiesRequest{
				Repository: "libs",
				Path:       "app/1.0/app.txt",
				Keys: []string{
					"qa",
				},
			},
		),
	); err != nil {
		t.Fatal(err)
	}

	if _, err := arts.CopyItem(
		ctx,
		connect.NewRequest(
			&relicv1.CopyItemRequest{
				Repository:   "libs",
				Path:         "app/1.0",
				ToRepository: "libs",
				ToPath:       "app/1.1",
			},
		),
	); err != nil {
		t.Fatalf("copy: %v", err)
	}

	if _, err := arts.MoveItem(
		ctx,
		connect.NewRequest(
			&relicv1.MoveItemRequest{
				Repository:   "libs",
				Path:         "app/1.1",
				ToRepository: "libs",
				ToPath:       "app/1.2",
			},
		),
	); code(err) != connect.CodePermissionDenied {
		t.Errorf("move without delete: %v", err)
	}

	if _, err := adminArts.MoveItem(
		ctx,
		connect.NewRequest(
			&relicv1.MoveItemRequest{
				Repository:   "libs",
				Path:         "app/1.1",
				ToRepository: "other",
				ToPath:       "app/1.1",
			},
		),
	); err != nil {
		t.Fatal(err)
	}

	if _, err := arts.DeleteItem(
		ctx,
		connect.NewRequest(&relicv1.DeleteItemRequest{
			Repository: "libs",
			Path:       "app/1.0",
		}),
	); code(err) != connect.CodePermissionDenied {
		t.Errorf("delete without delete: %v", err)
	}

	do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/dev-del",
			body: `{"name":"dev-del","repo":{"repositories":["libs"],
		"include-patterns":["app/**"],"exclude-patterns":["app/1.0/keep/**"],"actions":{"users":{"dev":["delete"]}}}}`,
		},
	)
	put(admin, "libs/app/1.0/keep/k.txt", "k")

	if _, err := arts.DeleteItem(
		ctx,
		connect.NewRequest(&relicv1.DeleteItemRequest{
			Repository: "libs",
			Path:       "app/1.0",
		}),
	); code(err) != connect.CodePermissionDenied {
		t.Errorf("folder delete with an undeletable file below: %v", err)
	}

	if _, err := adminArts.DeleteItem(
		ctx,
		connect.NewRequest(&relicv1.DeleteItemRequest{
			Repository: "libs",
		}),
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("delete the root: %v", err)
	}

	if _, err := adminArts.DeleteItem(
		ctx,
		connect.NewRequest(&relicv1.DeleteItemRequest{
			Repository: "libs",
			Path:       "app/1.0",
		}),
	); err != nil {
		t.Fatal(err)
	}

	if _, err := adminArts.StatItem(
		ctx,
		connect.NewRequest(&relicv1.StatItemRequest{
			Repository: "libs",
			Path:       "app/1.0/app.txt",
		}),
	); code(err) != connect.CodeNotFound {
		t.Errorf("after delete: %v", err)
	}
}

func TestRelicAPISecurity(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()

	sec := relicv1connect.NewSecurityServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")
	if _, err := sec.PutUser(
		ctx,
		connect.NewRequest(&relicv1.PutUserRequest{
			User: &relicv1.User{
				Name:  "ann",
				Email: "ann@example.com",
			},
		}),
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("new user without password: %v", err)
	}

	u, err := sec.PutUser(
		ctx,
		connect.NewRequest(
			&relicv1.PutUserRequest{
				User: &relicv1.User{
					Name:     "ann",
					Email:    "ann@example.com",
					Password: "Ann-pass-12345",
				},
			},
		),
	)
	if err != nil || !u.Msg.GetCreated() || u.Msg.GetUser().GetRealm() != relicv1.Realm_REALM_INTERNAL ||
		u.Msg.GetUser().GetPassword() != "" {
		t.Fatalf("put user = %v %v", u, err)
	}

	g, err := sec.PutGroup(
		ctx,
		connect.NewRequest(
			&relicv1.PutGroupRequest{
				Group: &relicv1.Group{
					Name:        "devs",
					Description: "developers",
					Members: []string{
						"ann",
					},
				},
			},
		),
	)
	if err != nil || g.Msg.GetGroup().GetMembers()[0] != "ann" {
		t.Fatalf("put group = %v %v", g, err)
	}

	grant := &relicv1.PermissionGrant{
		Principal: &relicv1.PermissionGrant_Group{
			Group: "devs",
		},
		Actions: []relicv1.Action{
			relicv1.Action_ACTION_READ,
			relicv1.Action_ACTION_WRITE,
		},
	}

	p := &relicv1.Permission{
		Name: "devs-libs",
		Rules: []*relicv1.PermissionRule{
			{
				Resource: relicv1.ResourceKind_RESOURCE_KIND_REPOSITORY,
				Include: []string{
					"**",
				},
				Selector: &relicv1.RepositorySelector{
					Sets: []relicv1.RepositorySet{
						relicv1.RepositorySet_REPOSITORY_SET_LOCAL,
					},
				},
				Grants: []*relicv1.PermissionGrant{
					grant,
				},
			},
		},
	}
	if _, err := sec.PutPermission(ctx, connect.NewRequest(&relicv1.PutPermissionRequest{
		Permission: p,
	})); err != nil {
		t.Fatal(err)
	}

	if _, b := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/v2/security/permissions/devs-libs",
		},
	); !strings.Contains(b, `"devs":["read","write"]`) ||
		!strings.Contains(b, `"repositories":["ANY LOCAL"]`) {
		t.Errorf("artifactory view = %s", b)
	}

	grant.Actions = []relicv1.Action{
		99,
	}

	if _, err := sec.PutPermission(
		ctx,
		connect.NewRequest(&relicv1.PutPermissionRequest{
			Permission: p,
		}),
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("unknown action: %v", err)
	}

	p.Rules = append(p.Rules, p.Rules[0])
	grant.Actions = []relicv1.Action{
		relicv1.Action_ACTION_READ,
	}

	if _, err := sec.PutPermission(
		ctx,
		connect.NewRequest(&relicv1.PutPermissionRequest{
			Permission: p,
		}),
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("two repository rules: %v", err)
	}

	users, err := sec.ListUsers(ctx, connect.NewRequest(&relicv1.ListUsersRequest{}))
	if err != nil || len(users.Msg.GetUsers()) < 2 {
		t.Errorf("users = %v %v", users, err)
	}

	perms, err := sec.ListPermissions(ctx, connect.NewRequest(&relicv1.ListPermissionsRequest{}))
	if err != nil || len(perms.Msg.GetPermissions()) != 1 {
		t.Errorf("permissions = %v %v", perms, err)
	}

	racing := make(chan error, 8)
	for range cap(racing) {
		go func() {
			_, err := sec.PutPermission(ctx, connect.NewRequest(&relicv1.PutPermissionRequest{
				Permission: &relicv1.Permission{
					Name: "racing",
				},
			}))
			racing <- err
		}()
	}

	for range cap(racing) {
		if err := <-racing; err != nil {
			t.Errorf("concurrent put of a new permission: %v", err)
		}
	}

	ann := relicv1connect.NewSecurityServiceClient(relicHTTP("ann", "Ann-pass-12345"), srv.URL+"/api")
	if _, err := ann.ListUsers(
		ctx,
		connect.NewRequest(&relicv1.ListUsersRequest{}),
	); code(err) != connect.CodePermissionDenied {
		t.Errorf("non-admin list users: %v", err)
	}

	if _, err := ann.CreateToken(
		ctx,
		connect.NewRequest(&relicv1.CreateTokenRequest{
			ExpiresDays: 9999,
		}),
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("token too long: %v", err)
	}

	tok, err := ann.CreateToken(ctx, connect.NewRequest(&relicv1.CreateTokenRequest{
		Description: "ci",
		ExpiresDays: 1,
	}))
	if err != nil || tok.Msg.GetAccessToken() == "" || tok.Msg.GetExpiresIn() != 86400 {
		t.Fatalf("token = %v %v", tok, err)
	}

	resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/repositories",
			noAuth: true,
			headers: map[string]string{
				"Authorization": "Bearer " + tok.Msg.GetAccessToken(),
			},
		},
	)
	if resp.StatusCode != 200 {
		t.Errorf("token on the Artifactory API = %d", resp.StatusCode)
	}

	if _, err := ann.CreateToken(
		ctx,
		connect.NewRequest(&relicv1.CreateTokenRequest{
			Scope: &relicv1.TokenScope{
				Kind: relicv1.TokenScopeKind(99),
			},
		}),
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("a token of an unknown scope kind = %v, want InvalidArgument", err)
	}

	for name, scope := range map[string]*relicv1.TokenScope{
		"admin": {
			Kind: relicv1.TokenScopeKind_TOKEN_SCOPE_KIND_ADMIN,
		},
		"foreign group": {
			Kind: relicv1.TokenScopeKind_TOKEN_SCOPE_KIND_GROUPS,
			Groups: []string{
				"ops",
			},
		},
	} {
		if _, err := ann.CreateToken(
			ctx,
			connect.NewRequest(&relicv1.CreateTokenRequest{
				Scope: scope,
			}),
		); code(err) != connect.CodePermissionDenied {
			t.Errorf("%s scope: %v", name, err)
		}
	}

	forever, err := ann.CreateToken(ctx, connect.NewRequest(&relicv1.CreateTokenRequest{
		Description: "forever",
	}))
	if err != nil || forever.Msg.GetExpiresIn() != int64(auth.MaxUserTokenLifetime/time.Second) {
		t.Errorf("never-expiring token of a user = %v %v", forever, err)
	}

	scoped, err := ann.CreateToken(ctx, connect.NewRequest(&relicv1.CreateTokenRequest{
		Description: "devs",
		ExpiresDays: 1,
		Scope: &relicv1.TokenScope{
			Kind: relicv1.TokenScopeKind_TOKEN_SCOPE_KIND_GROUPS,
			Groups: []string{
				"devs",
			},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}

	viaScoped := relicv1connect.NewSecurityServiceClient(relicHTTP("ann", scoped.Msg.GetAccessToken()), srv.URL+"/api")

	minted, err := viaScoped.CreateToken(
		ctx,
		connect.NewRequest(&relicv1.CreateTokenRequest{
			Description: "wider",
			ExpiresDays: 1,
		}),
	)
	if err != nil || minted.Msg.GetScope().GetKind() != relicv1.TokenScopeKind_TOKEN_SCOPE_KIND_GROUPS ||
		strings.Join(minted.Msg.GetScope().GetGroups(), ",") != "devs" {
		t.Errorf("token minted by a group-scoped token = %v %v", minted, err)
	}

	const cliSession = "0b7e3f4a-5c6d-4e8f-9a0b-1c2d3e4f5a6b"

	sessions := relicv1connect.NewSessionServiceClient(http.DefaultClient, srv.URL+"/api")
	if _, err := sessions.StartWebLogin(ctx, connect.NewRequest(&relicv1.StartWebLoginRequest{
		Session: cliSession,
	})); err != nil {
		t.Fatal(err)
	}

	if _, err := relicv1connect.NewSessionServiceClient(
		relicHTTP("ann", scoped.Msg.GetAccessToken()),
		srv.URL+"/api",
	).ApproveLogin(ctx, connect.NewRequest(&relicv1.ApproveLoginRequest{
		Session: cliSession,
		Code:    cliSession[len(cliSession)-4:],
	})); code(err) != connect.CodePermissionDenied {
		t.Errorf("a group-scoped token approving a CLI sign-in: %v", err)
	}

	signIn := connect.NewRequest(&relicv1.LoginRequest{
		Username: "ann",
		Password: scoped.Msg.GetAccessToken(),
	})
	signIn.Header().Set(auth.UIHeader, "1")

	uiSession, err := sessions.Login(ctx, signIn)
	if err != nil {
		t.Fatal(err)
	}

	sessionCookie, err := http.ParseSetCookie(uiSession.Header().Get("Set-Cookie"))
	if err != nil {
		t.Fatal(err)
	}

	approveWithSession := connect.NewRequest(&relicv1.ApproveLoginRequest{
		Session: cliSession,
		Code:    cliSession[len(cliSession)-4:],
	})
	approveWithSession.Header().Set("Cookie", sessionCookie.Name+"="+sessionCookie.Value)
	approveWithSession.Header().Set(auth.UIHeader, "1")

	if _, err := sessions.ApproveLogin(ctx, approveWithSession); code(err) != connect.CodePermissionDenied {
		t.Errorf("a UI session signed in with a group-scoped token approving a CLI sign-in: %v", err)
	}

	for _, err := range []error{
		func() error {
			_, err := sec.DeletePermission(ctx, connect.NewRequest(&relicv1.DeletePermissionRequest{
				Name: "devs-libs",
			}))

			return err
		}(),
		func() error {
			_, err := sec.DeleteGroup(ctx, connect.NewRequest(&relicv1.DeleteGroupRequest{
				Name: "devs",
			}))

			return err
		}(),
		func() error {
			_, err := sec.DeleteUser(ctx, connect.NewRequest(&relicv1.DeleteUserRequest{
				Name: "ann",
			}))

			return err
		}(),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}

	if _, err := sec.GetUser(
		ctx,
		connect.NewRequest(&relicv1.GetUserRequest{
			Name: "ann",
		}),
	); code(err) != connect.CodeNotFound {
		t.Errorf("deleted user: %v", err)
	}
}

func TestRelicAPIReplicationsAndBuilds(t *testing.T) {
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
	reps := relicv1connect.NewReplicationServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")

	r := &relicv1.Replication{
		Repository:     "libs",
		TargetUrl:      "https://target.example/artifactory/libs",
		Username:       "u",
		Password:       "p",
		Schedule:       "0 12 * * *",
		OnChange:       true,
		TimeoutSeconds: 20,
	}
	if _, err := reps.PutReplication(
		ctx,
		connect.NewRequest(&relicv1.PutReplicationRequest{
			Replication: r,
		}),
	); err != nil {
		t.Fatal(err)
	}

	if _, b := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/replications/libs",
		},
	); !strings.Contains(b, `"cronExp": "0 0 12 * * ?"`) ||
		!strings.Contains(b, `"socketTimeoutMillis": 20000`) {
		t.Errorf("artifactory view = %s", b)
	}

	for _, bad := range []string{
		"not cron",
		"0 0 12 * * ?",
	} {
		r.Schedule = bad
		if _, err := reps.PutReplication(
			ctx,
			connect.NewRequest(&relicv1.PutReplicationRequest{
				Replication: r,
			}),
		); code(err) != connect.CodeInvalidArgument {
			t.Errorf("schedule %q: %v", bad, err)
		}
	}

	r.Schedule = "0 12 * * *"
	r.Direction = relicv1.ReplicationDirection(99)

	if _, err := reps.PutReplication(
		ctx,
		connect.NewRequest(&relicv1.PutReplicationRequest{
			Replication: r,
		}),
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("unknown direction: %v", err)
	}

	r.Direction = relicv1.ReplicationDirection_REPLICATION_DIRECTION_UNSPECIFIED

	got, err := reps.GetReplication(ctx, connect.NewRequest(&relicv1.GetReplicationRequest{
		Repository: "libs",
	}))
	if m := got.Msg.GetReplication(); err != nil || !m.GetHasPassword() || m.GetPassword() != "" || !m.GetOnChange() ||
		m.GetLastStatus() != relicv1.ReplicationStatus_REPLICATION_STATUS_NEVER {
		t.Errorf("replication = %v %v", got, err)
	}

	do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/replications/libs",
			body:   `{"url":"https://target.example/artifactory/libs","cronExp":"30 30 2 ? * 2-6","socketTimeoutMillis":1500}`,
		},
	)

	got, err = reps.GetReplication(ctx, connect.NewRequest(&relicv1.GetReplicationRequest{
		Repository: "libs",
	}))
	if err != nil || got.Msg.GetReplication().GetSchedule() != "30 2 * * 1-5" ||
		got.Msg.GetReplication().GetTimeoutSeconds() != 2 {
		t.Errorf("schedule from Quartz = %v %v", got, err)
	}

	if _, b := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/replications/libs",
		},
	); !strings.Contains(b, `"cronExp": "30 30 2 ? * 2-6"`) ||
		!strings.Contains(b, `"socketTimeoutMillis": 1500`) {
		t.Errorf("artifactory view after a Quartz update = %s", b)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/replications/libs",
			body:   `{"cronExp":"*/10 * * * * ?"}`,
		},
	); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a schedule finer than a minute = %d, want 400", resp.StatusCode)
	}

	if _, err := reps.DeleteReplication(
		ctx,
		connect.NewRequest(&relicv1.DeleteReplicationRequest{
			Repository: "libs",
		}),
	); err != nil {
		t.Fatal(err)
	}

	if _, err := reps.GetReplication(
		ctx,
		connect.NewRequest(&relicv1.GetReplicationRequest{
			Repository: "libs",
		}),
	); code(err) != connect.CodeNotFound {
		t.Errorf("after delete: %v", err)
	}

	for _, b := range []string{
		"app",
		"secret",
	} {
		do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/build",
				body:   `{"name":"` + b + `","number":"1","started":"2026-09-25T10:00:00.000+0000"}`,
			},
		)
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/security/users/dev",
			body:   `{"email":"dev@example.com","password":"Dev-pass-12345"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/dev-app",
			body: `{"name":"dev-app","build":{"repositories":["artifactory-build-info"],
		"include-patterns":["app/**"],"actions":{"users":{"dev":["read"]}}}}`,
		},
	)
	builds := relicv1connect.NewBuildServiceClient(relicHTTP("dev", "Dev-pass-12345"), srv.URL+"/api")

	list, err := builds.ListBuilds(ctx, connect.NewRequest(&relicv1.ListBuildsRequest{}))
	if err != nil || len(list.Msg.GetBuilds()) != 1 || list.Msg.GetBuilds()[0].GetName() != "app" {
		t.Errorf("builds = %v %v", list, err)
	}

	if runs, err := builds.ListBuildRuns(
		ctx,
		connect.NewRequest(&relicv1.ListBuildRunsRequest{
			Name: "app",
		}),
	); err != nil ||
		len(runs.Msg.GetRuns()) != 1 {
		t.Errorf("runs = %v %v", runs, err)
	}

	if _, err := builds.ListBuildRuns(
		ctx,
		connect.NewRequest(&relicv1.ListBuildRunsRequest{
			Name: "secret",
		}),
	); code(err) != connect.CodeNotFound {
		t.Errorf("unreadable build: %v", err)
	}
}

func TestRelicAPIScan(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()
	scans := relicv1connect.NewScanServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")

	pol := &relicv1.Policy{
		Name: "critical",
		Kind: relicv1.PolicyKind_POLICY_KIND_SECURITY,
		Rules: []*relicv1.PolicyRule{
			{
				Name:          "crit",
				Priority:      1,
				MinSeverity:   relicv1.Severity_SEVERITY_CRITICAL,
				BlockDownload: true,
			},
		},
	}
	if _, err := scans.PutPolicy(ctx, connect.NewRequest(&relicv1.PutPolicyRequest{
		Policy: pol,
	})); err != nil {
		t.Fatal(err)
	}

	pol.Description = "updated"
	if _, err := scans.PutPolicy(ctx, connect.NewRequest(&relicv1.PutPolicyRequest{
		Policy: pol,
	})); err != nil {
		t.Fatalf("replace: %v", err)
	}

	if _, b := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/xray/api/v2/policies/critical",
		},
	); !strings.Contains(b, `"min_severity": "Critical"`) ||
		!strings.Contains(b, `"updated"`) {
		t.Errorf("xray view = %s", b)
	}

	if _, err := scans.PutWatch(
		ctx,
		connect.NewRequest(
			&relicv1.PutWatchRequest{
				Watch: &relicv1.Watch{
					Name:   "w",
					Active: true,
					Policies: []string{
						"nope",
					},
				},
			},
		),
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("unknown policy: %v", err)
	}

	w := &relicv1.Watch{
		Name:   "w",
		Active: true,
		Scopes: []*relicv1.WatchScope{
			{
				Kind: relicv1.WatchScopeKind_WATCH_SCOPE_KIND_REPOSITORY,
				All:  true,
			},
		},
		Policies: []string{
			"critical",
		},
	}
	if _, err := scans.PutWatch(ctx, connect.NewRequest(&relicv1.PutWatchRequest{
		Watch: w,
	})); err != nil {
		t.Fatal(err)
	}

	if ws, err := scans.ListWatches(ctx, connect.NewRequest(&relicv1.ListWatchesRequest{})); err != nil ||
		ws.Msg.GetWatches()[0].GetPolicies()[0] != "critical" {
		t.Errorf("watches = %v %v", ws, err)
	}

	rule, err := scans.CreateIgnoreRule(
		ctx,
		connect.NewRequest(&relicv1.CreateIgnoreRuleRequest{
			Rule: &relicv1.IgnoreRule{
				Notes: "accepted",
				Cves: []string{
					"CVE-2021-44228",
				},
				Packages: []string{
					"pkg:maven/org.apache/log4j@2.14.1",
				},
				Builds: []*relicv1.IgnoredBuild{
					{
						Name:   "app",
						Number: "7",
					},
				},
			},
		}),
	)
	if err != nil || rule.Msg.GetRule().GetId() == "" ||
		rule.Msg.GetRule().GetPackages()[0] != "pkg:maven/org.apache/log4j@2.14.1" ||
		rule.Msg.GetRule().GetBuilds()[0].GetNumber() != "7" {
		t.Fatalf("ignore rule = %v %v", rule, err)
	}

	if _, b := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/xray/api/v1/ignore_rules/" + rule.Msg.GetRule().GetId(),
		},
	); !strings.Contains(b, `"name": "gav://org.apache:log4j"`) ||
		!strings.Contains(b, `"version": "2.14.1"`) {
		t.Errorf("xray view of the ignore rule = %s", b)
	}

	if _, err := scans.DeletePolicy(
		ctx,
		connect.NewRequest(&relicv1.DeletePolicyRequest{
			Name: "critical",
		}),
	); code(err) != connect.CodeFailedPrecondition {
		t.Errorf("delete a watched policy: %v", err)
	}

	if _, err := scans.DeleteIgnoreRule(
		ctx,
		connect.NewRequest(&relicv1.DeleteIgnoreRuleRequest{
			Id: rule.Msg.GetRule().GetId(),
		}),
	); err != nil {
		t.Fatal(err)
	}

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
		path:   "/artifactory/libs/app.jar",
		body:   "not really a jar",
	})

	s, err := scans.GetItemScan(
		ctx,
		connect.NewRequest(&relicv1.GetItemScanRequest{
			Repository: "libs",
			Path:       "app.jar",
		}),
	)
	if err != nil || s.Msg.GetScan().GetStatus() != relicv1.ScanStatus_SCAN_STATUS_NONE {
		t.Errorf("unscanned = %v %v", s, err)
	}

	if f, err := scans.ListFindings(ctx, connect.NewRequest(&relicv1.ListFindingsRequest{})); err != nil ||
		f.Msg.GetTotal() != 0 {
		t.Errorf("findings = %v %v", f, err)
	}

	if c, err := scans.ListCves(ctx, connect.NewRequest(&relicv1.ListCvesRequest{})); err != nil ||
		c.Msg.GetTotal() != 0 {
		t.Errorf("cves = %v %v", c, err)
	}

	if _, err := scans.GetCve(
		ctx,
		connect.NewRequest(&relicv1.GetCveRequest{
			Id: "CVE-2000-0001",
		}),
	); code(err) != connect.CodeNotFound {
		t.Errorf("unknown CVE: %v", err)
	}
}

func TestRelicAPILdap(t *testing.T) {
	t.Parallel()
	srv, db := serverDB(t)
	ctx := t.Context()
	ldapc := relicv1connect.NewLdapServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")

	dir := &relicv1.LdapServer{
		Name:    "corp",
		Enabled: true,
		Url:     "ldap://127.0.0.1:1/dc=example,dc=com",
		Search: &relicv1.LdapUserSearch{
			Filter:          "(uid={0})",
			ManagerDn:       "cn=admin,dc=example,dc=com",
			ManagerPassword: "s3cret",
		},
	}
	if _, err := ldapc.PutLdapServer(
		ctx,
		connect.NewRequest(&relicv1.PutLdapServerRequest{
			LdapServer: dir,
		}),
	); err != nil {
		t.Fatal(err)
	}

	if _, err := ldapc.PutLdapServer(
		ctx,
		connect.NewRequest(
			&relicv1.PutLdapServerRequest{
				LdapServer: &relicv1.LdapServer{
					Name: "bad",
					Url:  "http://x",
				},
			},
		),
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("bad URL: %v", err)
	}

	list, err := ldapc.ListLdapServers(ctx, connect.NewRequest(&relicv1.ListLdapServersRequest{}))
	if err != nil || len(list.Msg.GetLdapServers()) != 1 || !list.Msg.GetLdapServers()[0].GetHasManagerPassword() ||
		list.Msg.GetLdapServers()[0].GetSearch().GetManagerPassword() != "" {
		t.Fatalf("directories = %v %v", list, err)
	}

	if _, b := do(t, srv, call{
		method: "GET",
		path:   "/access/api/v1/ldap/settings/corp",
	}); !strings.Contains(
		b,
		`"ldap_url": "ldap://127.0.0.1:1/dc=example,dc=com"`,
	) {
		t.Errorf("access view = %s", b)
	}

	m := &relicv1.LdapGroupMapping{
		Name:            "corp-groups",
		LdapServer:      "corp",
		BaseDn:          "ou=groups",
		NameAttribute:   "cn",
		MemberAttribute: "uniqueMember",
		Strategy:        relicv1.GroupStrategy_GROUP_STRATEGY_STATIC,
	}
	if _, err := ldapc.PutLdapGroupMapping(
		ctx,
		connect.NewRequest(&relicv1.PutLdapGroupMappingRequest{
			LdapGroupMapping: m,
		}),
	); err != nil {
		t.Fatal(err)
	}

	unreadable := `{"name": "legacy", "strategy": 5, "extensions": {"artifactory": {"enabled": true}}}`
	if err := db.UpsertLdapGroupMapping(ctx, database.UpsertLdapGroupMappingParams{
		Name:   "legacy",
		Config: []byte(unreadable),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := ldapc.PutLdapGroupMapping(ctx, connect.NewRequest(&relicv1.PutLdapGroupMappingRequest{
		LdapGroupMapping: &relicv1.LdapGroupMapping{
			Name:            "legacy",
			LdapServer:      "corp",
			NameAttribute:   "cn",
			MemberAttribute: "member",
			Strategy:        relicv1.GroupStrategy_GROUP_STRATEGY_STATIC,
		},
	})); err == nil {
		t.Error("a mapping whose stored settings could not be read was replaced")
	}

	if row, err := db.GetLdapGroupMapping(
		ctx,
		"legacy",
	); err != nil ||
		!strings.Contains(string(row.Config), `"enabled"`) {
		t.Errorf("stored mapping = %s %v, want its extensions kept", row.Config, err)
	}

	if _, err := db.DeleteLdapGroupMapping(ctx, "legacy"); err != nil {
		t.Fatal(err)
	}

	do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/access/api/v1/ldap/groups",
			body:   `{"name":"corp-ous","enabled_ldap":"corp","group_name_attribute":"ou","strategy":"HIERARCHICAL"}`,
		},
	)

	ms, err := ldapc.ListLdapGroupMappings(ctx, connect.NewRequest(&relicv1.ListLdapGroupMappingsRequest{}))
	if err != nil || len(ms.Msg.GetLdapGroupMappings()) != 2 ||
		ms.Msg.GetLdapGroupMappings()[1].GetStrategy() != relicv1.GroupStrategy_GROUP_STRATEGY_HIERARCHY {
		t.Errorf("mappings = %v %v", ms, err)
	}

	if _, b := do(t, srv, call{
		method: "GET",
		path:   "/access/api/v1/ldap/groups/corp-ous",
	}); !strings.Contains(
		b,
		`"strategy": "hierarchical"`,
	) {
		t.Errorf("access view of the hierarchy strategy = %s", b)
	}

	res, err := ldapc.TestLdapSignIn(
		ctx,
		connect.NewRequest(&relicv1.TestLdapSignInRequest{
			LdapServer: dir,
			Username:   "ann",
			Password:   "x",
		}),
	)
	if err != nil || res.Msg.GetOk() || res.Msg.GetError() == "" {
		t.Errorf("test sign-in against nothing = %v %v", res, err)
	}

	for _, m := range []string{
		"corp-groups",
		"corp-ous",
	} {
		if _, err := ldapc.DeleteLdapGroupMapping(
			ctx,
			connect.NewRequest(&relicv1.DeleteLdapGroupMappingRequest{
				Name: m,
			}),
		); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := ldapc.DeleteLdapServer(
		ctx,
		connect.NewRequest(&relicv1.DeleteLdapServerRequest{
			Name: "corp",
		}),
	); err != nil {
		t.Fatal(err)
	}

	if _, err := ldapc.DeleteLdapServer(
		ctx,
		connect.NewRequest(&relicv1.DeleteLdapServerRequest{
			Name: "corp",
		}),
	); code(err) != connect.CodeNotFound {
		t.Errorf("second delete: %v", err)
	}
}

func TestRelicAPIUIParity(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()
	admin := relicHTTP("admin", "password")

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/libs",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/npm-local",
			body:   `{"rclass":"local","packageType":"npm"}`,
		},
	)

	var zbuf bytes.Buffer

	zw := zip.NewWriter(&zbuf)
	f, _ := zw.Create("docs/readme.txt")
	_, _ = f.Write([]byte("read me"))
	_ = zw.Close()

	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/libs/app/app.zip",
		body:   zbuf.String(),
	})

	var tbuf bytes.Buffer

	gz := gzip.NewWriter(&tbuf)
	tw := tar.NewWriter(gz)
	pj := []byte(`{"name":"left-pad","version":"1.3.0","license":"WTFPL","dependencies":{"x":"^1"}}`)
	_ = tw.WriteHeader(&tar.Header{
		Name: "package/package.json",
		Mode: 0o644,
		Size: int64(len(pj)),
	})
	_, _ = tw.Write(pj)
	_ = tw.Close()
	_ = gz.Close()

	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/npm-local/left-pad/-/left-pad-1.3.0.tgz",
		body:   tbuf.String(),
	})

	arts := relicv1connect.NewArtifactServiceClient(admin, srv.URL+"/api")
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/storage/libs/app/app.zip?properties=qa=passed",
	})

	det, err := arts.GetItemDetails(
		ctx,
		connect.NewRequest(&relicv1.GetItemDetailsRequest{
			Repository: "libs",
			Path:       "app/app.zip",
		}),
	)
	if err != nil || !det.Msg.GetCanDelete() || !det.Msg.GetCanAnnotate() ||
		det.Msg.GetProperties()["qa"].GetValues()[0] != "passed" ||
		det.Msg.GetScan().GetStatus() != relicv1.ScanStatus_SCAN_STATUS_NONE ||
		det.Msg.GetFormat() != relicv1.Format_FORMAT_GENERIC {
		t.Fatalf("details = %v %v", det, err)
	}

	entries, err := arts.ListArchiveEntries(
		ctx,
		connect.NewRequest(&relicv1.ListArchiveEntriesRequest{
			Repository: "libs",
			Path:       "app/app.zip",
		}),
	)
	if err != nil || entries.Msg.GetKind() != "zip" || len(entries.Msg.GetEntries()) != 1 {
		t.Fatalf("entries = %v %v", entries, err)
	}

	if resp, body := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/api/v1/archive/libs/app/app.zip?entry=docs/readme.txt",
		},
	); resp.StatusCode != 200 ||
		body != "read me" {
		t.Errorf("entry = %d %q", resp.StatusCode, body)
	}

	if _, err := arts.ListArchiveEntries(
		ctx,
		connect.NewRequest(&relicv1.ListArchiveEntriesRequest{
			Repository: "npm-local",
			Path:       "left-pad",
		}),
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("folder as archive: %v", err)
	}

	grants, err := arts.GetEffectivePermissions(
		ctx,
		connect.NewRequest(&relicv1.GetEffectivePermissionsRequest{
			Repository: "libs",
			Path:       "app/app.zip",
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	_ = grants

	scans := relicv1connect.NewScanServiceClient(admin, srv.URL+"/api")
	if _, err := scans.GetItemSbom(
		ctx,
		connect.NewRequest(&relicv1.GetItemSbomRequest{
			Repository: "libs",
			Path:       "app/app.zip",
		}),
	); code(err) != connect.CodeNotFound {
		t.Errorf("sbom of an unscanned file: %v", err)
	}

	pkgs := relicv1connect.NewPackageServiceClient(admin, srv.URL+"/api")

	np, err := pkgs.GetNpmPackage(
		ctx,
		connect.NewRequest(
			&relicv1.GetNpmPackageRequest{
				Repository: "npm-local",
				Path:       "left-pad/-/left-pad-1.3.0.tgz",
			},
		),
	)
	if err != nil || np.Msg.GetName() != "left-pad" || np.Msg.GetDependencies()["x"] != "^1" {
		t.Errorf("npm package = %v %v", np, err)
	}

	if _, err := pkgs.GetContainerImage(
		ctx,
		connect.NewRequest(&relicv1.GetContainerImageRequest{
			Repository: "libs",
			Path:       "app",
		}),
	); code(err) != connect.CodeNotFound {
		t.Errorf("image of a plain folder: %v", err)
	}

	if _, err := pkgs.GetPythonPackage(
		ctx,
		connect.NewRequest(&relicv1.GetPythonPackageRequest{
			Repository: "libs",
			Path:       "app/app.zip",
		}),
	); code(err) != connect.CodeNotFound {
		t.Errorf("python package in a generic repository: %v", err)
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/build",
			body: `{"name":"app","number":"7","started":"2026-09-25T10:00:00.000+0000",
		"agent":{"name":"jf","version":"2.124.0"},"vcs":[{"url":"https://git.example/app","revision":"abc"}],
		"modules":[{"id":"app:1","type":"generic","artifacts":[{"name":"app.zip","path":"app/app.zip","sha1":"x"}],"dependencies":[{"id":"lib:2","scopes":["compile"]}]}]}`,
		},
	)

	run, err := relicv1connect.NewBuildServiceClient(admin, srv.URL+"/api").
		GetBuildRun(ctx, connect.NewRequest(&relicv1.GetBuildRunRequest{
			Name:   "app",
			Number: "7",
		}))
	if err != nil || run.Msg.GetAgent() != "jf 2.124.0" || run.Msg.GetStarted() == nil ||
		len(run.Msg.GetModules()) != 1 ||
		run.Msg.GetModules()[0].GetDependencies()[0].GetScopes()[0] != "compile" ||
		run.Msg.GetVcs()[0].GetRevision() != "abc" {
		t.Errorf("run = %v %v", run, err)
	}

	sys := relicv1connect.NewSystemServiceClient(admin, srv.URL+"/api")

	ov, err := sys.GetDashboardSummary(ctx, connect.NewRequest(&relicv1.GetDashboardSummaryRequest{}))
	if err != nil || ov.Msg.GetRepositories()["local"] != 3 || ov.Msg.GetBuilds() != 1 {
		t.Errorf("overview = %v %v", ov, err)
	}

	trash := relicv1connect.NewTrashServiceClient(admin, srv.URL+"/api")

	if _, err := arts.DeleteItem(
		ctx,
		connect.NewRequest(&relicv1.DeleteItemRequest{
			Repository: "libs",
			Path:       "app/app.zip",
		}),
	); err != nil {
		t.Fatal(err)
	}

	list, err := trash.ListTrash(ctx, connect.NewRequest(&relicv1.ListTrashRequest{}))
	if err != nil || len(list.Msg.GetEntries()) != 1 || list.Msg.GetEntries()[0].GetOriginalRepository() != "libs" {
		t.Fatalf("trash = %v %v", list, err)
	}

	if _, err := trash.RestoreTrash(
		ctx,
		connect.NewRequest(&relicv1.RestoreTrashRequest{
			Path: list.Msg.GetEntries()[0].GetPath(),
		}),
	); err != nil {
		t.Fatal(err)
	}

	if _, err := arts.StatItem(
		ctx,
		connect.NewRequest(&relicv1.StatItemRequest{
			Repository: "libs",
			Path:       "app/app.zip",
		}),
	); err != nil {
		t.Errorf("after restore: %v", err)
	}

	if _, err := trash.EmptyTrash(ctx, connect.NewRequest(&relicv1.EmptyTrashRequest{})); err != nil {
		t.Fatal(err)
	}

	sessions := relicv1connect.NewSessionServiceClient(admin, srv.URL+"/api")
	if _, err := sessions.ApproveLogin(
		ctx,
		connect.NewRequest(&relicv1.ApproveLoginRequest{
			Session: "nope",
			Code:    "1234",
		}),
	); code(err) != connect.CodeNotFound {
		t.Errorf("unknown jf login: %v", err)
	}
}
