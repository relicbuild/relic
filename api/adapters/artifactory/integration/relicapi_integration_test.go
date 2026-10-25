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

	"connectrpc.com/connect/v2"

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
	anon := relicv1connect.NewSystemServiceClient(apiClient(relicHTTP("", ""), srv.URL))
	admin := relicv1connect.NewSystemServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	dev := relicv1connect.NewSystemServiceClient(apiClient(relicHTTP("dev", "Dev-pass-12345"), srv.URL))
	ctx := t.Context()

	v, err := anon.GetVersion(ctx, &relicv1.GetVersionRequest{})
	if err != nil || v.GetVersion() == "" {
		t.Fatalf("version = %v %v", v, err)
	}

	if _, err := anon.GetSystemStatus(
		ctx,
		&relicv1.GetSystemStatusRequest{},
	); code(err) != connect.CodeUnauthenticated {
		t.Errorf("anonymous status: %v", err)
	}

	if _, err := dev.GetSystemStatus(
		ctx,
		&relicv1.GetSystemStatusRequest{},
	); code(err) != connect.CodePermissionDenied {
		t.Errorf("non-admin status: %v", err)
	}

	st, err := admin.GetSystemStatus(ctx, &relicv1.GetSystemStatusRequest{})
	if err != nil || st.GetStorage() == nil || len(st.GetChecks()) == 0 || st.GetScanner() == nil {
		t.Fatalf("status = %v %v", st, err)
	}

	bad := relicv1connect.NewSystemServiceClient(apiClient(relicHTTP("admin", "wrong"), srv.URL))
	if _, err := bad.GetVersion(
		ctx,
		&relicv1.GetVersionRequest{},
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
	sessions := relicv1connect.NewSessionServiceClient(apiClient(client, srv.URL))

	login := &relicv1.LoginRequest{
		Username: "admin",
		Password: "password",
	}
	if _, err := sessions.Login(ctx, login); code(err) != connect.CodePermissionDenied {
		t.Fatalf("login without the UI header: %v", err)
	}

	uiCtx, uiInfo := connect.NewClientContext(ctx)
	uiInfo.RequestHeader().Set("X-Relic-UI", "1")

	if _, err := sessions.Login(
		ctx,
		&relicv1.LoginRequest{
			Username: "admin",
			Password: "nope",
		},
	); code(err) != connect.CodePermissionDenied {
		t.Fatalf("bad password without header: %v", err)
	}

	bad := &relicv1.LoginRequest{
		Username: "admin",
		Password: "nope",
	}
	if _, err := sessions.Login(uiCtx, bad); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("bad password: %v", err)
	}

	resp, err := sessions.Login(uiCtx, login)
	if err != nil || resp.GetPrincipal().GetUsername() != "admin" || jar.cookie == "" {
		t.Fatalf("login = %v %v, cookie %q", resp, err, jar.cookie)
	}

	me := func(header bool) (*relicv1.GetCurrentUserResponse, error) {
		req := &relicv1.GetCurrentUserRequest{}

		ctx, info := connect.NewClientContext(ctx)
		if header {
			info.RequestHeader().Set("X-Relic-UI", "1")
		}

		return sessions.GetCurrentUser(ctx, req)
	}
	if got, err := me(true); err != nil || !got.GetPrincipal().GetAdmin() {
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

	out := &relicv1.LogoutRequest{}
	cookie := jar.cookie

	if _, err := sessions.Logout(uiCtx, out); err != nil {
		t.Fatal(err)
	}

	jar.cookie = cookie

	if _, err := me(true); code(err) != connect.CodeUnauthenticated {
		t.Errorf("after logout: %v", err)
	}
}

func TestRelicAPISessionsEndWithTheTokenTheyWereMadeWith(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()

	const password = "Bob-pass-12345"

	if _, err := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL)).
		PutUser(ctx, &relicv1.PutUserRequest{
			User: &relicv1.User{
				Name:     "bob",
				Email:    "bob@example.com",
				Password: password,
			},
		}); err != nil {
		t.Fatal(err)
	}

	bobs := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("bob", password), srv.URL))

	short, err := bobs.CreateToken(ctx, &relicv1.CreateTokenRequest{
		Description:      "short",
		ExpiresInSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}

	uiCtx, uiInfo := connect.NewClientContext(ctx)
	uiInfo.RequestHeader().Set(auth.UIHeader, "1")

	signIn := func(secret string) (*cookieJar, relicv1connect.SecurityServiceClient) {
		jar := &cookieJar{}
		if _, err := relicv1connect.NewSessionServiceClient(apiClient(&http.Client{
			Transport: jar,
		}, srv.URL)).Login(uiCtx, &relicv1.LoginRequest{
			Username: "bob",
			Password: secret,
		}); err != nil || jar.cookie == "" {
			t.Fatalf("sign in: %v", err)
		}

		return jar, relicv1connect.NewSecurityServiceClient(apiClient(&http.Client{
			Transport: jar,
		}, srv.URL))
	}

	_, tokenSession := signIn(short.GetAccessToken())

	fromSession, err := tokenSession.CreateToken(uiCtx, &relicv1.CreateTokenRequest{
		Description:      "from a token session",
		ExpiresInSeconds: 30 * 24 * 3600,
	})
	if err != nil || fromSession.GetExpiresIn() > 60 {
		t.Fatalf("token from a session made with a 60 s token = %v %v, want it to live at most 60 s", fromSession, err)
	}

	fromToken, err := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("bob", short.GetAccessToken()), srv.URL)).
		CreateToken(ctx, &relicv1.CreateTokenRequest{
			Description: "child",
		})
	if err != nil || fromToken.GetExpiresIn() > 60 || fromToken.GetRefreshToken() != "" {
		t.Fatalf("token minted with a 60 s token = %v %v, want it to live at most 60 s, unrefreshable", fromToken, err)
	}

	const cliSession = "5f0c1d2e-3a4b-4c5d-8e6f-7a8b9c0d1e2f"

	if _, err := relicv1connect.NewSessionServiceClient(apiClient(http.DefaultClient, srv.URL)).
		StartWebLogin(ctx, &relicv1.StartWebLoginRequest{
			Session: cliSession,
		}); err != nil {
		t.Fatal(err)
	}

	tokenJar, _ := signIn(short.GetAccessToken())
	if _, err := relicv1connect.NewSessionServiceClient(apiClient(&http.Client{
		Transport: tokenJar,
	}, srv.URL)).ApproveLogin(uiCtx, &relicv1.ApproveLoginRequest{
		Session: cliSession,
		Code:    cliSession[len(cliSession)-4:],
	}); code(err) != connect.CodePermissionDenied {
		t.Errorf("a session made with a token approving a CLI sign-in: %v, want permission denied", err)
	}

	passwordJar, passwordSession := signIn(password)

	if _, err := relicv1connect.NewSessionServiceClient(apiClient(&http.Client{
		Transport: passwordJar,
	}, srv.URL)).ApproveLogin(uiCtx, &relicv1.ApproveLoginRequest{
		Session: cliSession,
		Code:    cliSession[len(cliSession)-4:],
	}); err != nil {
		t.Fatal(err)
	}

	polled, err := relicv1connect.NewSessionServiceClient(apiClient(http.DefaultClient, srv.URL)).
		PollWebLogin(ctx, &relicv1.PollWebLoginRequest{
			Session: cliSession,
		})
	if err != nil || polled.GetAccessToken() == "" {
		t.Fatalf("poll = %v %v", polled, err)
	}

	traded, err := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("bob", polled.GetAccessToken()), srv.URL)).
		CreateToken(ctx, &relicv1.CreateTokenRequest{
			Description:      "relic CLI",
			ExpiresInSeconds: 90 * 24 * 3600,
		})
	if err != nil || traded.GetExpiresIn() != 90*24*3600 {
		t.Errorf("the CLI's token traded for a browser sign-in = %v %v, want the 90 days asked for", traded, err)
	}

	fromPassword, err := passwordSession.CreateToken(uiCtx, &relicv1.CreateTokenRequest{
		Description:      "from a password session",
		ExpiresInSeconds: 30 * 24 * 3600,
	})
	if err != nil || fromPassword.GetExpiresIn() != 30*24*3600 {
		t.Fatalf("token from a password session = %v %v, want the 30 days asked for", fromPassword, err)
	}

	list, err := bobs.ListTokens(ctx, &relicv1.ListTokensRequest{})
	if err != nil {
		t.Fatal(err)
	}

	for _, tok := range list.GetTokens() {
		if tok.GetDescription() != "short" {
			continue
		}

		if _, err := bobs.RevokeToken(ctx, &relicv1.RevokeTokenRequest{
			Id: tok.GetId(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := tokenSession.ListTokens(
		uiCtx,
		&relicv1.ListTokensRequest{},
	); code(
		err,
	) != connect.CodeUnauthenticated {
		t.Errorf("the session made with a revoked token: %v, want unauthenticated", err)
	}

	for name, tok := range map[string]string{
		"from the token session": fromSession.GetAccessToken(),
		"minted with the token":  fromToken.GetAccessToken(),
	} {
		_, err := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("bob", tok), srv.URL)).
			ListTokens(ctx, &relicv1.ListTokensRequest{})
		if code(err) != connect.CodeUnauthenticated {
			t.Errorf("token %s after its parent was revoked: %v, want unauthenticated", name, err)
		}
	}

	if _, err := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("bob", fromPassword.GetAccessToken()), srv.URL)).
		ListTokens(ctx, &relicv1.ListTokensRequest{}); err != nil {
		t.Errorf("a token made in a password session: %v, want it still valid", err)
	}
}

func TestRelicAPIChangePassword(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()

	const (
		password    = "Carl-pass-12345"
		newPassword = "Carl-new-pass-1"
	)

	if _, err := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL)).
		PutUser(ctx, &relicv1.PutUserRequest{
			User: &relicv1.User{
				Name:     "carl",
				Email:    "carl@example.com",
				Password: password,
			},
		}); err != nil {
		t.Fatal(err)
	}

	token, err := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("carl", password), srv.URL)).
		CreateToken(ctx, &relicv1.CreateTokenRequest{
			Description: "carl's token",
		})
	if err != nil {
		t.Fatal(err)
	}

	carl := relicv1connect.NewSessionServiceClient(apiClient(relicHTTP("carl", password), srv.URL))

	for _, tt := range []struct {
		name, current, next string
		want                connect.Code
	}{
		{
			name:    "a wrong current password",
			current: "Wrong-pass-123",
			next:    newPassword,
			want:    connect.CodePermissionDenied,
		},
		{
			name:    "a token instead of the current password",
			current: token.GetAccessToken(),
			next:    newPassword,
			want:    connect.CodePermissionDenied,
		},
		{
			name:    "a short new password",
			current: password,
			next:    "short",
			want:    connect.CodeInvalidArgument,
		},
	} {
		if _, err := carl.ChangePassword(ctx, &relicv1.ChangePasswordRequest{
			CurrentPassword: tt.current,
			NewPassword:     tt.next,
		}); code(err) != tt.want {
			t.Errorf("%s: %v, want %v", tt.name, err, tt.want)
		}
	}

	if _, err := relicv1connect.NewSessionServiceClient(apiClient(relicHTTP("", ""), srv.URL)).
		ChangePassword(ctx, &relicv1.ChangePasswordRequest{
			CurrentPassword: password,
			NewPassword:     newPassword,
		}); code(err) != connect.CodeUnauthenticated {
		t.Errorf("an anonymous password change: %v, want unauthenticated", err)
	}

	if _, err := carl.ChangePassword(ctx, &relicv1.ChangePasswordRequest{
		CurrentPassword: password,
		NewPassword:     newPassword,
	}); err != nil {
		t.Fatalf("change the password: %v", err)
	}

	signIn := func(secret string) error {
		_, err := relicv1connect.NewSessionServiceClient(apiClient(relicHTTP("carl", secret), srv.URL)).
			GetCurrentUser(ctx, &relicv1.GetCurrentUserRequest{})

		return err
	}

	if err := signIn(password); code(err) != connect.CodeUnauthenticated {
		t.Errorf("sign in with the old password: %v, want unauthenticated", err)
	}

	if err := signIn(newPassword); err != nil {
		t.Errorf("sign in with the new password: %v", err)
	}

	events, err := relicv1connect.NewAuditServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL)).
		ListAuditEvents(ctx, &relicv1.ListAuditEventsRequest{
			Actor: "carl",
		})
	if err != nil {
		t.Fatal(err)
	}

	changed := false
	for _, e := range events.GetEvents() {
		changed = changed || e.GetAction() == "relic.v1.SessionService/ChangePassword" && e.GetTarget() == "carl" &&
			e.GetOutcome() == relicv1.AuditOutcome_AUDIT_OUTCOME_SUCCESS
	}

	if !changed {
		t.Errorf("audit events = %v, want carl's password change", events.GetEvents())
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
	repos := relicv1connect.NewRepositoryServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	create := func(r *relicv1.Repository) error {
		_, err := repos.CreateRepository(ctx, &relicv1.CreateRepositoryRequest{
			Repository: r,
		})

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

	gomod, err := repos.GetRepository(ctx, &relicv1.GetRepositoryRequest{
		Name: "golibs",
	})
	if err != nil || gomod.GetRepository().GetFormat() != relicv1.Format_FORMAT_GOMOD {
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

	got, err := repos.GetRepository(ctx, &relicv1.GetRepositoryRequest{
		Name: "central",
	})
	if err != nil || got.GetRepository().GetRemote().GetUrl() != "https://repo1.maven.org/maven2" ||
		got.GetRepository().GetRemote().GetPassword() != "" {
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

	upd := got.GetRepository()

	upd.Description, upd.Remote.Offline = "maven central", true
	if _, err := repos.UpdateRepository(
		ctx,
		&relicv1.UpdateRepositoryRequest{
			Repository: upd,
		},
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
	dev := relicv1connect.NewRepositoryServiceClient(apiClient(relicHTTP("dev", "Dev-pass-12345"), srv.URL))

	list, err := dev.ListRepositories(ctx, &relicv1.ListRepositoriesRequest{})
	if err != nil || len(list.GetRepositories()) != 1 || list.GetRepositories()[0].GetName() != "libs" {
		t.Errorf("dev list = %v %v", list, err)
	}

	if _, err := dev.GetRepository(
		ctx,
		&relicv1.GetRepositoryRequest{
			Name: "central",
		},
	); code(err) != connect.CodeNotFound {
		t.Errorf("dev get unreadable: %v", err)
	}

	if _, err := dev.DeleteRepository(
		ctx,
		&relicv1.DeleteRepositoryRequest{
			Name: "libs",
		},
	); code(err) != connect.CodePermissionDenied {
		t.Errorf("dev delete: %v", err)
	}

	filtered, err := repos.ListRepositories(
		ctx,
		&relicv1.ListRepositoriesRequest{
			Class: relicv1.RepositoryClass_REPOSITORY_CLASS_REMOTE,
		},
	)
	if err != nil || len(filtered.GetRepositories()) != 1 {
		t.Errorf("remote filter = %v %v", filtered, err)
	}

	if _, err := repos.DeleteRepository(
		ctx,
		&relicv1.DeleteRepositoryRequest{
			Name: "maven",
		},
	); err != nil {
		t.Fatal(err)
	}

	if _, err := repos.GetRepository(
		ctx,
		&relicv1.GetRepositoryRequest{
			Name: "maven",
		},
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

	if code, _ := put(dev, "libs/app/2.0/app.bin?build.name=team/app&build.number=7", "other"); code != 403 {
		t.Errorf("upload with properties without annotate = %d, want 403", code)
	}

	if code, body := put(admin, "libs/app/2.0/app.bin?build.name=team/app&build.number=7", "other"); code != 201 {
		t.Fatalf("upload = %d %s", code, body)
	}

	if code, _ := put(dev, "libs/app/2.0/bad.bin?bad%3Dkey=x", "x"); code != 400 {
		t.Errorf("upload with an invalid property key = %d, want 400", code)
	}

	uploaded, err := relicv1connect.NewArtifactServiceClient(apiClient(&http.Client{
		Transport: dev,
	}, srv.URL)).GetProperties(ctx, &relicv1.GetPropertiesRequest{
		Repository: "libs",
		Path:       "app/2.0/app.bin",
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := uploaded.GetProperties(); strings.Join(got["build.name"].GetValues(), ",") != "team/app" ||
		strings.Join(got["build.number"].GetValues(), ",") != "7" {
		t.Errorf("properties set by the upload = %v", got)
	}

	_, err = relicv1connect.NewArtifactServiceClient(apiClient(&http.Client{
		Transport: dev,
	}, srv.URL)).CopyItem(ctx, &relicv1.CopyItemRequest{
		Repository:   "libs",
		Path:         "app/2.0/app.bin",
		ToRepository: "libs",
		ToPath:       "app/1.0/app.txt",
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("copy over a file without delete = %v, want permission denied", err)
	}

	copyReq, _ := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		srv.URL+"/artifactory/api/copy/libs/app/2.0/app.bin?to=/libs/app/1.0/app.txt",
		nil,
	)
	if resp, err := (&http.Client{
		Transport: dev,
	}).Do(copyReq); err != nil {
		t.Fatal(err)
	} else {
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("adapter copy over a file without delete = %d, want 403", resp.StatusCode)
		}
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

	arts := relicv1connect.NewArtifactServiceClient(apiClient(&http.Client{
		Transport: dev,
	}, srv.URL))
	adminArts := relicv1connect.NewArtifactServiceClient(apiClient(&http.Client{
		Transport: admin,
	}, srv.URL))

	st, err := arts.StatItem(
		ctx,
		&relicv1.StatItemRequest{
			Repository: "libs",
			Path:       "app/1.0/app.txt",
		},
	)
	if err != nil || st.GetItem().GetSize() != 5 || st.GetItem().GetChecksums().GetSha256() == "" {
		t.Fatalf("stat = %v %v", st, err)
	}

	kids, err := arts.ListChildren(ctx, &relicv1.ListChildrenRequest{
		Repository: "libs",
	})
	if err != nil || len(kids.GetItems()) != 1 || kids.GetItems()[0].GetName() != "app" {
		t.Errorf("children = %v %v", kids, err)
	}

	found, err := arts.SearchItems(ctx, &relicv1.SearchItemsRequest{
		Name: ".txt",
	})
	if err != nil || len(found.GetItems()) != 1 {
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
		&relicv1.SetPropertiesRequest{
			Repository: "libs",
			Path:       "app/1.0/app.txt",
			Properties: props,
		},
	); code(err) != connect.CodePermissionDenied {
		t.Errorf("set properties without annotate: %v", err)
	}

	if _, err := adminArts.SetProperties(
		ctx,
		&relicv1.SetPropertiesRequest{
			Repository: "libs",
			Path:       "app/1.0/app.txt",
			Properties: props,
		},
	); err != nil {
		t.Fatal(err)
	}

	got, err := arts.GetProperties(
		ctx,
		&relicv1.GetPropertiesRequest{
			Repository: "libs",
			Path:       "app/1.0/app.txt",
		},
	)
	if err != nil || got.GetProperties()["qa"].GetValues()[0] != "passed" {
		t.Errorf("properties = %v %v", got, err)
	}

	if _, err := adminArts.DeleteProperties(
		ctx,
		&relicv1.DeletePropertiesRequest{
			Repository: "libs",
			Path:       "app/1.0/app.txt",
			Keys: []string{
				"qa",
			},
		},
	); err != nil {
		t.Fatal(err)
	}

	if _, err := arts.CopyItem(
		ctx,
		&relicv1.CopyItemRequest{
			Repository:   "libs",
			Path:         "app/1.0",
			ToRepository: "libs",
			ToPath:       "app/1.1",
		},
	); err != nil {
		t.Fatalf("copy: %v", err)
	}

	if _, err := arts.MoveItem(
		ctx,
		&relicv1.MoveItemRequest{
			Repository:   "libs",
			Path:         "app/1.1",
			ToRepository: "libs",
			ToPath:       "app/1.2",
		},
	); code(err) != connect.CodePermissionDenied {
		t.Errorf("move without delete: %v", err)
	}

	if _, err := adminArts.MoveItem(
		ctx,
		&relicv1.MoveItemRequest{
			Repository:   "libs",
			Path:         "app/1.1",
			ToRepository: "other",
			ToPath:       "app/1.1",
		},
	); err != nil {
		t.Fatal(err)
	}

	if _, err := arts.DeleteItem(
		ctx,
		&relicv1.DeleteItemRequest{
			Repository: "libs",
			Path:       "app/1.0",
		},
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
		&relicv1.DeleteItemRequest{
			Repository: "libs",
			Path:       "app/1.0",
		},
	); code(err) != connect.CodePermissionDenied {
		t.Errorf("folder delete with an undeletable file below: %v", err)
	}

	if _, err := adminArts.DeleteItem(
		ctx,
		&relicv1.DeleteItemRequest{
			Repository: "libs",
		},
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("delete the root: %v", err)
	}

	if _, err := adminArts.DeleteItem(
		ctx,
		&relicv1.DeleteItemRequest{
			Repository: "libs",
			Path:       "app/1.0",
		},
	); err != nil {
		t.Fatal(err)
	}

	if _, err := adminArts.StatItem(
		ctx,
		&relicv1.StatItemRequest{
			Repository: "libs",
			Path:       "app/1.0/app.txt",
		},
	); code(err) != connect.CodeNotFound {
		t.Errorf("after delete: %v", err)
	}
}

func TestRelicAPISecurity(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()

	sec := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	if _, err := sec.PutUser(
		ctx,
		&relicv1.PutUserRequest{
			User: &relicv1.User{
				Name:  "ann",
				Email: "ann@example.com",
			},
		},
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("new user without password: %v", err)
	}

	u, err := sec.PutUser(
		ctx,
		&relicv1.PutUserRequest{
			User: &relicv1.User{
				Name:     "ann",
				Email:    "ann@example.com",
				Password: "Ann-pass-12345",
			},
		},
	)
	if err != nil || !u.GetCreated() || u.GetUser().GetRealm() != relicv1.Realm_REALM_INTERNAL ||
		u.GetUser().GetPassword() != "" {
		t.Fatalf("put user = %v %v", u, err)
	}

	g, err := sec.PutGroup(
		ctx,
		&relicv1.PutGroupRequest{
			Group: &relicv1.Group{
				Name:        "devs",
				Description: "developers",
				Members: []string{
					"ann",
				},
			},
		},
	)
	if err != nil || g.GetGroup().GetMembers()[0] != "ann" {
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
	if _, err := sec.PutPermission(ctx, &relicv1.PutPermissionRequest{
		Permission: p,
	}); err != nil {
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
		&relicv1.PutPermissionRequest{
			Permission: p,
		},
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("unknown action: %v", err)
	}

	p.Rules = append(p.Rules, p.Rules[0])
	grant.Actions = []relicv1.Action{
		relicv1.Action_ACTION_READ,
	}

	if _, err := sec.PutPermission(
		ctx,
		&relicv1.PutPermissionRequest{
			Permission: p,
		},
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("two repository rules: %v", err)
	}

	users, err := sec.ListUsers(ctx, &relicv1.ListUsersRequest{})
	if err != nil || len(users.GetUsers()) < 2 {
		t.Errorf("users = %v %v", users, err)
	}

	perms, err := sec.ListPermissions(ctx, &relicv1.ListPermissionsRequest{})
	if err != nil || len(perms.GetPermissions()) != 1 {
		t.Errorf("permissions = %v %v", perms, err)
	}

	racing := make(chan error, 8)
	for range cap(racing) {
		go func() {
			_, err := sec.PutPermission(ctx, &relicv1.PutPermissionRequest{
				Permission: &relicv1.Permission{
					Name: "racing",
				},
			})
			racing <- err
		}()
	}

	for range cap(racing) {
		if err := <-racing; err != nil {
			t.Errorf("concurrent put of a new permission: %v", err)
		}
	}

	ann := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("ann", "Ann-pass-12345"), srv.URL))
	if _, err := ann.ListUsers(
		ctx,
		&relicv1.ListUsersRequest{},
	); code(err) != connect.CodePermissionDenied {
		t.Errorf("non-admin list users: %v", err)
	}

	if _, err := ann.CreateToken(
		ctx,
		&relicv1.CreateTokenRequest{
			ExpiresInSeconds: 9999 * 24 * 60 * 60,
		},
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("token too long: %v", err)
	}

	tok, err := ann.CreateToken(ctx, &relicv1.CreateTokenRequest{
		Description:      "ci",
		ExpiresInSeconds: 24 * 60 * 60,
	})
	if err != nil || tok.GetAccessToken() == "" || tok.GetExpiresIn() != 86400 {
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
				"Authorization": "Bearer " + tok.GetAccessToken(),
			},
		},
	)
	if resp.StatusCode != 200 {
		t.Errorf("token on the Artifactory API = %d", resp.StatusCode)
	}

	if _, err := ann.CreateToken(
		ctx,
		&relicv1.CreateTokenRequest{
			Username: "admin",
		},
	); code(err) != connect.CodePermissionDenied {
		t.Errorf("a non-admin's token for another user = %v, want PermissionDenied", err)
	}

	forAnn, err := sec.CreateToken(ctx, &relicv1.CreateTokenRequest{
		Description:      "short-lived for ann",
		ExpiresInSeconds: 600,
		Username:         "ann",
		Refreshable:      true,
	})
	if err != nil || forAnn.GetExpiresIn() != 600 || forAnn.GetRefreshToken() == "" {
		t.Fatalf("an administrator's refreshable 10-minute token for ann = %v %v", forAnn, err)
	}

	annTokens, err := sec.ListTokens(ctx, &relicv1.ListTokensRequest{
		Username: "ann",
	})
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, at := range annTokens.GetTokens() {
		found = found || at.GetDescription() == "short-lived for ann" && at.GetRefreshable()
	}

	if !found {
		t.Errorf("ann's tokens %v lack the refreshable one the administrator created", annTokens.GetTokens())
	}

	if _, err := sec.CreateToken(
		ctx,
		&relicv1.CreateTokenRequest{
			Username: "no-such-user",
		},
	); code(err) != connect.CodeNotFound {
		t.Errorf("a token for an unknown user = %v, want NotFound", err)
	}

	if _, err := ann.CreateToken(
		ctx,
		&relicv1.CreateTokenRequest{
			Scope: &relicv1.TokenScope{
				Kind: relicv1.TokenScopeKind(99),
			},
		},
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
			&relicv1.CreateTokenRequest{
				Scope: scope,
			},
		); code(err) != connect.CodePermissionDenied {
			t.Errorf("%s scope: %v", name, err)
		}
	}

	if _, err := relicv1connect.NewProjectServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL)).PutProject(
		ctx,
		&relicv1.PutProjectRequest{
			Project: &relicv1.Project{
				Key:  "tokens",
				Name: "Tokens",
			},
		},
	); err != nil {
		t.Fatal(err)
	}

	for name, key := range map[string]string{
		"an unknown project":                    "nosuchproject",
		"a project the user is not a member of": "tokens",
	} {
		if _, err := ann.CreateToken(
			ctx,
			&relicv1.CreateTokenRequest{
				ExpiresInSeconds: 24 * 60 * 60,
				Project:          key,
			},
		); code(err) != connect.CodeNotFound {
			t.Errorf("token labelled with %s = %v, want NotFound", name, err)
		}
	}

	if _, err := sec.CreateToken(
		ctx,
		&relicv1.CreateTokenRequest{
			ExpiresInSeconds: 24 * 60 * 60,
			Project:          "tokens",
		},
	); err != nil {
		t.Errorf("token labelled with a project an administrator sees = %v", err)
	}

	forever, err := ann.CreateToken(ctx, &relicv1.CreateTokenRequest{
		Description: "forever",
	})
	if err != nil || forever.GetExpiresIn() != int64(auth.MaxUserTokenLifetime/time.Second) {
		t.Errorf("never-expiring token of a user = %v %v", forever, err)
	}

	scoped, err := ann.CreateToken(ctx, &relicv1.CreateTokenRequest{
		Description:      "devs",
		ExpiresInSeconds: 24 * 60 * 60,
		Scope: &relicv1.TokenScope{
			Kind: relicv1.TokenScopeKind_TOKEN_SCOPE_KIND_GROUPS,
			Groups: []string{
				"devs",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	viaScoped := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("ann", scoped.GetAccessToken()), srv.URL))

	minted, err := viaScoped.CreateToken(
		ctx,
		&relicv1.CreateTokenRequest{
			Description:      "wider",
			ExpiresInSeconds: 24 * 60 * 60,
		},
	)
	if err != nil || minted.GetScope().GetKind() != relicv1.TokenScopeKind_TOKEN_SCOPE_KIND_GROUPS ||
		strings.Join(minted.GetScope().GetGroups(), ",") != "devs" {
		t.Errorf("token minted by a group-scoped token = %v %v", minted, err)
	}

	const cliSession = "0b7e3f4a-5c6d-4e8f-9a0b-1c2d3e4f5a6b"

	sessions := relicv1connect.NewSessionServiceClient(apiClient(http.DefaultClient, srv.URL))
	if _, err := sessions.StartWebLogin(ctx, &relicv1.StartWebLoginRequest{
		Session: cliSession,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := relicv1connect.NewSessionServiceClient(apiClient(
		relicHTTP("ann", scoped.GetAccessToken()),
		srv.URL,
	)).ApproveLogin(ctx, &relicv1.ApproveLoginRequest{
		Session: cliSession,
		Code:    cliSession[len(cliSession)-4:],
	}); code(err) != connect.CodePermissionDenied {
		t.Errorf("a group-scoped token approving a CLI sign-in: %v", err)
	}

	signIn := &relicv1.LoginRequest{
		Username: "ann",
		Password: scoped.GetAccessToken(),
	}
	signInCtx, signInInfo := connect.NewClientContext(ctx)
	signInInfo.RequestHeader().Set(auth.UIHeader, "1")

	if _, err := sessions.Login(signInCtx, signIn); err != nil {
		t.Fatal(err)
	}

	sessionCookie, err := http.ParseSetCookie(signInInfo.ResponseHeader().Get("Set-Cookie"))
	if err != nil {
		t.Fatal(err)
	}

	approveWithSession := &relicv1.ApproveLoginRequest{
		Session: cliSession,
		Code:    cliSession[len(cliSession)-4:],
	}
	approveCtx, approveInfo := connect.NewClientContext(ctx)
	approveInfo.RequestHeader().Set("Cookie", sessionCookie.Name+"="+sessionCookie.Value)
	approveInfo.RequestHeader().Set(auth.UIHeader, "1")

	if _, err := sessions.ApproveLogin(approveCtx, approveWithSession); code(err) != connect.CodePermissionDenied {
		t.Errorf("a UI session signed in with a group-scoped token approving a CLI sign-in: %v", err)
	}

	for _, err := range []error{
		func() error {
			_, err := sec.DeletePermission(ctx, &relicv1.DeletePermissionRequest{
				Name: "devs-libs",
			})

			return err
		}(),
		func() error {
			_, err := sec.DeleteGroup(ctx, &relicv1.DeleteGroupRequest{
				Name: "devs",
			})

			return err
		}(),
		func() error {
			_, err := sec.DeleteUser(ctx, &relicv1.DeleteUserRequest{
				Name: "ann",
			})

			return err
		}(),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}

	if _, err := sec.GetUser(
		ctx,
		&relicv1.GetUserRequest{
			Name: "ann",
		},
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
	reps := relicv1connect.NewReplicationServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

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
		&relicv1.PutReplicationRequest{
			Replication: r,
		},
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
			&relicv1.PutReplicationRequest{
				Replication: r,
			},
		); code(err) != connect.CodeInvalidArgument {
			t.Errorf("schedule %q: %v", bad, err)
		}
	}

	r.Schedule = "0 12 * * *"
	r.Direction = relicv1.ReplicationDirection(99)

	if _, err := reps.PutReplication(
		ctx,
		&relicv1.PutReplicationRequest{
			Replication: r,
		},
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("unknown direction: %v", err)
	}

	r.Direction = relicv1.ReplicationDirection_REPLICATION_DIRECTION_UNSPECIFIED

	got, err := reps.GetReplication(ctx, &relicv1.GetReplicationRequest{
		Repository: "libs",
	})
	if m := got.GetReplication(); err != nil || !m.GetHasPassword() || m.GetPassword() != "" || !m.GetOnChange() ||
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

	got, err = reps.GetReplication(ctx, &relicv1.GetReplicationRequest{
		Repository: "libs",
	})
	if err != nil || got.GetReplication().GetSchedule() != "30 2 * * 1-5" ||
		got.GetReplication().GetTimeoutSeconds() != 2 {
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
		&relicv1.DeleteReplicationRequest{
			Repository: "libs",
		},
	); err != nil {
		t.Fatal(err)
	}

	if _, err := reps.GetReplication(
		ctx,
		&relicv1.GetReplicationRequest{
			Repository: "libs",
		},
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
	builds := relicv1connect.NewBuildServiceClient(apiClient(relicHTTP("dev", "Dev-pass-12345"), srv.URL))

	list, err := builds.ListBuilds(ctx, &relicv1.ListBuildsRequest{})
	if err != nil || len(list.GetBuilds()) != 1 || list.GetBuilds()[0].GetName() != "app" {
		t.Errorf("builds = %v %v", list, err)
	}

	if runs, err := builds.ListBuildRuns(
		ctx,
		&relicv1.ListBuildRunsRequest{
			Name: "app",
		},
	); err != nil ||
		len(runs.GetRuns()) != 1 {
		t.Errorf("runs = %v %v", runs, err)
	}

	if _, err := builds.ListBuildRuns(
		ctx,
		&relicv1.ListBuildRunsRequest{
			Name: "secret",
		},
	); code(err) != connect.CodeNotFound {
		t.Errorf("unreadable build: %v", err)
	}
}

func TestRelicAPIScan(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()
	scans := relicv1connect.NewScanServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

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
	if _, err := scans.PutPolicy(ctx, &relicv1.PutPolicyRequest{
		Policy: pol,
	}); err != nil {
		t.Fatal(err)
	}

	pol.Description = "updated"
	if _, err := scans.PutPolicy(ctx, &relicv1.PutPolicyRequest{
		Policy: pol,
	}); err != nil {
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
		&relicv1.PutWatchRequest{
			Watch: &relicv1.Watch{
				Name:   "w",
				Active: true,
				Policies: []string{
					"nope",
				},
			},
		},
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
	if _, err := scans.PutWatch(ctx, &relicv1.PutWatchRequest{
		Watch: w,
	}); err != nil {
		t.Fatal(err)
	}

	if ws, err := scans.ListWatches(ctx, &relicv1.ListWatchesRequest{}); err != nil ||
		ws.GetWatches()[0].GetPolicies()[0] != "critical" {
		t.Errorf("watches = %v %v", ws, err)
	}

	rule, err := scans.CreateIgnoreRule(
		ctx,
		&relicv1.CreateIgnoreRuleRequest{
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
		},
	)
	if err != nil || rule.GetRule().GetId() == "" ||
		rule.GetRule().GetPackages()[0] != "pkg:maven/org.apache/log4j@2.14.1" ||
		rule.GetRule().GetBuilds()[0].GetNumber() != "7" {
		t.Fatalf("ignore rule = %v %v", rule, err)
	}

	if _, b := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/xray/api/v1/ignore_rules/" + rule.GetRule().GetId(),
		},
	); !strings.Contains(b, `"name": "gav://org.apache:log4j"`) ||
		!strings.Contains(b, `"version": "2.14.1"`) {
		t.Errorf("xray view of the ignore rule = %s", b)
	}

	if _, err := scans.DeletePolicy(
		ctx,
		&relicv1.DeletePolicyRequest{
			Name: "critical",
		},
	); code(err) != connect.CodeFailedPrecondition {
		t.Errorf("delete a watched policy: %v", err)
	}

	if _, err := scans.DeleteIgnoreRule(
		ctx,
		&relicv1.DeleteIgnoreRuleRequest{
			Id: rule.GetRule().GetId(),
		},
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
		&relicv1.GetItemScanRequest{
			Repository: "libs",
			Path:       "app.jar",
		},
	)
	if err != nil || s.GetScan().GetStatus() != relicv1.ScanStatus_SCAN_STATUS_QUEUED {
		t.Errorf("unscanned = %v %v", s, err)
	}

	if f, err := scans.ListFindings(ctx, &relicv1.ListFindingsRequest{}); err != nil ||
		f.GetTotal() != 0 {
		t.Errorf("findings = %v %v", f, err)
	}

	if c, err := scans.ListCves(ctx, &relicv1.ListCvesRequest{}); err != nil ||
		c.GetTotal() != 0 {
		t.Errorf("cves = %v %v", c, err)
	}

	if _, err := scans.GetCve(
		ctx,
		&relicv1.GetCveRequest{
			Id: "CVE-2000-0001",
		},
	); code(err) != connect.CodeNotFound {
		t.Errorf("unknown CVE: %v", err)
	}
}

func TestRelicAPILdap(t *testing.T) {
	t.Parallel()
	srv, db := serverDB(t)
	ctx := t.Context()
	ldapc := relicv1connect.NewLdapServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

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
		&relicv1.PutLdapServerRequest{
			LdapServer: dir,
		},
	); err != nil {
		t.Fatal(err)
	}

	if _, err := ldapc.PutLdapServer(
		ctx,
		&relicv1.PutLdapServerRequest{
			LdapServer: &relicv1.LdapServer{
				Name: "bad",
				Url:  "http://x",
			},
		},
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("bad URL: %v", err)
	}

	list, err := ldapc.ListLdapServers(ctx, &relicv1.ListLdapServersRequest{})
	if err != nil || len(list.GetLdapServers()) != 1 || !list.GetLdapServers()[0].GetHasManagerPassword() ||
		list.GetLdapServers()[0].GetSearch().GetManagerPassword() != "" {
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
		&relicv1.PutLdapGroupMappingRequest{
			LdapGroupMapping: m,
		},
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

	if _, err := ldapc.PutLdapGroupMapping(ctx, &relicv1.PutLdapGroupMappingRequest{
		LdapGroupMapping: &relicv1.LdapGroupMapping{
			Name:            "legacy",
			LdapServer:      "corp",
			NameAttribute:   "cn",
			MemberAttribute: "member",
			Strategy:        relicv1.GroupStrategy_GROUP_STRATEGY_STATIC,
		},
	}); err == nil {
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

	ms, err := ldapc.ListLdapGroupMappings(ctx, &relicv1.ListLdapGroupMappingsRequest{})
	if err != nil || len(ms.GetLdapGroupMappings()) != 2 ||
		ms.GetLdapGroupMappings()[1].GetStrategy() != relicv1.GroupStrategy_GROUP_STRATEGY_HIERARCHY {
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
		&relicv1.TestLdapSignInRequest{
			LdapServer: dir,
			Username:   "ann",
			Password:   "x",
		},
	)
	if err != nil || res.GetOk() || res.GetError() == "" {
		t.Errorf("test sign-in against nothing = %v %v", res, err)
	}

	for _, m := range []string{
		"corp-groups",
		"corp-ous",
	} {
		if _, err := ldapc.DeleteLdapGroupMapping(
			ctx,
			&relicv1.DeleteLdapGroupMappingRequest{
				Name: m,
			},
		); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := ldapc.DeleteLdapServer(
		ctx,
		&relicv1.DeleteLdapServerRequest{
			Name: "corp",
		},
	); err != nil {
		t.Fatal(err)
	}

	if _, err := ldapc.DeleteLdapServer(
		ctx,
		&relicv1.DeleteLdapServerRequest{
			Name: "corp",
		},
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

	arts := relicv1connect.NewArtifactServiceClient(apiClient(admin, srv.URL))
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/storage/libs/app/app.zip?properties=qa=passed",
	})

	det, err := arts.GetItemDetails(
		ctx,
		&relicv1.GetItemDetailsRequest{
			Repository: "libs",
			Path:       "app/app.zip",
		},
	)
	if err != nil || !det.GetCanDelete() || !det.GetCanAnnotate() ||
		det.GetProperties()["qa"].GetValues()[0] != "passed" ||
		det.GetScan().GetStatus() != relicv1.ScanStatus_SCAN_STATUS_QUEUED ||
		det.GetFormat() != relicv1.Format_FORMAT_GENERIC {
		t.Fatalf("details = %v %v", det, err)
	}

	entries, err := arts.ListArchiveEntries(
		ctx,
		&relicv1.ListArchiveEntriesRequest{
			Repository: "libs",
			Path:       "app/app.zip",
		},
	)
	if err != nil || entries.GetKind() != "zip" || len(entries.GetEntries()) != 1 {
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
		&relicv1.ListArchiveEntriesRequest{
			Repository: "npm-local",
			Path:       "left-pad",
		},
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("folder as archive: %v", err)
	}

	grants, err := arts.GetEffectivePermissions(
		ctx,
		&relicv1.GetEffectivePermissionsRequest{
			Repository: "libs",
			Path:       "app/app.zip",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	_ = grants

	scans := relicv1connect.NewScanServiceClient(apiClient(admin, srv.URL))
	if _, err := scans.GetItemSbom(
		ctx,
		&relicv1.GetItemSbomRequest{
			Repository: "libs",
			Path:       "app/app.zip",
		},
	); code(err) != connect.CodeNotFound {
		t.Errorf("sbom of an unscanned file: %v", err)
	}

	pkgs := relicv1connect.NewPackageServiceClient(apiClient(admin, srv.URL))

	np, err := pkgs.GetNpmPackage(
		ctx,
		&relicv1.GetNpmPackageRequest{
			Repository: "npm-local",
			Path:       "left-pad/-/left-pad-1.3.0.tgz",
		},
	)
	if err != nil || np.GetName() != "left-pad" || np.GetDependencies()["x"] != "^1" {
		t.Errorf("npm package = %v %v", np, err)
	}

	if _, err := pkgs.GetContainerImage(
		ctx,
		&relicv1.GetContainerImageRequest{
			Repository: "libs",
			Path:       "app",
		},
	); code(err) != connect.CodeNotFound {
		t.Errorf("image of a plain folder: %v", err)
	}

	if _, err := pkgs.GetPythonPackage(
		ctx,
		&relicv1.GetPythonPackageRequest{
			Repository: "libs",
			Path:       "app/app.zip",
		},
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

	run, err := relicv1connect.NewBuildServiceClient(apiClient(admin, srv.URL)).
		GetBuildRun(ctx, &relicv1.GetBuildRunRequest{
			Name:   "app",
			Number: "7",
		})
	if err != nil || run.GetAgent() != "jf 2.124.0" || run.GetStarted() == nil ||
		len(run.GetModules()) != 1 ||
		run.GetModules()[0].GetDependencies()[0].GetScopes()[0] != "compile" ||
		run.GetVcs()[0].GetRevision() != "abc" {
		t.Errorf("run = %v %v", run, err)
	}

	sys := relicv1connect.NewSystemServiceClient(apiClient(admin, srv.URL))

	ov, err := sys.GetDashboardSummary(ctx, &relicv1.GetDashboardSummaryRequest{})
	if err != nil || ov.GetRepositories()["local"] != 3 || ov.GetBuilds() != 1 {
		t.Errorf("overview = %v %v", ov, err)
	}

	trash := relicv1connect.NewTrashServiceClient(apiClient(admin, srv.URL))

	if _, err := arts.DeleteItem(
		ctx,
		&relicv1.DeleteItemRequest{
			Repository: "libs",
			Path:       "app/app.zip",
		},
	); err != nil {
		t.Fatal(err)
	}

	list, err := trash.ListTrash(ctx, &relicv1.ListTrashRequest{})
	if err != nil || len(list.GetEntries()) != 1 || list.GetEntries()[0].GetOriginalRepository() != "libs" {
		t.Fatalf("trash = %v %v", list, err)
	}

	if _, err := trash.RestoreTrash(
		ctx,
		&relicv1.RestoreTrashRequest{
			Path: list.GetEntries()[0].GetPath(),
		},
	); err != nil {
		t.Fatal(err)
	}

	if _, err := arts.StatItem(
		ctx,
		&relicv1.StatItemRequest{
			Repository: "libs",
			Path:       "app/app.zip",
		},
	); err != nil {
		t.Errorf("after restore: %v", err)
	}

	if _, err := trash.EmptyTrash(ctx, &relicv1.EmptyTrashRequest{}); err != nil {
		t.Fatal(err)
	}

	sessions := relicv1connect.NewSessionServiceClient(apiClient(admin, srv.URL))
	if _, err := sessions.ApproveLogin(
		ctx,
		&relicv1.ApproveLoginRequest{
			Session: "nope",
			Code:    "1234",
		},
	); code(err) != connect.CodeNotFound {
		t.Errorf("unknown jf login: %v", err)
	}
}
