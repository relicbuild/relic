//go:build integration

package integration

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"relic/api/adapters/artifactory/access"
	"relic/internal/access/auth"
	"relic/internal/app"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestAnonymousVisitorsReadTheSignInNote(t *testing.T) {
	t.Parallel()

	const note = "Sign in as demo / demo. This demo is read-only."

	db := databasetest.NewDB(t)

	tokens, err := auth.NewTokens(t.Context(), db, nil, access.Format{})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(
		app.NewHandler(db, objectstoretest.NewMemory(), tokens, nil, app.Options{
			SignInNote: note,
		}, slog.New(slog.DiscardHandler)),
	)
	t.Cleanup(srv.Close)

	resp, body := do(t, srv, call{
		method: "GET",
		path:   "/api/v1/sign-in-note",
		noAuth: true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}

	var got struct {
		Note string `json:"note"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}

	if got.Note != note {
		t.Errorf("note = %q, want %q", got.Note, note)
	}
}
