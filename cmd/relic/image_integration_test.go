//go:build integration

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type registry struct {
	t    *testing.T
	url  string
	repo string
}

type registryReply struct {
	status   int
	location string
}

func (r registry) send(method, path, contentType, body string) registryReply {
	r.t.Helper()

	req, err := http.NewRequestWithContext(r.t.Context(), method, path, strings.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}

	req.SetBasicAuth("admin", "password")

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}

	_ = resp.Body.Close()

	return registryReply{
		status:   resp.StatusCode,
		location: resp.Header.Get("Location"),
	}
}

func contentDigest(content string) string {
	sum := sha256.Sum256([]byte(content))

	return "sha256:" + hex.EncodeToString(sum[:])
}

func (r registry) blob(image, content string) string {
	r.t.Helper()

	digest := contentDigest(content)

	started := r.send(http.MethodPost, r.url+"/v2/"+r.repo+"/"+image+"/blobs/uploads/", "", "")
	if started.status != http.StatusAccepted {
		r.t.Fatalf("start upload = %d", started.status)
	}

	loc := started.location
	if !strings.HasPrefix(loc, "http") {
		loc = r.url + loc
	}

	sep := "?"
	if strings.Contains(loc, "?") {
		sep = "&"
	}

	if finished := r.send(
		http.MethodPut,
		loc+sep+"digest="+digest,
		"",
		content,
	); finished.status != http.StatusCreated {
		r.t.Fatalf("finish upload = %d", finished.status)
	}

	return digest
}

func (r registry) imageManifest(image, layerContent string) string {
	r.t.Helper()

	config, layer := r.blob(image, "{}"), r.blob(image, layerContent)

	return fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json",`+
		`"config":{"mediaType":"application/vnd.docker.container.image.v1+json","digest":%q,"size":2},`+
		`"layers":[{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","digest":%q,"size":%d}]}`,
		config, layer, len(layerContent))
}

func (r registry) push(image, reference, mediaType, manifest string) {
	r.t.Helper()

	if pushed := r.send(
		http.MethodPut,
		r.url+"/v2/"+r.repo+"/"+image+"/manifests/"+reference,
		mediaType,
		manifest,
	); pushed.status != http.StatusCreated {
		r.t.Fatalf("push manifest %s = %d", reference, pushed.status)
	}
}

const dockerManifestType = "application/vnd.docker.distribution.manifest.v2+json"

func pushImage(t *testing.T, url, repo, image, tag string) {
	t.Helper()

	r := registry{
		t:    t,
		url:  url,
		repo: repo,
	}
	r.push(image, tag, dockerManifestType, r.imageManifest(image, "layer-"+tag))
}

func pushIndex(t *testing.T, url, repo, image, tag string, platforms ...string) {
	t.Helper()

	r := registry{
		t:    t,
		url:  url,
		repo: repo,
	}

	entries := make([]string, 0, len(platforms))

	for _, p := range platforms {
		manifest := r.imageManifest(image, "layer-"+tag+"-"+p)
		r.push(image, contentDigest(manifest), dockerManifestType, manifest)

		system, architecture, _ := strings.Cut(p, "/")
		entries = append(entries, fmt.Sprintf(
			`{"mediaType":%q,"digest":%q,"size":%d,"platform":{"os":%q,"architecture":%q}}`,
			dockerManifestType,
			contentDigest(manifest),
			len(manifest),
			system,
			architecture,
		))
	}

	r.push(
		image,
		tag,
		"application/vnd.oci.image.index.v1+json",
		`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[`+
			strings.Join(entries, ",")+`]}`,
	)
}

func TestImageCommands(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	config := t.TempDir() + "/config.yaml"

	if _, err := relic(
		t,
		config,
		"password\n",
		"auth",
		"login",
		srv.URL,
		"--user",
		"admin",
		"--password-stdin",
	); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(t, config, `apiVersion: relic.build/v1
kind: Repository
metadata: {name: img-dev}
spec: {class: local, format: docker}
---
apiVersion: relic.build/v1
kind: Repository
metadata: {name: img-prod}
spec: {class: local, format: docker}
`, "apply", "-f", "-"); err != nil {
		t.Fatal(err)
	}

	pushImage(t, srv.URL, "img-dev", "team/app", "1.0")
	pushImage(t, srv.URL, "img-dev", "team/app", "1.1")

	if out, err := relic(t, config, "", "build", "add-image", "app", "3", "img-dev/team/app:1.0"); err != nil ||
		!strings.Contains(out, "img-dev/team/app:1.0 with 3 files recorded") {
		t.Errorf("add-image = %q %v", out, err)
	}

	if _, err := relic(t, config, "", "build", "add-image", "app", "3", "img-dev/team/app:9.9"); err == nil {
		t.Error("add-image of a missing tag succeeded")
	}

	pushIndex(t, srv.URL, "img-dev", "team/multi", "2.0", "linux/amd64", "linux/arm64")

	if out, err := relic(t, config, "", "build", "add-image", "app", "3", "img-dev/team/multi:2.0"); err != nil ||
		!strings.Contains(out, "img-dev/team/multi:2.0 with 7 files recorded") {
		t.Errorf("add-image of a multi-platform image = %q %v", out, err)
	}

	if out, err := relic(
		t,
		config,
		"",
		"image",
		"promote",
		"img-dev/team/app:1.0",
		"img-prod/release/app:stable",
		"--copy",
	); err != nil ||
		out != "img-dev/team/app:1.0 copied to img-prod/release/app:stable\n" {
		t.Errorf("promote --copy = %q %v", out, err)
	}

	for target, want := range map[string]bool{
		"img-prod/release/app/stable/manifest.json": true,
		"img-dev/team/app/1.0/manifest.json":        true,
	} {
		if _, err := relic(t, config, "", "artifact", "download", target, "-o", "-"); (err == nil) != want {
			t.Errorf("%s exists: %v", target, err == nil)
		}
	}

	if _, err := relic(t, config, "", "image", "promote", "img-dev/team/app:x", "img-prod", "--all-tags"); err != nil {
		t.Fatal(err)
	}

	for target, want := range map[string]bool{
		"img-prod/team/app/1.0/manifest.json": true,
		"img-prod/team/app/1.1/manifest.json": true,
		"img-dev/team/app/1.0/manifest.json":  false,
		"img-dev/team/app/1.1/manifest.json":  false,
	} {
		if _, err := relic(t, config, "", "artifact", "download", target, "-o", "-"); (err == nil) != want {
			t.Errorf("after --all-tags, %s exists: %v", target, err == nil)
		}
	}

	if _, err := relic(t, config, "", "image", "promote", "img-dev/team/app", "img-prod"); err == nil {
		t.Error("a reference without a tag was accepted")
	}
}
