// Package webhook delivers Relic's events to subscribed URLs: queued in the
// database, sent by the worker, signed and retried with backoff.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"uuid"

	"relic/internal/platform/database"
	"relic/internal/platform/events"
	"relic/internal/platform/sealing"
	"relic/internal/platform/telemetry"
)

// Errors.
var (
	ErrNotFound = errors.New("webhook not found")
	ErrInvalid  = errors.New("invalid webhook")
)

// Formats of the request body.
const (
	FormatJSON  = "json"
	FormatSlack = "slack"
)

// AllEvents subscribes a webhook to every event type.
const AllEvents = "*"

const (
	cacheTTL        = 10 * time.Second
	claimBatch      = 50
	lease           = 2 * time.Minute
	sendTimeout     = 10 * time.Second
	maxAttempts     = 8
	firstBackoff    = 30 * time.Second
	maxBackoff      = time.Hour
	retention       = 30 * 24 * time.Hour
	purgeInterval   = time.Hour
	maxErrorLength  = 500
	maxResponseRead = 4 << 10
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Webhook subscribes a URL to event types.
type Webhook struct {
	Name        string   `json:"-"`
	URL         string   `json:"url"`
	Events      []string `json:"events"`
	Format      string   `json:"format"`
	Enabled     bool     `json:"enabled"`
	Description string   `json:"description,omitempty"`
	// Secret signs request bodies (X-Relic-Signature); it is sealed when
	// stored and never returned.
	Secret  string    `json:"secret,omitempty"`
	Created time.Time `json:"-"`
	Updated time.Time `json:"-"`
}

// HasSecret reports whether deliveries are signed.
func (w Webhook) HasSecret() bool { return w.Secret != "" }

func (w Webhook) subscribes(eventType string) bool {
	return w.Enabled && (slices.Contains(w.Events, AllEvents) || slices.Contains(w.Events, eventType))
}

// Delivery is one event sent, or to be sent, to a webhook.
type Delivery struct {
	ID             string
	Webhook        string
	Event          string
	Payload        json.RawMessage
	Status         string
	Attempts       int
	NextAttempt    time.Time
	ResponseStatus int
	Error          string
	Created        time.Time
	Delivered      *time.Time
}

// Service stores webhooks and delivers events to them.
type Service struct {
	db     *database.DB
	box    *sealing.Box
	client *http.Client
	log    *slog.Logger
	now    func() time.Time
	// nudge asks the worker to dispatch soon; nil leaves it to its schedule.
	nudge func(context.Context)

	mu        sync.Mutex
	cached    []Webhook
	loaded    time.Time
	lastPurge time.Time
}

// New returns a Service; box seals secrets.
func New(db *database.DB, box *sealing.Box, log *slog.Logger) *Service {
	return &Service{
		db:  db,
		box: box,
		client: &http.Client{
			Timeout: sendTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		log: log,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

// SetNudge makes queued events ask the worker to dispatch at once.
func (s *Service) SetNudge(fn func(context.Context)) { s.nudge = fn }

// SetHTTPClient replaces the client deliveries are sent with.
func (s *Service) SetHTTPClient(c *http.Client) { s.client = c }

func secretPlace(name string) string { return "webhooks/" + name + "/secret" }

func (s *Service) domainWebhook(r database.Webhook) (Webhook, error) {
	var w Webhook
	if err := json.Unmarshal(r.Config, &w); err != nil {
		return Webhook{}, fmt.Errorf("decode webhook %s: %w", r.Name, err)
	}

	w.Name, w.Created, w.Updated = r.Name, r.Created, r.Updated

	return w, nil
}

// List returns the webhooks, their secrets left sealed.
func (s *Service) List(ctx context.Context) ([]Webhook, error) {
	rows, err := s.db.ListWebhooks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}

	out := make([]Webhook, 0, len(rows))

	for _, r := range rows {
		w, err := s.domainWebhook(r)
		if err != nil {
			return nil, err
		}

		out = append(out, w)
	}

	return out, nil
}

// Get returns one webhook, its secret left sealed.
func (s *Service) Get(ctx context.Context, name string) (Webhook, error) {
	r, err := s.db.GetWebhook(ctx, name)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Webhook{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}

	if err != nil {
		return Webhook{}, fmt.Errorf("get webhook: %w", err)
	}

	return s.domainWebhook(r)
}

// SealStored seals the secrets stored before an encryption key was set.
func (s *Service) SealStored(ctx context.Context) error {
	if !s.box.Enabled() {
		return nil
	}

	stored, err := s.List(ctx)
	if err != nil {
		return err
	}

	var errs []error

	for _, w := range stored {
		if w.Secret == "" || sealing.SealedString(w.Secret) {
			continue
		}

		if _, err := s.Put(ctx, w); err != nil {
			errs = append(errs, fmt.Errorf("seal the secret of webhook %s: %w", w.Name, err))
		}
	}

	return errors.Join(errs...)
}

// Put creates or replaces a webhook; an empty secret keeps the stored one.
func (s *Service) Put(ctx context.Context, w Webhook) (Webhook, error) {
	if err := validate(&w); err != nil {
		return Webhook{}, err
	}

	if w.Secret == "" {
		old, err := s.Get(ctx, w.Name)

		switch {
		case err == nil:
			w.Secret = old.Secret
		case !errors.Is(err, ErrNotFound):
			return Webhook{}, fmt.Errorf("read the stored secret: %w", err)
		}
	} else {
		sealed, err := s.box.SealString(w.Secret, secretPlace(w.Name))
		if err != nil {
			return Webhook{}, fmt.Errorf("seal webhook secret: %w", err)
		}

		w.Secret = sealed
	}

	doc, err := json.Marshal(w) //nolint:gosec // the secret is sealed above when a sealing key is set
	if err != nil {
		return Webhook{}, err
	}

	if err := s.db.UpsertWebhook(ctx, database.UpsertWebhookParams{
		Name:   w.Name,
		Config: doc,
		Now:    s.now(),
	}); err != nil {
		return Webhook{}, fmt.Errorf("put webhook: %w", err)
	}

	s.invalidate()

	return s.Get(ctx, w.Name)
}

func validate(w *Webhook) error {
	if !namePattern.MatchString(w.Name) {
		return fmt.Errorf("%w: name %q", ErrInvalid, w.Name)
	}

	u, err := url.Parse(w.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w: url must be an absolute http or https URL", ErrInvalid)
	}

	if w.Format == "" {
		w.Format = FormatJSON
	}

	if w.Format != FormatJSON && w.Format != FormatSlack {
		return fmt.Errorf("%w: format must be json or slack", ErrInvalid)
	}

	if len(w.Events) == 0 {
		return fmt.Errorf("%w: subscribe to at least one event type", ErrInvalid)
	}

	for _, e := range w.Events {
		if e != AllEvents && !slices.Contains(events.Types, e) {
			return fmt.Errorf("%w: unknown event type %q; known: %s", ErrInvalid, e, strings.Join(events.Types, ", "))
		}
	}

	seen := map[string]bool{}
	w.Events = slices.DeleteFunc(w.Events, func(e string) bool {
		dup := seen[e]
		seen[e] = true

		return dup
	})

	return nil
}

// Delete removes a webhook and its deliveries.
func (s *Service) Delete(ctx context.Context, name string) error {
	n, err := s.db.DeleteWebhook(ctx, name)
	if err != nil {
		return fmt.Errorf("delete webhook: %w", err)
	}

	if n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}

	s.invalidate()

	return nil
}

// Deliveries lists the latest deliveries, of one webhook or of all for "".
func (s *Service) Deliveries(ctx context.Context, name string, limit int) ([]Delivery, error) {
	rows, err := s.db.ListWebhookDeliveries(ctx, database.ListWebhookDeliveriesParams{
		Webhook: name,
		MaxRows: int32(min(max(limit, 1), 1000)), //nolint:gosec // bounded
	})
	if err != nil {
		return nil, fmt.Errorf("list deliveries: %w", err)
	}

	out := make([]Delivery, 0, len(rows))
	for _, r := range rows {
		out = append(out, Delivery{
			ID:             r.ID.String(),
			Webhook:        r.Webhook,
			Event:          r.Event,
			Payload:        r.Payload,
			Status:         r.Status,
			Attempts:       int(r.Attempts),
			NextAttempt:    r.NextAttempt,
			ResponseStatus: int(r.ResponseStatus),
			Error:          r.Error,
			Created:        r.Created,
			Delivered:      r.Delivered,
		})
	}

	return out, nil
}

// Ping queues a webhook.ping event for one webhook, whatever it subscribes
// to.
func (s *Service) Ping(ctx context.Context, name, user string) (string, error) {
	w, err := s.Get(ctx, name)
	if err != nil {
		return "", err
	}

	id, err := s.enqueue(ctx, w, events.Event{
		Type:    events.WebhookPing,
		Subject: w.Name,
		Actor:   user,
		Time:    s.now(),
	})
	if err != nil {
		return "", err
	}

	s.poke(ctx)

	return id, nil
}

func (s *Service) invalidate() {
	s.mu.Lock()
	s.loaded = time.Time{}
	s.mu.Unlock()
}

func (s *Service) subscribers(ctx context.Context) ([]Webhook, error) {
	s.mu.Lock()
	if s.now().Sub(s.loaded) < cacheTTL {
		list := s.cached
		s.mu.Unlock()

		return list, nil
	}
	s.mu.Unlock()

	list, err := s.List(ctx)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.cached, s.loaded = list, s.now()
	s.mu.Unlock()

	return list, nil
}

// Publish queues e for every enabled webhook subscribed to its type. It
// implements events.Publisher.
func (s *Service) Publish(ctx context.Context, e events.Event) {
	list, err := s.subscribers(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "load webhooks", "event", e.Type, "error", err)
		return
	}

	queued := false

	for _, w := range list {
		if !w.subscribes(e.Type) {
			continue
		}

		if _, err := s.enqueue(ctx, w, e); err != nil {
			s.log.WarnContext(ctx, "queue webhook delivery", "webhook", w.Name, "event", e.Type, "error", err)
			continue
		}

		queued = true
	}

	if queued {
		s.poke(ctx)
	}
}

func (s *Service) poke(ctx context.Context) {
	if s.nudge != nil {
		s.nudge(ctx)
	}
}

type payload struct {
	ID string `json:"id"`
	events.Event
}

func (s *Service) enqueue(ctx context.Context, w Webhook, e events.Event) (string, error) {
	id := uuid.NewV7()

	body, err := json.Marshal(payload{
		ID:    id.String(),
		Event: e,
	})
	if err != nil {
		return "", fmt.Errorf("encode event: %w", err)
	}

	if err := s.db.InsertWebhookDelivery(ctx, database.InsertWebhookDeliveryParams{
		ID:      id,
		Webhook: w.Name,
		Event:   e.Type,
		Payload: body,
		Now:     s.now(),
	}); err != nil {
		return "", fmt.Errorf("queue delivery: %w", err)
	}

	return id.String(), nil
}

// Dispatch sends the deliveries that are due, and reports how many it
// tried.
func (s *Service) Dispatch(ctx context.Context) (int, error) {
	s.purge(ctx)

	now := s.now()

	rows, err := s.db.ClaimWebhookDeliveries(ctx, database.ClaimWebhookDeliveriesParams{
		LeaseUntil: now.Add(lease),
		Now:        now,
		MaxRows:    claimBatch,
	})
	if err != nil {
		return 0, fmt.Errorf("claim deliveries: %w", err)
	}

	hooks := map[string]*Webhook{}
	unreadable := map[string]bool{}

	for _, r := range rows {
		if unreadable[r.Webhook] {
			continue
		}

		w, ok := hooks[r.Webhook]
		if !ok {
			got, err := s.Get(ctx, r.Webhook)

			switch {
			case err == nil:
				w = &got
			case !errors.Is(err, ErrNotFound):
				s.log.WarnContext(ctx, "read webhook for delivery", "webhook", r.Webhook, "error", err)
				unreadable[r.Webhook] = true

				continue
			}

			hooks[r.Webhook] = w
		}

		s.deliver(ctx, w, r)
	}

	return len(rows), nil
}

func (s *Service) purge(ctx context.Context) {
	s.mu.Lock()

	due := s.now().Sub(s.lastPurge) >= purgeInterval
	if due {
		s.lastPurge = s.now()
	}
	s.mu.Unlock()

	if !due {
		return
	}

	if _, err := s.db.PurgeWebhookDeliveries(ctx, s.now().Add(-retention)); err != nil {
		s.log.WarnContext(ctx, "purge webhook deliveries", "error", err)
	}
}

func backoff(attempts int) time.Duration {
	d := firstBackoff << min(attempts-1, 16)

	return min(d, maxBackoff)
}

func (s *Service) deliver(ctx context.Context, w *Webhook, r database.WebhookDelivery) {
	ctx, span := telemetry.Tracer().Start(ctx, "webhook.Deliver")

	attempts := int(r.Attempts) + 1
	start := time.Now()

	code, sendErr := s.send(ctx, w, r)

	finish := database.FinishWebhookDeliveryParams{
		ID:             r.ID,
		Attempts:       int32(min(attempts, maxAttempts)), //nolint:gosec // bounded
		ResponseStatus: int32(code),                       //nolint:gosec // an HTTP status
		NextAttempt:    s.now(),
	}

	result := "delivered"

	switch {
	case sendErr == nil:
		finish.Status = "delivered"
		delivered := s.now()
		finish.Delivered = &delivered
	case w == nil || !w.Enabled || attempts >= maxAttempts:
		result = "failed"
		finish.Status = "failed"
		finish.Error = truncate(sendErr.Error())
	default:
		result = "retry"
		finish.Status = "pending"
		finish.Error = truncate(sendErr.Error())
		finish.NextAttempt = s.now().Add(backoff(attempts))
	}

	telemetry.WebhookDelivered(ctx, result, time.Since(start))
	telemetry.End(span, sendErr)

	if err := s.db.FinishWebhookDelivery(context.WithoutCancel(ctx), finish); err != nil {
		s.log.WarnContext(ctx, "record webhook delivery", "delivery", r.ID, "error", err)
	}
}

func truncate(s string) string {
	if len(s) > maxErrorLength {
		return strings.ToValidUTF8(s[:maxErrorLength], "")
	}

	return s
}

func (s *Service) send(ctx context.Context, w *Webhook, r database.WebhookDelivery) (int, error) {
	if w == nil {
		return 0, errors.New("the webhook no longer exists")
	}

	if !w.Enabled {
		return 0, errors.New("the webhook is disabled")
	}

	body := r.Payload

	if w.Format == FormatSlack {
		var err error
		if body, err = slackBody(r.Payload); err != nil {
			return 0, err
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Relic-Webhook")
	req.Header.Set("X-Relic-Event", r.Event)
	req.Header.Set("X-Relic-Delivery", r.ID.String())

	if w.Secret != "" {
		secret, err := s.box.OpenString(w.Secret, secretPlace(w.Name))
		if err != nil {
			return 0, fmt.Errorf("open webhook secret: %w", err)
		}

		req.Header.Set("X-Relic-Signature", Sign([]byte(secret), body))
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseRead))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("the receiver answered %d", resp.StatusCode)
	}

	return resp.StatusCode, nil
}

// Sign is the X-Relic-Signature of body: sha256= and the hex HMAC-SHA256
// of body under secret.
func Sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)

	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func slackBody(stored []byte) ([]byte, error) {
	var p payload
	if err := json.Unmarshal(stored, &p); err != nil {
		return nil, fmt.Errorf("decode event: %w", err)
	}

	text := fmt.Sprintf("*%s* `%s`", p.Type, p.Subject)
	if p.Actor != "" {
		text += " by " + p.Actor
	}

	for _, key := range []string{
		"environment",
		"status",
		"reason",
	} {
		if v, ok := p.Data[key].(string); ok && v != "" {
			text += "\n" + key + ": " + v
		}
	}

	return json.Marshal(map[string]string{
		"text": text,
	})
}
