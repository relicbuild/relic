//go:build integration

package policy_test

import (
	"errors"
	"testing"

	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/scanning/scan"
	"relic/internal/scanning/scan/policy"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func TestWatchCoversOnlyRepositoriesThatHoldFiles(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	admin := &policy.Admin{
		DB: db,
	}

	for key, class := range map[string]string{
		"libs":   "local",
		"mirror": "remote",
		"all":    "virtual",
	} {
		if err := db.CreateRepository(ctx, database.CreateRepositoryParams{
			Key:    key,
			Class:  class,
			Format: "generic",
			Config: []byte(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}

	for name, tt := range map[string]struct {
		scope   policy.Scope
		refused bool
	}{
		"local and remote repositories": {
			scope: policy.Scope{
				Kind: policy.ScopeRepository,
				Names: []string{
					"libs",
					"mirror",
				},
			},
		},
		"every repository": {
			scope: policy.Scope{
				Kind: policy.ScopeRepository,
				All:  true,
			},
		},
		"a build not published yet": {
			scope: policy.Scope{
				Kind: policy.ScopeBuild,
				Names: []string{
					"app",
				},
			},
		},
		"a virtual repository": {
			scope: policy.Scope{
				Kind: policy.ScopeRepository,
				Names: []string{
					"all",
				},
			},
			refused: true,
		},
		"a repository that does not exist": {
			scope: policy.Scope{
				Kind: policy.ScopeRepository,
				Names: []string{
					"libz",
				},
			},
			refused: true,
		},
	} {
		err := admin.SaveWatch(ctx, policy.Watch{
			Name:   name,
			Active: true,
			Scopes: []policy.Scope{
				tt.scope,
			},
			Policies: []string{},
		}, true)
		if refused := errors.Is(err, policy.ErrInvalid); refused != tt.refused || (err != nil && !refused) {
			t.Errorf("%s: SaveWatch = %v, want refused %v", name, err, tt.refused)
		}
	}
}

func TestReplacingADeletedPolicyOrWatchDoesNotRecreateIt(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	admin := &policy.Admin{
		DB: databasetest.NewDB(t),
	}

	p := policy.Policy{
		Name: "critical",
		Kind: policy.KindSecurity,
		Rules: []policy.Rule{
			{
				Name:        "any",
				MinSeverity: scan.SeverityLow,
			},
		},
	}
	w := policy.Watch{
		Name:     "everything",
		Active:   true,
		Policies: []string{},
	}

	if err := admin.SavePolicy(ctx, p, true); err != nil {
		t.Fatal(err)
	}

	if err := admin.SaveWatch(ctx, w, true); err != nil {
		t.Fatal(err)
	}

	if err := admin.DeletePolicy(ctx, p.Name); err != nil {
		t.Fatal(err)
	}

	if err := admin.DeleteWatch(ctx, w.Name); err != nil {
		t.Fatal(err)
	}

	if err := admin.SavePolicy(ctx, p, false); !errors.Is(err, policy.ErrNotFound) {
		t.Errorf("replacing a deleted policy = %v, want ErrNotFound", err)
	}

	if _, err := admin.Policy(ctx, p.Name); !errors.Is(err, policy.ErrNotFound) {
		t.Errorf("the deleted policy after the replace: %v, want ErrNotFound", err)
	}

	if err := admin.SaveWatch(ctx, w, false); !errors.Is(err, policy.ErrNotFound) {
		t.Errorf("replacing a deleted watch = %v, want ErrNotFound", err)
	}

	if _, err := admin.Watch(ctx, w.Name); !errors.Is(err, policy.ErrNotFound) {
		t.Errorf("the deleted watch after the replace: %v, want ErrNotFound", err)
	}
}

func TestImportIgnoreRulesKeepsRulesAlreadyStored(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	admin := &policy.Admin{
		DB: databasetest.NewDB(t),
	}
	rule := func(notes, pkg string) policy.IgnoreRule {
		return policy.IgnoreRule{
			Notes: notes,
			CVEs: []string{
				"CVE-2024-0001",
			},
			Packages: []string{
				pkg,
			},
		}
	}

	tests := []struct {
		name         string
		rules        []policy.IgnoreRule
		wantCreated  int
		wantExisting int
	}{
		{
			name: "a document with a statement twice",
			rules: []policy.IgnoreRule{
				rule("VEX 1", "pkg:npm/lodash@4.17.15"),
				rule("VEX 1 again", "pkg:npm/lodash@4.17.15"),
				rule("VEX 1", "pkg:npm/left-pad@1.3.0"),
			},
			wantCreated:  2,
			wantExisting: 1,
		},
		{
			name: "the same document again",
			rules: []policy.IgnoreRule{
				rule("VEX 1", "pkg:npm/lodash@4.17.15"),
				rule("VEX 1", "pkg:npm/left-pad@1.3.0"),
			},
			wantExisting: 2,
		},
		{
			name: "a new package",
			rules: []policy.IgnoreRule{
				rule("VEX 2", "pkg:npm/lodash@4.17.15"),
				rule("VEX 2", "pkg:npm/minimist@1.2.5"),
			},
			wantCreated:  1,
			wantExisting: 1,
		},
	}
	for _, tt := range tests {
		created, existing, err := admin.ImportIgnoreRules(ctx, tt.rules, "admin")
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}

		if len(created) != tt.wantCreated || existing != tt.wantExisting {
			t.Errorf("%s: %d created, %d existing; want %d, %d",
				tt.name, len(created), existing, tt.wantCreated, tt.wantExisting)
		}
	}

	stored, err := admin.IgnoreRules(ctx, false)
	if err != nil || len(stored) != 3 {
		t.Errorf("stored rules = %d %v, want 3", len(stored), err)
	}
}
