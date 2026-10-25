//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func TestWebhooks(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()

	hooks := relicv1connect.NewWebhookServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	put, err := hooks.PutWebhook(ctx, &relicv1.PutWebhookRequest{
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
	})
	if err != nil {
		t.Fatal(err)
	}

	w := put.GetWebhook()
	if w.GetSecret() != "" || !w.GetHasSecret() || w.GetFormat() != relicv1.WebhookFormat_WEBHOOK_FORMAT_JSON {
		t.Errorf("put = %v, want the secret hidden and the JSON format", w)
	}

	if _, err := hooks.PutWebhook(ctx, &relicv1.PutWebhookRequest{
		Webhook: &relicv1.Webhook{
			Name: "deploys",
			Url:  "https://hooks.example.com/relic",
			Events: []string{
				"artifact.deployed",
			},
			Enabled: true,
		},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := hooks.GetWebhook(ctx, &relicv1.GetWebhookRequest{
		Name: "deploys",
	})
	if err != nil || !got.GetWebhook().GetHasSecret() || len(got.GetWebhook().GetEvents()) != 1 {
		t.Errorf("an update without a secret = %v, %v; want the secret kept", got, err)
	}

	if _, err := hooks.PutWebhook(ctx, &relicv1.PutWebhookRequest{
		Webhook: &relicv1.Webhook{
			Name: "nope",
			Url:  "https://hooks.example.com/relic",
			Events: []string{
				"artifact.renamed",
			},
		},
	}); code(err) != connect.CodeInvalidArgument {
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

	ping, err := hooks.PingWebhook(ctx, &relicv1.PingWebhookRequest{
		Name: "deploys",
	})
	if err != nil || ping.GetDelivery() == "" {
		t.Fatalf("ping = %v, %v", ping, err)
	}

	deliveries, err := hooks.ListWebhookDeliveries(ctx, &relicv1.ListWebhookDeliveriesRequest{
		Webhook: "deploys",
	})
	if err != nil {
		t.Fatal(err)
	}

	d := deliveries.GetDeliveries()
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

	//nolint:gosec // G101: a made-up URL whose user info is refused
	if _, err := hooks.PutWebhook(ctx, &relicv1.PutWebhookRequest{
		Webhook: &relicv1.Webhook{
			Name: "userinfo",
			Url:  "https://u:pw@hooks.example.com/x",
			Events: []string{
				"artifact.deployed",
			},
		},
	}); code(err) != connect.CodeInvalidArgument {
		t.Errorf("a URL with a user and password: %v, want invalid argument", err)
	}

	if _, err := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL)).
		PutUser(ctx, &relicv1.PutUserRequest{
			User: &relicv1.User{
				Name:     "watcher",
				Email:    "watcher@example.com",
				Password: "Watcher-pass-1",
				Viewer:   true,
			},
		}); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		user, password, want string
	}{
		{
			user:     "watcher",
			password: "Watcher-pass-1",
			want:     "https://hooks.example.com/",
		},
		{
			user:     "admin",
			password: "password",
			want:     "https://hooks.example.com/relic",
		},
	} {
		got, err := relicv1connect.NewWebhookServiceClient(apiClient(relicHTTP(tt.user, tt.password), srv.URL)).
			GetWebhook(ctx, &relicv1.GetWebhookRequest{
				Name: "deploys",
			})
		if err != nil || got.GetWebhook().GetUrl() != tt.want {
			t.Errorf("%s reads the URL %q, %v; want %q", tt.user, got.GetWebhook().GetUrl(), err, tt.want)
		}
	}

	if _, err := relicv1connect.NewWebhookServiceClient(apiClient(relicHTTP("", ""), srv.URL)).ListWebhooks(
		ctx,
		&relicv1.ListWebhooksRequest{},
	); code(err) != connect.CodeUnauthenticated {
		t.Errorf("anonymous list: %v", err)
	}

	if _, err := hooks.DeleteWebhook(ctx, &relicv1.DeleteWebhookRequest{
		Name: "deploys",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := hooks.GetWebhook(ctx, &relicv1.GetWebhookRequest{
		Name: "deploys",
	}); code(err) != connect.CodeNotFound {
		t.Errorf("get after delete: %v", err)
	}
}
