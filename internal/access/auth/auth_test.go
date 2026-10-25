package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"relic/internal/platform/database"
	"relic/internal/platform/sealing"
)

type fakeUsers struct {
	users   map[string]database.User
	lookups atomic.Int32
}

func (f *fakeUsers) GetUserByUsername(_ context.Context, username string) (database.User, error) {
	f.lookups.Add(1)

	u, ok := f.users[username]
	if !ok {
		return database.User{}, database.ErrNotFound
	}

	return u, nil
}

func newTestAuthenticator(t *testing.T) *Authenticator {
	t.Helper()

	adminHash, err := HashPassword("adminpw")
	if err != nil {
		t.Fatal(err)
	}

	userHash, err := HashPassword("userpw")
	if err != nil {
		t.Fatal(err)
	}

	users := &fakeUsers{
		users: map[string]database.User{
			"admin": {
				ID:           uuid.New(),
				Username:     "admin",
				PasswordHash: adminHash,
				Admin:        true,
			},
			"bob": {
				ID:           uuid.New(),
				Username:     "bob",
				PasswordHash: userHash,
			},
		},
	}

	a := NewAuthenticator(users, fakeGroups{}, fakeTokens{}, slog.New(slog.DiscardHandler))
	a.SetPasswordSealing(testPasswordBox(t))

	return a
}

func testPasswordBox(t *testing.T) *sealing.Box {
	t.Helper()

	box, err := sealing.New(bytes.Repeat([]byte{
		3,
	}, 32))
	if err != nil {
		t.Fatal(err)
	}

	return box
}

func sealedPassword(t *testing.T, username, password string) string {
	t.Helper()

	sealed, err := SealPassword(testPasswordBox(t), username, password)
	if err != nil {
		t.Fatal(err)
	}

	if !sealing.SealedString(sealed) {
		t.Fatalf("sealed password %q shows the password", sealed)
	}

	return sealed
}

type fakeGroups struct{}

func (fakeGroups) Groups(_ context.Context, username string) ([]string, Privileges, error) {
	admin := Privileges{
		Admin: true,
	}
	viewer := Privileges{
		Viewer: true,
	}

	switch username {
	case "bob":
		return []string{
			"devs",
		}, Privileges{}, nil
	case "opsuser":
		return []string{
			"ops",
		}, admin, nil
	case "auditor":
		return []string{
			"auditors",
		}, viewer, nil
	}

	return nil, Privileges{}, nil
}

type fakeTokens struct{}

func (fakeTokens) Verify(_ context.Context, token string) (Token, error) {
	name, ok := strings.CutPrefix(token, "good-")
	if !ok {
		return Token{}, ErrInvalidToken
	}

	return Token{
		Username: name,
		Scope:    UserScope,
	}, nil
}

func TestMiddlewareAndRequire(t *testing.T) {
	t.Parallel()
	a := newTestAuthenticator(t)
	sealedBob := sealedPassword(t, "bob", "userpw")
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(FromContext(r.Context()).Name()))
	})

	tests := []struct {
		name     string
		access   Access
		setAuth  func(r *http.Request)
		wantCode int
		wantBody string
	}{
		{
			name:     "public anonymous",
			access:   Public,
			wantCode: http.StatusOK,
			wantBody: "anonymous",
		},
		{
			name:     "public bad credentials",
			access:   Public,
			setAuth:  basic("bob", "nope"),
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "authenticated anonymous",
			access:   Authenticated,
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "authenticated user",
			access:   Authenticated,
			setAuth:  basic("bob", "userpw"),
			wantCode: http.StatusOK,
			wantBody: "bob",
		},
		{
			name:     "unknown user",
			access:   Authenticated,
			setAuth:  basic("eve", "x"),
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "sealed password",
			access:   Authenticated,
			setAuth:  basic("bob", sealedBob),
			wantCode: http.StatusOK,
			wantBody: "bob",
		},
		{
			name:     "sealed password with the username in other case",
			access:   Authenticated,
			setAuth:  basic("bob", sealedPassword(t, "Bob", "userpw")),
			wantCode: http.StatusOK,
			wantBody: "bob",
		},
		{
			name:     "sealed password of another user",
			access:   Authenticated,
			setAuth:  basic("admin", sealedBob),
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "tampered sealed password",
			access:   Authenticated,
			setAuth:  basic("bob", tampered(sealedBob)),
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "sealed wrong password",
			access:   Authenticated,
			setAuth:  basic("bob", sealedPassword(t, "bob", "nope")),
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "admin as user",
			access:   Admin,
			setAuth:  basic("bob", "userpw"),
			wantCode: http.StatusForbidden,
		},
		{
			name:     "admin as admin",
			access:   Admin,
			setAuth:  basic("admin", "adminpw"),
			wantCode: http.StatusOK,
			wantBody: "admin",
		},
		{
			name:     "bearer token",
			access:   Admin,
			setAuth:  bearer("good-admin"),
			wantCode: http.StatusOK,
			wantBody: "admin",
		},
		{
			name:     "bearer bad token",
			access:   Authenticated,
			setAuth:  bearer("forged"),
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "bearer for deleted user",
			access:   Authenticated,
			setAuth:  bearer("good-ghost"),
			wantCode: http.StatusUnauthorized,
		},
		{
			name:   "malformed header",
			access: Authenticated,
			setAuth: func(r *http.Request) {
				r.Header.Set("Authorization", "Negotiate x")
			},
			wantCode: http.StatusUnauthorized,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			if tt.setAuth != nil {
				tt.setAuth(req)
			}

			rec := httptest.NewRecorder()
			a.Middleware(Require(tt.access, ok)).ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}

			if tt.wantBody != "" && rec.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want %q", rec.Body.String(), tt.wantBody)
			}

			if rec.Code == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("401 without WWW-Authenticate")
			}
		})
	}
}

func TestVerifiedPasswordsAreCachedAndExpire(t *testing.T) {
	t.Parallel()
	a := newTestAuthenticator(t)
	now := time.Now()
	a.now = func() time.Time {
		return now
	}
	ctx := t.Context()

	if _, err := a.password(ctx, "bob", "userpw"); err != nil {
		t.Fatal(err)
	}

	key := func() int {
		a.mu.Lock()
		defer a.mu.Unlock()

		return len(a.verified)
	}
	if key() != 1 {
		t.Fatalf("cache size = %d, want 1", key())
	}

	if _, err := a.password(ctx, "bob", "wrong"); err == nil {
		t.Fatal("wrong password accepted after a cached success")
	}

	now = now.Add(verifiedTTL + time.Second)

	if a.recentlyVerified([32]byte{}) {
		t.Error("zero key reported as verified")
	}

	if _, err := a.password(ctx, "bob", "userpw"); err != nil {
		t.Fatalf("re-verification after expiry failed: %v", err)
	}
}

func tampered(sealed string) string {
	middle := len(sealed) / 2

	replacement := "A"
	if sealed[middle] == 'A' {
		replacement = "B"
	}

	return sealed[:middle] + replacement + sealed[middle+1:]
}

func bearer(token string) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
	}
}

func basic(user, pass string) func(*http.Request) {
	return func(r *http.Request) {
		r.SetBasicAuth(user, pass)
	}
}

func TestDisabledUsersAreRefused(t *testing.T) {
	t.Parallel()
	a := newTestAuthenticator(t)
	hash, _ := HashPassword("pw")
	a.users.(*fakeUsers).users["gone"] = database.User{
		ID:           uuid.New(),
		Username:     "gone",
		PasswordHash: hash,
		Disabled:     true,
	}

	if _, err := a.password(t.Context(), "gone", "pw"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("password of a disabled user: %v", err)
	}

	if _, err := a.bearer(t.Context(), "good-gone"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("token of a disabled user: %v", err)
	}
}

func TestSingleSignOnUsersHaveNoPassword(t *testing.T) {
	t.Parallel()
	a := newTestAuthenticator(t)
	hash, _ := HashPassword("pw")
	a.users.(*fakeUsers).users["federated"] = database.User{
		ID:           uuid.New(),
		Username:     "federated",
		PasswordHash: hash,
		Realm:        RealmSSO,
	}

	if _, err := a.password(t.Context(), "federated", "pw"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("a single sign-on user signed in with a password: %v", err)
	}

	if p, err := a.bearer(t.Context(), "good-federated"); err != nil || p.Username != "federated" {
		t.Errorf("a single sign-on user's token = %+v, %v", p, err)
	}
}

func TestGroupViewerPrivileges(t *testing.T) {
	t.Parallel()
	a := newTestAuthenticator(t)
	hash, _ := HashPassword("pw")
	a.users.(*fakeUsers).users["auditor"] = database.User{
		ID:           uuid.New(),
		Username:     "auditor",
		PasswordHash: hash,
	}

	p, err := a.password(t.Context(), "auditor", "pw")
	if err != nil || !p.Viewer || p.Admin {
		t.Errorf("principal = %+v, %v", p, err)
	}

	narrowed := TokenScope{
		Kind: ScopeGroups,
		Groups: []string{
			"auditors",
		},
	}.Apply(p)
	if narrowed.Viewer {
		t.Errorf("a groups-scoped token keeps the viewer privilege: %+v", narrowed)
	}
}

func TestScopeApply(t *testing.T) {
	t.Parallel()

	bob := Principal{
		Username: "bob",
		Groups: []string{
			"devs",
			"qa",
		},
	}
	root := Principal{
		Username: "bob",
		Admin:    true,
		Groups: []string{
			"devs",
		},
	}

	tests := []struct {
		name       string
		p          Principal
		scope      TokenScope
		wantAdmin  bool
		wantGroups []string
		wantScoped bool
	}{
		{
			name:  "user scope",
			p:     bob,
			scope: UserScope,
			wantGroups: []string{
				"devs",
				"qa",
			},
		},
		{
			name: "groups narrow",
			p:    bob,
			scope: TokenScope{
				Kind: ScopeGroups,
				Groups: []string{
					"qa",
				},
			},
			wantGroups: []string{
				"qa",
			},
			wantScoped: true,
		},
		{
			name: "groups the user left are dropped",
			p:    bob,
			scope: TokenScope{
				Kind: ScopeGroups,
				Groups: []string{
					"qa",
					"ops",
				},
			},
			wantGroups: []string{
				"qa",
			},
			wantScoped: true,
		},
		{
			name: "no groups acts with none",
			p:    bob,
			scope: TokenScope{
				Kind: ScopeGroups,
			},
			wantScoped: true,
		},
		{
			name: "admin scope does not elevate",
			p:    bob,
			scope: TokenScope{
				Kind: ScopeAdmin,
			},
			wantGroups: []string{
				"devs",
				"qa",
			},
		},
		{
			name: "admin keeps admin",
			p:    root,
			scope: TokenScope{
				Kind: ScopeAdmin,
			},
			wantAdmin: true,
			wantGroups: []string{
				"devs",
			},
		},
		{
			name: "admin with groups",
			p:    root,
			scope: TokenScope{
				Kind: ScopeAdmin,
				Groups: []string{
					"ops",
				},
			},
			wantAdmin: true,
			wantGroups: []string{
				"ops",
			},
			wantScoped: true,
		},
		{
			name: "admin group scope narrows",
			p:    root,
			scope: TokenScope{
				Kind: ScopeGroups,
				Groups: []string{
					"ops",
				},
			},
			wantGroups: []string{
				"ops",
			},
			wantScoped: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.scope.Apply(tt.p)
			if got.Admin != tt.wantAdmin || strings.Join(got.Groups, ",") != strings.Join(tt.wantGroups, ",") ||
				got.Scoped != tt.wantScoped ||
				got.Username != "bob" {
				t.Errorf("Apply(%+v) = %+v", tt.scope, got)
			}
		})
	}
}

func TestCeilScope(t *testing.T) {
	t.Parallel()

	scoped := Principal{
		Username: "bob",
		Groups: []string{
			"qa",
		},
		Scoped: true,
	}
	if got := scoped.CeilScope(UserScope); got.Kind != ScopeGroups || strings.Join(got.Groups, ",") != "qa" {
		t.Errorf("scoped caller widened to %+v", got)
	}

	scopedAdmin := Principal{
		Username: "root",
		Admin:    true,
		Groups: []string{
			"qa",
		},
		Scoped: true,
	}
	for _, requested := range []TokenScope{
		{
			Kind: ScopeAdmin,
		},
		{
			Kind: ScopeAdmin,
			Groups: []string{
				"ops",
			},
		},
	} {
		if got := scopedAdmin.CeilScope(requested); got.Kind != ScopeGroups || strings.Join(got.Groups, ",") != "qa" {
			t.Errorf("scoped administrator asking for %+v widened to %+v", requested, got)
		}
	}

	if got := (Principal{
		Username: "bob",
	}).CeilScope(UserScope); got.Kind != ScopeUser {
		t.Errorf("unscoped caller changed to %+v", got)
	}
}

func TestMayGrant(t *testing.T) {
	t.Parallel()

	bob := Principal{
		Username: "bob",
		Groups: []string{
			"devs",
		},
	}

	tests := []struct {
		name  string
		p     Principal
		scope TokenScope
		want  bool
	}{
		{
			name:  "own rights",
			p:     bob,
			scope: UserScope,
			want:  true,
		},
		{
			name: "own group",
			p:    bob,
			scope: TokenScope{
				Kind: ScopeGroups,
				Groups: []string{
					"devs",
				},
			},
			want: true,
		},
		{
			name: "foreign group",
			p:    bob,
			scope: TokenScope{
				Kind: ScopeGroups,
				Groups: []string{
					"ops",
				},
			},
		},
		{
			name: "admin scope",
			p:    bob,
			scope: TokenScope{
				Kind: ScopeAdmin,
			},
		},
		{
			name: "administrators may",
			p: Principal{
				Username: "root",
				Admin:    true,
			},
			scope: TokenScope{
				Kind: ScopeAdmin,
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.p.MayGrant(tt.scope); got != tt.want {
				t.Errorf("MayGrant(%+v) = %v", tt.scope, got)
			}
		})
	}
}

func TestGroupAdminPrivileges(t *testing.T) {
	t.Parallel()
	a := newTestAuthenticator(t)
	hash, _ := HashPassword("pw")
	a.users.(*fakeUsers).users["opsuser"] = database.User{
		ID:           uuid.New(),
		Username:     "opsuser",
		PasswordHash: hash,
	}

	p, err := a.password(t.Context(), "opsuser", "pw")
	if err != nil || !p.Admin || p.Groups[0] != "ops" {
		t.Errorf("principal = %+v, %v", p, err)
	}
}

func TestUnauthorizedChallenge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want string
	}{
		{
			"/packages/api/repositories",
			`Basic realm="Relic"`,
		},
		{
			"/api/relic.v1.SessionService/GetCurrentUser",
			"",
		},
		{
			"/api/v1/files/libs/a.txt",
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			Unauthorized(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.path, nil))

			if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != tt.want {
				t.Errorf(
					"%s: %d challenge %q, want %q",
					tt.path,
					rec.Code,
					rec.Header().Get("WWW-Authenticate"),
					tt.want,
				)
			}
		})
	}
}

func TestAcceptAPIKey(t *testing.T) {
	t.Parallel()
	a := newTestAuthenticator(t)
	a.AcceptAPIKey("X-Key", "/pkg/")

	tests := []struct {
		name, path, key string
		want            string
		wantErr         error
	}{
		{
			"key on its prefix",
			"/pkg/push",
			"bob:userpw",
			"bob",
			nil,
		},
		{
			"wrong password",
			"/pkg/push",
			"bob:nope",
			"",
			ErrBadCredentials,
		},
		{
			"no separator",
			"/pkg/push",
			"bob",
			"",
			ErrBadCredentials,
		},
		{
			"ignored elsewhere",
			"/other/push",
			"bob:userpw",
			"",
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPut, tt.path, nil)
			r.Header.Set("X-Key", tt.key)

			p, err := a.Authenticate(r)
			if !errors.Is(err, tt.wantErr) || p.Username != tt.want {
				t.Errorf("Authenticate = %q, %v; want %q, %v", p.Username, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestAllowExpiredBearer(t *testing.T) {
	t.Parallel()
	a := newTestAuthenticator(t)
	a.AllowExpiredBearer("/refresh")
	h := a.Middleware(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	)

	tests := []struct {
		name, method, path string
		want               int
	}{
		{
			"refresh route lets the expired token through",
			http.MethodPost,
			"/refresh",
			http.StatusNoContent,
		},
		{
			"only POST",
			http.MethodGet,
			"/refresh",
			http.StatusUnauthorized,
		},
		{
			"other routes refuse it",
			http.MethodPost,
			"/elsewhere",
			http.StatusUnauthorized,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, nil)
			r.Header.Set("Authorization", "Bearer expired.token.value")

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)

			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestLeaveBearer(t *testing.T) {
	t.Parallel()
	a := newTestAuthenticator(t)
	a.LeaveBearer("own_", "/own/")

	tests := []struct {
		name, path, token string
		wantErr           error
	}{
		{
			name:  "a token with the prefix on its path stays anonymous",
			path:  "/mount/repo/own/v1/thing",
			token: "own_anything",
		},
		{
			name:    "a token with the prefix elsewhere is verified",
			path:    "/api/v1/repositories",
			token:   "own_anything",
			wantErr: ErrBadCredentials,
		},
		{
			name:    "other tokens on the path are verified",
			path:    "/mount/repo/own/v1/thing",
			token:   "other_anything",
			wantErr: ErrBadCredentials,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.path, nil)
			r.Header.Set("Authorization", "Bearer "+tt.token)

			p, err := a.Authenticate(r)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Authenticate error = %v, want %v", err, tt.wantErr)
			}

			if err == nil && !p.Anonymous() {
				t.Fatalf("principal = %+v, want anonymous", p)
			}
		})
	}
}

func TestLimitIssueBoundsTheLifetime(t *testing.T) {
	t.Parallel()

	now := time.Now()
	tests := []struct {
		name      string
		p         Principal
		requested time.Duration
		want      time.Duration
	}{
		{
			name: "a user asking for a token that never expires",
			p: Principal{
				Username: "bob",
			},
			want: MaxUserTokenLifetime,
		},
		{
			name: "a user asking for more than the maximum",
			p: Principal{
				Username: "bob",
			},
			requested: 2 * MaxUserTokenLifetime,
			want:      MaxUserTokenLifetime,
		},
		{
			name: "a user within the maximum",
			p: Principal{
				Username: "bob",
			},
			requested: time.Hour,
			want:      time.Hour,
		},
		{
			name: "an administrator",
			p: Principal{
				Username: "root",
				Admin:    true,
			},
		},
		{
			name: "a workload identity's token",
			p: Principal{
				Username:             "ci-bot",
				WorkloadIdentity:     "github",
				WorkloadTokenExpires: now.Add(time.Hour),
			},
			want: time.Hour,
		},
		{
			name: "a user signed in with a short-lived token",
			p: Principal{
				Username:   "bob",
				BoundBy:    "parent",
				BoundUntil: now.Add(time.Minute),
			},
			requested: 12 * time.Hour,
			want:      time.Minute,
		},
		{
			name: "an administrator signed in with a token",
			p: Principal{
				Username:   "root",
				Admin:      true,
				BoundBy:    "parent",
				BoundUntil: now.Add(time.Minute),
			},
			want: time.Minute,
		},
		{
			name: "a user signed in with a token that never expires",
			p: Principal{
				Username: "bob",
				BoundBy:  "parent",
			},
			requested: time.Hour,
			want:      time.Hour,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.p.LimitIssue(IssueParams{
				Username:    tt.p.Username,
				Scope:       UserScope,
				ExpiresIn:   tt.requested,
				Refreshable: true,
			}, now)
			if got.ExpiresIn != tt.want {
				t.Errorf("ExpiresIn = %v, want %v", got.ExpiresIn, tt.want)
			}

			bound := tt.p.WorkloadIdentity != ""
			if got.BoundBy != tt.p.BoundBy || got.Refreshable == bound {
				t.Errorf(
					"BoundBy = %q, Refreshable = %v; want %q, %v",
					got.BoundBy,
					got.Refreshable,
					tt.p.BoundBy,
					!bound,
				)
			}
		})
	}
}

func TestGroupScopedTokensDropTheUsersOwnGrants(t *testing.T) {
	t.Parallel()

	perms := Permissions{
		All: []Permission{
			{
				Name: "alice-direct",
				Repository: &Section{
					Selector: Selector{
						Repositories: []string{
							"ops-local",
						},
					},
					Users: map[string][]Action{
						"alice": {
							ActionRead,
							ActionWrite,
						},
					},
				},
			},
		},
	}
	alice := Principal{
		Username: "alice",
		Groups: []string{
			"devs",
		},
	}
	repo := Repository{
		Name:  "ops-local",
		Class: "local",
	}

	if !perms.CanInRepository(alice.Subject(), repo, "f.txt", ActionWrite) {
		t.Fatal("alice cannot use her own grant")
	}

	for _, scope := range []TokenScope{
		{
			Kind: ScopeGroups,
			Groups: []string{
				"devs",
			},
		},
		{
			Kind: ScopeGroups,
		},
	} {
		scoped := scope.Apply(alice)
		if scoped.Username != "alice" {
			t.Errorf("a token scoped to %v signs in as %q", scope.Groups, scoped.Username)
		}

		if perms.CanInRepository(scoped.Subject(), repo, "f.txt", ActionWrite) {
			t.Errorf("a token scoped to %v keeps alice's own grant", scope.Groups)
		}
	}
}

func TestBearerSchemeIgnoresCase(t *testing.T) {
	t.Parallel()
	a := newTestAuthenticator(t)
	a.AllowExpiredBearer("/refresh")
	h := a.Middleware(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	)

	for _, scheme := range []string{
		"Bearer",
		"bearer",
		"BEARER",
	} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		r.Header.Set("Authorization", scheme+" good-bob")

		if p, err := a.Authenticate(r); err != nil || p.Username != "bob" {
			t.Errorf("%s token = %+v, %v", scheme, p, err)
		}

		r = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/refresh", nil)
		r.Header.Set("Authorization", scheme+" expired.token.value")

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)

		if rec.Code != http.StatusNoContent {
			t.Errorf("%s expired token on the refresh route = %d", scheme, rec.Code)
		}
	}
}
