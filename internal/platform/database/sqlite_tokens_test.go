package database

import (
	"testing"
	"uuid"
)

func TestSQLiteListAccessTokensPutsTheLaterOfTwoSimultaneousTokensFirst(t *testing.T) {
	t.Parallel()

	db := newSQLiteTestDB(t)
	earlier, later := uuid.NewV7(), uuid.NewV7()

	for _, id := range []uuid.UUID{
		earlier,
		later,
	} {
		if err := db.InsertAccessToken(t.Context(), InsertAccessTokenParams{
			ID:        id,
			Username:  "dana",
			ScopeKind: "user",
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := db.engine.(*sqliteEngine).writer.ExecContext(
		t.Context(),
		`UPDATE access_tokens SET created = '2026-01-01 00:00:00.000000+00:00'`,
	); err != nil {
		t.Fatal(err)
	}

	username := "dana"

	tokens, err := db.ListAccessTokens(t.Context(), ListAccessTokensParams{
		Username: &username,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(tokens) != 2 || tokens[0].ID != later || tokens[1].ID != earlier {
		t.Errorf("tokens created at the same time are listed %v, want the later %s first", tokens, later)
	}
}
