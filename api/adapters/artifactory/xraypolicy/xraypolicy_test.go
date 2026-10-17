package xraypolicy

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"relic/internal/scanning/scan"
	"relic/internal/scanning/scan/policy"
)

const (
	securityPolicy = `{"name":"sec","type":"security","description":"d","rules":[{"name":"crit","priority":1,
		"criteria":{"min_severity":"Critical","fix_version_dependant":true},"actions":{"fail_build":true,"block_download":{"active":true,"unscanned":false}}},
		{"name":"cvss","priority":2,"criteria":{"cvss_range":{"from":7,"to":10}},"actions":{"fail_build":false,"block_download":{"active":false,"unscanned":false}}}]}`
	licensePolicy = `{"name":"lic","type":"license","rules":[{"name":"banned","priority":1,
		"criteria":{"banned_licenses":["AGPL-3.0"],"allow_unknown":false},"actions":{"fail_build":false,"block_download":{"active":true,"unscanned":true}}}]}`
	watch = `{"general_data":{"name":"w","description":"all","active":true},
		"project_resources":{"resources":[{"type":"repository","name":"libs","bin_mgr_id":"default"},{"type":"all-builds","bin_mgr_id":"default"}]},
		"assigned_policies":[{"name":"sec","type":"security"},{"name":"lic","type":"license"}]}`
	ignoreRule = `{"id":"7d1c2f7e-0000-4000-8000-000000000001","notes":"accepted","expires_at":"2027-01-01T00:00:00Z",
		"ignore_filters":{"cves":["CVE-2021-44228"],"components":[{"name":"gav://org.apache:log4j","version":"2.14.1"}],"artifacts":[{"path":"libs/app/"}]}}`
)

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	for name, doc := range map[string]string{
		"security policy": securityPolicy,
		"license policy":  licensePolicy,
	} {
		var p Policy
		if err := json.Unmarshal([]byte(doc), &p); err != nil {
			t.Fatal(err)
		}

		same(t, name, PolicyToXray(PolicyFromXray(p)), doc)
	}

	var w Watch
	if err := json.Unmarshal([]byte(watch), &w); err != nil {
		t.Fatal(err)
	}

	kinds := map[string]policy.Kind{
		"sec": policy.KindSecurity,
		"lic": policy.KindLicense,
	}
	same(t, "watch", WatchToXray(WatchFromXray(w), kinds), watch)

	var r IgnoreRule
	if err := json.Unmarshal([]byte(ignoreRule), &r); err != nil {
		t.Fatal(err)
	}

	native, err := IgnoreRuleFromXray(r)
	if err != nil {
		t.Fatal(err)
	}

	if native.Packages[0] != "pkg:maven/org.apache/log4j@2.14.1" {
		t.Errorf("packages = %v", native.Packages)
	}

	same(t, "ignore rule", IgnoreRuleToXray(native), ignoreRule)

	unmapped := "pkg:swift/github.com/apple/swift-nio@2.0.0"
	if got := IgnoreRuleToXray(policy.IgnoreRule{
		Packages: []string{
			unmapped,
		},
	}).IgnoreFilters.Components; len(got) != 1 || got[0].Name != unmapped {
		t.Errorf("components of a package without an Xray name = %+v, want it named by its package URL", got)
	}
}

func TestNative(t *testing.T) {
	t.Parallel()

	var p Policy
	if err := json.Unmarshal([]byte(securityPolicy), &p); err != nil {
		t.Fatal(err)
	}

	native := PolicyFromXray(p)
	if native.Kind != policy.KindSecurity || native.Rules[0].MinSeverity != scan.SeverityCritical ||
		!native.Rules[0].BlockDownload {
		t.Errorf("policy = %+v", native)
	}

	if Severity("Information") != scan.SeverityLow {
		t.Error("the Information severity is not low")
	}

	var w Watch
	if err := json.Unmarshal([]byte(watch), &w); err != nil {
		t.Fatal(err)
	}

	got := WatchFromXray(w)

	want := []policy.Scope{
		{
			Kind: policy.ScopeRepository,
			Names: []string{
				"libs",
			},
		},
		{
			Kind: policy.ScopeBuild,
			All:  true,
		},
	}
	if !reflect.DeepEqual(got.Scopes, want) || !reflect.DeepEqual(got.Policies, []string{
		"sec",
		"lic",
	}) {
		t.Errorf("watch = %+v", got)
	}

	if _, err := IgnoreRuleFromXray(
		IgnoreRule{
			IgnoreFilters: IgnoreFilters{
				Components: []IgnoreComponent{
					{
						Name: "docker://img",
					},
				},
			},
		},
	); !errors.Is(err, policy.ErrInvalid) {
		t.Errorf("unsupported component: %v", err)
	}
}

func same(t *testing.T, name string, got any, want string) {
	t.Helper()

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}

	var g, w any
	if json.Unmarshal(b, &g) != nil || json.Unmarshal([]byte(want), &w) != nil {
		t.Fatal("bad JSON")
	}

	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s:\ngot  %s\nwant %s", name, b, want)
	}
}
