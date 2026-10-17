//go:build integration

package blocking_test

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
	"uuid"

	"relic/internal/access/audit"
	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
	"relic/internal/scanning/scan"
	"relic/internal/scanning/scan/blocking"
	"relic/internal/scanning/scan/policy"
)

func TestDownloadBlocking(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := databasetest.NewDB(t)

	repos := repository.NewService(db, objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))
	if err := repos.CreateRepository(
		ctx,
		repository.Repository{
			Name:   "libs",
			Class:  "local",
			Format: "maven",
		},
	); err != nil {
		t.Fatal(err)
	}

	deploy := func(p, body string) string {
		it, err := repos.Deploy(
			ctx,
			repository.DeployInput{
				Repository: "libs",
				Path:       p,
				Body:       strings.NewReader(body),
				User:       "admin",
			},
		)
		if err != nil {
			t.Fatal(err)
		}

		return it.Checksums.SHA256
	}
	vulnerable, clean, unscanned := deploy("a-1.jar", "a"), deploy("b-1.jar", "b"), deploy("c-1.jar", "c")

	now := time.Now()
	for _, sha := range []string{
		vulnerable,
		clean,
	} {
		if _, err := db.ClaimUnscanned(
			ctx,
			database.ClaimUnscannedParams{
				Now:         now,
				NamePattern: "^[ab]-1.jar$",
				MaxRows:     10,
			},
		); err != nil {
			t.Fatal(err)
		}

		if err := db.FinishScanArtifact(
			ctx,
			database.FinishScanArtifactParams{
				Sha256: sha,
				Status: scan.StatusDone,
				Now:    now,
			},
		); err != nil {
			t.Fatal(err)
		}
	}

	cvss, _ := json.Marshal([]scan.CVSS{
		{
			Version: "3.1",
			Score:   9.8,
		},
	})
	if err := db.InsertFinding(ctx, database.InsertFindingParams{
		Sha256:         vulnerable,
		VulnID:         "CVE-1",
		Purl:           "pkg:maven/g/a@1",
		PackageName:    "a",
		PackageVersion: "1",
		Severity:       string(scan.SeverityCritical),
		Summary:        "bad",
		Cves: []string{
			"CVE-1",
		},
		FixedVersions: []string{},
		Cvss:          cvss,
		Urls:          []string{},
	}); err != nil {
		t.Fatal(err)
	}

	b := blocking.New(db)
	decisions := &policy.Decisions{
		DB: db,
	}
	b.SetDecisions(decisions)

	check := func(sha, name string) bool {
		t.Helper()

		d, err := b.Check(ctx, "libs", name, sha)
		if err != nil {
			t.Fatal(err)
		}

		return d.Blocked
	}
	if check(vulnerable, "a-1.jar") {
		t.Error("blocked with no watches")
	}

	pol := policy.Policy{
		Name: "block",
		Kind: policy.KindSecurity,
		Rules: []policy.Rule{
			{
				Name:           "critical",
				MinSeverity:    scan.SeverityCritical,
				BlockDownload:  true,
				BlockUnscanned: true,
			},
		},
	}
	watch := policy.Watch{
		Name:   "w",
		Active: true,
		Scopes: []policy.Scope{
			{
				Kind: policy.ScopeRepository,
				Names: []string{
					"libs",
				},
			},
		},
		Policies: []string{
			"block",
		},
	}
	pd, _ := json.Marshal(pol)
	wd, _ := json.Marshal(watch)

	if err := db.UpsertScanPolicy(
		ctx,
		database.UpsertScanPolicyParams{
			Name: "block",
			Kind: string(policy.KindSecurity),
			Doc:  pd,
			Now:  now,
		},
	); err != nil {
		t.Fatal(err)
	}

	if err := db.UpsertScanWatch(ctx, database.UpsertScanWatchParams{
		Name: "w",
		Doc:  wd,
		Now:  now,
	}); err != nil {
		t.Fatal(err)
	}

	b.Invalidate()

	if !check(vulnerable, "a-1.jar") {
		t.Error("critical finding not blocked")
	}

	if check(clean, "b-1.jar") {
		t.Error("clean artifact blocked")
	}

	if !check(unscanned, "c-1.jar") {
		t.Error("unscanned scannable artifact not blocked")
	}

	if check(unscanned, "README.md") {
		t.Error("non-scannable file blocked as unscanned")
	}

	if d, err := b.Check(ctx, "other", "a-1.jar", vulnerable); err != nil || d.Blocked {
		t.Errorf("repository no watch covers: %+v, %v; want allowed", d, err)
	}

	_ = check(vulnerable, "a-1.jar")

	recorded, err := decisions.List(ctx, policy.DecisionDownload, 10)
	if err != nil {
		t.Fatal(err)
	}

	byPath := map[string]policy.Decision{}
	for _, d := range recorded {
		byPath[d.Subject] = d
	}

	if d := byPath["libs/a-1.jar"]; d.Policy != "block" || d.Watch != "w" || d.Rule != "critical" || d.Count != 2 {
		t.Errorf("the decision on a-1.jar = %+v, want policy block, watch w, rule critical, twice", d)
	}

	if d := byPath["libs/c-1.jar"]; d.Rule != "unscanned" || d.Watch != "w" {
		t.Errorf("the decision on c-1.jar = %+v, want rule unscanned of watch w", d)
	}

	if len(recorded) != 2 {
		t.Errorf("recorded %d decisions, want 2: %+v", len(recorded), recorded)
	}

	admin := &policy.Admin{
		DB: db,
	}
	preview := policy.Policy{
		Name: "draft",
		Kind: policy.KindSecurity,
		Rules: []policy.Rule{
			{
				Name:        "high",
				MinSeverity: scan.SeverityHigh,
				FailBuild:   true,
			},
		},
	}

	hits, truncated, err := admin.Preview(ctx, preview, nil, 10)
	if err != nil || truncated || len(hits) != 1 || hits[0].Repository != "libs" || hits[0].Path != "a-1.jar" ||
		hits[0].Rule != "high" || !hits[0].FailBuild || hits[0].Issue != "CVE-1 in pkg:maven/g/a@1" {
		t.Errorf("preview = %+v, %v, %v", hits, truncated, err)
	}

	if hits, _, err := admin.Preview(ctx, preview, []string{
		"other",
	}, 10); err != nil || len(hits) != 0 {
		t.Errorf("preview of another repository = %+v, %v", hits, err)
	}

	if err := repos.CreateRepository(ctx, repository.Repository{
		Name:   "zz-libs",
		Class:  "local",
		Format: "maven",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := repos.Deploy(ctx, repository.DeployInput{
		Repository: "zz-libs",
		Path:       "copy/a-1.jar",
		Body:       strings.NewReader("a"),
		User:       "admin",
	}); err != nil {
		t.Fatal(err)
	}

	if hits, _, err := admin.Preview(ctx, preview, []string{
		"zz-libs",
	}, 10); err != nil || len(hits) != 1 || hits[0].Repository != "zz-libs" || hits[0].Path != "copy/a-1.jar" {
		t.Errorf("preview of the repository holding the second copy = %+v, %v", hits, err)
	}

	if _, _, err := admin.Preview(ctx, policy.Policy{
		Name: "empty",
	}, nil, 10); err == nil {
		t.Error("previewed an invalid policy")
	}

	rule, _ := json.Marshal(policy.IgnoreRule{
		Packages: []string{
			"pkg:maven/g/a",
		},
	})
	if err := db.InsertIgnoreRule(
		ctx,
		database.InsertIgnoreRuleParams{
			ID:       uuid.NewV4(),
			Doc:      rule,
			Now:      now,
			Username: "admin",
		},
	); err != nil {
		t.Fatal(err)
	}

	b.Invalidate()

	if check(vulnerable, "a-1.jar") {
		t.Error("blocked despite an ignore rule on its package")
	}
}

func TestLicenseBlocking(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := databasetest.NewDB(t)

	repos := repository.NewService(db, objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))
	if err := repos.CreateRepository(
		ctx,
		repository.Repository{
			Name:   "libs",
			Class:  "local",
			Format: "npm",
		},
	); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	scanned := func(name, license string) string {
		t.Helper()

		it, err := repos.Deploy(
			ctx,
			repository.DeployInput{
				Repository: "libs",
				Path:       name,
				Body:       strings.NewReader(name),
				User:       "admin",
			},
		)
		if err != nil {
			t.Fatal(err)
		}

		sbom := `{"artifacts":[{"name":"dep","version":"1","type":"npm","purl":"pkg:npm/dep@1",` +
			`"licenses":[{"value":"` + license + `","spdxExpression":"` + license + `"}]}]}`

		var gz strings.Builder

		zw := gzip.NewWriter(&gz)
		_, _ = zw.Write([]byte(sbom))
		_ = zw.Close()

		if _, err := db.ClaimUnscanned(
			ctx,
			database.ClaimUnscannedParams{
				Now:         now,
				NamePattern: "^" + name + "$",
				MaxRows:     10,
			},
		); err != nil {
			t.Fatal(err)
		}

		if err := db.FinishScanArtifact(ctx, database.FinishScanArtifactParams{
			Sha256:     it.Checksums.SHA256,
			Status:     scan.StatusDone,
			Sbom:       []byte(gz.String()),
			Components: 1,
			Now:        now,
		}); err != nil {
			t.Fatal(err)
		}

		return it.Checksums.SHA256
	}
	gpl, dual := scanned("gpl-1.tgz", "MIT AND GPL-3.0-only"), scanned("dual-1.tgz", "MIT OR GPL-3.0-only")

	pd, _ := json.Marshal(policy.Policy{
		Name: "no-gpl",
		Kind: policy.KindLicense,
		Rules: []policy.Rule{
			{
				Name: "ban",
				BannedLicenses: []string{
					"GPL-3.0-only",
				},
				BlockDownload: true,
			},
		},
	})
	wd, _ := json.Marshal(policy.Watch{
		Name:   "w",
		Active: true,
		Scopes: []policy.Scope{
			{
				Kind: policy.ScopeRepository,
				Names: []string{
					"libs",
				},
			},
		},
		Policies: []string{
			"no-gpl",
		},
	})

	if err := db.UpsertScanPolicy(
		ctx,
		database.UpsertScanPolicyParams{
			Name: "no-gpl",
			Kind: string(policy.KindLicense),
			Doc:  pd,
			Now:  now,
		},
	); err != nil {
		t.Fatal(err)
	}

	if err := db.UpsertScanWatch(ctx, database.UpsertScanWatchParams{
		Name: "w",
		Doc:  wd,
		Now:  now,
	}); err != nil {
		t.Fatal(err)
	}

	b := blocking.New(db)
	recorded := &recorder{}
	b.SetAudit(recorded)

	d, err := b.Check(auth.WithPrincipal(ctx, auth.Principal{
		Username: "bob",
	}), "libs", "gpl-1.tgz", gpl)
	if err != nil {
		t.Fatal(err)
	}

	if !d.Blocked || !strings.Contains(d.Reason, "GPL-3.0-only") || !strings.Contains(d.Reason, "no-gpl") {
		t.Errorf("GPL component: %+v, want blocked by no-gpl", d)
	}

	if d, err := b.Check(ctx, "libs", "dual-1.tgz", dual); err != nil || d.Blocked {
		t.Errorf("a component that may be used under MIT: %+v, %v; want allowed", d, err)
	}

	if len(recorded.events) != 1 || recorded.events[0].Actor != "bob" ||
		recorded.events[0].Target != "libs/gpl-1.tgz" || recorded.events[0].Outcome != audit.Denied {
		t.Errorf("audit events = %+v, want bob's blocked download of libs/gpl-1.tgz", recorded.events)
	}
}

type recorder struct {
	events []audit.Event
}

func (r *recorder) Record(_ context.Context, e audit.Event) {
	r.events = append(r.events, e)
}

func TestSecretsBlocking(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	repos := repository.NewService(db, objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))

	if err := repos.CreateRepository(ctx, repository.Repository{
		Name:   "apps",
		Class:  "local",
		Format: "generic",
	}); err != nil {
		t.Fatal(err)
	}

	it, err := repos.Deploy(ctx, repository.DeployInput{
		Repository: "apps",
		Path:       "app-1.tgz",
		Body:       strings.NewReader("app"),
		User:       "admin",
	})
	if err != nil {
		t.Fatal(err)
	}

	sha, now := it.Checksums.SHA256, time.Now()
	if _, err := db.ClaimUnscanned(ctx, database.ClaimUnscannedParams{
		Now:         now,
		NamePattern: "^app-1.tgz$",
		MaxRows:     10,
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.FinishScanArtifact(ctx, database.FinishScanArtifactParams{
		Sha256: sha,
		Status: scan.StatusDone,
		Now:    now,
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.InsertIssue(ctx, database.InsertIssueParams{
		Sha256:   sha,
		Analyzer: scan.AnalyzerSecrets,
		Rule:     "aws-access-token",
		Severity: string(scan.SeverityHigh),
		Title:    "AWS credentials",
		File:     "config/credentials.ini",
		Line:     1,
	}); err != nil {
		t.Fatal(err)
	}

	b := blocking.New(db)
	save := func(rule policy.Rule) {
		t.Helper()

		pd, _ := json.Marshal(policy.Policy{
			Name: "p",
			Kind: policy.KindSecurity,
			Rules: []policy.Rule{
				rule,
			},
		})
		wd, _ := json.Marshal(policy.Watch{
			Name:   "w",
			Active: true,
			Scopes: []policy.Scope{
				{
					Kind: policy.ScopeRepository,
					Names: []string{
						"apps",
					},
				},
			},
			Policies: []string{
				"p",
			},
		})

		if err := db.UpsertScanPolicy(ctx, database.UpsertScanPolicyParams{
			Name: "p",
			Kind: string(policy.KindSecurity),
			Doc:  pd,
			Now:  now,
		}); err != nil {
			t.Fatal(err)
		}

		if err := db.UpsertScanWatch(ctx, database.UpsertScanWatchParams{
			Name: "w",
			Doc:  wd,
			Now:  now,
		}); err != nil {
			t.Fatal(err)
		}

		b.Invalidate()
	}

	save(policy.Rule{
		Name:          "vulnerabilities",
		MinSeverity:   scan.SeverityLow,
		BlockDownload: true,
	})

	if d, err := b.Check(ctx, "apps", "app-1.tgz", sha); err != nil || d.Blocked {
		t.Errorf("a vulnerability rule blocked a secret: %+v %v", d, err)
	}

	save(policy.Rule{
		Name:          "secrets",
		Secrets:       true,
		BlockDownload: true,
	})

	d, err := b.Check(ctx, "apps", "app-1.tgz", sha)
	if err != nil || !d.Blocked || !strings.Contains(d.Reason, "secrets aws-access-token in config/credentials.ini") {
		t.Errorf("secret not blocked: %+v %v", d, err)
	}
}

func TestFileLicenseBlocking(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	repos := repository.NewService(db, objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))

	if err := repos.CreateRepository(ctx, repository.Repository{
		Name:   "src",
		Class:  "local",
		Format: "generic",
	}); err != nil {
		t.Fatal(err)
	}

	it, err := repos.Deploy(ctx, repository.DeployInput{
		Repository: "src",
		Path:       "lib-1.tgz",
		Body:       strings.NewReader("lib"),
		User:       "admin",
	})
	if err != nil {
		t.Fatal(err)
	}

	sha, now := it.Checksums.SHA256, time.Now()
	if _, err := db.ClaimUnscanned(ctx, database.ClaimUnscannedParams{
		Now:         now,
		NamePattern: ".",
		MaxRows:     10,
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.FinishScanArtifact(ctx, database.FinishScanArtifactParams{
		Sha256: sha,
		Status: scan.StatusDone,
		Now:    now,
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.InsertIssue(ctx, database.InsertIssueParams{
		Sha256:   sha,
		Analyzer: scan.AnalyzerLicense,
		Rule:     "GPL-3.0-only",
		Severity: string(scan.SeverityUnknown),
		Title:    "License GPL-3.0-only",
		File:     "lib/main.c",
		Line:     1,
	}); err != nil {
		t.Fatal(err)
	}

	pd, _ := json.Marshal(policy.Policy{
		Name: "no-gpl",
		Kind: policy.KindLicense,
		Rules: []policy.Rule{
			{
				Name: "banned",
				BannedLicenses: []string{
					"GPL-3.0-only",
				},
				BlockDownload: true,
			},
		},
	})
	wd, _ := json.Marshal(policy.Watch{
		Name:   "w",
		Active: true,
		Scopes: []policy.Scope{
			{
				Kind: policy.ScopeRepository,
				Names: []string{
					"src",
				},
			},
		},
		Policies: []string{
			"no-gpl",
		},
	})

	if err := db.UpsertScanPolicy(ctx, database.UpsertScanPolicyParams{
		Name: "no-gpl",
		Kind: string(policy.KindLicense),
		Doc:  pd,
		Now:  now,
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.UpsertScanWatch(ctx, database.UpsertScanWatchParams{
		Name: "w",
		Doc:  wd,
		Now:  now,
	}); err != nil {
		t.Fatal(err)
	}

	d, err := blocking.New(db).Check(ctx, "src", "lib-1.tgz", sha)
	if err != nil || !d.Blocked || !strings.Contains(d.Reason, "lib/main.c under GPL-3.0-only") {
		t.Errorf("decision = %+v %v", d, err)
	}
}
