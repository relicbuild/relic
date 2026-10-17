//go:build integration

package integration

import (
	"crypto/sha1" //nolint:gosec // wire checksum
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMavenRepository(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/libs-release",
			body:   `{"rclass":"local","packageType":"maven"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/libs-snapshot",
			body:   `{"rclass":"local","packageType":"maven"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/libs",
			body:   `{"rclass":"virtual","packageType":"maven","repositories":["libs-release","libs-snapshot"]}`,
		},
	)

	base := "/artifactory/libs-release/org/acme/lib"
	for _, v := range []string{
		"1.0",
		"1.10",
		"1.9",
		"2.0-RC1",
	} {
		if resp, _ := do(
			t,
			srv,
			call{
				method: "PUT",
				path:   base + "/" + v + "/lib-" + v + ".jar",
				body:   "jar " + v,
			},
		); resp.StatusCode != 201 {
			t.Fatalf("deploy %s = %d", v, resp.StatusCode)
		}

		do(t, srv, call{
			method: "PUT",
			path:   base + "/" + v + "/lib-" + v + ".pom",
			body:   "<project/>",
		})
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   base + "/maven-metadata.xml",
			body:   `<metadata><versioning><versions><version>0.1</version></versions></versioning></metadata>`,
		},
	)

	_, md := do(t, srv, call{
		method: "GET",
		path:   base + "/maven-metadata.xml",
	})
	for _, want := range []string{
		"<groupId>org.acme</groupId>",
		"<latest>2.0-RC1</latest>",
		"<release>2.0-RC1</release>",
		"<version>1.0</version>\n      <version>1.9</version>\n      <version>1.10</version>\n      <version>2.0-RC1</version>",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("artifact metadata lacks %q:\n%s", want, md)
		}
	}

	if strings.Contains(md, "0.1") {
		t.Errorf("client metadata not replaced:\n%s", md)
	}

	sum := sha1.Sum([]byte("jar 1.0")) //nolint:gosec // wire checksum
	if _, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   base + "/1.0/lib-1.0.jar.sha1",
		},
	); got != hex.EncodeToString(sum[:]) {
		t.Errorf("sha1 file = %q", got)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   base + "/1.0/lib-1.0.jar.sha1",
			body:   hex.EncodeToString(sum[:]) + "  lib-1.0.jar",
		},
	); resp.StatusCode != 201 {
		t.Errorf("matching checksum upload = %d", resp.StatusCode)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   base + "/1.0/lib-1.0.jar.sha1",
			body:   strings.Repeat("0", 40),
		},
	); resp.StatusCode != 409 {
		t.Errorf("mismatching checksum upload = %d, want 409", resp.StatusCode)
	}

	if resp, _ := do(t, srv, call{
		method: "HEAD",
		path:   base + "/1.0/lib-1.0.jar.sha1",
	}); resp.StatusCode != 200 {
		t.Errorf("checksum not stored as an item but still served: %d", resp.StatusCode)
	}

	snap := "/artifactory/libs-snapshot/org/acme/lib/3.0-SNAPSHOT"
	for _, f := range []string{
		"lib-3.0-20260923.101010-1.jar",
		"lib-3.0-20260923.101010-1.pom",
		"lib-3.0-20260923.121212-2.jar",
		"lib-3.0-20260923.121212-2-sources.jar",
		"lib-3.0-20260923.121212-2.pom",
	} {
		do(t, srv, call{
			method: "PUT",
			path:   snap + "/" + f,
			body:   f,
		})
	}

	_, smd := do(t, srv, call{
		method: "GET",
		path:   snap + "/maven-metadata.xml",
	})
	for _, want := range []string{
		"<version>3.0-SNAPSHOT</version>",
		"<timestamp>20260923.121212</timestamp>",
		"<buildNumber>2</buildNumber>",
		"<classifier>sources</classifier>",
		"<value>3.0-20260923.121212-2</value>",
	} {
		if !strings.Contains(smd, want) {
			t.Errorf("snapshot metadata lacks %q:\n%s", want, smd)
		}
	}

	if strings.Contains(smd, "101010-1") {
		t.Errorf("old snapshot build in metadata:\n%s", smd)
	}

	_, vmd := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/libs/org/acme/lib/maven-metadata.xml",
	})
	if !strings.Contains(vmd, "<version>2.0-RC1</version>") ||
		!strings.Contains(vmd, "<version>3.0-SNAPSHOT</version>") ||
		!strings.Contains(vmd, "<latest>3.0-SNAPSHOT</latest>") {
		t.Errorf("merged metadata:\n%s", vmd)
	}

	merged := sha1.Sum([]byte(vmd)) //nolint:gosec // wire checksum

	shaResp, sha := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/libs/org/acme/lib/maven-metadata.xml.sha1",
	})
	if shaResp.StatusCode != 200 || sha != hex.EncodeToString(merged[:]) {
		t.Errorf("merged metadata .sha1 = %d %q, want %x", shaResp.StatusCode, sha, merged)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/libs/org/acme/lib/maven-metadata.xml",
		},
	); resp.Header.Get("X-Checksum-Sha1") != hex.EncodeToString(merged[:]) {
		t.Errorf("merged metadata X-Checksum-Sha1 = %q", resp.Header.Get("X-Checksum-Sha1"))
	}

	if resp, md5sum := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/libs/org/acme/lib/maven-metadata.xml.md5",
		},
	); resp.StatusCode != 200 ||
		len(md5sum) != 32 {
		t.Errorf("merged metadata .md5 = %d %q", resp.StatusCode, md5sum)
	}

	if resp, body := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/libs/org/acme/lib/1.9/lib-1.9.jar",
		},
	); resp.StatusCode != 200 ||
		body != "jar 1.9" {
		t.Errorf("virtual artifact = %d %q", resp.StatusCode, body)
	}
}

func TestMavenRemoteMetadataRevalidation(t *testing.T) {
	t.Parallel()

	var version atomic.Value
	version.Store("1.0")

	var hits atomic.Int32

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/maven-metadata.xml") {
			hits.Add(1)

			_, _ = io.WriteString(
				w,
				"<metadata><versioning><latest>"+version.Load().(string)+"</latest></versioning></metadata>",
			)

			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/central",
			body:   `{"rclass":"remote","packageType":"maven","url":"` + upstream.URL + `","retrievalCachePeriodSecs":"1"}`,
		},
	)

	path := "/artifactory/central/org/x/y/maven-metadata.xml"
	start := time.Now()

	if _, body := do(t, srv, call{
		method: "GET",
		path:   path,
	}); !strings.Contains(body, "1.0") {
		t.Fatalf("first fetch = %s", body)
	}

	version.Store("2.0")

	_, body := do(t, srv, call{
		method: "GET",
		path:   path,
	})
	if time.Since(start) >= time.Second {
		t.Skip("the second fetch ended after the one-second cache period")
	}

	if !strings.Contains(body, "1.0") {
		t.Errorf("within the cache period the cached copy must be served: %s", body)
	}

	time.Sleep(1100 * time.Millisecond)

	if _, body := do(t, srv, call{
		method: "GET",
		path:   path,
	}); !strings.Contains(body, "2.0") {
		t.Errorf("after the cache period metadata must be re-fetched: %s", body)
	}

	if n := hits.Load(); n != 2 {
		t.Errorf("upstream metadata hits = %d, want 2", n)
	}
}
