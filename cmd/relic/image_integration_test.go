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

func pushImage(t *testing.T, url, repo, image, tag string) {
	t.Helper()

	type registryReply struct {
		status   int
		location string
	}

	send := func(method, path, contentType, body string) registryReply {
		t.Helper()

		req, err := http.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}

		req.SetBasicAuth("admin", "password")

		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		_ = resp.Body.Close()

		return registryReply{
			status:   resp.StatusCode,
			location: resp.Header.Get("Location"),
		}
	}

	blob := func(content string) string {
		sum := sha256.Sum256([]byte(content))
		digest := "sha256:" + hex.EncodeToString(sum[:])

		started := send(http.MethodPost, url+"/v2/"+repo+"/"+image+"/blobs/uploads/", "", "")
		if started.status != http.StatusAccepted {
			t.Fatalf("start upload = %d", started.status)
		}

		loc := started.location
		if !strings.HasPrefix(loc, "http") {
			loc = url + loc
		}

		sep := "?"
		if strings.Contains(loc, "?") {
			sep = "&"
		}

		if finished := send(
			http.MethodPut,
			loc+sep+"digest="+digest,
			"",
			content,
		); finished.status != http.StatusCreated {
			t.Fatalf("finish upload = %d", finished.status)
		}

		return digest
	}

	config, layer := blob("{}"), blob("layer-"+tag)
	manifest := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json",`+
		`"config":{"mediaType":"application/vnd.docker.container.image.v1+json","digest":%q,"size":2},`+
		`"layers":[{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","digest":%q,"size":%d}]}`,
		config, layer, len("layer-"+tag))

	if pushed := send(
		http.MethodPut,
		url+"/v2/"+repo+"/"+image+"/manifests/"+tag,
		"application/vnd.docker.distribution.manifest.v2+json",
		manifest,
	); pushed.status != http.StatusCreated {
		t.Fatalf("push manifest = %d", pushed.status)
	}
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
