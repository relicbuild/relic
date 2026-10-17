//go:build integration

package integration

import (
	"log/slog"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	"relic/api/adapters/artifactory"
	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/artifacts/repository"
	"relic/internal/delivery/replication"
)

func replicate(t *testing.T, in instance, direction relicv1.ReplicationDirection, peer instance) *replication.Pusher {
	t.Helper()

	reps := relicv1connect.NewReplicationServiceClient(relicHTTP("admin", "password"), in.srv.URL+"/api")
	if _, err := reps.PutReplication(t.Context(), connect.NewRequest(&relicv1.PutReplicationRequest{
		Replication: &relicv1.Replication{
			Repository: "fed",
			TargetUrl:  peer.srv.URL + "/artifactory/fed",
			Username:   "admin",
			Password:   "password",
			Enabled:    true,
			Schedule:   "@daily",
			Direction:  direction,
		},
	})); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.DiscardHandler)

	return replication.NewPusher(
		in.db,
		repository.NewService(in.db, in.blobs, log),
		artifactory.ReplicationTargets(nil),
		log,
	)
}

func content(t *testing.T, in instance, path string) (int, string) {
	t.Helper()

	return send(t, http.MethodGet, in.srv.URL+"/artifactory/fed/"+path, "")
}

// laterThanLastModified waits past the second Last-Modified headers resolve
// to, so a write that follows is newer.
func laterThanLastModified() { time.Sleep(1100 * time.Millisecond) }

func TestPullReplication(t *testing.T) {
	t.Parallel()

	src, dst := relicInstance(t), relicInstance(t)
	for _, in := range []instance{
		src,
		dst,
	} {
		send(t, http.MethodPut, in.srv.URL+"/artifactory/api/repositories/fed", `{"rclass":"local"}`)
	}

	send(t, http.MethodPut, src.srv.URL+"/artifactory/fed/a/one.txt", "one")
	send(t, http.MethodPut, src.srv.URL+"/artifactory/fed/b/two.txt", "two")

	puller := replicate(t, dst, relicv1.ReplicationDirection_REPLICATION_DIRECTION_PULL, src)

	res, err := puller.Run(t.Context(), "fed")
	if err != nil || res.Pulled != 2 {
		t.Fatalf("first pull = %+v, %v; want 2 pulled", res, err)
	}

	if code, body := content(t, dst, "a/one.txt"); code != http.StatusOK || body != "one" {
		t.Errorf("pulled file = %d %q", code, body)
	}

	if code, body := send(
		t,
		http.MethodGet,
		src.srv.URL+"/artifactory/api/replications/fed",
		"",
	); code != http.StatusNotFound {
		t.Errorf("the source's replication = %d %s; want 404, a pull configures only the puller", code, body)
	}

	if res, err := puller.Run(t.Context(), "fed"); err != nil || res.Pulled != 0 || res.Skipped != 2 {
		t.Errorf("second pull = %+v, %v; want 2 skipped", res, err)
	}
}

func TestFederatedReplication(t *testing.T) {
	t.Parallel()

	a, b := relicInstance(t), relicInstance(t)
	for _, in := range []instance{
		a,
		b,
	} {
		send(t, http.MethodPut, in.srv.URL+"/artifactory/api/repositories/fed", `{"rclass":"local"}`)
	}

	federated := relicv1.ReplicationDirection_REPLICATION_DIRECTION_FEDERATED
	fromA, fromB := replicate(t, a, federated, b), replicate(t, b, federated, a)

	run := func(p *replication.Pusher, name string) replication.Result {
		t.Helper()

		res, err := p.Run(t.Context(), "fed")
		if err != nil {
			t.Fatalf("%s run: %v", name, err)
		}

		return res
	}

	send(t, http.MethodPut, a.srv.URL+"/artifactory/fed/x.txt", "x from a")
	run(fromA, "a")

	if code, body := content(t, b, "x.txt"); code != http.StatusOK || body != "x from a" {
		t.Fatalf("b after a's run = %d %q", code, body)
	}

	laterThanLastModified()
	send(t, http.MethodPut, b.srv.URL+"/artifactory/fed/x.txt", "x from b, later")
	run(fromA, "a")

	if _, body := content(t, a, "x.txt"); body != "x from b, later" {
		t.Errorf("a after b's later write = %q; want the later write to win", body)
	}

	laterThanLastModified()
	send(t, http.MethodDelete, a.srv.URL+"/artifactory/fed/x.txt", "")
	run(fromA, "a")

	if code, _ := content(t, b, "x.txt"); code != http.StatusNotFound {
		t.Errorf("b after a's delete = %d; want the delete to win over the older write", code)
	}

	run(fromB, "b")

	if code, _ := content(t, a, "x.txt"); code != http.StatusNotFound {
		t.Errorf("a after b's run = %d; a deleted file came back", code)
	}

	send(t, http.MethodPut, b.srv.URL+"/artifactory/fed/y.txt", "y one")
	run(fromB, "b")
	laterThanLastModified()
	send(t, http.MethodDelete, a.srv.URL+"/artifactory/fed/y.txt", "")
	laterThanLastModified()
	send(t, http.MethodPut, b.srv.URL+"/artifactory/fed/y.txt", "y two, after the delete")
	run(fromA, "a")

	if _, body := content(t, a, "y.txt"); body != "y two, after the delete" {
		t.Errorf("a's y.txt = %q; want a write newer than the delete to win", body)
	}
}
