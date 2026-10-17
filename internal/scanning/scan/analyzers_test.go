package scan

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestParseAnalyzerReports(t *testing.T) {
	t.Parallel()

	b, err := os.ReadFile("testdata/gitleaks.json")
	if err != nil {
		t.Fatal(err)
	}

	secrets, err := parseGitleaks(b, "/src", false)
	if err != nil || len(secrets) != 1 {
		t.Fatalf("gitleaks = %+v %v, want the fixture's one finding", secrets, err)
	}

	s := secrets[0]
	if s.Analyzer != AnalyzerSecrets || s.Rule != "aws-access-token" || s.File != "chart/credentials.ini" ||
		s.Line != 1 ||
		s.Severity != SeverityHigh {
		t.Errorf("secret = %+v", s)
	}

	b, err = os.ReadFile("testdata/trivy-config.json")
	if err != nil {
		t.Fatal(err)
	}

	misconfigs, err := parseTrivy(b)
	if err != nil || len(misconfigs) != 18 {
		t.Fatalf("trivy = %d %v; want the fixture's 18 failed checks", len(misconfigs), err)
	}

	found := false

	for _, m := range misconfigs {
		if m.Analyzer != AnalyzerMisconfiguration || !m.Severity.Known() || m.File != "templates/deployment.yaml" {
			t.Errorf("misconfiguration = %+v", m)
		}

		found = found || (m.Rule == "KSV-0017" && m.Severity == SeverityHigh)
	}

	if !found {
		t.Errorf("no privileged-container check (KSV-0017) in %+v", misconfigs)
	}

	var syntax *json.SyntaxError
	if _, err := parseGitleaks([]byte("not json"), "/", false); !errors.As(err, &syntax) {
		t.Errorf("gitleaks: bad output gave %v, want the decode error", err)
	}
}

func TestParseScanCode(t *testing.T) {
	t.Parallel()

	b, err := os.ReadFile("testdata/scancode.json")
	if err != nil {
		t.Fatal(err)
	}

	issues, err := parseScanCode(b, "src")
	if err != nil || len(issues) != 2 {
		t.Fatalf("scancode = %+v %v", issues, err)
	}

	want := map[string]string{
		"LICENSE": "MIT",
		"main.c":  "GPL-3.0-only",
	}
	for _, i := range issues {
		if i.Analyzer != AnalyzerLicense || want[i.File] != i.Rule || i.Line != 1 || i.Severity != SeverityUnknown {
			t.Errorf("issue = %+v", i)
		}
	}
}
