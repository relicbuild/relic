package release

import (
	"errors"
	"testing"
)

func TestEvaluateGate(t *testing.T) {
	t.Parallel()

	vars := map[string]any{
		"release": map[string]any{
			"name":    "app",
			"version": "1.0",
			"tag":     "",
			"artifacts": []any{
				map[string]any{
					"path": "app.jar",
					"size": int64(3),
				},
			},
		},
		"environment": "PROD",
		"evidence": []any{
			map[string]any{
				"predicateType": "https://slsa.dev/provenance/v1",
				"verified":      true,
				"predicate": map[string]any{
					"builder": "ci",
				},
			},
		},
		"scan": map[string]int64{
			"critical":  0,
			"high":      2,
			"unscanned": 0,
		},
		"signed": true,
	}

	tests := []struct {
		name        string
		condition   string
		allowed     bool
		invalid     bool
		wantEvalErr bool
	}{
		{
			name:      "verified provenance",
			condition: `evidence.exists(e, e.predicateType == "https://slsa.dev/provenance/v1" && e.verified)`,
			allowed:   true,
		},
		{
			name:      "predicate field",
			condition: `evidence.exists(e, e.predicate.builder == "ci")`,
			allowed:   true,
		},
		{
			name:      "scan threshold",
			condition: `scan.critical == 0 && scan.high < 2`,
		},
		{
			name:      "signed and every artifact named",
			condition: `signed && release.artifacts.all(a, a.path != "")`,
			allowed:   true,
		},
		{
			name:        "a missing key refuses",
			condition:   `release.missing == "x"`,
			wantEvalErr: true,
		},
		{
			name:      "not a boolean",
			condition: `release.name`,
			invalid:   true,
		},
		{
			name:      "unknown variable",
			condition: `build.name == "x"`,
			invalid:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			allowed, err := evaluateGate(tt.condition, vars)
			if tt.invalid != errors.Is(err, ErrInvalid) || (err != nil) != (tt.invalid || tt.wantEvalErr) {
				t.Fatalf("err = %v, want invalid %v, an evaluation error %v", err, tt.invalid, tt.wantEvalErr)
			}

			if allowed != tt.allowed {
				t.Errorf("allowed = %v, want %v", allowed, tt.allowed)
			}
		})
	}
}
