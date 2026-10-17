package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const observability = "../../../deploy/observability"

var seriesName = regexp.MustCompile(`\brelic_[a-z0-9_]+`)

func registered(name string) bool {
	byName := map[string]string{}
	for _, m := range Catalog() {
		byName[m.Name] = m.Type
	}

	if _, ok := byName[name]; ok {
		return true
	}

	for _, suffix := range []string{
		"_bucket",
		"_sum",
		"_count",
	} {
		if base, ok := strings.CutSuffix(name, suffix); ok && byName[base] == "histogram" {
			return true
		}
	}

	return false
}

type grafanaDashboard struct {
	UID        string   `json:"uid"`
	Title      string   `json:"title"`
	Tags       []string `json:"tags"`
	Templating struct {
		List []struct {
			Name string `json:"name"`
		} `json:"list"`
	} `json:"templating"`
	Panels []grafanaPanel `json:"panels"`
}

type grafanaPanel struct {
	Title   string `json:"title"`
	Targets []struct {
		Expr string `json:"expr"`
	} `json:"targets"`
	Panels []grafanaPanel `json:"panels"`
}

func (p grafanaPanel) exprs() []string {
	var out []string
	for _, t := range p.Targets {
		out = append(out, t.Expr)
	}

	for _, c := range p.Panels {
		out = append(out, c.exprs()...)
	}

	return out
}

func TestDashboards(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob(filepath.Join(observability, "grafana/dashboards/*.json"))
	if err != nil || len(paths) != 7 {
		t.Fatalf("dashboards %v, %v: want the seven of the plan", paths, err)
	}

	uids := map[string]bool{}

	for _, p := range paths {
		b, err := os.ReadFile(p) //nolint:gosec // a path in the repository
		if err != nil {
			t.Fatal(err)
		}

		var d grafanaDashboard
		if err := json.Unmarshal(b, &d); err != nil {
			t.Errorf("%s: invalid JSON: %v", p, err)
			continue
		}

		if d.UID == "" || uids[d.UID] || !slices.Contains(d.Tags, "relic") {
			t.Errorf("%s: uid %q (unique, set) and the relic tag %v", p, d.UID, d.Tags)
		}

		uids[d.UID] = true

		vars := make([]string, 0, len(d.Templating.List))
		for _, v := range d.Templating.List {
			vars = append(vars, v.Name)
		}

		for _, want := range []string{
			"datasource",
			"namespace",
			"instance",
		} {
			if !slices.Contains(vars, want) {
				t.Errorf("%s: no %s variable (has %v)", p, want, vars)
			}
		}

		for _, panel := range d.Panels {
			for _, e := range panel.exprs() {
				for _, name := range seriesName.FindAllString(e, -1) {
					if !registered(name) {
						t.Errorf("%s: panel %q uses %s, which Relic does not export", p, panel.Title, name)
					}
				}

				if strings.Contains(e, "relic_") && !strings.Contains(e, `instance=~"$instance"`) {
					t.Errorf("%s: panel %q ignores the instance variable: %s", p, panel.Title, e)
				}
			}
		}
	}
}

type ruleFile struct {
	Groups []struct {
		Name  string `yaml:"name"`
		Rules []struct {
			Record      string            `yaml:"record"`
			Alert       string            `yaml:"alert"`
			Expr        string            `yaml:"expr"`
			Labels      map[string]string `yaml:"labels"`
			Annotations map[string]string `yaml:"annotations"`
		} `yaml:"rules"`
	} `yaml:"groups"`
}

func TestAlertRules(t *testing.T) {
	t.Parallel()

	b, err := os.ReadFile(filepath.Join(observability, "prometheus/relic-rules.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var f ruleFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}

	guide, err := os.ReadFile("../../../web/layers/site/content/docs/3.guides/5.monitoring.md")
	if err != nil {
		t.Fatal(err)
	}

	anchors := map[string]bool{}

	for line := range strings.SplitSeq(string(guide), "\n") {
		if h, ok := strings.CutPrefix(line, "### "); ok {
			anchors[strings.ToLower(strings.TrimSpace(h))] = true
		}
	}

	alerts := map[string]bool{}

	for _, g := range f.Groups {
		for _, r := range g.Rules {
			for _, name := range seriesName.FindAllString(r.Expr, -1) {
				if !registered(name) {
					t.Errorf("rule %s%s uses %s, which Relic does not export", r.Record, r.Alert, name)
				}
			}

			if r.Alert == "" {
				continue
			}

			alerts[r.Alert] = true
			if !slices.Contains([]string{
				"critical",
				"warning",
				"info",
			}, r.Labels["severity"]) {
				t.Errorf("alert %s: severity %q", r.Alert, r.Labels["severity"])
			}

			_, anchor, _ := strings.Cut(r.Annotations["runbook_url"], "#")
			if !strings.HasPrefix(r.Annotations["runbook_url"], "/docs/guides/monitoring#") || !anchors[anchor] {
				t.Errorf(
					"alert %s: runbook_url %q has no section in the Monitoring guide",
					r.Alert,
					r.Annotations["runbook_url"],
				)
			}
		}
	}

	for _, want := range []string{
		"RelicDown",
		"RelicHighErrorRate",
		"RelicDownloadLatencySLO",
		"RelicUpstreamErrors",
		"RelicDBPoolExhausted",
		"RelicJobBacklog",
		"RelicScanBacklog",
		"RelicCVEFeedStale",
		"RelicGCFailing",
		"RelicReplicationFailing",
		"RelicStorageErrors",
		"RelicContentIntegrity",
		"RelicDiskSpaceLow",
		"RelicServerCertificateExpiring",
	} {
		if !alerts[want] {
			t.Errorf("no %s alert", want)
		}
	}
}
