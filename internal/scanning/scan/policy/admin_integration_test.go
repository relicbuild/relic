//go:build integration

package policy_test

import (
	"errors"
	"testing"

	"relic/internal/platform/database/databasetest"
	"relic/internal/scanning/scan"
	"relic/internal/scanning/scan/policy"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

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
