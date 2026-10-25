package relicapi

import (
	"context"
	"net/url"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/delivery/webhook"
)

type webhookServer struct {
	d Dependencies
}

const defaultDeliveryLimit = 100

var webhookFormats = map[string]relicv1.WebhookFormat{
	webhook.FormatJSON:  relicv1.WebhookFormat_WEBHOOK_FORMAT_JSON,
	webhook.FormatSlack: relicv1.WebhookFormat_WEBHOOK_FORMAT_SLACK,
}

var deliveryStatuses = map[string]relicv1.WebhookDeliveryStatus{
	"pending":   relicv1.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_PENDING,
	"delivered": relicv1.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_DELIVERED,
	"failed":    relicv1.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_FAILED,
}

func webhookMessage(ctx context.Context, w webhook.Webhook) *relicv1.Webhook {
	return &relicv1.Webhook{
		Name:        w.Name,
		Url:         webhookURL(ctx, w.URL),
		Events:      w.Events,
		Format:      webhookFormats[w.Format],
		Enabled:     w.Enabled,
		Description: w.Description,
		HasSecret:   w.HasSecret(),
		Created:     timestamppb.New(w.Created),
		Updated:     timestamppb.New(w.Updated),
	}
}

func domainWebhook(m *relicv1.Webhook) (webhook.Webhook, error) {
	format := ""

	for f, v := range webhookFormats {
		if v == m.GetFormat() {
			format = f
		}
	}

	if format == "" && m.GetFormat() != relicv1.WebhookFormat_WEBHOOK_FORMAT_UNSPECIFIED {
		return webhook.Webhook{}, connect.Errorf(
			connect.CodeInvalidArgument,
			"unknown webhook format %d", m.GetFormat(),
		)
	}

	return webhook.Webhook{
		Name:        m.GetName(),
		URL:         m.GetUrl(),
		Events:      m.GetEvents(),
		Format:      format,
		Enabled:     m.GetEnabled(),
		Description: m.GetDescription(),
		Secret:      m.GetSecret(),
	}, nil
}

func webhookURL(ctx context.Context, raw string) string {
	if auth.FromContext(ctx).Admin {
		return raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	return (&url.URL{
		Scheme: u.Scheme,
		Host:   u.Host,
		Path:   "/",
	}).String()
}

func (s *webhookServer) ListWebhooks(
	ctx context.Context,
	_ *relicv1.ListWebhooksRequest,
) (*relicv1.ListWebhooksResponse, error) {
	list, err := s.d.Webhooks.List(ctx)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListWebhooksResponse{
		Webhooks: make([]*relicv1.Webhook, 0, len(list)),
	}
	for _, w := range list {
		out.Webhooks = append(out.Webhooks, webhookMessage(ctx, w))
	}

	return out, nil
}

func (s *webhookServer) GetWebhook(
	ctx context.Context,
	req *relicv1.GetWebhookRequest,
) (*relicv1.GetWebhookResponse, error) {
	w, err := s.d.Webhooks.Get(ctx, req.GetName())
	if err != nil {
		return nil, err
	}

	return &relicv1.GetWebhookResponse{
		Webhook: webhookMessage(ctx, w),
	}, nil
}

func (s *webhookServer) PutWebhook(
	ctx context.Context,
	req *relicv1.PutWebhookRequest,
) (*relicv1.PutWebhookResponse, error) {
	hook, err := domainWebhook(req.GetWebhook())
	if err != nil {
		return nil, err
	}

	w, err := s.d.Webhooks.Put(ctx, hook)
	if err != nil {
		return nil, err
	}

	return &relicv1.PutWebhookResponse{
		Webhook: webhookMessage(ctx, w),
	}, nil
}

func (s *webhookServer) DeleteWebhook(
	ctx context.Context,
	req *relicv1.DeleteWebhookRequest,
) (*relicv1.DeleteWebhookResponse, error) {
	if err := s.d.Webhooks.Delete(ctx, req.GetName()); err != nil {
		return nil, err
	}

	return &relicv1.DeleteWebhookResponse{}, nil
}

func (s *webhookServer) ListWebhookDeliveries(
	ctx context.Context,
	req *relicv1.ListWebhookDeliveriesRequest,
) (*relicv1.ListWebhookDeliveriesResponse, error) {
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = defaultDeliveryLimit
	}

	list, err := s.d.Webhooks.Deliveries(ctx, req.GetWebhook(), limit)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListWebhookDeliveriesResponse{
		Deliveries: make([]*relicv1.WebhookDelivery, 0, len(list)),
	}
	for _, d := range list {
		out.Deliveries = append(out.Deliveries, &relicv1.WebhookDelivery{
			Id:             d.ID,
			Webhook:        d.Webhook,
			Event:          d.Event,
			Payload:        string(d.Payload),
			Status:         deliveryStatuses[d.Status],
			Attempts:       int32(d.Attempts), //nolint:gosec // at most 8
			NextAttempt:    timestamppb.New(d.NextAttempt),
			ResponseStatus: int32(d.ResponseStatus), //nolint:gosec // an HTTP status
			Error:          d.Error,
			Created:        timestamppb.New(d.Created),
			Delivered:      ts(d.Delivered),
		})
	}

	return out, nil
}

func (s *webhookServer) PingWebhook(
	ctx context.Context,
	req *relicv1.PingWebhookRequest,
) (*relicv1.PingWebhookResponse, error) {
	id, err := s.d.Webhooks.Ping(ctx, req.GetName(), auth.FromContext(ctx).Name())
	if err != nil {
		return nil, err
	}

	return &relicv1.PingWebhookResponse{
		Delivery: id,
	}, nil
}
