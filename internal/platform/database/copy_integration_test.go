//go:build integration

package database_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
)

const copySeed = `
INSERT INTO repositories (key, class, format, project, config, created, updated) VALUES
  ('libs', 'local', 'maven', '', '{"layout": "maven-2", "n": 1}', '2026-01-02 03:04:05.123456+04', '2026-01-02 03:04:05+00'),
  ('npm', 'remote', 'npm', 'proj', '{"url": "https://registry.npmjs.org"}', now(), now());
INSERT INTO blobs (sha256, sha1, md5, size, refcount, created, unreferenced_since) VALUES
  ('aa', 'a1', 'a5', 10, 1, '2026-01-02 00:00:00.000001+00', NULL),
  ('bb', 'b1', 'b5', 0, 0, now(), '2026-03-04 05:06:07.654321+00');
INSERT INTO items (id, repo_key, path, name, type, size, sha1, sha256, md5, created, created_by, modified, modified_by, updated) VALUES
  ('0192f7a0-0000-7000-8000-000000000001', 'libs', 'org/app', 'app-1.0.jar', 'file', 10, 'a1', 'aa', 'a5', now(), 'admin', now(), 'admin', now()),
  ('0192f7a0-0000-7000-8000-000000000002', 'libs', 'org', 'app', 'folder', 0, NULL, NULL, NULL, now(), 'ünïcødé', now(), 'admin', now());
INSERT INTO item_properties (item_id, key, value) VALUES
  ('0192f7a0-0000-7000-8000-000000000001', 'release', '1.0'),
  ('0192f7a0-0000-7000-8000-000000000001', 'quote', 'it''s "quoted"');
INSERT INTO download_stats (item_id, downloads, last_downloaded, last_downloaded_by) VALUES
  ('0192f7a0-0000-7000-8000-000000000001', 7, now(), 'ci');
INSERT INTO npm_versions (item_id, repo_key, name, version, manifest, integrity, created) VALUES
  ('0192f7a0-0000-7000-8000-000000000001', 'libs', '@scope/pkg', '1.0.0', '{"name": "@scope/pkg", "deprecated": "use 2"}', 'sha512-x', now());
INSERT INTO npm_dist_tags (repo_key, name, tag, version) VALUES ('npm', '@scope/pkg', 'latest', '1.0.0');
INSERT INTO replications (repository, config, updated, last_run, last_status, last_error) VALUES
  ('libs', '{"url": "https://other/libs", "cron": "0 0 * * *"}', now(), NULL, 'never', '');
INSERT INTO users (id, username, email, password_hash, admin, created, extensions, realm) VALUES
  ('0192f7a0-0000-7000-8000-000000000010', 'admin', 'a@example.com', 'hash', true, now(), '{}', 'internal'),
  ('0192f7a0-0000-7000-8000-000000000011', 'dev', '', 'hash', false, now(), '{"profile": {"uiDisabled": true}}', 'ldap');
INSERT INTO groups (name, description, default_for_new_users, admin, created) VALUES ('devs', 'Développeurs', true, false, now());
INSERT INTO group_members (group_name, username, source) VALUES ('devs', 'dev', 'ldap');
INSERT INTO permissions (name, config, created, updated) VALUES ('p', '{"repositories": ["libs"], "actions": {"users": {"dev": ["read"]}}}', now(), now());
INSERT INTO access_tokens (id, username, scope_kind, refresh_token_hash, expires, created, revoked, description, project, scope_groups) VALUES
  ('0192f7a0-0000-7000-8000-000000000020', 'dev', 'groups', 'r1', '2027-01-01 00:00:00+00', now(), false, 'ci', '', ARRAY['devs', 'readers']),
  ('0192f7a0-0000-7000-8000-000000000021', 'admin', 'admin', NULL, NULL, now(), true, '', '', '{}');
INSERT INTO signing_keys (kid, private_key, created) VALUES ('k1', '\x00ff10'::bytea, now());
INSERT INTO trusted_keys (kid, alias, key_type, public_key, fingerprint, issued_by, issued) VALUES ('t1', 'ci', 'rsa', 'PEM', 'fp', 'admin', now());
INSERT INTO key_pairs (name, kind, private_key, public_key, created) VALUES ('gpg1', 'gpg', '\x0001'::bytea, 'PUB', now());
INSERT INTO web_login_sessions (id, created, approved_by) VALUES ('s1', now(), NULL), ('s2', now(), 'admin');
INSERT INTO ldap_servers (name, config, updated) VALUES ('corp', '{"url": "ldap://corp", "enabled": true}', now());
INSERT INTO ldap_group_mappings (name, config, updated) VALUES ('static', '{"strategy": "static"}', now());
INSERT INTO builds (id, project, name, number, started, started_ms, info, created, created_by) VALUES
  ('0192f7a0-0000-7000-8000-000000000030', '', 'app', '1', '2026-05-06 07:08:09.101112+00', 1778051289101, '{"name": "app", "modules": []}', now(), 'ci');
INSERT INTO build_artifacts (build_id, module_id, name, type, path, sha1, sha256, md5) VALUES
  ('0192f7a0-0000-7000-8000-000000000030', 'm', 'app.jar', 'jar', 'org/app/app.jar', 'a1', 'aa', 'a5');
INSERT INTO build_dependencies (build_id, module_id, dep_id, type, scopes, sha1, sha256, md5) VALUES
  ('0192f7a0-0000-7000-8000-000000000030', 'm', 'dep:1', 'jar', 'compile', 'd1', 'dd', 'd5');
INSERT INTO build_promotions (id, build_id, status, comment, repository, user_name, created) VALUES
  ('0192f7a0-0000-7000-8000-000000000031', '0192f7a0-0000-7000-8000-000000000030', 'released', '', 'libs', 'ci', now());
INSERT INTO releases (id, project, repository, name, version, status, draft, tag, artifacts, created_by, created) VALUES
  ('0192f7a0-0000-7000-8000-000000000040', '', 'release-bundles', 'r', '1', 'COMPLETED', false, '', '[{"path": "libs/a"}]', 'ci', now());
INSERT INTO release_promotions (id, release_id, environment, status, included_repositories, excluded_repositories, messages, created_by, created, created_millis) VALUES
  ('0192f7a0-0000-7000-8000-000000000041', '0192f7a0-0000-7000-8000-000000000040', 'PROD', 'COMPLETED', '{libs}', '{}', '[]', 'ci', now(), 1778051289102);
INSERT INTO evidence (id, subject_repo, subject_path, subject_name, subject_sha256, predicate_type, predicate_slug, predicate_category, predicate, provider_id, key_alias, verified, attachments, item_path, item_sha256, created_by, created) VALUES
  ('0192f7a0-0000-7000-8000-000000000050', 'libs', 'org/app', 'app-1.0.jar', 'aa', 'https://slsa.dev/provenance/v1', 'provenance', 'build', '{"builder": {"id": "ci"}}', '', '', true, '[]', 'libs/org/app/app-1.0.jar.evd', 'ee', 'ci', now()),
  ('0192f7a0-0000-7000-8000-000000000051', 'libs', 'org/app', 'app-1.0.jar', 'aa', 't', 's', 'c', NULL, '', '', false, '[]', 'p', 's', 'ci', now());
INSERT INTO scan_artifacts (sha256, file_name, status, reason, sbom, components, db_built, updated, malicious_checked) VALUES
  ('aa', 'app-1.0.jar', 'done', '', '\x7b7d'::bytea, 3, now(), now(), NULL),
  ('bb', 'b', 'queued', '', NULL, 0, NULL, now(), now());
INSERT INTO scan_findings (sha256, vuln_id, purl, package_name, package_version, severity, summary, cves, fixed_versions, cvss, urls) VALUES
  ('aa', 'GHSA-x', 'pkg:maven/a/b@1', 'b', '1', 'high', 'bad', '{CVE-2026-1}', '{}', '[{"score": 7.5}]', '{https://x}');
INSERT INTO scan_issues (sha256, analyzer, rule, severity, title, file, line, resource) VALUES ('aa', 'secrets', 'aws-key', 'high', 'AWS key', 'a/b', 3, '');
INSERT INTO scan_graphs (id, username, status, request, result, error, created, updated, build_name, build_number, project) VALUES
  ('0192f7a0-0000-7000-8000-000000000060', 'ci', 'done', '{"graph": {}}', '"a JSON string"', '', now(), now(), 'app', '1', NULL);
INSERT INTO scan_policies (name, kind, doc, created, updated) VALUES ('sec', 'security', '{"rules": []}', now(), now());
INSERT INTO item_holds (repo_key, path, reason, created_by, created) VALUES ('libs', 'a', 'audit', 'admin', now());
INSERT INTO identity_providers (name, config, created, updated) VALUES
  ('corp', '{"enabled": true, "oidc": {"issuer": "https://login.example.com", "clientId": "relic"}}', now(), now());
INSERT INTO sso_requests (state, provider, verifier, nonce, redirect, created) VALUES ('s1', 'corp', 'v', 'n', '/', now());
INSERT INTO quarantine_decisions (repo_key, sha256, decision, reason, decided_by, decided) VALUES ('libs', 'aa', 'released', 'vetted', 'admin', now());
INSERT INTO waivers (id, requested_by, reason, doc, status, created) VALUES
  ('0192f7a0-0000-7000-8000-000000000081', 'bob', 'not reachable', '{"packages": ["pkg:npm/lodash"]}', 'pending', now());
INSERT INTO promotion_gates (name, environment, condition, description, created, updated) VALUES
  ('prod-signed', 'PROD', 'signed', 'signed releases only', now(), now());
INSERT INTO policy_decisions (kind, subject, policy, watch, rule, reason, actor, count, first_seen, last_seen) VALUES
  ('download', 'libs/a.jar', 'sec', 'w', 'critical', 'CVE-1 in a', 'bob', 3, now(), now());
INSERT INTO webhooks (name, config, created, updated) VALUES
  ('ci', '{"url": "https://ci.example.com/hook", "events": ["artifact.deployed"], "format": "json", "enabled": true}', now(), now());
INSERT INTO webhook_deliveries (id, webhook, event, payload, status, attempts, next_attempt, response_status, error, created) VALUES
  ('0192f7a0-0000-7000-8000-000000000082', 'ci', 'artifact.deployed', '{"type": "artifact.deployed"}', 'pending', 1, now(), 503, 'the receiver answered 503', now());
INSERT INTO projects (key, config, created, updated) VALUES
  ('acme', '{"name": "Acme", "storageQuotaBytes": 1024, "members": [{"name": "bob", "role": "developer"}]}', now(), now());
INSERT INTO registry_entries (repo_key, path, kind, name, version, description, sha256, doc, updated) VALUES
  ('libs', 'weather/server.json', 'mcp', 'io.example/weather', '1.0.0', 'Forecasts', 'aa', '{"name": "io.example/weather"}', now());
INSERT INTO approvals (repo_key, path, sha256, approved_by, approved) VALUES ('libs', 'weather/server.json', 'aa', 'admin', now());
INSERT INTO replication_tombstones (repo_key, path, deleted) VALUES ('libs', 'gone.txt', now());
INSERT INTO debian_release_dates (repo_key, distribution, digest, date) VALUES ('libs', 'bookworm', 'd1', now());
INSERT INTO usage_daily (day, repo_key, files, bytes, downloads, deploys) VALUES ('2026-10-05', 'libs', 2, 10, 3, 1);
INSERT INTO usage_users_daily (day, username, downloads, deploys) VALUES ('2026-10-05', 'bob', 3, 1);
INSERT INTO workload_identities (name, config, created, updated) VALUES
  ('github', '{"enabled": true, "issuer": "https://token.actions.githubusercontent.com", "audience": "relic", "rules": []}', now(), now());
INSERT INTO audit_events (id, time, actor, action, target, outcome, client, detail) VALUES
  ('0192f7a0-0000-7000-8000-000000000080', now(), 'admin', 'relic.v1.SecurityService/PutUser', 'bob', 'success', '10.0.0.1', '');
INSERT INTO cleanup_policies (name, doc, created, updated, last_run, last_moved, last_error) VALUES
    ('old', '{"enabled": true, "repositories": ["libs"], "pathPattern": "**", "olderThanDays": 30}', now(), now(), now(), 3, '');
INSERT INTO scan_watches (name, doc, created, updated) VALUES ('w', '{"policies": ["sec"]}', now(), now());
INSERT INTO scan_ignore_rules (id, doc, expires_at, created, created_by) VALUES ('0192f7a0-0000-7000-8000-000000000070', '{"vulnerabilities": ["CVE-2026-1"]}', NULL, now(), 'admin');
INSERT INTO cves (id, title, description, state, published, updated, cwes, refs, cvss_score, cvss_vector, cvss_version, cvss_source, ssvc_exploitation, ssvc_automatable, ssvc_impact, epss, epss_percentile, epss_date, kev_added, kev_due, kev_ransomware, kev_action, record_checked, created) VALUES
  ('CVE-2026-1', 'Title', 'Desc', 'PUBLISHED', now(), NULL, '{CWE-79}', '{}', 7.5, 'CVSS:3.1/AV:N', '3.1', 'nvd', 'active', 'yes', 'total', 0.123456, 0.9, '2026-09-30', '2026-01-02', NULL, true, 'patch', NULL, now());
INSERT INTO cve_feeds (name, version, fetched) VALUES ('epss', '2026-09-30', now());
INSERT INTO malicious_packages (id, ecosystem, name, all_versions, versions, ranges, summary, modified) VALUES
  ('MAL-2026-1', 'npm', 'evil', false, '{1.0.0,1.0.1}', '[{"type": "SEMVER"}]', 'steals tokens', now());
INSERT INTO malicious_feeds (ecosystem, etag, fetched, changed) VALUES ('npm', '"e1"', now(), now());
INSERT INTO end_of_life_products (product, label) VALUES ('django', 'Django');
INSERT INTO end_of_life_identifiers (purl, product) VALUES ('pkg:pypi/django', 'django');
INSERT INTO end_of_life_cycles (product, cycle, eol_from, is_eol, is_maintained, latest) VALUES ('django', '4.2', '2026-04-30', true, false, '4.2.25');
INSERT INTO end_of_life_feeds (source, etag, fetched, changed, products) VALUES ('feed', '"v1"', now(), now(), 1);
INSERT INTO deps_dev_versions (system, name, version, deprecated, reason, maintained, fetched) VALUES ('NPM', 'request', '2.88.2', true, 'déprécié', 0, now());
INSERT INTO scan_reachability (sha256, osv_id, module, aliases, reachable, trace) VALUES ('aa', 'GO-2021-0113', 'golang.org/x/text', '{CVE-2021-38561,GHSA-ppp9-7jff-5vj2}', true, 'golang.org/x/text/language.Parse ← main.main');
INSERT INTO scan_go_extracts (sha256, executable, extract) VALUES ('aa', '/bin/app', '\x1f8b');
INSERT INTO scan_operational_risks (sha256, purl, kind, severity, package_name, package_version, product, cycle, eol_from, detail) VALUES ('aa', 'pkg:pypi/django@4.2.1', 'end_of_life', 'high', 'Django', '4.2.1', 'django', '4.2', '2026-04-30', 'Django 4.2 reached end of life on 2026-04-30');
`

func TestCopyRoundTrip(t *testing.T) {
	t.Parallel()
	databasetest.RequirePostgreSQL(t)

	ctx := t.Context()
	src := databasetest.NewDB(t)

	if _, err := src.Pool().Exec(ctx, copySeed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	lite, err := database.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "relic.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(lite.Close)

	copied := map[string]int64{}

	if err := database.Copy(ctx, src, lite, func(table string, n int64) {
		copied[table] = n
	}); err != nil {
		t.Fatalf("copy to SQLite: %v", err)
	}

	back := databasetest.NewDB(t)
	if err := database.Copy(ctx, lite, back, nil); err != nil {
		t.Fatalf("copy back to PostgreSQL: %v", err)
	}

	want := database.CanonicalRows(ctx, t, src, src)

	for table, rows := range want {
		if len(rows) == 0 {
			t.Errorf("the seed has no rows in %s", table)
		}

		if copied[table] != int64(len(rows)) {
			t.Errorf("%s: copied %d rows, want %d", table, copied[table], len(rows))
		}
	}

	for name, db := range map[string]*database.DB{
		"SQLite":                lite,
		"PostgreSQL round trip": back,
	} {
		got := database.CanonicalRows(ctx, t, db, src)
		for table := range want {
			if !sameRows(got[table], want[table]) {
				t.Errorf("%s, %s:\n got  %v\n want %v", name, table, got[table], want[table])
			}
		}
	}
}

func TestCopyRefusesATargetWithData(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	src := databasetest.NewDB(t)
	dst := databasetest.NewDB(t)

	if _, err := dst.UpsertGroup(ctx, database.UpsertGroupParams{
		Name: "existing",
	}); err != nil {
		t.Fatal(err)
	}

	if err := database.Copy(ctx, src, dst, nil); !errors.Is(err, database.ErrTargetNotEmpty) {
		t.Errorf("Copy into a database with a group = %v, want ErrTargetNotEmpty", err)
	}
}

func TestFailedCopyRollsBackAndReleasesItsConnection(t *testing.T) {
	t.Parallel()

	dst := databasetest.NewDB(t)
	cause := errors.New("the target rejected a row")

	if err := database.FailCopy(t.Context(), dst, cause); !errors.Is(err, cause) {
		t.Fatalf("FailCopy = %v, want %v", err, cause)
	}

	if n := database.ConnectionsInUse(dst); n != 0 {
		t.Errorf("a failed copy left %d connections in use, want 0", n)
	}
}

func sameRows(a, b []string) bool {
	norm := func(rows []string) []string {
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			var v any
			if json.Unmarshal([]byte(r), &v) != nil {
				return nil
			}

			b, _ := json.Marshal(v) //nolint:errchkjson // decoded JSON encodes
			out = append(out, string(b))
		}

		slices.Sort(out)

		return out
	}

	return reflect.DeepEqual(norm(a), norm(b))
}
