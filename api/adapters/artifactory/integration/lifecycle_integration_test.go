//go:build integration

package integration

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestReleaseBundles(t *testing.T) {
	t.Parallel()
	srv := server(t)

	const lc = "/lifecycle/api/v2/"

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/dev-local",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/prod-local",
			body:   `{"rclass":"local","packageType":"generic","environments":"PROD"}`,
		},
	)
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/dev-local/app/app.bin",
		body:   "binary v1",
	})
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/dev-local/app/app.cfg",
		body:   "config v1",
	})

	create := `{"release_bundle_name":"app","release_bundle_version":"1.0.0","source_type":"artifacts",
		"source":{"artifacts":[{"path":"dev-local/app/app.bin","sha256":"` + sha256Hex("binary v1") + `"}]}}`
	if resp, body := do(
		t,
		srv,
		call{
			method: "POST",
			path:   lc + "release_bundle?async=false",
			body:   create,
		},
	); resp.StatusCode != 201 {
		t.Fatalf("create = %d %s", resp.StatusCode, body)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "POST",
			path:   lc + "release_bundle?async=false",
			body:   create,
		},
	); resp.StatusCode != 409 {
		t.Errorf("duplicate create = %d, want 409", resp.StatusCode)
	}

	aql := `{"release_bundle_name":"app","release_bundle_version":"1.1.0","sources":[
		{"source_type":"aql","aql":"items.find({\"repo\":\"dev-local\",\"path\":\"app\"})"},
		{"source_type":"release_bundles","release_bundles":[{"release_bundle_name":"app","release_bundle_version":"1.0.0"}]}]}`
	if resp, body := do(
		t,
		srv,
		call{
			method: "POST",
			path:   lc + "release_bundle?async=true",
			body:   aql,
		},
	); resp.StatusCode != 202 {
		t.Fatalf("multi-source create = %d %s", resp.StatusCode, body)
	}

	bad := `{"release_bundle_name":"app","release_bundle_version":"9","source_type":"artifacts","source":{"artifacts":[{"path":"dev-local/nope"}]}}`
	if resp, _ := do(
		t,
		srv,
		call{
			method: "POST",
			path:   lc + "release_bundle?async=false",
			body:   bad,
		},
	); resp.StatusCode != 400 {
		t.Errorf("missing artifact = %d, want 400", resp.StatusCode)
	}

	if resp, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/release-bundles-v2/app/1.1.0/artifacts/app/app.cfg",
		},
	); resp.StatusCode != 200 ||
		got != "config v1" {
		t.Errorf("bundle file = %d %q", resp.StatusCode, got)
	}

	_, evd := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/release-bundles-v2/app/1.1.0/release-bundle.json.evd",
	})
	if !strings.Contains(evd, `"payloadType":"application/vnd.in-toto+json"`) {
		t.Errorf("manifest = %s", evd)
	}

	if _, body := do(t, srv, call{
		method: "GET",
		path:   lc + "release_bundle/existence/app/1.1.0",
	}); !strings.Contains(
		body,
		`"exists": true`,
	) {
		t.Errorf("existence = %s", body)
	}

	if _, body := do(t, srv, call{
		method: "GET",
		path:   lc + "release_bundle/existence/app/7",
	}); !strings.Contains(
		body,
		`"exists": false`,
	) {
		t.Errorf("existence of a missing version = %s", body)
	}

	_, groups := do(t, srv, call{
		method: "GET",
		path:   lc + "release_bundle/groups",
	})
	if !strings.Contains(groups, `"release_bundle_version_latest": "1.1.0"`) ||
		!strings.Contains(groups, `"release_bundle_versions_count": 2`) {
		t.Errorf("groups = %s", groups)
	}

	if _, other := do(t, srv, call{
		method: "GET",
		path:   lc + "release_bundle/groups?project=acme",
	}); strings.Contains(
		other,
		`"release_bundle_name": "app"`,
	) {
		t.Errorf("groups of project acme = %s; want no release of the default project", other)
	}

	_, versions := do(t, srv, call{
		method: "GET",
		path:   lc + "release_bundle/records/app?limit=1&offset=1",
	})
	if !strings.Contains(versions, `"release_bundle_version": "1.0.0"`) || !strings.Contains(versions, `"total": 2`) {
		t.Errorf("versions page = %s", versions)
	}

	resp, promo := do(
		t,
		srv,
		call{
			method: "POST",
			path:   lc + "promotion/records/app/1.1.0?async=false&operation=copy",
			body:   `{"environment":"PROD"}`,
		},
	)

	var p struct {
		Environment   string `json:"environment"`
		CreatedMillis int64  `json:"created_millis"`
	}
	if err := json.Unmarshal([]byte(promo), &p); resp.StatusCode != 201 || err != nil || p.Environment != "PROD" ||
		p.CreatedMillis == 0 {
		t.Fatalf("promote = %d %s", resp.StatusCode, promo)
	}

	if resp, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/prod-local/app/app.bin",
		},
	); resp.StatusCode != 200 ||
		got != "binary v1" {
		t.Errorf("promoted file = %d %q", resp.StatusCode, got)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "POST",
			path:   lc + "promotion/records/app/1.1.0?async=false",
			body:   `{"environment":"QA"}`,
		},
	); resp.StatusCode != 400 {
		t.Errorf("promotion to an environment without repositories = %d, want 400", resp.StatusCode)
	}

	if _, list := do(t, srv, call{
		method: "GET",
		path:   lc + "promotion/records/app/1.1.0",
	}); !strings.Contains(
		list,
		`"environment": "PROD"`,
	) {
		t.Errorf("promotions = %s", list)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "DELETE",
			path:   lc + "promotion/records/app/1.1.0/" + jsonNumber(p.CreatedMillis) + "?async=false",
		},
	); resp.StatusCode != 204 {
		t.Errorf("delete promotion = %d", resp.StatusCode)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   lc + "release_bundle/records/app/1.1.0/tag?project=default",
			body:   `{"tag":"rc","exist":true}`,
		},
	); resp.StatusCode != 200 {
		t.Errorf("tag = %d", resp.StatusCode)
	}

	if _, v := do(t, srv, call{
		method: "GET",
		path:   lc + "release_bundle/records/app/1.1.0",
	}); !strings.Contains(
		v,
		`"release_bundle_tag": "rc"`,
	) {
		t.Errorf("version = %s", v)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/storage/release-bundles-v2/app/1.1.0/release-bundle.json.evd?properties=team%3Dcore&recursive=1",
		},
	); resp.StatusCode != 204 &&
		resp.StatusCode != 200 {
		t.Errorf("set properties = %d", resp.StatusCode)
	}

	if props := storageProps(t, srv, "release-bundles-v2/app/1.1.0/release-bundle.json.evd"); len(props["team"]) != 1 ||
		props["team"][0] != "core" {
		t.Errorf("manifest properties = %v", props)
	}

	if _, st := do(
		t,
		srv,
		call{
			method: "GET",
			path:   lc + "distribution/export/status/app/1.1.0?async=true",
		},
	); !strings.Contains(st, "NOT_TRIGGERED") {
		t.Errorf("status before export = %s", st)
	}

	if resp, body := do(
		t,
		srv,
		call{
			method: "POST",
			path:   lc + "distribution/export/app/1.1.0?async=true",
			body:   `{"mappings":[{"input":"","output":""}]}`,
		},
	); resp.StatusCode != 202 {
		t.Fatalf("export = %d %s", resp.StatusCode, body)
	}

	_, st := do(t, srv, call{
		method: "GET",
		path:   lc + "distribution/export/status/app/1.1.0?async=true",
	})

	var status struct {
		Status   string `json:"status"`
		Relative string `json:"relative_download_url"`
	}
	if err := json.Unmarshal([]byte(st), &status); err != nil || status.Status != "COMPLETED" || status.Relative == "" {
		t.Fatalf("export status = %s", st)
	}

	_, archive := do(t, srv, call{
		method: "GET",
		path:   "/artifactory" + status.Relative,
	})

	other := server(t)
	importArchive := call{
		method: "POST",
		path:   "/artifactory/api/release/import/",
		body:   archive,
		headers: map[string]string{
			"Content-Type": "application/octet-stream",
		},
	}

	if resp, body := do(t, other, importArchive); resp.StatusCode != 400 || !strings.Contains(body, "not trusted") {
		t.Fatalf("import signed by an untrusted key = %d %s; want 400", resp.StatusCode, body)
	}

	_, sourceKey := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/security/keypair/release-bundles/public",
	})

	trustBody, err := json.Marshal(map[string]string{
		"alias": "source",
		"key":   sourceKey,
	})
	if err != nil {
		t.Fatal(err)
	}

	if resp, body := do(
		t,
		other,
		call{
			method: "POST",
			path:   "/artifactory/api/security/keys/trusted",
			body:   string(trustBody),
		},
	); resp.StatusCode != 201 {
		t.Fatalf("trust the source's key = %d %s", resp.StatusCode, body)
	}

	if resp, body := do(t, other, importArchive); resp.StatusCode != 202 {
		t.Fatalf("import = %d %s", resp.StatusCode, body)
	}

	if resp, body := do(
		t,
		other,
		call{
			method: "POST",
			path:   "/artifactory/api/release/import/",
			body:   archive,
		},
	); resp.StatusCode != 400 ||
		!strings.Contains(body, "Bundle already exists") {
		t.Errorf("re-import = %d %s", resp.StatusCode, body)
	}

	if resp, got := do(
		t,
		other,
		call{
			method: "GET",
			path:   "/artifactory/release-bundles-v2/app/1.1.0/artifacts/app/app.bin",
		},
	); resp.StatusCode != 200 ||
		got != "binary v1" {
		t.Errorf("imported file = %d %q", resp.StatusCode, got)
	}

	if _, evd2 := do(
		t,
		other,
		call{
			method: "GET",
			path:   "/artifactory/release-bundles-v2/app/1.1.0/release-bundle.json.evd",
		},
	); evd2 != evd {
		t.Errorf("imported manifest differs")
	}

	draft := `{"release_bundle_name":"app","release_bundle_version":"2.0.0","draft":true,"source_type":"artifacts","source":{"artifacts":[{"path":"dev-local/app/app.bin"}]}}`
	if resp, body := do(
		t,
		srv,
		call{
			method: "POST",
			path:   lc + "release_bundle?async=false",
			body:   draft,
		},
	); resp.StatusCode != 201 {
		t.Fatalf("draft = %d %s", resp.StatusCode, body)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "POST",
			path:   lc + "promotion/records/app/2.0.0?async=false",
			body:   `{"environment":"PROD"}`,
		},
	); resp.StatusCode != 400 {
		t.Errorf("promote draft = %d, want 400", resp.StatusCode)
	}

	add := `{"release_bundle_name":"app","release_bundle_version":"2.0.0","add_sources":[{"source_type":"artifacts","artifacts":[{"path":"dev-local/app/app.cfg"}]}]}`
	if resp, body := do(
		t,
		srv,
		call{
			method: "PATCH",
			path:   lc + "release_bundle?async=false",
			body:   add,
		},
	); resp.StatusCode != 200 {
		t.Fatalf("update = %d %s", resp.StatusCode, body)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "POST",
			path:   lc + "release_bundle/app/2.0.0/finalize?async=false",
		},
	); resp.StatusCode != 200 {
		t.Errorf("finalize = %d", resp.StatusCode)
	}

	if _, st := do(t, srv, call{
		method: "GET",
		path:   lc + "release_bundle/statuses/app/2.0.0",
	}); !strings.Contains(
		st,
		"COMPLETED",
	) {
		t.Errorf("status = %s", st)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "PATCH",
			path:   lc + "release_bundle?async=false",
			body:   add,
		},
	); resp.StatusCode != 400 {
		t.Errorf("update after finalize = %d, want 400", resp.StatusCode)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "DELETE",
			path:   lc + "release_bundle/records/app/1.0.0",
			body:   `{"dry_run":false,"distribution_rules":[{"site_name":"*"}]}`,
		},
	); resp.StatusCode != 200 {
		t.Errorf("remote delete = %d", resp.StatusCode)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "DELETE",
			path:   lc + "release_bundle/records/app/1.0.0?async=false",
		},
	); resp.StatusCode != 204 {
		t.Errorf("local delete = %d", resp.StatusCode)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/release-bundles-v2/app/1.0.0/release-bundle.json.evd",
		},
	); resp.StatusCode != 404 {
		t.Errorf("deleted manifest = %d, want 404", resp.StatusCode)
	}

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/prod-local/app/app.bin",
	}); resp.StatusCode != 200 {
		t.Errorf("promoted copy after bundle delete = %d, want 200", resp.StatusCode)
	}

	if resp, body := do(
		t,
		srv,
		call{
			method: "POST",
			path:   lc + "distribution/distribute/app/1.1.0",
			body:   `{}`,
		},
	); resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "Relic does not distribute release bundles") {
		t.Errorf("distribute = %d %s, want 404 and the reason", resp.StatusCode, body)
	}
}

func TestReleaseBundlesAreSignedWithTheReleaseKey(t *testing.T) {
	t.Parallel()
	srv := server(t)

	const lc = "/lifecycle/api/v2/"

	for _, c := range []call{
		{
			method: "PUT",
			path:   "/artifactory/api/repositories/dev-local",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
		{
			method: "PUT",
			path:   "/artifactory/api/repositories/prod-local",
			body:   `{"rclass":"local","packageType":"generic","environments":"PROD"}`,
		},
		{
			method: "PUT",
			path:   "/artifactory/dev-local/app/app.bin",
			body:   "binary v1",
		},
	} {
		if resp, body := do(t, srv, c); resp.StatusCode >= 300 {
			t.Fatalf("%s %s = %d %s", c.method, c.path, resp.StatusCode, body)
		}
	}

	create := func(version string) string {
		return `{"release_bundle_name":"app","release_bundle_version":"` + version + `","source_type":"artifacts",` +
			`"source":{"artifacts":[{"path":"dev-local/app/app.bin"}]}}`
	}
	signedWith := func(key string) map[string]string {
		return map[string]string{
			"X-JFrog-Signing-Key": key,
		}
	}

	tests := []struct {
		name       string
		call       call
		wantStatus int
	}{
		{
			name: "create naming a key that does not exist",
			call: call{
				method:  "POST",
				path:    lc + "release_bundle?async=false",
				body:    create("1"),
				headers: signedWith("no-such-key"),
			},
			wantStatus: 400,
		},
		{
			name: "create naming the release key",
			call: call{
				method:  "POST",
				path:    lc + "release_bundle?async=false",
				body:    create("2"),
				headers: signedWith("release-bundles"),
			},
			wantStatus: 201,
		},
		{
			name: "promote naming another key",
			call: call{
				method:  "POST",
				path:    lc + "promotion/records/app/2?async=false",
				body:    `{"environment":"PROD"}`,
				headers: signedWith("no-such-key"),
			},
			wantStatus: 400,
		},
		{
			name: "promote without a key",
			call: call{
				method: "POST",
				path:   lc + "promotion/records/app/2?async=false",
				body:   `{"environment":"PROD"}`,
			},
			wantStatus: 201,
		},
	}
	for _, tt := range tests {
		if resp, body := do(t, srv, tt.call); resp.StatusCode != tt.wantStatus {
			t.Errorf("%s = %d %s, want %d", tt.name, resp.StatusCode, body, tt.wantStatus)
		}
	}

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   lc + "release_bundle/statuses/app/1",
	}); resp.StatusCode != 404 {
		t.Errorf("status of the refused bundle = %d, want 404", resp.StatusCode)
	}
}

func jsonNumber(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestReleasePermissions(t *testing.T) {
	t.Parallel()
	srv := server(t)

	const lc = "/lifecycle/api/v2/"

	as := func(user string) map[string]string {
		return map[string]string{
			"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":Rel-pass-12345")),
		}
	}

	for _, u := range []string{
		"rel",
		"other",
	} {
		do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/security/users/" + u,
				body:   `{"email":"` + u + `@example.com","password":"Rel-pass-12345"}`,
			},
		)
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/src-local",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/secret-local",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/qa-local",
			body:   `{"rclass":"local","packageType":"generic","environments":"QA"}`,
		},
	)
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/src-local/web/app.js",
		body:   "js",
	})
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/secret-local/key.pem",
		body:   "k",
	})
	do(t, srv, call{
		method: "POST",
		path:   "/artifactory/api/v2/security/permissions/rel",
		body: `{"name":"rel",
		"repo":{"repositories":["src-local"],"actions":{"users":{"rel":["read"]}}},
		"releaseBundle":{"repositories":["ANY"],"include-patterns":["web/**"],"actions":{"users":{"rel":["read","write","distribute"]}}}}`,
	})

	create := func(user, name, path string) int {
		body := `{"release_bundle_name":"` + name + `","release_bundle_version":"1","source_type":"artifacts","source":{"artifacts":[{"path":"` + path + `"}]}}`
		resp, _ := do(
			t,
			srv,
			call{
				method:  "POST",
				path:    lc + "release_bundle?async=false",
				body:    body,
				noAuth:  true,
				headers: as(user),
			},
		)

		return resp.StatusCode
	}
	if got := create("rel", "web", "src-local/web/app.js"); got != 201 {
		t.Fatalf("create with write on the release = %d", got)
	}

	if got := create("rel", "api", "src-local/web/app.js"); got != 403 {
		t.Errorf("create outside the release rule = %d, want 403", got)
	}

	if got := create("rel", "web2", "secret-local/key.pem"); got != 403 {
		t.Errorf("create from an unreadable repository = %d, want 403", got)
	}

	if got := create("other", "web", "src-local/web/app.js"); got != 403 {
		t.Errorf("create without a release rule = %d, want 403", got)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method:  "GET",
			path:    lc + "release_bundle/records/web/1",
			noAuth:  true,
			headers: as("rel"),
		},
	); resp.StatusCode != 200 {
		t.Errorf("read = %d", resp.StatusCode)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method:  "GET",
			path:    lc + "release_bundle/records/web/1",
			noAuth:  true,
			headers: as("other"),
		},
	); resp.StatusCode != 403 {
		t.Errorf("read without a rule = %d, want 403", resp.StatusCode)
	}

	if _, body := do(
		t,
		srv,
		call{
			method:  "GET",
			path:    lc + "release_bundle/groups",
			noAuth:  true,
			headers: as("other"),
		},
	); !strings.Contains(body, `"total": 0`) {
		t.Errorf("groups of a user without rules = %s", body)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method:  "DELETE",
			path:    lc + "release_bundle/records/web/1?async=false",
			noAuth:  true,
			headers: as("rel"),
		},
	); resp.StatusCode != 403 {
		t.Errorf("delete without the delete action = %d, want 403", resp.StatusCode)
	}

	promote := call{
		method:  "POST",
		path:    lc + "promotion/records/web/1?async=false",
		body:    `{"environment":"QA"}`,
		noAuth:  true,
		headers: as("rel"),
	}
	if resp, body := do(t, srv, promote); resp.StatusCode != 403 {
		t.Errorf("promote into an unwritable repository = %d %s, want 403", resp.StatusCode, body)
	}

	do(t, srv, call{
		method: "POST",
		path:   "/artifactory/api/v2/security/permissions/rel-qa",
		body: `{"name":"rel-qa",
		"repo":{"repositories":["qa-local"],"actions":{"users":{"rel":["write"]}}}}`,
	})

	if resp, body := do(t, srv, promote); resp.StatusCode != 201 {
		t.Errorf("promote = %d %s", resp.StatusCode, body)
	}
}
