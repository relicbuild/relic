//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func TestRelicAPIViewer(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()
	admin := relicv1connect.NewSecurityServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")

	if _, err := admin.PutGroup(ctx, connect.NewRequest(&relicv1.PutGroupRequest{
		Group: &relicv1.Group{
			Name:   "auditors",
			Viewer: true,
		},
	})); err != nil {
		t.Fatal(err)
	}

	for _, u := range []*relicv1.User{
		{
			Name:     "audrey",
			Email:    "audrey@example.com",
			Password: "Audrey-pw-1",
			Groups: []string{
				"auditors",
			},
		},
		{
			Name:     "vera",
			Email:    "vera@example.com",
			Password: "Vera-pw-1",
			Viewer:   true,
		},
		{
			Name:     "plain",
			Email:    "plain@example.com",
			Password: "Plain-pw-1",
		},
	} {
		if _, err := admin.PutUser(ctx, connect.NewRequest(&relicv1.PutUserRequest{
			User: u,
		})); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		user, password string
		read, write    connect.Code
	}{
		{
			user:     "audrey",
			password: "Audrey-pw-1",
			write:    connect.CodePermissionDenied,
		},
		{
			user:     "vera",
			password: "Vera-pw-1",
			write:    connect.CodePermissionDenied,
		},
		{
			user:     "plain",
			password: "Plain-pw-1",
			read:     connect.CodePermissionDenied,
			write:    connect.CodePermissionDenied,
		},
	}
	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			t.Parallel()

			sec := relicv1connect.NewSecurityServiceClient(relicHTTP(tt.user, tt.password), srv.URL+"/api")
			system := relicv1connect.NewSystemServiceClient(relicHTTP(tt.user, tt.password), srv.URL+"/api")

			_, err := sec.ListUsers(ctx, connect.NewRequest(&relicv1.ListUsersRequest{}))
			if got := code(err); got != tt.read {
				t.Errorf("list users: %v, want %v", err, tt.read)
			}

			_, err = system.GetSystemStatus(ctx, connect.NewRequest(&relicv1.GetSystemStatusRequest{}))
			if got := code(err); got != tt.read {
				t.Errorf("system status: %v, want %v", err, tt.read)
			}

			_, err = sec.DeleteUser(ctx, connect.NewRequest(&relicv1.DeleteUserRequest{
				Name: "plain",
			}))
			if got := code(err); got != tt.write {
				t.Errorf("delete user: %v, want %v", err, tt.write)
			}
		})
	}

	session := relicv1connect.NewSessionServiceClient(relicHTTP("audrey", "Audrey-pw-1"), srv.URL+"/api")

	me, err := session.GetCurrentUser(ctx, connect.NewRequest(&relicv1.GetCurrentUserRequest{}))
	if err != nil || !me.Msg.GetPrincipal().GetViewer() || me.Msg.GetPrincipal().GetAdmin() {
		t.Errorf("current user = %v, %v", me, err)
	}

	update, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		srv.URL+"/artifactory/api/security/users/vera",
		strings.NewReader(`{"email":"vera2@example.com"}`),
	)
	if err != nil {
		t.Fatal(err)
	}

	update.SetBasicAuth("admin", "password")
	update.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(update)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	vera, err := admin.GetUser(ctx, connect.NewRequest(&relicv1.GetUserRequest{
		Name: "vera",
	}))
	if err != nil || !vera.Msg.GetUser().GetViewer() || vera.Msg.GetUser().GetEmail() != "vera2@example.com" {
		t.Errorf("after an update through the adapter, vera = %v, %v", vera, err)
	}
}

func TestRelicAPIDisableUser(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()
	admin := relicv1connect.NewSecurityServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")

	put := func(disabled bool) error {
		_, err := admin.PutUser(ctx, connect.NewRequest(&relicv1.PutUserRequest{
			User: &relicv1.User{
				Name:     "ivy",
				Email:    "ivy@example.com",
				Password: "Ivy-pw-1",
				Disabled: disabled,
			},
		}))

		return err
	}

	signIn := func() connect.Code {
		session := relicv1connect.NewSessionServiceClient(relicHTTP("ivy", "Ivy-pw-1"), srv.URL+"/api")
		_, err := session.GetCurrentUser(ctx, connect.NewRequest(&relicv1.GetCurrentUserRequest{}))

		return code(err)
	}

	if err := put(true); err != nil {
		t.Fatal(err)
	}

	if got := signIn(); got != connect.CodeUnauthenticated {
		t.Errorf("a disabled user signed in: %v", got)
	}

	update, err := http.NewRequestWithContext(
		ctx,
		http.MethodPut,
		srv.URL+"/artifactory/api/security/users/ivy",
		strings.NewReader(`{"email":"ivy@example.com","password":"Ivy-pw-1"}`),
	)
	if err != nil {
		t.Fatal(err)
	}

	update.SetBasicAuth("admin", "password")
	update.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(update)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /artifactory/api/security/users/ivy = %d, want 200", resp.StatusCode)
	}

	if got := signIn(); got != connect.CodeUnauthenticated {
		t.Errorf("a user replaced through the adapter was enabled again: %v", got)
	}

	if err := put(false); err != nil {
		t.Fatal(err)
	}

	if got := signIn(); got != 0 {
		t.Errorf("an enabled user cannot sign in: %v", got)
	}

	if _, err := admin.PutUser(ctx, connect.NewRequest(&relicv1.PutUserRequest{
		User: &relicv1.User{
			Name:     "admin",
			Admin:    true,
			Disabled: true,
		},
	})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("disabling yourself: %v", err)
	}
}
