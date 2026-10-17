//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func TestWebhooks(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()

	hooks := relicv1connect.NewWebhookServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")

	put, err := hooks.PutWebhook(ctx, connect.NewRequest(&relicv1.PutWebhookRequest{
		Webhook: &relicv1.Webhook{
			Name: "deploys",
			Url:  "https://hooks.example.com/relic",
			Events: []string{
				"artifact.deployed",
				"artifact.deleted",
			},
			Enabled: true,
			Secret:  "hook-secret",
		},
	}))
	if err != nil {
		t.Fatal(err)
	}

	w := put.Msg.GetWebhook()
	if w.GetSecret() != "" || !w.GetHasSecret() || w.GetFormat() != relicv1.WebhookFormat_WEBHOOK_FORMAT_JSON {
		t.Errorf("put = %v, want the secret hidden and the JSON format", w)
	}

	if _, err := hooks.PutWebhook(ctx, connect.NewRequest(&relicv1.PutWebhookRequest{
		Webhook: &relicv1.Webhook{
			Name: "deploys",
			Url:  "https://hooks.example.com/relic",
			Events: []string{
				"artifact.deployed",
			},
			Enabled: true,
		},
	})); err != nil {
		t.Fatal(err)
	}

	got, err := hooks.GetWebhook(ctx, connect.NewRequest(&relicv1.GetWebhookRequest{
		Name: "deploys",
	}))
	if err != nil || !got.Msg.GetWebhook().GetHasSecret() || len(got.Msg.GetWebhook().GetEvents()) != 1 {
		t.Errorf("an update without a secret = %v, %v; want the secret kept", got, err)
	}

	if _, err := hooks.PutWebhook(ctx, connect.NewRequest(&relicv1.PutWebhookRequest{
		Webhook: &relicv1.Webhook{
			Name: "nope",
			Url:  "https://hooks.example.com/relic",
			Events: []string{
				"artifact.renamed",
			},
		},
	})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("an unknown event type: %v", err)
	}

	if resp, body := do(t, srv, call{
		method: http.MethodPut,
		path:   "/artifactory/api/repositories/hooked",
		body:   `{"rclass":"local","packageType":"generic"}`,
	}); resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("create repository = %d %s", resp.StatusCode, body)
	}

	if resp, body := do(t, srv, call{
		method: http.MethodPut,
		path:   "/artifactory/hooked/a/b.txt",
		body:   "hello",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("deploy = %d %s", resp.StatusCode, body)
	}

	ping, err := hooks.PingWebhook(ctx, connect.NewRequest(&relicv1.PingWebhookRequest{
		Name: "deploys",
	}))
	if err != nil || ping.Msg.GetDelivery() == "" {
		t.Fatalf("ping = %v, %v", ping, err)
	}

	deliveries, err := hooks.ListWebhookDeliveries(ctx, connect.NewRequest(&relicv1.ListWebhookDeliveriesRequest{
		Webhook: "deploys",
	}))
	if err != nil {
		t.Fatal(err)
	}

	d := deliveries.Msg.GetDeliveries()
	if len(d) != 2 || d[0].GetEvent() != "webhook.ping" || d[1].GetEvent() != "artifact.deployed" ||
		d[1].GetStatus() != relicv1.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_PENDING {
		t.Fatalf("deliveries = %v", d)
	}

	var event struct {
		Subject string `json:"subject"`
		Actor   string `json:"actor"`
		Data    struct {
			SHA256 string `json:"sha256"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(d[1].GetPayload()), &event); err != nil || event.Subject != "hooked/a/b.txt" ||
		event.Actor != "admin" || len(event.Data.SHA256) != 64 {
		t.Errorf("the deploy's event = %s, %v", d[1].GetPayload(), err)
	}

	if _, err := relicv1connect.NewWebhookServiceClient(relicHTTP("", ""), srv.URL+"/api").ListWebhooks(
		ctx,
		connect.NewRequest(&relicv1.ListWebhooksRequest{}),
	); code(err) != connect.CodeUnauthenticated {
		t.Errorf("anonymous list: %v", err)
	}

	if _, err := hooks.DeleteWebhook(ctx, connect.NewRequest(&relicv1.DeleteWebhookRequest{
		Name: "deploys",
	})); err != nil {
		t.Fatal(err)
	}

	if _, err := hooks.GetWebhook(ctx, connect.NewRequest(&relicv1.GetWebhookRequest{
		Name: "deploys",
	})); code(err) != connect.CodeNotFound {
		t.Errorf("get after delete: %v", err)
	}
}
