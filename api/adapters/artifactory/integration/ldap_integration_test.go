//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"relic/api/adapters/artifactory/access"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/access/auth"
	"relic/internal/access/ldap"
	"relic/internal/access/ldap/ldaptest"
	"relic/internal/app"
	"relic/internal/platform/database" //nolint:depguard // the test stores an unreadable row directly
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
	"relic/internal/platform/sealing"
)

func TestLDAPSignIn(t *testing.T) {
	t.Parallel()
	dir := ldaptest.Start(t,
		[]ldaptest.Person{
			{
				UID:      "alice",
				Password: "alice-pass",
				Mail:     "alice@example.org",
			},
			{
				UID:      "bob",
				Password: "bob-pass",
			},
			{
				UID:      "carol",
				Password: "carol-pass",
			},
			{
				UID:      "admin",
				Password: "directory-admin-pass",
			},
		},
		[]ldaptest.Group{
			{
				CN: "devs",
				Members: []string{
					"alice",
					"carol",
				},
			},
			{
				CN: "unmapped",
				Members: []string{
					"bob",
				},
			},
		},
	)
	db := databasetest.NewDB(t)

	log := slog.New(slog.DiscardHandler)
	if err := app.BootstrapAdmin(t.Context(), db, "admin", "password", log); err != nil {
		t.Fatal(err)
	}

	tokens, err := auth.NewTokens(t.Context(), db, nil, access.Format{})
	if err != nil {
		t.Fatal(err)
	}

	box, err := sealing.New(bytes.Repeat([]byte{
		7,
	}, 32))
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(
		app.NewHandler(db, objectstoretest.NewMemory(), tokens, nil, app.Options{
			Secrets: box,
			LDAPTLS: dir.TLS,
		}, log),
	)
	t.Cleanup(srv.Close)

	basic := func(user, pass string) map[string]string {
		return map[string]string{
			"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass)),
		}
	}
	as := func(user, pass string, c call) reply {
		t.Helper()

		c.noAuth, c.headers = true, basic(user, pass)
		resp, _ := do(t, srv, c)

		return resp
	}

	settings := `{"key":"corp","enabled":true,"ldap_url":"` + dir.LDAP + `/` + ldaptest.BaseDN + `","use_start_tls":true,
		"search":{"search_filter":"uid={0}","search_base":"ou=people","search_sub_tree":true,"manager_dn":"` + ldaptest.AdminDN + `","manager_password":"` + ldaptest.AdminPassword + `"},
		"auto_create_user":true,"email_attribute":"mail"}`
	for _, c := range []call{
		{
			method: "POST",
			path:   "/access/api/v1/ldap/settings",
			body:   settings,
		},
		{
			method: "POST",
			path:   "/access/api/v1/ldap/groups",
			body: `{"name":"corp-groups","enabled_ldap":"corp","group_base_dn":"ou=groups","group_name_attribute":"cn",
			"group_member_attribute":"uniqueMember","filter":"(objectClass=groupOfUniqueNames)","strategy":"static","sub_tree":true}`,
		},
		{
			method: "PUT",
			path:   "/artifactory/api/security/groups/devs",
			body:   `{"name":"devs"}`,
		},
		{
			method: "PUT",
			path:   "/artifactory/api/repositories/team",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
		{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/devs",
			body:   `{"name":"devs","repo":{"repositories":["team"],"include-patterns":["**"],"actions":{"groups":{"devs":["read","write"]}}}}`,
		},
	} {
		if resp, body := do(t, srv, c); resp.StatusCode >= 300 {
			t.Fatalf("%s %s = %d %s", c.method, c.path, resp.StatusCode, body)
		}
	}

	t.Run("the manager password is masked and sealed", func(t *testing.T) {
		resp, body := do(t, srv, call{
			method: "GET",
			path:   "/access/api/v1/ldap/settings/corp",
		})
		if resp.StatusCode != 200 || strings.Contains(body, ldaptest.AdminPassword) ||
			!strings.Contains(body, `"manager_dn"`) {
			t.Errorf("GET = %d %s", resp.StatusCode, body)
		}

		row, err := db.GetLdapServer(t.Context(), "corp")
		if err != nil || bytes.Contains(row.Config, []byte(ldaptest.AdminPassword)) {
			t.Errorf("stored config %s (%v)", row.Config, err)
		}

		if resp := as(
			"alice",
			"alice-pass",
			call{
				method: "GET",
				path:   "/access/api/v1/ldap/settings",
			},
		); resp.StatusCode != 403 {
			t.Errorf("non-admin read = %d", resp.StatusCode)
		}
	})
	t.Run("a create over an unreadable entry fails and keeps it", func(t *testing.T) {
		ctx := t.Context()
		if err := db.UpsertLdapServer(ctx, database.UpsertLdapServerParams{
			Name:   "unreadable",
			Config: []byte(`[]`),
		}); err != nil {
			t.Fatal(err)
		}

		if err := db.UpsertLdapGroupMapping(ctx, database.UpsertLdapGroupMappingParams{
			Name:   "unreadable",
			Config: []byte(`[]`),
		}); err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() {
			_, _ = db.DeleteLdapGroupMapping(context.WithoutCancel(ctx), "unreadable")
			_, _ = db.DeleteLdapServer(context.WithoutCancel(ctx), "unreadable")
		})

		for _, c := range []call{
			{
				method: "POST",
				path:   "/access/api/v1/ldap/settings",
				body:   `{"key":"unreadable","ldap_url":"ldap://ldap.example.com/dc=example","user_dn_pattern":"uid={0}"}`,
			},
			{
				method: "POST",
				path:   "/access/api/v1/ldap/groups",
				body:   `{"name":"unreadable","enabled_ldap":"corp","group_name_attribute":"cn","group_member_attribute":"uniqueMember","strategy":"static"}`,
			},
		} {
			if resp, body := do(t, srv, c); resp.StatusCode != 500 {
				t.Errorf("%s = %d %s, want 500", c.path, resp.StatusCode, body)
			}
		}

		if row, err := db.GetLdapServer(ctx, "unreadable"); err != nil || string(row.Config) != `[]` {
			t.Errorf("stored directory = %s (%v)", row.Config, err)
		}

		if row, err := db.GetLdapGroupMapping(ctx, "unreadable"); err != nil || string(row.Config) != `[]` {
			t.Errorf("stored group mapping = %s (%v)", row.Config, err)
		}
	})
	t.Run("the Relic API", func(t *testing.T) {
		ctx := t.Context()
		lc := relicv1connect.NewLdapServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

		dirs, err := lc.ListLdapServers(ctx, &relicv1.ListLdapServersRequest{})
		if err != nil || len(dirs.GetLdapServers()) != 1 || !dirs.GetLdapServers()[0].GetHasManagerPassword() ||
			dirs.GetLdapServers()[0].GetSearch().GetManagerPassword() != "" {
			t.Fatalf("directories = %v %v", dirs, err)
		}

		maps, err := lc.ListLdapGroupMappings(ctx, &relicv1.ListLdapGroupMappingsRequest{})
		if err != nil || len(maps.GetLdapGroupMappings()) != 1 ||
			maps.GetLdapGroupMappings()[0].GetName() != "corp-groups" {
			t.Errorf("group mappings = %v %v", maps, err)
		}

		corp := dirs.GetLdapServers()[0]

		res, err := lc.TestLdapSignIn(
			ctx,
			&relicv1.TestLdapSignInRequest{
				LdapServer: corp,
				Username:   "alice",
				Password:   "alice-pass",
			},
		)
		if err != nil || !res.GetOk() || !slices.Contains(res.GetGroups(), "devs") ||
			!strings.Contains(res.GetDn(), "uid=alice") {
			t.Errorf("test = %v %v", res, err)
		}

		res, err = lc.TestLdapSignIn(
			ctx,
			&relicv1.TestLdapSignInRequest{
				LdapServer: corp,
				Username:   "alice",
				Password:   "wrong",
			},
		)
		if err != nil || res.GetOk() {
			t.Errorf("failed test = %v %v", res, err)
		}
	})
	t.Run("a directory user signs in and gets their groups", func(t *testing.T) {
		if resp := as(
			"alice",
			"alice-pass",
			call{
				method: "PUT",
				path:   "/artifactory/team/a.txt",
				body:   "hi",
			},
		); resp.StatusCode != 201 {
			t.Fatalf("alice deploy = %d", resp.StatusCode)
		}

		u, err := db.GetUserByUsername(t.Context(), "alice")
		if err != nil || u.Realm != "ldap" || u.Email != "alice@example.org" {
			t.Errorf("user = %+v (%v)", u, err)
		}

		if _, body := do(t, srv, call{
			method: "GET",
			path:   "/artifactory/api/security/users/alice",
		}); !strings.Contains(
			body,
			`"realm": "ldap"`,
		) {
			t.Errorf("user JSON = %s", body)
		}

		if _, body := do(
			t,
			srv,
			call{
				method: "GET",
				path:   "/artifactory/api/security/users",
			},
		); !strings.Contains(body, `"realm": "ldap"`) ||
			!strings.Contains(body, `"realm": "internal"`) {
			t.Errorf("users = %s", body)
		}

		if resp := as(
			"bob",
			"bob-pass",
			call{
				method: "GET",
				path:   "/artifactory/team/a.txt",
			},
		); resp.StatusCode != 403 &&
			resp.StatusCode != 404 {
			t.Errorf("bob read = %d", resp.StatusCode)
		}

		if resp := as(
			"bob",
			"bob-pass",
			call{
				method: "GET",
				path:   "/artifactory/api/system/version",
			},
		); resp.StatusCode != 200 {
			t.Errorf("bob signed in = %d", resp.StatusCode)
		}
	})
	t.Run("tokens, docker login and the UI", func(t *testing.T) {
		resp, body := do(t, srv, call{
			method:  "POST",
			path:    "/access/api/v1/tokens",
			body:    `{"scope":"applied-permissions/user","expires_in":600}`,
			noAuth:  true,
			headers: basic("alice", "alice-pass"),
		})

		var tok struct {
			AccessToken string `json:"access_token"`
		}
		if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &tok) != nil || tok.AccessToken == "" {
			t.Fatalf("token = %d %s", resp.StatusCode, body)
		}

		if resp, _ := do(
			t,
			srv,
			call{
				method: "GET",
				path:   "/artifactory/team/a.txt",
				noAuth: true,
				headers: map[string]string{
					"Authorization": "Bearer " + tok.AccessToken,
				},
			},
		); resp.StatusCode != 200 {
			t.Errorf("bearer read = %d", resp.StatusCode)
		}

		if resp := as("alice", "alice-pass", call{
			method: "GET",
			path:   "/v2/",
		}); resp.StatusCode != 200 {
			t.Errorf("docker login = %d", resp.StatusCode)
		}

		login := &relicv1.LoginRequest{
			Username: "carol",
			Password: "carol-pass",
		}
		if _, err := relicv1connect.NewSessionServiceClient(apiClient(relicHTTP("", ""), srv.URL)).
			Login(withRequestHeader(t.Context(), auth.UIHeader, "1"), login); err != nil {
			t.Errorf("UI sign-in: %v", err)
		}
	})
	t.Run("a deleted directory user signs in again at once", func(t *testing.T) {
		if resp, body := do(
			t,
			srv,
			call{
				method: "DELETE",
				path:   "/artifactory/api/security/users/bob",
			},
		); resp.StatusCode >= 300 {
			t.Fatalf("delete bob = %d %s", resp.StatusCode, body)
		}

		if resp := as(
			"bob",
			"bob-pass",
			call{
				method: "GET",
				path:   "/artifactory/api/system/version",
			},
		); resp.StatusCode != 200 {
			t.Errorf("bob after delete = %d", resp.StatusCode)
		}

		if u, err := db.GetUserByUsername(t.Context(), "bob"); err != nil || u.Realm != "ldap" {
			t.Errorf("bob re-created = %+v (%v)", u, err)
		}
	})
	t.Run("a directory entry never signs in as a local user", func(t *testing.T) {
		if resp := as(
			"admin",
			"directory-admin-pass",
			call{
				method: "GET",
				path:   "/artifactory/api/system/version",
			},
		); resp.StatusCode != 401 {
			t.Errorf("directory admin = %d", resp.StatusCode)
		}

		if resp := as(
			"admin",
			"password",
			call{
				method: "GET",
				path:   "/artifactory/api/system/version",
			},
		); resp.StatusCode != 200 {
			t.Errorf("local admin = %d", resp.StatusCode)
		}
	})
	t.Run("group sync removes memberships the directory dropped", func(t *testing.T) {
		groups, err := db.UserGroups(t.Context(), "carol")
		if err != nil || len(groups) != 1 || groups[0].Name != "devs" {
			t.Fatalf("carol's groups before = %+v (%v)", groups, err)
		}

		dir.SetGroup(t, ldaptest.Group{
			CN: "devs",
			Members: []string{
				"alice",
			},
		})

		if err := ldap.NewService(db, box, dir.TLS, log).Authenticate(t.Context(), "carol", "carol-pass"); err != nil {
			t.Fatal(err)
		}

		if groups, _ := db.UserGroups(t.Context(), "carol"); len(groups) != 0 {
			t.Errorf("carol's groups after = %+v", groups)
		}
	})
	t.Run("an outage refuses directory users and nobody else", func(t *testing.T) {
		dir.Stop(t)

		if resp := as(
			"bob",
			"a-new-password",
			call{
				method: "GET",
				path:   "/artifactory/api/system/version",
			},
		); resp.StatusCode != 401 {
			t.Errorf("directory user during outage = %d", resp.StatusCode)
		}

		if resp := as(
			"admin",
			"password",
			call{
				method: "GET",
				path:   "/artifactory/api/system/version",
			},
		); resp.StatusCode != 200 {
			t.Errorf("local admin during outage = %d", resp.StatusCode)
		}
	})
}

func TestLDAPSettingsCreateTakesTheKey(t *testing.T) {
	t.Parallel()

	srv, db := serverDB(t)
	settings := call{
		method: "POST",
		path:   "/access/api/v1/ldap/settings",
		body: `{"key":"corp","enabled":true,"ldap_url":"ldap://ldap.example.com/dc=example,dc=com",` +
			`"search":{"search_filter":"uid={0}","search_base":"ou=people"}}`,
	}

	held := make(chan struct{})
	unlock := make(chan struct{})
	lockErr := make(chan error, 1)

	go func() {
		lockErr <- db.WithLock(t.Context(), "ldap-server:corp", func() error {
			close(held)
			<-unlock

			return nil
		})
	}()

	<-held

	created := make(chan int, 1)

	go func() {
		resp, _ := do(t, srv, settings)
		created <- resp.StatusCode
	}()

	select {
	case status := <-created:
		t.Errorf("create went ahead (%d) while another change to the key held it", status)
		close(unlock)
	case <-time.After(2 * time.Second):
		close(unlock)

		if status := <-created; status != 201 {
			t.Fatalf("create = %d, want 201", status)
		}
	}

	if err := <-lockErr; err != nil {
		t.Fatal(err)
	}

	if resp, _ := do(t, srv, settings); resp.StatusCode != 409 {
		t.Errorf("second create = %d, want 409", resp.StatusCode)
	}
}
