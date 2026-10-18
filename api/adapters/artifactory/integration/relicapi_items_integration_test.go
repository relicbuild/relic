//go:build integration

package integration

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha1" //nolint:gosec // G505: wire checksum
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/platform/database"
)

func TestRelicAPIImageScan(t *testing.T) {
	t.Parallel()
	srv, db := serverDB(t)
	ctx := t.Context()
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/images",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/images/app/1.0/manifest.json",
		body:   `{"schemaVersion":2}`,
	})

	claimed, err := db.ClaimUnscanned(
		ctx,
		database.ClaimUnscannedParams{
			Now:         time.Now(),
			NamePattern: `manifest\.json$`,
			MaxRows:     10,
		},
	)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v %v", claimed, err)
	}

	sha := claimed[0].Sha256
	built := time.Now()

	var sbom bytes.Buffer

	zw := gzip.NewWriter(&sbom)

	_, _ = zw.Write(
		[]byte(
			`{"artifacts":[{"name":"openssl","version":"3.0","type":"deb","purl":"pkg:deb/debian/openssl@3.0",` +
				`"licenses":[{"value":"Apache-2.0 AND MIT","spdxExpression":"Apache-2.0 AND MIT"}],` +
				`"locations":[{"path":"/var/lib/dpkg/status","layerID":"sha256:l1"}]}]}`,
		),
	)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := db.FinishScanArtifact(
		ctx,
		database.FinishScanArtifactParams{
			Sha256:     sha,
			Status:     "done",
			Sbom:       sbom.Bytes(),
			Components: 1,
			DbBuilt:    &built,
			Now:        built,
		},
	); err != nil {
		t.Fatal(err)
	}

	for _, f := range []struct {
		id, sev string
	}{
		{
			"CVE-2026-1",
			"critical",
		},
		{
			"CVE-2026-2",
			"high",
		},
		{
			"CVE-2026-3",
			"high",
		},
	} {
		err := db.InsertFinding(ctx, database.InsertFindingParams{
			Sha256:         sha,
			VulnID:         f.id,
			Purl:           "pkg:deb/debian/openssl@3.0",
			PackageName:    "openssl",
			PackageVersion: "3.0",
			Severity:       f.sev,
			Cves: []string{
				f.id,
			},
			FixedVersions: []string{
				"3.1",
			},
			Urls: []string{},
			Cvss: []byte(`[{"version":"3.1","vector":"CVSS:3.1/AV:N","score":9.8}]`),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	admin := relicHTTP("admin", "password")
	arts := relicv1connect.NewArtifactServiceClient(admin, srv.URL+"/api")
	scans := relicv1connect.NewScanServiceClient(admin, srv.URL+"/api")

	for _, p := range []string{
		"app/1.0",
		"app/1.0/manifest.json",
	} {
		d, err := arts.GetItemDetails(
			ctx,
			connect.NewRequest(&relicv1.GetItemDetailsRequest{
				Repository: "images",
				Path:       p,
			}),
		)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}

		s := d.Msg.GetScan()

		wantSource := map[string]string{
			"app/1.0": "manifest.json",
		}[p]
		if s.GetStatus() != relicv1.ScanStatus_SCAN_STATUS_DONE || s.GetSource() != wantSource ||
			s.GetCounts()["critical"] != 1 ||
			s.GetCounts()["high"] != 2 ||
			len(s.GetFindings()) != 3 ||
			s.GetFindings()[0].GetId() != "CVE-2026-1" ||
			len(s.GetFindings()[0].GetCvss()) != 1 ||
			s.GetFindings()[0].GetCvss()[0].GetScore() != 9.8 {
			t.Errorf("%s: scan = %v", p, s)
		}
	}

	for _, tc := range []struct {
		folder, name string
	}{
		{
			"app",
			"1.0",
		},
		{
			"app/1.0",
			"manifest.json",
		},
	} {
		kids, err := arts.ListChildren(
			ctx,
			connect.NewRequest(&relicv1.ListChildrenRequest{
				Repository: "images",
				Path:       tc.folder,
			}),
		)
		if err != nil || len(kids.Msg.GetItems()) != 1 || kids.Msg.GetItems()[0].GetName() != tc.name ||
			kids.Msg.GetItems()[0].GetVulnerabilities()["critical"] != 1 || kids.Msg.GetItems()[0].GetVulnerabilities()["high"] != 2 {
			t.Errorf("children of %s = %v %v", tc.folder, kids, err)
		}
	}

	sb, err := scans.GetItemSbom(
		ctx,
		connect.NewRequest(&relicv1.GetItemSbomRequest{
			Repository: "images",
			Path:       "app/1.0",
		}),
	)
	if err != nil || sb.Msg.GetSource() != "manifest.json" || len(sb.Msg.GetComponents()) != 1 ||
		sb.Msg.GetComponents()[0].GetName() != "openssl" ||
		len(sb.Msg.GetComponents()[0].GetLocations()) != 1 ||
		sb.Msg.GetComponents()[0].GetLocations()[0].GetLayer() != "sha256:l1" {
		t.Errorf("sbom = %v %v", sb, err)
	}

	if got := sb.Msg.GetComponents()[0]; !slices.Equal(got.GetLicenseIds(), []string{
		"Apache-2.0",
		"MIT",
	}) ||
		len(got.GetViolations()) != 0 {
		t.Errorf("licenses before any license policy = %v, violations %v", got.GetLicenseIds(), got.GetViolations())
	}

	if _, err := scans.PutPolicy(ctx, connect.NewRequest(&relicv1.PutPolicyRequest{
		Policy: &relicv1.Policy{
			Name: "no-apache",
			Kind: relicv1.PolicyKind_POLICY_KIND_LICENSE,
			Rules: []*relicv1.PolicyRule{
				{
					Name: "ban",
					BannedLicenses: []string{
						"Apache-2.0",
					},
					FailBuild: true,
				},
			},
		},
	})); err != nil {
		t.Fatal(err)
	}

	if _, err := scans.PutWatch(ctx, connect.NewRequest(&relicv1.PutWatchRequest{
		Watch: &relicv1.Watch{
			Name:   "images",
			Active: true,
			Policies: []string{
				"no-apache",
			},
			Scopes: []*relicv1.WatchScope{
				{
					Kind: relicv1.WatchScopeKind_WATCH_SCOPE_KIND_REPOSITORY,
					Names: []string{
						"images",
					},
				},
			},
		},
	})); err != nil {
		t.Fatal(err)
	}

	sb, err = scans.GetItemSbom(
		ctx,
		connect.NewRequest(&relicv1.GetItemSbomRequest{
			Repository: "images",
			Path:       "app/1.0",
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	if v := sb.Msg.GetComponents()[0].GetViolations(); len(v) != 1 || v[0].GetPolicy() != "no-apache" ||
		v[0].GetWatch() != "images" || v[0].GetRule() != "ban" || v[0].GetLicense() != "Apache-2.0 AND MIT" ||
		!v[0].GetFailBuild() {
		t.Errorf("violations = %v", v)
	}

	if _, err := scans.GetItemSbom(
		ctx,
		connect.NewRequest(&relicv1.GetItemSbomRequest{
			Repository: "images",
			Path:       "app",
		}),
	); code(err) != connect.CodeNotFound {
		t.Errorf("sbom of a plain folder: %v", err)
	}

	if d, err := arts.GetItemDetails(
		ctx,
		connect.NewRequest(&relicv1.GetItemDetailsRequest{
			Repository: "images",
			Path:       "app",
		}),
	); err != nil ||
		d.Msg.GetScan().GetStatus() != relicv1.ScanStatus_SCAN_STATUS_NONE {
		t.Errorf("folder scan = %v %v", d, err)
	}
}

func TestRelicAPIDeleteItem(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/libs",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/all",
			body:   `{"rclass":"virtual","packageType":"generic","repositories":["libs"]}`,
		},
	)

	for _, f := range []string{
		"app/a.txt",
		"tmp/c.txt",
	} {
		do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/libs/" + f,
			body:   "x",
		})
	}

	arts := relicv1connect.NewArtifactServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")

	tests := []struct {
		name, repo, path string
		want             connect.Code
	}{
		{
			"virtual repository",
			"all",
			"tmp/c.txt",
			connect.CodeInvalidArgument,
		},
		{
			"file",
			"libs",
			"app/a.txt",
			0,
		},
		{
			"folder",
			"libs",
			"tmp",
			0,
		},
		{
			"gone",
			"libs",
			"tmp",
			connect.CodeNotFound,
		},
	}
	for _, tc := range tests {
		_, err := arts.DeleteItem(
			ctx,
			connect.NewRequest(&relicv1.DeleteItemRequest{
				Repository: tc.repo,
				Path:       tc.path,
			}),
		)
		if code(err) != tc.want {
			t.Errorf("%s: delete %s/%s = %v, want %v", tc.name, tc.repo, tc.path, err, tc.want)
		}
	}
}

func TestRelicAPISearchItems(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/libs",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)

	for _, f := range []string{
		"org/app/App-1.0.jar",
		"org/app/app-1.0.pom",
		"org/lib/lib_%25.txt",
		"secret/app-key.txt",
		"docs/h%C3%A9llo%20w%C3%B6rld.txt",
	} {
		do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/libs/" + f,
			body:   "x",
		})
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/security/users/sam",
			body:   `{"email":"s@example.com","password":"Sam-pass-1234"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/sam",
			body:   `{"name":"sam","repo":{"repositories":["libs"],"include-patterns":["**"],"exclude-patterns":["secret/**"],"actions":{"users":{"sam":["read"]}}}}`,
		},
	)
	admin := relicv1connect.NewArtifactServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")
	sam := relicv1connect.NewArtifactServiceClient(relicHTTP("sam", "Sam-pass-1234"), srv.URL+"/api")
	paths := func(c relicv1connect.ArtifactServiceClient, name string) []string {
		t.Helper()

		r, err := c.SearchItems(ctx, connect.NewRequest(&relicv1.SearchItemsRequest{
			Repository: "libs",
			Name:       name,
		}))
		if err != nil {
			t.Fatalf("search %q: %v", name, err)
		}

		out := make([]string, 0, len(r.Msg.GetItems()))
		for _, it := range r.Msg.GetItems() {
			out = append(out, it.GetPath())
		}

		return out
	}

	tests := []struct {
		name   string
		client relicv1connect.ArtifactServiceClient
		q      string
		want   []string
	}{
		{
			"case-insensitive, files and folders",
			admin,
			"app",
			[]string{
				"org/app",
				"org/app/App-1.0.jar",
				"org/app/app-1.0.pom",
				"secret/app-key.txt",
			},
		},
		{
			"case-insensitive beyond ASCII",
			admin,
			"WÖRLD",
			[]string{
				"docs/héllo wörld.txt",
			},
		},
		{
			"LIKE wildcards are literal",
			admin,
			"_%",
			[]string{
				"org/lib/lib_%.txt",
			},
		},
		{
			"excluded paths are hidden",
			sam,
			"app",
			[]string{
				"org/app",
				"org/app/App-1.0.jar",
				"org/app/app-1.0.pom",
			},
		},
	}
	for _, tc := range tests {
		if got := paths(tc.client, tc.q); !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}

	for _, one := range []string{
		"a",
		"é",
	} {
		if _, err := admin.SearchItems(
			ctx,
			connect.NewRequest(&relicv1.SearchItemsRequest{
				Repository: "libs",
				Name:       one,
			}),
		); code(err) != connect.CodeInvalidArgument {
			t.Errorf("one-character search %q: %v", one, err)
		}
	}
}

func TestRelicAPIItemBuilds(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/libs",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/libs/app.jar",
		body:   "app",
	})
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/libs/dep.jar",
		body:   "dep",
	})

	sha1 := func(s string) string {
		sum := sha1.Sum([]byte(s)) //nolint:gosec // G401: wire checksum
		return hex.EncodeToString(sum[:])
	}

	build := `{"version":"1.0.1","name":"shop","number":"7","started":"2026-09-24T10:00:00.000+0000",` +
		`"modules":[{"id":"shop:app",` +
		`"artifacts":[{"name":"app.jar","type":"jar","sha1":"` + sha1("app") + `"}],` +
		`"dependencies":[{"id":"dep:dep:1","type":"jar","scopes":["compile"],"sha1":"` + sha1("dep") + `"}]}]}`
	if resp, body := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/build",
			body:   build,
		},
	); resp.StatusCode >= 300 {
		t.Fatalf("publish = %d %s", resp.StatusCode, body)
	}

	details := func(c *relicv1connect.ArtifactServiceClient, path string) *relicv1.GetItemDetailsResponse {
		t.Helper()

		d, err := (*c).GetItemDetails(
			ctx,
			connect.NewRequest(&relicv1.GetItemDetailsRequest{
				Repository: "libs",
				Path:       path,
			}),
		)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}

		return d.Msg
	}

	admin := relicv1connect.NewArtifactServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")
	if d := details(
		&admin,
		"app.jar",
	); len(d.GetProducedBy()) != 1 || d.GetProducedBy()[0].GetName() != "shop" || d.GetProducedBy()[0].GetNumber() != "7" ||
		len(d.GetUsedBy()) != 0 {
		t.Errorf("app.jar builds = %v %v", d.GetProducedBy(), d.GetUsedBy())
	}

	if d := details(&admin, "dep.jar"); len(d.GetUsedBy()) != 1 || d.GetUsedBy()[0].GetScopes() == "" {
		t.Errorf("dep.jar builds = %v", d.GetUsedBy())
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/security/users/rita",
			body:   `{"email":"r@example.com","password":"Rita-pass-123"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/rita",
			body:   `{"name":"rita","repo":{"repositories":["libs"],"include-patterns":["**"],"actions":{"users":{"rita":["read"]}}}}`,
		},
	)

	rita := relicv1connect.NewArtifactServiceClient(relicHTTP("rita", "Rita-pass-123"), srv.URL+"/api")
	if d := details(&rita, "app.jar"); len(d.GetProducedBy()) != 0 {
		t.Errorf("builds shown without build permission: %v", d.GetProducedBy())
	}
}

func TestRelicAPISearchItemsOfAVirtualRepository(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()

	for _, repo := range []struct {
		key  string
		body string
	}{
		{
			key:  "libs",
			body: `{"rclass":"local","packageType":"generic"}`,
		},
		{
			key:  "extras",
			body: `{"rclass":"local","packageType":"generic"}`,
		},
		{
			key:  "inner",
			body: `{"rclass":"virtual","packageType":"generic","repositories":["extras"]}`,
		},
		{
			key:  "all",
			body: `{"rclass":"virtual","packageType":"generic","repositories":["libs","inner"]}`,
		},
	} {
		do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/repositories/" + repo.key,
				body:   repo.body,
			},
		)
	}

	for _, f := range []string{
		"libs/org/app-1.0.jar",
		"extras/tools/app-tool.sh",
		"extras/tools/other.sh",
	} {
		do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/" + f,
			body:   "x",
		})
	}

	admin := relicv1connect.NewArtifactServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")

	r, err := admin.SearchItems(ctx, connect.NewRequest(&relicv1.SearchItemsRequest{
		Repository: "all",
		Name:       "app",
	}))
	if err != nil {
		t.Fatal(err)
	}

	got := make([]string, 0, len(r.Msg.GetItems()))
	for _, it := range r.Msg.GetItems() {
		got = append(got, it.GetRepository()+"/"+it.GetPath())
	}

	want := []string{
		"libs/org/app-1.0.jar",
		"extras/tools/app-tool.sh",
	}
	if !slices.Equal(got, want) {
		t.Errorf("search of the virtual repository = %v, want %v", got, want)
	}
}

func TestRelicAPIItemBuildsNameTheirProject(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/libs",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/libs/app.jar",
		body:   "app",
	})

	projects := relicv1connect.NewProjectServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")
	if _, err := projects.PutProject(ctx, connect.NewRequest(&relicv1.PutProjectRequest{
		Project: &relicv1.Project{
			Key: "shop",
		},
	})); err != nil {
		t.Fatal(err)
	}

	sum := sha1.Sum([]byte("app")) //nolint:gosec // G401: wire checksum
	build := `{"version":"1.0.1","name":"shop-app","number":"3","started":"2026-09-24T10:00:00.000+0000",` +
		`"modules":[{"id":"shop:app","artifacts":[{"name":"app.jar","type":"jar","sha1":"` +
		hex.EncodeToString(sum[:]) + `"}]}]}`

	if resp, body := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/build?project=shop",
			body:   build,
		},
	); resp.StatusCode >= 300 {
		t.Fatalf("publish = %d %s", resp.StatusCode, body)
	}

	artifacts := relicv1connect.NewArtifactServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")

	d, err := artifacts.GetItemDetails(
		ctx,
		connect.NewRequest(&relicv1.GetItemDetailsRequest{
			Repository: "libs",
			Path:       "app.jar",
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	if links := d.Msg.GetProducedBy(); len(links) != 1 || links[0].GetProject() != "shop" {
		t.Errorf("app.jar produced by %v, want the run of shop-app in project shop", links)
	}
}

func TestRelicAPIEffectivePermissions(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/libs",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/libs/org/a.txt",
		body:   "x",
	})
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/security/users/uma",
			body:   `{"email":"u@example.com","password":"Uma-pass-1234"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/uma",
			body:   `{"name":"uma","repo":{"repositories":["libs"],"include-patterns":["org/**"],"actions":{"users":{"uma":["read","write"]}}}}`,
		},
	)

	req := &relicv1.GetEffectivePermissionsRequest{
		Repository: "libs",
		Path:       "org/a.txt",
	}

	got, err := relicv1connect.NewArtifactServiceClient(relicHTTP("admin", "password"), srv.URL+"/api").
		GetEffectivePermissions(ctx, connect.NewRequest(req))
	if err != nil || len(got.Msg.GetGrants()) != 1 || got.Msg.GetGrants()[0].GetPrincipal() != "uma" ||
		got.Msg.GetGrants()[0].GetGroup() ||
		!slices.Equal(
			got.Msg.GetGrants()[0].GetActions(),
			[]relicv1.Action{
				relicv1.Action_ACTION_READ,
				relicv1.Action_ACTION_WRITE,
			},
		) ||
		!slices.Equal(got.Msg.GetGrants()[0].GetPermissions(), []string{
			"uma",
		}) {
		t.Errorf("effective permissions = %v %v", got, err)
	}

	uma := relicv1connect.NewArtifactServiceClient(relicHTTP("uma", "Uma-pass-1234"), srv.URL+"/api")
	if _, err := uma.GetEffectivePermissions(ctx, connect.NewRequest(req)); code(err) != connect.CodePermissionDenied {
		t.Errorf("non-admin: %v", err)
	}
}

func TestRelicAPIProperties(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/libs",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)

	for _, f := range []string{
		"org/a.txt",
		"org/keep/b.txt",
	} {
		do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/libs/" + f,
			body:   "x",
		})
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/security/users/vic",
			body:   `{"email":"v@example.com","password":"Vic-pass-1234"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/vic",
			body:   `{"name":"vic","repo":{"repositories":["libs"],"include-patterns":["**"],"exclude-patterns":["org/keep/**"],"actions":{"users":{"vic":["read","annotate"]}}}}`,
		},
	)
	admin := relicv1connect.NewArtifactServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")
	vic := relicv1connect.NewArtifactServiceClient(relicHTTP("vic", "Vic-pass-1234"), srv.URL+"/api")
	vals := func(v ...string) *relicv1.PropertyValues {
		return &relicv1.PropertyValues{
			Values: v,
		}
	}

	tests := []struct {
		name   string
		client relicv1connect.ArtifactServiceClient
		req    *relicv1.SetPropertiesRequest
		want   connect.Code
	}{
		{
			"set",
			vic,
			&relicv1.SetPropertiesRequest{
				Repository: "libs",
				Path:       "org/a.txt",
				Properties: map[string]*relicv1.PropertyValues{
					"team": vals("core", "web"),
				},
			},
			0,
		},
		{
			"bad key",
			admin,
			&relicv1.SetPropertiesRequest{
				Repository: "libs",
				Path:       "org/a.txt",
				Properties: map[string]*relicv1.PropertyValues{
					"a=b": vals("x"),
				},
			},
			connect.CodeInvalidArgument,
		},
		{
			"no values",
			admin,
			&relicv1.SetPropertiesRequest{
				Repository: "libs",
				Path:       "org/a.txt",
				Properties: map[string]*relicv1.PropertyValues{
					"k": vals(),
				},
			},
			connect.CodeInvalidArgument,
		},
		{
			"recursive over an excluded path",
			vic,
			&relicv1.SetPropertiesRequest{
				Repository: "libs",
				Path:       "org",
				Properties: map[string]*relicv1.PropertyValues{
					"k": vals("v"),
				},
				Recursive: true,
			},
			connect.CodePermissionDenied,
		},
		{
			"recursive as admin",
			admin,
			&relicv1.SetPropertiesRequest{
				Repository: "libs",
				Path:       "org",
				Properties: map[string]*relicv1.PropertyValues{
					"stage": vals("qa"),
				},
				Recursive: true,
			},
			0,
		},
	}
	for _, tc := range tests {
		if _, err := tc.client.SetProperties(ctx, connect.NewRequest(tc.req)); code(err) != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}

	if p, err := admin.GetProperties(
		ctx,
		connect.NewRequest(&relicv1.GetPropertiesRequest{
			Repository: "libs",
			Path:       "org/keep/b.txt",
		}),
	); err != nil ||
		p.Msg.GetProperties()["stage"] == nil {
		t.Errorf("recursive set missed a descendant: %v %v", p, err)
	}

	if _, err := vic.DeleteProperties(
		ctx,
		connect.NewRequest(
			&relicv1.DeletePropertiesRequest{
				Repository: "libs",
				Path:       "org/a.txt",
				Keys: []string{
					"team",
				},
			},
		),
	); err != nil {
		t.Fatal(err)
	}

	d, err := vic.GetItemDetails(
		ctx,
		connect.NewRequest(&relicv1.GetItemDetailsRequest{
			Repository: "libs",
			Path:       "org/a.txt",
		}),
	)
	if err != nil || d.Msg.GetProperties()["team"] != nil || !d.Msg.GetCanAnnotate() {
		t.Errorf("after delete = %v %v", d, err)
	}
}

func TestRelicAPIArchive(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/libs",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)

	var zbuf bytes.Buffer

	zw := zip.NewWriter(&zbuf)
	for name, content := range map[string]string{
		"META-INF/MANIFEST.MF": "Manifest-Version: 1.0\n",
		"org/App.class":        "cafebabe",
	} {
		f, _ := zw.Create(name)
		_, _ = f.Write([]byte(content))
	}

	_ = zw.Close()

	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/libs/app.jar",
		body:   zbuf.String(),
	})
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/libs/pkg.tgz",
		body:   string(npmTarball(t, "pkg", "1.0.0")),
	})
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/libs/notes.txt",
		body:   "hi",
	})

	arts := relicv1connect.NewArtifactServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")
	if e, err := arts.ListArchiveEntries(
		ctx,
		connect.NewRequest(&relicv1.ListArchiveEntriesRequest{
			Repository: "libs",
			Path:       "app.jar",
		}),
	); err != nil || e.Msg.GetKind() != "zip" ||
		len(e.Msg.GetEntries()) != 2 {
		t.Errorf("jar listing = %v %v", e, err)
	}

	if _, err := arts.ListArchiveEntries(
		ctx,
		connect.NewRequest(&relicv1.ListArchiveEntriesRequest{
			Repository: "libs",
			Path:       "notes.txt",
		}),
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("not an archive: %v", err)
	}

	tests := []struct {
		path string
		want int
		body string
	}{
		{
			"/api/v1/archive/libs/app.jar?entry=META-INF/MANIFEST.MF",
			200,
			"Manifest-Version: 1.0\n",
		},
		{
			"/api/v1/archive/libs/pkg.tgz?entry=package/package.json",
			200,
			"",
		},
		{
			"/api/v1/archive/libs/app.jar?entry=nope",
			404,
			"",
		},
		{
			"/api/v1/archive/libs/notes.txt?entry=x",
			400,
			"",
		},
	}
	for _, tc := range tests {
		if resp, body := do(t, srv, call{
			method: "GET",
			path:   tc.path,
		}); resp.StatusCode != tc.want ||
			(tc.body != "" && body != tc.body) {
			t.Errorf("GET %s = %d %q, want %d", tc.path, resp.StatusCode, body, tc.want)
		}
	}
}

func TestRelicAPIFilePaths(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/libs",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/libs/org/a-1.0.txt",
		body:   "hello",
	})

	for _, tc := range []struct {
		name, path string
		want       int
	}{
		{
			"file",
			"/api/v1/files/libs/org/a-1.0.txt",
			200,
		},
		{
			"missing",
			"/api/v1/files/libs/org/nope.txt",
			404,
		},
		{
			"REST API path",
			"/api/v1/files/api/security/encryptedPassword",
			404,
		},
		{
			"dot segments",
			"/api/v1/files/libs/org/%2E%2E/%2E%2E/api/repositories",
			400,
		},
	} {
		if resp, _ := do(t, srv, call{
			method: "GET",
			path:   tc.path,
		}); resp.StatusCode != tc.want {
			t.Errorf("%s: GET %s = %d, want %d", tc.name, tc.path, resp.StatusCode, tc.want)
		}
	}
}

func TestRelicAPIReplicationRules(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/libs",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/proxy",
			body:   `{"rclass":"remote","packageType":"generic","url":"https://example.org"}`,
		},
	)
	reps := relicv1connect.NewReplicationServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")
	valid := func(repo string) *relicv1.Replication {
		return &relicv1.Replication{
			Repository:  repo,
			TargetUrl:   "https://peer.example/artifactory/libs",
			Username:    "push",
			Password:    "s3cret",
			Schedule:    "0 12 * * *",
			Enabled:     true,
			SyncDeletes: true,
		}
	}
	put := func(r *relicv1.Replication) error {
		_, err := reps.PutReplication(ctx, connect.NewRequest(&relicv1.PutReplicationRequest{
			Replication: r,
		}))

		return err
	}
	withURL := func(u string) *relicv1.Replication {
		r := valid("libs")
		r.TargetUrl = u

		return r
	}

	tests := []struct {
		name string
		r    *relicv1.Replication
		want connect.Code
	}{
		{
			"remote repositories are not pushed",
			valid("proxy"),
			connect.CodeInvalidArgument,
		},
		{
			"unknown repository",
			valid("nope"),
			connect.CodeNotFound,
		},
		{
			"credentials in the URL",
			withURL("https://u:p@peer.example/x"),
			connect.CodeInvalidArgument,
		},
		{
			"not http",
			withURL("file:///etc"),
			connect.CodeInvalidArgument,
		},
		{
			"create",
			valid("libs"),
			0,
		},
		{
			"edit without a password keeps it",
			&relicv1.Replication{
				Repository: "libs",
				TargetUrl:  "https://peer.example/artifactory/libs2",
				Username:   "push",
				Schedule:   "0 * * * *",
			},
			0,
		},
	}
	for _, tc := range tests {
		if err := put(tc.r); code(err) != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}

	list, err := reps.ListReplications(ctx, connect.NewRequest(&relicv1.ListReplicationsRequest{}))
	if err != nil || len(list.Msg.GetReplications()) != 1 ||
		list.Msg.GetReplications()[0].GetTargetUrl() != "https://peer.example/artifactory/libs2" ||
		!list.Msg.GetReplications()[0].GetHasPassword() ||
		list.Msg.GetReplications()[0].GetEnabled() {
		t.Errorf("replications = %v %v", list, err)
	}

	var jf []struct {
		CronExp string
	}
	if _, body := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/replications/libs",
		},
	); json.Unmarshal([]byte(body), &jf) != nil || len(jf) != 1 ||
		jf[0].CronExp != "0 0 * * * ?" {
		t.Errorf("jf view = %s", body)
	}

	if _, err := reps.RunReplication(
		ctx,
		connect.NewRequest(&relicv1.RunReplicationRequest{
			Repository: "libs",
		}),
	); code(err) != connect.CodeUnavailable {
		t.Errorf("run without a job queue: %v", err)
	}

	if _, err := reps.RunReplication(
		ctx,
		connect.NewRequest(&relicv1.RunReplicationRequest{
			Repository: "proxy",
		}),
	); code(err) != connect.CodeNotFound {
		t.Errorf("run unknown: %v", err)
	}

	if _, err := reps.DeleteReplication(
		ctx,
		connect.NewRequest(&relicv1.DeleteReplicationRequest{
			Repository: "libs",
		}),
	); err != nil {
		t.Fatal(err)
	}

	if _, err := reps.DeleteReplication(
		ctx,
		connect.NewRequest(&relicv1.DeleteReplicationRequest{
			Repository: "libs",
		}),
	); code(err) != connect.CodeNotFound {
		t.Errorf("second delete: %v", err)
	}
}

func TestRelicAPIAdminRules(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()

	sec := relicv1connect.NewSecurityServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")
	if _, err := sec.DeleteUser(
		ctx,
		connect.NewRequest(&relicv1.DeleteUserRequest{
			Name: "admin",
		}),
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("delete admin: %v", err)
	}

	repos := relicv1connect.NewRepositoryServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")
	if _, err := repos.CreateRepository(
		ctx,
		connect.NewRequest(&relicv1.CreateRepositoryRequest{
			Repository: &relicv1.Repository{
				Name:   "bad key",
				Class:  relicv1.RepositoryClass_REPOSITORY_CLASS_LOCAL,
				Format: relicv1.Format_FORMAT_NPM,
			},
		}),
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("bad key: %v", err)
	}
}

func TestOpenAPIServed(t *testing.T) {
	t.Parallel()
	srv := server(t)

	want, err := os.ReadFile("../../../../api/apispec/relic.openapi.json")
	if err != nil {
		t.Fatal(err)
	}

	if resp, body := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/api/v1/openapi.json",
			noAuth: true,
		},
	); resp.StatusCode != 200 ||
		body != string(want) {
		t.Errorf(
			"served spec differs from api/apispec/relic.openapi.json (%d, %d bytes vs %d)",
			resp.StatusCode,
			len(body),
			len(want),
		)
	}
}
