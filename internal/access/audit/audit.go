// Package audit records changes to administration settings, sign-ins, tokens
// and blocked downloads, and answers who did what.
package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"time"
	"unicode/utf8"
	"uuid"

	"relic/internal/platform/database"
	"relic/internal/platform/telemetry"
)

// Outcome is how an audited request ended.
type Outcome string

// Outcomes.
const (
	Success Outcome = "success"
	Denied  Outcome = "denied"
	Failure Outcome = "failure"
)

// HTTPOutcome is the outcome of a response with status.
func HTTPOutcome(status int) Outcome {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return Denied
	case status >= http.StatusBadRequest:
		return Failure
	}

	return Success
}

// Event is one audited action.
type Event struct {
	ID      string
	Time    time.Time
	Actor   string
	Action  string
	Target  string
	Outcome Outcome
	Client  string
	Detail  string
}

// Filter selects events; zero fields select everything.
type Filter struct {
	Actor   string
	Action  string
	Target  string
	Outcome Outcome
	Since   time.Time
	Until   time.Time
	// Page counts from 1.
	Page     int
	PageSize int
}

// ErrInvalid reports a filter Relic cannot apply.
var ErrInvalid = errors.New("invalid audit filter")

const (
	defaultPageSize = 100
	maxPageSize     = 1000
)

// Service records and lists audit events.
type Service struct {
	db  *database.DB
	log *slog.Logger
	now func() time.Time
}

// New returns a Service storing events in db and writing each to log.
func New(db *database.DB, log *slog.Logger) *Service {
	return &Service{
		db:  db,
		log: log,
		now: time.Now,
	}
}

// Recorder records audit events.
type Recorder interface {
	Record(ctx context.Context, e Event)
}

type clientKey struct{}

// Middleware makes the client's address available to events recorded while
// serving a request.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addr := r.RemoteAddr
		if ap, err := netip.ParseAddrPort(addr); err == nil {
			addr = ap.Addr().Unmap().String()
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientKey{}, addr)))
	})
}

// Record stores e. A failure is logged and counted, never returned: the
// action it describes has already happened.
func (s *Service) Record(ctx context.Context, e Event) {
	e.ID = uuid.NewV7().String()
	e.Time = s.now().UTC()

	if e.Client == "" {
		e.Client, _ = ctx.Value(clientKey{}).(string)
	}

	e = e.bounded()

	err := s.db.InsertAuditEvent(context.WithoutCancel(ctx), database.InsertAuditEventParams{
		ID:      uuid.MustParse(e.ID),
		Time:    e.Time,
		Actor:   e.Actor,
		Action:  e.Action,
		Target:  e.Target,
		Outcome: string(e.Outcome),
		Client:  e.Client,
		Detail:  e.Detail,
	})
	telemetry.AuditEvent(string(e.Outcome), err)

	attrs := []any{
		"actor",
		e.Actor,
		"action",
		e.Action,
		"target",
		e.Target,
		"outcome",
		string(e.Outcome),
		"client",
		e.Client,
	}
	if err != nil {
		s.log.ErrorContext(ctx, "audit event not stored", append(attrs, "error", err)...)
		return
	}

	s.log.InfoContext(ctx, "relic.audit", attrs...)
}

// List returns a page of the events f selects, newest first, and how many
// it selects in all.
func (s *Service) List(ctx context.Context, f Filter) ([]Event, int64, error) {
	switch f.Outcome {
	case "", Success, Denied, Failure:
	default:
		return nil, 0, fmt.Errorf("%w: outcome %q", ErrInvalid, f.Outcome)
	}

	if f.Page < 0 {
		return nil, 0, fmt.Errorf("%w: page %d", ErrInvalid, f.Page)
	}

	size := PageSize(f.PageSize)
	page := max(f.Page, 1)

	if (page-1)*size > math.MaxInt32 {
		return nil, 0, fmt.Errorf("%w: page %d is past any audit log", ErrInvalid, f.Page)
	}

	until := f.Until
	if until.IsZero() {
		until = s.now().Add(time.Hour)
	}

	count := database.CountAuditEventsParams{
		Actor:   f.Actor,
		Action:  f.Action,
		Target:  f.Target,
		Outcome: string(f.Outcome),
		Since:   f.Since.UTC(),
		Until:   until.UTC(),
	}

	total, err := s.db.CountAuditEvents(ctx, count)
	if err != nil {
		return nil, 0, fmt.Errorf("count audit events: %w", err)
	}

	rows, err := s.db.ListAuditEvents(ctx, database.ListAuditEventsParams{
		Actor:   count.Actor,
		Action:  count.Action,
		Target:  count.Target,
		Outcome: count.Outcome,
		Since:   count.Since,
		Until:   count.Until,
		Skip:    int32((page - 1) * size), //nolint:gosec // a page number times a bounded page size
		MaxRows: int32(size),              //nolint:gosec // bounded by maxPageSize
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list audit events: %w", err)
	}

	events := make([]Event, len(rows))
	for i, r := range rows {
		events[i] = Event{
			ID:      r.ID.String(),
			Time:    r.Time,
			Actor:   r.Actor,
			Action:  r.Action,
			Target:  r.Target,
			Outcome: Outcome(r.Outcome),
			Client:  r.Client,
			Detail:  r.Detail,
		}
	}

	return events, total, nil
}

// PageSize is the page size List uses when asked for size.
func PageSize(size int) int {
	if size <= 0 {
		return defaultPageSize
	}

	return min(size, maxPageSize)
}

// Purge deletes events older than retention and returns how many.
func (s *Service) Purge(ctx context.Context, retention time.Duration) (int64, error) {
	n, err := s.db.DeleteAuditEventsBefore(ctx, s.now().Add(-retention).UTC())
	if err != nil {
		return 0, fmt.Errorf("purge audit events: %w", err)
	}

	return n, nil
}

// maxFieldBytes bounds each text field of a stored or logged event.
const maxFieldBytes = 1 << 10

const ellipsis = "…"

func (e Event) bounded() Event {
	e.Actor = truncated(e.Actor)
	e.Action = truncated(e.Action)
	e.Target = truncated(e.Target)
	e.Client = truncated(e.Client)
	e.Detail = truncated(e.Detail)

	return e
}

// truncated cuts s to maxFieldBytes at a rune boundary, marking the cut.
func truncated(s string) string {
	if len(s) <= maxFieldBytes {
		return s
	}

	end := maxFieldBytes - len(ellipsis)
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}

	return s[:end] + ellipsis
}
