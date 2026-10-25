//go:build integration

package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"relic/api/adapters/artifactory"
	"relic/api/adapters/artifactory/access"

	"relic/internal/access/auth"
	"relic/internal/app"
	"relic/internal/artifacts/repository"
	"relic/internal/delivery/replication"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore"
	"relic/internal/platform/objectstore/objectstoretest"
)

type instance struct {
	srv   *httptest.Server
	db    *database.DB
	blobs *objectstoretest.Memory
}

func relicInstance(t *testing.T) instance {
	t.Helper()
	db := databasetest.NewDB(t)

	log := slog.New(slog.DiscardHandler)
	if err := app.BootstrapAdmin(t.Context(), db, "admin", "password", log); err != nil {
		t.Fatal(err)
	}

	tokens, err := auth.NewTokens(t.Context(), db, nil, access.Format{})
	if err != nil {
		t.Fatal(err)
	}

	blobs := objectstoretest.NewMemory()
	srv := httptest.NewServer(app.NewHandler(db, blobs, tokens, nil, app.Options{}, log))
	t.Cleanup(srv.Close)

	return instance{
		srv:   srv,
		db:    db,
		blobs: blobs,
	}
}

func send(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	req.SetBasicAuth("admin", "password")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	b, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, string(b)
}

func TestPushReplication(t *testing.T) {
	t.Parallel()

	src, dst := relicInstance(t), relicInstance(t)
	for _, in := range []instance{
		src,
		dst,
	} {
		if code, _ := send(
			t,
			"PUT",
			in.srv.URL+"/artifactory/api/repositories/libs",
			`{"rclass":"local"}`,
		); code != 200 {
			t.Fatalf("create repo = %d", code)
		}
	}

	send(t, "PUT", src.srv.URL+"/artifactory/libs/a/one.txt;team=x", "one")
	send(t, "PUT", src.srv.URL+"/artifactory/libs/b/two.txt", "two")
	send(t, "PUT", dst.srv.URL+"/artifactory/libs/stale.txt", "stale")

	cfg, _ := json.Marshal(map[string]any{
		"username":       "admin",
		"password":       "password",
		"url":            dst.srv.URL + "/artifactory/libs",
		"repoKey":        "libs",
		"enabled":        true,
		"syncDeletes":    true,
		"syncProperties": true,
		"cronExp":        "0 0 12 * * ?",
	})
	if code, _ := send(t, "PUT", src.srv.URL+"/artifactory/api/replications/libs", string(cfg)); code != 201 {
		t.Fatalf("configure replication = %d", code)
	}

	log := slog.New(slog.DiscardHandler)
	pusher := replication.NewPusher(
		src.db,
		repository.NewService(src.db, src.blobs, log),
		artifactory.ReplicationTargets(nil),
		log,
	)

	res, err := pusher.Run(t.Context(), "libs")
	if err != nil {
		t.Fatal(err)
	}

	if res.Pushed != 2 || res.Deleted != 1 {
		t.Errorf("first run = %+v, want 2 pushed, 1 deleted", res)
	}

	for path, want := range map[string]int{
		"a/one.txt": 200,
		"b/two.txt": 200,
		"stale.txt": 404,
	} {
		if code, _ := send(t, "HEAD", dst.srv.URL+"/artifactory/libs/"+path, ""); code != want {
			t.Errorf("target %s = %d, want %d", path, code, want)
		}
	}

	if _, props := send(
		t,
		"GET",
		dst.srv.URL+"/artifactory/api/storage/libs/a/one.txt?properties",
		"",
	); !strings.Contains(props, `"team"`) {
		t.Errorf("properties not replicated: %s", props)
	}

	if _, status := send(t, "GET", src.srv.URL+"/artifactory/api/replication/libs", ""); !strings.Contains(
		status,
		`"status": "ok"`,
	) {
		t.Errorf("status = %s", status)
	}

	cfg, _ = json.Marshal(
		map[string]any{
			"username": "admin",
			"password": "password",
			"url":      dst.srv.URL + "/artifactory/libs",
			"repoKey":  "libs",
			"enabled":  true,
		},
	)
	send(t, "PUT", src.srv.URL+"/artifactory/api/replications/libs", string(cfg))

	if res, err := pusher.Run(t.Context(), "libs"); err != nil || res.Skipped != 2 || res.Pushed != 0 {
		t.Errorf("second run = %+v, %v; want 2 skipped", res, err)
	}

	if n := len(dst.blobs.Keys()); n != 3 {
		t.Errorf("target objects = %d, want 3", n)
	}
}

func TestPushReplicationErrors(t *testing.T) {
	t.Parallel()
	src := relicInstance(t)
	log := slog.New(slog.DiscardHandler)

	pusher := replication.NewPusher(
		src.db,
		repository.NewService(src.db, src.blobs, log),
		artifactory.ReplicationTargets(nil),
		log,
	)
	if _, err := pusher.Run(t.Context(), "nope"); err == nil {
		t.Error("run without configuration succeeded")
	}

	send(t, "PUT", src.srv.URL+"/artifactory/api/repositories/libs", `{"rclass":"local"}`)
	send(t, "PUT", src.srv.URL+"/artifactory/libs/f.txt", "x")

	cfg, _ := json.Marshal(
		map[string]any{
			"url":     "http://127.0.0.1:1/artifactory/libs",
			"repoKey": "libs",
			"enabled": true,
		},
	)
	send(t, "PUT", src.srv.URL+"/artifactory/api/replications/libs", string(cfg))

	if _, err := pusher.Run(t.Context(), "libs"); err == nil {
		t.Fatal("run against an unreachable target succeeded")
	}

	if _, status := send(t, "GET", src.srv.URL+"/artifactory/api/replication/libs", ""); !strings.Contains(
		status,
		`"status": "error"`,
	) {
		t.Errorf("status = %s", status)
	}
}

func TestPushReplicationContinuesPastAFileItCannotRead(t *testing.T) {
	t.Parallel()

	src, dst := relicInstance(t), relicInstance(t)
	for _, in := range []instance{
		src,
		dst,
	} {
		if code, _ := send(
			t,
			"PUT",
			in.srv.URL+"/artifactory/api/repositories/libs",
			`{"rclass":"local"}`,
		); code != 200 {
			t.Fatalf("create repo = %d", code)
		}
	}

	send(t, "PUT", src.srv.URL+"/artifactory/libs/a/lost.txt", "lost content")
	send(t, "PUT", src.srv.URL+"/artifactory/libs/b/kept.txt", "kept content")

	sum := sha256.Sum256([]byte("lost content"))
	if err := src.blobs.Delete(t.Context(), objectstore.Key(hex.EncodeToString(sum[:]))); err != nil {
		t.Fatal(err)
	}

	cfg, _ := json.Marshal(map[string]any{
		"username": "admin",
		"password": "password",
		"url":      dst.srv.URL + "/artifactory/libs",
		"repoKey":  "libs",
		"enabled":  true,
		"cronExp":  "0 0 12 * * ?",
	})
	if code, _ := send(t, "PUT", src.srv.URL+"/artifactory/api/replications/libs", string(cfg)); code != 201 {
		t.Fatalf("configure replication = %d", code)
	}

	log := slog.New(slog.DiscardHandler)
	pusher := replication.NewPusher(
		src.db,
		repository.NewService(src.db, src.blobs, log),
		artifactory.ReplicationTargets(nil),
		log,
	)

	res, err := pusher.Run(t.Context(), "libs")
	if err == nil || !strings.Contains(err.Error(), "a/lost.txt") {
		t.Fatalf("run = %v, want a failure naming a/lost.txt", err)
	}

	if res.Pushed != 1 || res.Failed != 1 {
		t.Errorf("result = %+v, want 1 pushed and 1 failed", res)
	}

	if code, _ := send(t, "HEAD", dst.srv.URL+"/artifactory/libs/b/kept.txt", ""); code != 200 {
		t.Errorf("the readable file was not pushed: %d", code)
	}
}

func TestPushReplicationSyncsPropertiesWithoutRedeploying(t *testing.T) {
	t.Parallel()

	src, dst := relicInstance(t), relicInstance(t)
	for _, in := range []instance{
		src,
		dst,
	} {
		if code, _ := send(
			t,
			"PUT",
			in.srv.URL+"/artifactory/api/repositories/libs",
			`{"rclass":"local"}`,
		); code != 200 {
			t.Fatalf("create repo = %d", code)
		}
	}

	send(t, "PUT", src.srv.URL+"/artifactory/libs/a/one.txt;team=x;env=a", "one")

	cfg, _ := json.Marshal(map[string]any{
		"username":       "admin",
		"password":       "password",
		"url":            dst.srv.URL + "/artifactory/libs",
		"repoKey":        "libs",
		"enabled":        true,
		"syncProperties": true,
		"cronExp":        "0 0 12 * * ?",
	})
	if code, _ := send(t, "PUT", src.srv.URL+"/artifactory/api/replications/libs", string(cfg)); code != 201 {
		t.Fatalf("configure replication = %d", code)
	}

	log := slog.New(slog.DiscardHandler)
	pusher := replication.NewPusher(
		src.db,
		repository.NewService(src.db, src.blobs, log),
		artifactory.ReplicationTargets(nil),
		log,
	)

	run := func(want replication.Result) {
		t.Helper()

		res, err := pusher.Run(t.Context(), "libs")
		if err != nil || res != want {
			t.Fatalf("run = %+v, %v; want %+v", res, err, want)
		}
	}

	targetProperties := func() string {
		t.Helper()

		code, body := send(t, "GET", dst.srv.URL+"/artifactory/api/storage/libs/a/one.txt?properties", "")
		if code == http.StatusNotFound {
			return "none"
		}

		var props struct {
			Properties map[string][]string `json:"properties"`
		}
		if err := json.Unmarshal([]byte(body), &props); err != nil {
			t.Fatalf("target properties %d %s: %v", code, body, err)
		}

		keys := slices.Sorted(maps.Keys(props.Properties))

		return strings.Join(keys, ",")
	}

	lastModified := func() string {
		t.Helper()

		_, body := send(t, "GET", dst.srv.URL+"/artifactory/api/storage/libs/a/one.txt", "")

		var info struct {
			LastModified string `json:"lastModified"`
		}
		if err := json.Unmarshal([]byte(body), &info); err != nil {
			t.Fatalf("target file info %s: %v", body, err)
		}

		return info.LastModified
	}

	run(replication.Result{
		Pushed: 1,
	})

	deployed := lastModified()

	run(replication.Result{
		Skipped: 1,
	})

	if again := lastModified(); again != deployed {
		t.Errorf("unchanged file modified on the target at %s, then %s; want it left alone", deployed, again)
	}

	for _, step := range []struct {
		remove string
		want   string
	}{
		{
			remove: "env",
			want:   "team",
		},
		{
			remove: "team",
			want:   "none",
		},
	} {
		if code, body := send(
			t,
			"DELETE",
			src.srv.URL+"/artifactory/api/storage/libs/a/one.txt?properties="+step.remove,
			"",
		); code != http.StatusNoContent {
			t.Fatalf("delete %s on the source = %d %s", step.remove, code, body)
		}

		run(replication.Result{
			Pushed: 1,
		})

		if got := targetProperties(); got != step.want {
			t.Errorf("target properties after removing %s = %s, want %s", step.remove, got, step.want)
		}
	}

	if again := lastModified(); again != deployed {
		t.Errorf("file modified on the target at %s, then %s by a property change; want no new deploy", deployed, again)
	}
}
