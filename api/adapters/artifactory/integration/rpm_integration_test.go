//go:build integration

package integration

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/google/rpmpack"

	"relic/internal/artifacts/formats/rpm"
)

func rpmPackage(t *testing.T, name, version string) []byte {
	t.Helper()

	r, err := rpmpack.NewRPM(rpmpack.RPMMetaData{
		Name:      name,
		Version:   version,
		Release:   "1",
		Arch:      "noarch",
		Summary:   name + " for the RPM tests",
		Licence:   "MIT",
		BuildTime: time.Unix(1_700_000_000, 0),
	})
	if err != nil {
		t.Fatal(err)
	}

	r.AddFile(rpmpack.RPMFile{
		Name: "/usr/bin/" + name,
		Body: []byte("#!/bin/sh\necho " + name + "\n"),
		Mode: 0o100755,
	})

	var buf bytes.Buffer
	if err := r.Write(&buf); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

type repoMDLocations struct {
	Data []struct {
		Type     string `xml:"type,attr"`
		Location struct {
			Href string `xml:"href,attr"`
		} `xml:"location"`
	} `xml:"data"`
}

type primaryPackages struct {
	Packages []struct {
		Name     string `xml:"name"`
		Location struct {
			Href string `xml:"href,attr"`
		} `xml:"location"`
	} `xml:"package"`
}

func primaryLocations(t *testing.T, srv *httptest.Server, base string) (string, map[string]string) {
	t.Helper()

	resp, repoMD := do(t, srv, call{
		method: http.MethodGet,
		path:   base + "/repodata/repomd.xml",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s/repodata/repomd.xml = %d %s", base, resp.StatusCode, repoMD)
	}

	var md repoMDLocations
	if err := xml.Unmarshal([]byte(repoMD), &md); err != nil {
		t.Fatalf("repomd.xml: %v\n%s", err, repoMD)
	}

	for _, d := range md.Data {
		if d.Type != "primary" {
			continue
		}

		resp, compressed := do(t, srv, call{
			method: http.MethodGet,
			path:   base + "/" + d.Location.Href,
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s/%s = %d", base, d.Location.Href, resp.StatusCode)
		}

		zr, err := gzip.NewReader(strings.NewReader(compressed))
		if err != nil {
			t.Fatal(err)
		}

		plain, err := io.ReadAll(zr)
		if err != nil {
			t.Fatal(err)
		}

		var primary primaryPackages
		if err := xml.Unmarshal(plain, &primary); err != nil {
			t.Fatalf("primary: %v", err)
		}

		locations := map[string]string{}
		for _, p := range primary.Packages {
			locations[p.Name] = p.Location.Href
		}

		return repoMD, locations
	}

	t.Fatalf("%s/repodata/repomd.xml lists no primary metadata", base)

	return "", nil
}

func TestRPMRepositories(t *testing.T) {
	t.Parallel()

	srv := server(t)

	upstreamRPM := rpmPackage(t, "upstream-tool", "2.0")

	upstreamPkg, err := rpm.Parse(bytes.NewReader(upstreamRPM))
	if err != nil {
		t.Fatal(err)
	}

	upstreamSum := sha256.Sum256(upstreamRPM)

	upstreamFiles, err := rpm.Build([]rpm.Entry{
		{
			Package:  upstreamPkg,
			Location: "Packages/u/upstream-tool-2.0-1.noarch.rpm",
			Size:     int64(len(upstreamRPM)),
			SHA256:   hexSum(upstreamSum[:]),
			Modified: 1_700_000_000,
		},
	}, nil, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Packages/u/upstream-tool-2.0-1.noarch.rpm" {
			_, _ = w.Write(upstreamRPM)
			return
		}

		if body, ok := upstreamFiles[strings.TrimPrefix(r.URL.Path, "/repodata/")]; ok {
			_, _ = w.Write(body)
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)

	for _, repo := range []struct {
		key  string
		body string
	}{
		{
			key:  "rpm-local",
			body: `{"rclass":"local","packageType":"yum"}`,
		},
		{
			key:  "rpm-deep",
			body: `{"rclass":"local","packageType":"rpm","yumRootDepth":1}`,
		},
		{
			key:  "rpm-remote",
			body: `{"rclass":"remote","packageType":"rpm","url":"` + upstream.URL + `"}`,
		},
		{
			key:  "rpm-virtual",
			body: `{"rclass":"virtual","packageType":"rpm","repositories":["rpm-local","rpm-remote"]}`,
		},
	} {
		if resp, body := do(t, srv, call{
			method: http.MethodPut,
			path:   "/artifactory/api/repositories/" + repo.key,
			body:   repo.body,
		}); resp.StatusCode != http.StatusOK {
			t.Fatalf("create %s = %d %s", repo.key, resp.StatusCode, body)
		}
	}

	_, config := do(t, srv, call{
		method: http.MethodGet,
		path:   "/artifactory/api/repositories/rpm-local",
	})

	var created struct {
		PackageType  string `json:"packageType"`
		YumRootDepth int    `json:"yumRootDepth"`
	}
	if err := json.Unmarshal([]byte(config), &created); err != nil || created.PackageType != "yum" {
		t.Errorf("a repository created as yum reads back as %q: %s", created.PackageType, config)
	}

	listed := func(packageType string) []string {
		_, body := do(t, srv, call{
			method: http.MethodGet,
			path:   "/artifactory/api/repositories?packageType=" + packageType,
		})

		var repos []struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal([]byte(body), &repos); err != nil {
			t.Fatal(err)
		}

		keys := make([]string, 0, len(repos))
		for _, r := range repos {
			keys = append(keys, r.Key)
		}

		slices.Sort(keys)

		return keys
	}

	if got := listed("yum"); !slices.Equal(got, []string{
		"rpm-local",
	}) {
		t.Errorf("packageType=yum lists %v", got)
	}

	if got := listed("rpm"); !slices.Equal(got, []string{
		"rpm-deep",
		"rpm-remote",
		"rpm-virtual",
	}) {
		t.Errorf("packageType=rpm lists %v", got)
	}

	hello := rpmPackage(t, "hello", "1.0")
	for _, upload := range []struct {
		path string
		body []byte
	}{
		{
			path: "/artifactory/rpm-local/h/hello-1.0-1.noarch.rpm",
			body: hello,
		},
		{
			path: "/artifactory/rpm-deep/el9/x/deep-1.0-1.noarch.rpm",
			body: rpmPackage(t, "deep", "1.0"),
		},
	} {
		if resp, body := do(t, srv, call{
			method: http.MethodPut,
			path:   upload.path,
			body:   string(upload.body),
		}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("upload %s = %d %s", upload.path, resp.StatusCode, body)
		}
	}

	repoMD, locations := primaryLocations(t, srv, "/artifactory/rpm-local")
	if locations["hello"] != "h/hello-1.0-1.noarch.rpm" || len(locations) != 1 {
		t.Errorf("rpm-local primary = %v", locations)
	}

	_, signature := do(t, srv, call{
		method: http.MethodGet,
		path:   "/artifactory/rpm-local/repodata/repomd.xml.asc",
	})
	_, key := do(t, srv, call{
		method: http.MethodGet,
		path:   "/artifactory/rpm-local/repodata/repomd.xml.key",
	})

	keyring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(key))
	if err != nil {
		t.Fatalf("repomd.xml.key: %v", err)
	}

	if _, err := openpgp.CheckArmoredDetachedSignature(
		keyring,
		strings.NewReader(repoMD),
		strings.NewReader(signature),
		nil,
	); err != nil {
		t.Errorf("repomd.xml.asc does not verify repomd.xml: %v", err)
	}

	if resp, native := do(t, srv, call{
		method: http.MethodGet,
		path:   "/rpm/rpm-local/repodata/repomd.xml",
	}); resp.StatusCode != http.StatusOK || native != repoMD {
		t.Errorf("/rpm/ mount = %d, same repomd.xml: %v", resp.StatusCode, native == repoMD)
	}

	if resp, _ := do(t, srv, call{
		method: http.MethodGet,
		path:   "/artifactory/rpm-deep/repodata/repomd.xml",
	}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("repodata above the metadata depth = %d, want 404", resp.StatusCode)
	}

	if _, deep := primaryLocations(t, srv, "/artifactory/rpm-deep/el9"); deep["deep"] != "x/deep-1.0-1.noarch.rpm" {
		t.Errorf("rpm-deep el9 primary = %v", deep)
	}

	_, merged := primaryLocations(t, srv, "/artifactory/rpm-virtual")
	if merged["hello"] != "h/hello-1.0-1.noarch.rpm" ||
		merged["upstream-tool"] != "Packages/u/upstream-tool-2.0-1.noarch.rpm" || len(merged) != 2 {
		t.Errorf("rpm-virtual primary = %v", merged)
	}

	if resp, body := do(t, srv, call{
		method: http.MethodGet,
		path:   "/artifactory/rpm-virtual/Packages/u/upstream-tool-2.0-1.noarch.rpm",
	}); resp.StatusCode != http.StatusOK || body != string(upstreamRPM) {
		t.Errorf("upstream package through the virtual = %d, %d bytes", resp.StatusCode, len(body))
	}
}

func TestRPMKeyIsServedWithoutBuildingRepodata(t *testing.T) {
	t.Parallel()

	srv := server(t)

	var upstreamRequests atomic.Int64

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamRequests.Add(1)
		http.NotFound(w, nil)
	}))
	t.Cleanup(upstream.Close)

	for _, repo := range []struct {
		key  string
		body string
	}{
		{
			key:  "rpm-key-local",
			body: `{"rclass":"local","packageType":"rpm"}`,
		},
		{
			key:  "rpm-key-remote",
			body: `{"rclass":"remote","packageType":"rpm","url":"` + upstream.URL + `"}`,
		},
		{
			key:  "rpm-key-virtual",
			body: `{"rclass":"virtual","packageType":"rpm","repositories":["rpm-key-local","rpm-key-remote"]}`,
		},
	} {
		if resp, body := do(t, srv, call{
			method: http.MethodPut,
			path:   "/artifactory/api/repositories/" + repo.key,
			body:   repo.body,
		}); resp.StatusCode != http.StatusOK {
			t.Fatalf("create %s = %d %s", repo.key, resp.StatusCode, body)
		}
	}

	resp, key := do(t, srv, call{
		method: http.MethodGet,
		path:   "/artifactory/rpm-key-virtual/repodata/repomd.xml.key",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("repomd.xml.key = %d %s", resp.StatusCode, key)
	}

	if _, err := openpgp.ReadArmoredKeyRing(strings.NewReader(key)); err != nil {
		t.Errorf("repomd.xml.key: %v", err)
	}

	if n := upstreamRequests.Load(); n != 0 {
		t.Errorf("serving the key sent %d requests upstream, want 0", n)
	}
}
