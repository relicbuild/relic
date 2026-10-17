//go:build integration

package integration

import (
	"crypto/sha1" //nolint:gosec // wire checksum
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"relic/api/adapters/artifactory/access"
)

func TestJFStatusContract(t *testing.T) {
	t.Parallel()
	srv := server(t)
	payload := strings.Repeat("relic", 3000)
	s1 := sha1.Sum([]byte(payload)) //nolint:gosec // wire checksum
	s256 := sha256.Sum256([]byte(payload))
	started := "2026-09-23T10:00:00.000+0000"
	buildJSON := `{"name":"b","number":"1","started":"` + started + `",` +
		`"modules":[{"id":"m","artifacts":[{"name":"f.bin","path":"dir/f.bin",` +
		`"sha1":"` + hexSum(s1[:]) + `","sha256":"` + hexSum(s256[:]) + `","md5":"x"}],` +
		`"dependencies":[{"id":"dep:f.bin","sha1":"` + hexSum(s1[:]) + `"}]}]}`

	steps := []struct {
		name  string
		call  call
		want  int
		check func(t *testing.T, resp reply, body string)
	}{
		{
			name: "ping is public",
			call: call{
				method: "GET",
				path:   "/artifactory/api/system/ping",
				noAuth: true,
			},
			want: 200,
			check: func(t *testing.T, _ reply, body string) {
				if body != "OK" {
					t.Errorf("body = %q", body)
				}
			},
		},
		{
			name: "version",
			call: call{
				method: "GET",
				path:   "/artifactory/api/system/version",
			},
			want: 200,
			check: func(t *testing.T, _ reply, body string) {
				for _, want := range []string{
					`"version": "7.136.0"`,
					`"servicesVersions"`,
					`"entitlements"`,
				} {
					if !strings.Contains(body, want) {
						t.Errorf("version lacks %s: %s", want, body)
					}
				}
			},
		},
		{
			name: "anonymous version rejected",
			call: call{
				method: "GET",
				path:   "/artifactory/api/system/version",
				noAuth: true,
			},
			want: 401,
		},
		{
			name: "usage report",
			call: call{
				method: "POST",
				path:   "/artifactory/api/system/usage",
				body:   `{"productId":"jfrog-cli-go/2.124.0"}`,
			},
			want: 200,
		},
		{
			name: "unknown repository's configuration",
			call: call{
				method: "GET",
				path:   "/artifactory/api/repositories/relic-no-such-repository",
			},
			want: 400,
			check: func(t *testing.T, _ reply, body string) {
				if !strings.Contains(body, `"message": "Bad Request"`) {
					t.Errorf("body = %s", body)
				}
			},
		},
		{
			name: "multipart disabled",
			call: call{
				method: "GET",
				path:   "/artifactory/api/v1/uploads/config",
			},
			want: 200,
			check: func(t *testing.T, _ reply, body string) {
				if !strings.Contains(body, `"supported": false`) {
					t.Errorf("body = %s", body)
				}
			},
		},
		{
			name: "repo create is exactly 200",
			call: call{
				method: "PUT",
				path:   "/artifactory/api/repositories/libs",
				body:   `{"key":"libs","rclass":"local","packageType":"generic"}`,
			},
			want: 200,
		},
		{
			name: "repo create duplicate",
			call: call{
				method: "PUT",
				path:   "/artifactory/api/repositories/libs",
				body:   `{"rclass":"local"}`,
			},
			want: 409,
		},
		{
			name: "repo list",
			call: call{
				method: "GET",
				path:   "/artifactory/api/repositories?type=local",
			},
			want: 200,
			check: func(t *testing.T, _ reply, body string) {
				var repos []map[string]string
				if err := json.Unmarshal([]byte(body), &repos); err != nil || len(repos) != 1 ||
					repos[0]["type"] != "LOCAL" {
					t.Errorf("repos = %s", body)
				}
			},
		},
		{
			name: "unknown checksum deploy falls back (non-2xx)",
			call: call{
				method: "PUT",
				path:   "/artifactory/libs/dir/f.bin",
				headers: map[string]string{
					"X-Checksum-Deploy": "true",
					"X-Checksum-Sha1":   hexSum(s1[:]),
					"X-Checksum":        hexSum(s256[:]),
				},
			},
			want: 404,
		},
		{
			name: "full upload with matrix params",
			call: call{
				method: "PUT",
				path:   "/artifactory/libs/dir/f.bin;build.name=b;build.number=1",
				body:   payload,
				headers: map[string]string{
					"X-Checksum-Sha1": hexSum(s1[:]),
					"X-Checksum":      hexSum(s256[:]),
				},
			},
			want: 201,
			check: func(t *testing.T, resp reply, body string) {
				var r struct {
					Checksums struct {
						Sha256 string
					} `json:"checksums"`
				}
				if err := json.Unmarshal([]byte(body), &r); err != nil || r.Checksums.Sha256 != hexSum(s256[:]) {
					t.Errorf("upload response lacks checksums.sha256: %s", body)
				}

				if location := resp.Header.Get(
					"Location",
				); !strings.HasSuffix(
					location,
					"/artifactory/libs/dir/f.bin",
				) {
					t.Errorf("Location = %q", location)
				}

				if got := resp.Header.Get("X-Checksum-Sha256"); got != hexSum(s256[:]) {
					t.Errorf("X-Checksum-Sha256 = %q", got)
				}

				if got := resp.Header.Get(
					"Content-Type",
				); got != "application/vnd.org.jfrog.artifactory.storage.ItemCreated+json" {
					t.Errorf("Content-Type = %q", got)
				}
			},
		},
		{
			name: "checksum mismatch rejected",
			call: call{
				method: "PUT",
				path:   "/artifactory/libs/bad.bin",
				body:   "x",
				headers: map[string]string{
					"X-Checksum-Sha1": strings.Repeat("0", 40),
				},
			},
			want: 409,
		},
		{
			name: "known checksum deploy succeeds without body",
			call: call{
				method: "PUT",
				path:   "/artifactory/libs/copy.bin",
				headers: map[string]string{
					"X-Checksum-Deploy": "true",
					"X-Checksum-Sha1":   hexSum(s1[:]),
				},
			},
			want: 201,
		},
		{
			name: "folder PUT",
			call: call{
				method: "PUT",
				path:   "/artifactory/libs/empty/",
			},
			want: 201,
		},
		{
			name: "AQL returns results first",
			call: call{
				method: "POST",
				path:   "/artifactory/api/search/aql",
				body:   `items.find({"path":{"$ne":"."},"$or":[{"$and":[{"repo":"libs","path":"dir","name":"f.bin"}]}]}).include("name","repo","path","actual_md5","actual_sha1","sha256","size","type","modified","created","property")`,
			},
			want: 200,
			check: func(t *testing.T, _ reply, body string) {
				if !strings.HasPrefix(body, `{"results":[{"name":"f.bin"`) ||
					!strings.Contains(body, `"actual_sha1":"`+hexSum(s1[:])+`"`) ||
					!strings.Contains(body, `"size":15000`) {
					t.Errorf("aql body = %s", body)
				}
			},
		},
		{
			name: "AQL syntax error",
			call: call{
				method: "POST",
				path:   "/artifactory/api/search/aql",
				body:   `items.find(`,
			},
			want: 400,
		},
		{
			name: "HEAD advertises ranges",
			call: call{
				method: "HEAD",
				path:   "/artifactory/libs/dir/f.bin",
			},
			want: 200,
			check: func(t *testing.T, resp reply, _ string) {
				if resp.Header.Get("Accept-Ranges") != "bytes" || resp.Header.Get("Content-Length") != "15000" {
					t.Errorf("headers = %v", resp.Header)
				}
			},
		},
		{
			name: "full GET",
			call: call{
				method: "GET",
				path:   "/artifactory/libs/dir/f.bin",
			},
			want: 200,
			check: func(t *testing.T, resp reply, body string) {
				if body != payload || resp.Header.Get("X-Checksum-Sha1") != hexSum(s1[:]) {
					t.Error("download body or checksum header wrong")
				}
			},
		},
		{
			name: "ranged GET is 206",
			call: call{
				method: "GET",
				path:   "/artifactory/libs/dir/f.bin",
				headers: map[string]string{
					"Range": "bytes=5-9",
				},
			},
			want: 206,
			check: func(t *testing.T, resp reply, body string) {
				if body != "relic" || resp.Header.Get("Content-Range") != "bytes 5-9/15000" {
					t.Errorf("range body %q, Content-Range %q", body, resp.Header.Get("Content-Range"))
				}
			},
		},
		{
			name: "unsatisfiable range",
			call: call{
				method: "GET",
				path:   "/artifactory/libs/dir/f.bin",
				headers: map[string]string{
					"Range": "bytes=20000-",
				},
			},
			want: 416,
		},
		{
			name: "storage file info",
			call: call{
				method: "GET",
				path:   "/artifactory/api/storage/libs/dir/f.bin",
			},
			want: 200,
			check: func(t *testing.T, _ reply, body string) {
				if !strings.Contains(body, `"size": "15000"`) {
					t.Errorf("size must be a string: %s", body)
				}
			},
		},
		{
			name: "set props is 204",
			call: call{
				method: "PUT",
				path:   "/artifactory/api/storage/libs/dir/f.bin?properties=k=v+1%2Cv2;x=a%3Db&recursive=0",
			},
			want: 204,
		},
		{
			name: "get props",
			call: call{
				method: "GET",
				path:   "/artifactory/api/storage/libs/dir/f.bin?properties",
			},
			want: 200,
			check: func(t *testing.T, _ reply, body string) {
				if !strings.Contains(body, `"v 1"`) || !strings.Contains(body, `"a=b"`) ||
					!strings.Contains(body, `"build.name"`) {
					t.Errorf("props = %s", body)
				}
			},
		},
		{
			name: "delete props is 204",
			call: call{
				method: "DELETE",
				path:   "/artifactory/api/storage/libs/dir/f.bin?properties=k,x&recursive=0",
			},
			want: 204,
		},
		{
			name: "copy is exactly 200",
			call: call{
				method: "POST",
				path:   "/artifactory/api/copy/libs/dir/f.bin?to=libs%2Fcopied%2Ff.bin",
			},
			want: 200,
		},
		{
			name: "copy dry run is 200",
			call: call{
				method: "POST",
				path:   "/artifactory/api/copy/libs/dir/f.bin?dry=1&to=libs%2Fdry%2Ff.bin",
			},
			want: 200,
		},
		{
			name: "dry run changed nothing",
			call: call{
				method: "GET",
				path:   "/artifactory/api/storage/libs/dry/f.bin",
			},
			want: 404,
		},
		{
			name: "move is exactly 200",
			call: call{
				method: "POST",
				path:   "/artifactory/api/move/libs/copied?to=libs%2Fmoved",
			},
			want: 200,
		},
		{
			name: "moved file exists",
			call: call{
				method: "HEAD",
				path:   "/artifactory/libs/moved/f.bin",
			},
			want: 200,
		},
		{
			name: "build publish",
			call: call{
				method: "PUT",
				path:   "/artifactory/api/build",
				body:   buildJSON,
				headers: map[string]string{
					"Content-Type": "application/vnd.org.jfrog.artifactory+json",
				},
			},
			want: 204,
		},
		{
			name: "build runs",
			call: call{
				method: "GET",
				path:   "/artifactory/api/build/b",
			},
			want: 200,
			check: func(t *testing.T, _ reply, body string) {
				if !strings.Contains(body, `"uri": "/1"`) {
					t.Errorf("runs = %s", body)
				}
			},
		},
		{
			name: "build get",
			call: call{
				method: "GET",
				path:   "/artifactory/api/build/b/1",
			},
			want: 200,
			check: func(t *testing.T, _ reply, body string) {
				if !strings.Contains(body, `"started": "`+started+`"`) {
					t.Errorf("build = %s", body)
				}

				var b struct {
					BuildInfo struct {
						Version string `json:"version"`
						Modules []struct {
							Dependencies []struct {
								Sha256 string `json:"sha256"`
							} `json:"dependencies"`
						} `json:"modules"`
					} `json:"buildInfo"`
				}
				if err := json.Unmarshal([]byte(body), &b); err != nil {
					t.Fatal(err)
				}

				if b.BuildInfo.Version != "1.0.1" {
					t.Errorf("build info version = %q, want 1.0.1", b.BuildInfo.Version)
				}

				if len(b.BuildInfo.Modules) != 1 || len(b.BuildInfo.Modules[0].Dependencies) != 1 ||
					b.BuildInfo.Modules[0].Dependencies[0].Sha256 != hexSum(s256[:]) {
					t.Errorf("the dependency known by its sha1 lacks its sha256: %s", body)
				}
			},
		},
		{
			name: "build-info file visible to AQL (LATEST lookup)",
			call: call{
				method: "POST",
				path:   "/artifactory/api/search/aql",
				body:   `items.find({"type": "file","repo": "artifactory-build-info","path": {"$match": "b"}}).sort({"$desc":["created"]}).limit(1)`,
			},
			want: 200,
			check: func(t *testing.T, _ reply, body string) {
				if !strings.Contains(body, `"name":"1-1790157600000.json"`) {
					t.Errorf("aql = %s", body)
				}
			},
		},
		{
			name: "build artifacts by AQL",
			call: call{
				method: "POST",
				path:   "/artifactory/api/search/aql",
				body:   `items.find({"$or":[{"$and":[{"artifact.module.build.name":"b","artifact.module.build.number":"1"}]}]}).include("name","repo","path","actual_sha1","property")`,
			},
			want: 200,
			check: func(t *testing.T, _ reply, body string) {
				if n := strings.Count(body, `"actual_sha1"`); n != 3 {
					t.Errorf("got %d build artifacts, want 3: %s", n, body)
				}
			},
		},
		{
			name: "promote is exactly 200",
			call: call{
				method: "PUT",
				path:   "/artifactory/api/repositories/prod",
				body:   `{"rclass":"local"}`,
			},
			want: 200,
		},
		{
			name: "promote",
			call: call{
				method: "POST",
				path:   "/artifactory/api/build/promote/b/1",
				body:   `{"targetRepo":"prod","status":"released","dependencies":false,"copy":true,"failFast":true,"dryRun":false}`,
			},
			want: 200,
		},
		{
			name: "build get after promotion lists the status",
			call: call{
				method: "GET",
				path:   "/artifactory/api/build/b/1",
			},
			want: 200,
			check: func(t *testing.T, _ reply, body string) {
				var b struct {
					BuildInfo struct {
						Statuses []struct {
							Status        string `json:"status"`
							TimestampDate int64  `json:"timestampDate"`
						} `json:"statuses"`
					} `json:"buildInfo"`
				}
				if err := json.Unmarshal([]byte(body), &b); err != nil {
					t.Fatal(err)
				}

				if len(b.BuildInfo.Statuses) != 1 || b.BuildInfo.Statuses[0].Status != "released" ||
					b.BuildInfo.Statuses[0].TimestampDate == 0 {
					t.Errorf("statuses = %+v", b.BuildInfo.Statuses)
				}
			},
		},
		{
			name: "promoted artifact",
			call: call{
				method: "HEAD",
				path:   "/artifactory/prod/dir/f.bin",
			},
			want: 200,
		},
		{
			name: "unknown build",
			call: call{
				method: "GET",
				path:   "/artifactory/api/build/nope/1",
			},
			want: 404,
		},
		{
			name: "discard count 0 rejected",
			call: call{
				method: "POST",
				path:   "/artifactory/api/build/retention/b?async=false",
				body:   `{"count":"0","deleteBuildArtifacts":false}`,
			},
			want: 400,
		},
		{
			name: "discard is exactly 204",
			call: call{
				method: "POST",
				path:   "/artifactory/api/build/retention/b?async=false",
				body:   `{"minimumBuildDate":"2100-01-01T00:00:00.000+0000","deleteBuildArtifacts":false}`,
			},
			want: 204,
		},
		{
			name: "discarded",
			call: call{
				method: "GET",
				path:   "/artifactory/api/build/b/1",
			},
			want: 404,
		},
		{
			name: "delete is exactly 204",
			call: call{
				method: "DELETE",
				path:   "/artifactory/libs/dir/",
			},
			want: 204,
		},
		{
			name: "deleted",
			call: call{
				method: "HEAD",
				path:   "/artifactory/libs/dir/f.bin",
			},
			want: 404,
		},
		{
			name: "without an encryption key the encrypted password is the basic password",
			call: call{
				method: "GET",
				path:   "/artifactory/api/security/encryptedPassword",
			},
			want: 200,
			check: func(t *testing.T, _ reply, body string) {
				if body != "password" {
					t.Errorf("body = %q", body)
				}
			},
		},
		{
			name: "repo delete is exactly 200",
			call: call{
				method: "DELETE",
				path:   "/artifactory/api/repositories/libs",
			},
			want: 200,
		},
	}
	for _, st := range steps {
		resp, body := do(t, srv, st.call)
		if resp.StatusCode != st.want {
			t.Fatalf(
				"%s: %s %s = %d, want %d\n%s",
				st.name,
				st.call.method,
				st.call.path,
				resp.StatusCode,
				st.want,
				body,
			)
		}

		if st.check != nil {
			st.check(t, resp, body)
		}
	}
}

func TestTokenLifecycle(t *testing.T) {
	t.Parallel()
	srv := server(t)
	form := "application/x-www-form-urlencoded"

	resp, body := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/security/token",
			body:   "expires_in=3600&refreshable=true&scope=member-of-groups%3A%2A&username=admin",
			headers: map[string]string{
				"Content-Type": form,
			},
		},
	)
	if resp.StatusCode != 200 {
		t.Fatalf("create = %d %s", resp.StatusCode, body)
	}

	var tok access.TokenResponse
	if err := json.Unmarshal([]byte(body), &tok); err != nil || tok.RefreshToken == "" || tok.ExpiresIn != 3600 ||
		tok.Scope != "member-of-groups:*" {
		t.Fatalf("token = %s", body)
	}

	claims, err := base64.RawStdEncoding.DecodeString(strings.Split(tok.AccessToken, ".")[1])
	if err != nil || !strings.Contains(string(claims), `"sub":"jfrt@relic/users/admin"`) ||
		!strings.Contains(string(claims), `"iss":"jfrt@relic"`) {
		t.Errorf("claims = %s %v", claims, err)
	}

	bearer := func(token string) int {
		r, _ := do(
			t,
			srv,
			call{
				method: "GET",
				path:   "/artifactory/api/system/version",
				noAuth: true,
				headers: map[string]string{
					"Authorization": "Bearer " + token,
				},
			},
		)

		return r.StatusCode
	}
	if code := bearer(tok.AccessToken); code != 200 {
		t.Fatalf("bearer = %d", code)
	}

	if code := bearer(tok.AccessToken[:len(tok.AccessToken)-2] + "xx"); code != 401 {
		t.Errorf("tampered token = %d, want 401", code)
	}

	resp, body = do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/security/token",
			noAuth: true,
			body:   "access_token=" + tok.AccessToken + "&grant_type=refresh_token&refresh_token=" + tok.RefreshToken,
			headers: map[string]string{
				"Content-Type": form,
			},
		},
	)
	if resp.StatusCode != 200 {
		t.Fatalf("refresh = %d %s", resp.StatusCode, body)
	}

	var next access.TokenResponse
	if err := json.Unmarshal([]byte(body), &next); err != nil || next.AccessToken == tok.AccessToken {
		t.Fatalf("refreshed = %s", body)
	}

	if code := bearer(tok.AccessToken); code != 401 {
		t.Errorf("old token after refresh = %d, want 401 (revoked)", code)
	}

	if code := bearer(next.AccessToken); code != 200 {
		t.Errorf("new token = %d", code)
	}

	resp, _ = do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/security/token",
			noAuth: true,
			body:   "grant_type=refresh_token&refresh_token=" + tok.RefreshToken,
			headers: map[string]string{
				"Content-Type": form,
			},
		},
	)
	if resp.StatusCode != 401 {
		t.Errorf("reused refresh token = %d, want 401", resp.StatusCode)
	}

	resp, _ = do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/security/token",
			noAuth: true,
			body:   "username=admin",
			headers: map[string]string{
				"Content-Type": form,
			},
		},
	)
	if resp.StatusCode != 401 {
		t.Errorf("anonymous create = %d, want 401", resp.StatusCode)
	}
}
