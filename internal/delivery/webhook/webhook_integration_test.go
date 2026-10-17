//go:build integration

package webhook_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
	"uuid"

	"relic/internal/delivery/webhook"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/events"
	"relic/internal/platform/sealing"
)

type received struct {
	body      []byte
	signature string
	event     string
}

type receiver struct {
	mu     sync.Mutex
	got    []received
	status int
}

func (rc *receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	rc.mu.Lock()
	defer rc.mu.Unlock()

	rc.got = append(rc.got, received{
		body:      body,
		signature: r.Header.Get("X-Relic-Signature"),
		event:     r.Header.Get("X-Relic-Event"),
	})
	w.WriteHeader(rc.status)
}

func (rc *receiver) setStatus(code int) {
	rc.mu.Lock()
	rc.status = code
	rc.mu.Unlock()
}

func (rc *receiver) received() []received {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	return append([]received(nil), rc.got...)
}

func newService(t *testing.T) (*webhook.Service, *database.DB) {
	t.Helper()

	db := databasetest.NewDB(t)

	box, err := sealing.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}

	return webhook.New(db, box, slog.New(slog.DiscardHandler)), db
}

func TestDeliveries(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	svc, db := newService(t)

	rc := &receiver{
		status: http.StatusOK,
	}
	srv := httptest.NewServer(rc)
	t.Cleanup(srv.Close)

	if _, err := svc.Put(ctx, webhook.Webhook{
		Name: "bad",
		URL:  "ftp://example.com",
		Events: []string{
			events.ArtifactDeployed,
		},
	}); err == nil {
		t.Error("a non-HTTP URL was accepted")
	}

	if _, err := svc.Put(ctx, webhook.Webhook{
		Name: "bad",
		URL:  srv.URL,
		Events: []string{
			"artifact.renamed",
		},
	}); err == nil {
		t.Error("an unknown event type was accepted")
	}

	broken := `{"url": 5, "secret": "sealed"}`
	if err := db.UpsertWebhook(ctx, database.UpsertWebhookParams{
		Name:   "broken",
		Config: []byte(broken),
		Now:    time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Put(ctx, webhook.Webhook{
		Name: "broken",
		URL:  srv.URL,
		Events: []string{
			events.ArtifactDeployed,
		},
	}); err == nil {
		t.Error("a webhook whose stored secret could not be read was replaced")
	}

	row, err := db.GetWebhook(ctx, "broken")
	if err != nil {
		t.Fatal(err)
	}

	var stored map[string]any
	if err := json.Unmarshal(row.Config, &stored); err != nil || stored["secret"] != "sealed" {
		t.Errorf("stored webhook = %s %v, want its secret kept", row.Config, err)
	}

	if _, err := db.DeleteWebhook(ctx, "broken"); err != nil {
		t.Fatal(err)
	}

	ci, err := svc.Put(ctx, webhook.Webhook{
		Name: "ci",
		URL:  srv.URL + "/hook",
		Events: []string{
			events.ArtifactDeployed,
		},
		Enabled: true,
		Secret:  "s3cret",
	})
	if err != nil || !ci.HasSecret() || ci.Secret == "s3cret" || ci.Format != webhook.FormatJSON {
		t.Fatalf("put = %+v, %v (the secret must be stored sealed)", ci, err)
	}

	if _, err := svc.Put(ctx, webhook.Webhook{
		Name: "chat",
		URL:  srv.URL + "/slack",
		Events: []string{
			webhook.AllEvents,
		},
		Format:  webhook.FormatSlack,
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Put(ctx, webhook.Webhook{
		Name: "off",
		URL:  srv.URL + "/off",
		Events: []string{
			webhook.AllEvents,
		},
		Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}

	svc.Publish(ctx, events.Event{
		Type:    events.ArtifactDeployed,
		Subject: "libs/a.jar",
		Actor:   "bob",
	})
	svc.Publish(ctx, events.Event{
		Type:    events.ReleasePromoted,
		Subject: "app/1.0",
		Data: map[string]any{
			"environment": "PROD",
		},
	})

	if n, err := svc.Dispatch(ctx); err != nil || n != 3 {
		t.Fatalf("dispatch = %d, %v; want 3 deliveries", n, err)
	}

	got := rc.received()
	if len(got) != 3 {
		t.Fatalf("received %d requests, want 3", len(got))
	}

	signed := 0

	for _, r := range got {
		if r.signature == "" {
			continue
		}

		signed++

		if r.signature != webhook.Sign([]byte("s3cret"), r.body) || r.event != events.ArtifactDeployed {
			t.Errorf("signature %q does not match the body %s", r.signature, r.body)
		}

		var p struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Subject string `json:"subject"`
			Actor   string `json:"actor"`
		}
		if err := json.Unmarshal(
			r.body,
			&p,
		); err != nil || p.ID == "" || p.Subject != "libs/a.jar" ||
			p.Actor != "bob" {
			t.Errorf("payload = %s", r.body)
		}
	}

	if signed != 1 {
		t.Errorf("%d signed requests, want 1", signed)
	}

	slack := 0

	for _, r := range got {
		var m map[string]string
		if json.Unmarshal(r.body, &m) == nil && m["text"] == "*release.promoted* `app/1.0`\nenvironment: PROD" {
			slack++
		}
	}

	if slack != 1 {
		t.Errorf("the Slack message for the promotion is missing: %+v", got)
	}

	rc.setStatus(http.StatusServiceUnavailable)

	if _, err := svc.Ping(ctx, "ci", "admin"); err != nil {
		t.Fatal(err)
	}

	if n, err := svc.Dispatch(ctx); err != nil || n != 1 {
		t.Fatalf("dispatch of the ping = %d, %v", n, err)
	}

	list, err := svc.Deliveries(ctx, "ci", 10)
	if err != nil || len(list) != 2 {
		t.Fatalf("deliveries = %+v, %v", list, err)
	}

	ping := list[0]
	if ping.Event != events.WebhookPing || ping.Status != "pending" || ping.Attempts != 1 ||
		ping.ResponseStatus != http.StatusServiceUnavailable || ping.Error == "" {
		t.Errorf("a failed delivery = %+v, want pending for a retry", ping)
	}

	if n, err := svc.Dispatch(ctx); err != nil || n != 0 {
		t.Errorf("a retry was sent before its backoff: %d, %v", n, err)
	}

	if list[1].Status != "delivered" || list[1].Delivered == nil {
		t.Errorf("the first delivery = %+v", list[1])
	}

	if err := db.FinishWebhookDelivery(ctx, database.FinishWebhookDeliveryParams{
		ID:          uuid.MustParse(ping.ID),
		Status:      "pending",
		Attempts:    7,
		NextAttempt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	if n, err := svc.Dispatch(ctx); err != nil || n != 1 {
		t.Fatalf("the last attempt = %d, %v", n, err)
	}

	if list, _ := svc.Deliveries(ctx, "ci", 1); list[0].Status != "failed" || list[0].Attempts != 8 {
		t.Errorf("after the last attempt = %+v, want failed", list[0])
	}

	if err := db.UpsertWebhook(ctx, database.UpsertWebhookParams{
		Name:   "unreadable",
		Config: []byte(`{"events": 5}`),
		Now:    time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.InsertWebhookDelivery(ctx, database.InsertWebhookDeliveryParams{
		ID:      uuid.NewV7(),
		Webhook: "unreadable",
		Event:   events.WebhookPing,
		Payload: []byte(`{}`),
		Now:     time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}

	if list, _ := svc.Deliveries(ctx, "unreadable", 1); len(list) != 1 || list[0].Status != "pending" ||
		list[0].Attempts != 0 {
		t.Errorf("a delivery whose webhook could not be read = %+v, want pending for the next dispatch", list)
	}
}
