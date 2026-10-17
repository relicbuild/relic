//go:build integration

package database_test

import (
	"slices"
	"testing"
	"uuid"

	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
)

func TestGroupNamesMatchIgnoringTheCaseOfNonASCIILetters(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)

	if _, err := db.UpsertGroup(ctx, database.UpsertGroupParams{
		Name: "Équipe-Ops",
	}); err != nil {
		t.Fatal(err)
	}

	groups, err := db.ListGroupsPage(ctx, database.ListGroupsPageParams{
		Name:    "équipe-ops",
		MaxRows: 10,
	})
	if err != nil || len(groups) != 1 {
		t.Errorf("groups named équipe-ops = %v, %v; want Équipe-Ops", groups, err)
	}

	if err := db.CreateUser(ctx, database.CreateUserParams{
		ID:           uuid.NewV7(),
		Username:     "ana",
		Email:        "ana@example.com",
		PasswordHash: "x",
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.ReplaceSsoMemberships(ctx, database.ReplaceSsoMembershipsParams{
		Username: "ana",
		GroupNames: []string{
			"équipe-ops",
		},
	}); err != nil {
		t.Fatal(err)
	}

	members, err := db.GroupMembers(ctx, "Équipe-Ops")
	if err != nil || !slices.Equal(members, []string{
		"ana",
	}) {
		t.Errorf("members of Équipe-Ops after the sign-in = %v, %v; want ana", members, err)
	}
}
