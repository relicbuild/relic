//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func TestXrayPolicyUpdateKeepsCriteriaTheDocumentCannotCarry(t *testing.T) {
	t.Parallel()

	srv := server(t)
	ctx := t.Context()
	scans := relicv1connect.NewScanServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	if _, err := scans.PutPolicy(ctx, &relicv1.PutPolicyRequest{
		Policy: &relicv1.Policy{
			Name:        "narrow",
			Kind:        relicv1.PolicyKind_POLICY_KIND_SECURITY,
			Description: "keep me",
			Rules: []*relicv1.PolicyRule{
				{
					Name:          "cond",
					Priority:      1,
					MinSeverity:   relicv1.Severity_SEVERITY_HIGH,
					Condition:     `package_name == "nothing-matches"`,
					BlockDownload: true,
				},
				{
					Name:          "mal",
					Priority:      2,
					Malicious:     true,
					BlockDownload: true,
				},
				{
					Name:              "reachable",
					Priority:          3,
					MinSeverity:       relicv1.Severity_SEVERITY_CRITICAL,
					IgnoreUnreachable: true,
					FailBuild:         true,
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	resp, doc := do(t, srv, call{
		method: http.MethodGet,
		path:   "/xray/api/v2/policies/narrow",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET policy = %d %s", resp.StatusCode, doc)
	}

	if resp, body := do(t, srv, call{
		method: http.MethodPut,
		path:   "/xray/api/v2/policies/narrow",
		body:   strings.Replace(doc, `"keep me"`, `"changed"`, 1),
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT of the policy as GET returned it = %d %s", resp.StatusCode, body)
	}

	list, err := scans.ListPolicies(ctx, &relicv1.ListPoliciesRequest{})
	if err != nil {
		t.Fatal(err)
	}

	rules := map[string]*relicv1.PolicyRule{}

	for _, p := range list.GetPolicies() {
		if p.GetName() != "narrow" {
			continue
		}

		if p.GetDescription() != "changed" {
			t.Errorf("description = %q, want the update applied", p.GetDescription())
		}

		for _, r := range p.GetRules() {
			rules[r.GetName()] = r
		}
	}

	if rules["cond"].GetCondition() != `package_name == "nothing-matches"` {
		t.Errorf("the condition rule after the round trip = %v", rules["cond"])
	}

	if !rules["mal"].GetMalicious() {
		t.Errorf("the malicious rule after the round trip = %v", rules["mal"])
	}

	if !rules["reachable"].GetIgnoreUnreachable() {
		t.Errorf("the reachability rule after the round trip = %v", rules["reachable"])
	}
}
