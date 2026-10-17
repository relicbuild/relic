//go:build integration

package sso_test

import (
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/crewjam/saml"

	"relic/internal/access/sso"
	"relic/internal/access/sso/ssotest"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
)

const samlBase = "https://relic.example.com/api/v1/sso/corp"

// samlResponse runs a browser to the provider and returns the form it posts
// back: the SAMLResponse and the RelayState.
func samlResponse(t *testing.T, s *sso.Service) (string, string) {
	t.Helper()

	redirect, state, protocol, err := s.Start(t.Context(), "corp", samlBase, "/x")
	if err != nil || protocol != sso.SAML {
		t.Fatalf("start = %v %v", protocol, err)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, redirect, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	page, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("identity provider = %d %s %v", resp.StatusCode, page, err)
	}

	action, fields := ssotest.Form(t, string(page))
	if action != samlBase+"/acs" || fields.Get("RelayState") != state {
		t.Fatalf("the provider posts to %s with relay state %q, want %s/acs with %q", action, fields.Get("RelayState"),
			samlBase, state)
	}

	return fields.Get("SAMLResponse"), state
}

func TestSAMLSignIn(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	idp := ssotest.NewSAML(t)
	s := sso.New(db, nil, slog.New(slog.DiscardHandler))

	if err := s.PutProvider(ctx, sso.Provider{
		Name:            "corp",
		Enabled:         true,
		AutoCreateUsers: true,
		GroupsClaim:     "eduPersonAffiliation",
		SAML: &sso.SAMLSettings{
			MetadataURL: idp.MetadataURL(),
		},
	}); err != nil {
		t.Fatal(err)
	}

	metadata, err := s.SAMLMetadata(ctx, "corp", samlBase)
	if err != nil || !strings.Contains(string(metadata), samlBase+"/acs") {
		t.Fatalf("service provider metadata = %s, %v", metadata, err)
	}

	idp.Trust(t, metadata, true)

	if _, err := db.UpsertGroup(ctx, database.UpsertGroupParams{
		Name: "devs",
	}); err != nil {
		t.Fatal(err)
	}

	idp.SignIn(saml.Session{
		NameID:    "erin",
		UserName:  "erin",
		UserEmail: "erin@example.com",
		Groups: []string{
			"devs",
		},
	})

	response, state := samlResponse(t, s)

	user, returnTo, err := s.FinishSAML(ctx, "corp", samlBase, state, response)
	if err != nil || user != "erin" || returnTo != "/x" {
		t.Fatalf("sign-in = %q %q %v", user, returnTo, err)
	}

	erin, err := db.GetUserByUsername(ctx, "erin")
	if err != nil || erin.Realm != "sso" || erin.Email != "erin@example.com" {
		t.Errorf("erin = %+v, %v", erin, err)
	}

	if got := groups(t, db, "erin"); !slices.Equal(got, []string{
		"devs",
	}) {
		t.Errorf("erin's groups = %v, want [devs]", got)
	}

	if _, _, err := s.FinishSAML(ctx, "corp", samlBase, state, response); !errors.Is(err, sso.ErrRefused) {
		t.Errorf("a replayed response was accepted: %v", err)
	}

	idp.Trust(t, metadata, false)
	response, state = samlResponse(t, s)

	raw, err := base64.StdEncoding.DecodeString(response)
	if err != nil || !strings.Contains(string(raw), ">erin<") {
		t.Fatalf("a plain assertion = %s, %v", raw, err)
	}

	forged := base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(string(raw), "erin", "eve0")))
	if _, _, err := s.FinishSAML(ctx, "corp", samlBase, state, forged); !errors.Is(err, sso.ErrRefused) {
		t.Errorf("a response altered after signing was accepted: %v", err)
	}

	response, state = samlResponse(t, s)
	if _, _, err := s.FinishSAML(
		ctx,
		"corp",
		"https://other.example.com/api/v1/sso/corp",
		state,
		response,
	); !errors.Is(err, sso.ErrRefused) {
		t.Errorf("a response for another service provider was accepted: %v", err)
	}
}
