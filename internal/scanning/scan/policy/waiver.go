package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"uuid"

	"relic/internal/platform/database"
)

// WaiverStatus is where a waiver request stands.
type WaiverStatus string

// Waiver statuses.
const (
	WaiverPending  WaiverStatus = "pending"
	WaiverApproved WaiverStatus = "approved"
	WaiverRejected WaiverStatus = "rejected"
)

// ErrDecided reports a waiver that was already approved or rejected.
var ErrDecided = errors.New("the waiver was already decided")

// ErrInvalidWaiver reports a waiver request or approval Relic refuses.
var ErrInvalidWaiver = errors.New("invalid waiver")

// waiverError is an error of kind whose message is its detail alone.
type waiverError struct {
	kind   error
	detail string
}

func (e waiverError) Error() string { return e.detail }

func (e waiverError) Is(target error) bool { return target == e.kind }

func invalidWaiver(format string, args ...any) error {
	return waiverError{
		kind:   ErrInvalidWaiver,
		detail: fmt.Sprintf(format, args...),
	}
}

func waiverNotFound(format string, args ...any) error {
	return waiverError{
		kind:   ErrNotFound,
		detail: fmt.Sprintf(format, args...),
	}
}

// maxWaiverDays bounds how long an approved waiver lasts.
const maxWaiverDays = 365

// Waiver asks for an ignore rule: Rule holds its filters. Approved, it
// becomes an ignore rule that expires.
type Waiver struct {
	ID           string
	RequestedBy  string
	Reason       string
	Rule         IgnoreRule
	Status       WaiverStatus
	DecidedBy    string
	DecisionNote string
	Decided      *time.Time
	Expires      *time.Time
	IgnoreRuleID string
	Created      time.Time
}

func domainWaiver(row database.Waiver) (Waiver, error) {
	w := Waiver{
		ID:           row.ID.String(),
		RequestedBy:  row.RequestedBy,
		Reason:       row.Reason,
		Status:       WaiverStatus(row.Status),
		DecidedBy:    row.DecidedBy,
		DecisionNote: row.DecisionNote,
		Decided:      row.Decided,
		Expires:      row.Expires,
		IgnoreRuleID: row.IgnoreRuleID,
		Created:      row.Created,
	}
	if err := json.Unmarshal(row.Doc, &w.Rule); err != nil {
		return Waiver{}, fmt.Errorf("decode waiver %s: %w", w.ID, err)
	}

	return w, nil
}

// RequestWaiver records user's request for an ignore rule with rule's
// filters.
func (a *Admin) RequestWaiver(ctx context.Context, rule IgnoreRule, reason, user string) (Waiver, error) {
	if strings.TrimSpace(reason) == "" {
		return Waiver{}, invalidWaiver("a waiver needs a reason")
	}

	rule.ID, rule.Expires, rule.Notes = "", nil, ""
	if rule.Empty() {
		return Waiver{}, invalidWaiver("a waiver needs at least one filter")
	}

	if p, found := firstInvalidPackage(rule.Packages); found {
		return Waiver{}, invalidWaiver("%q is not a package URL", p)
	}

	doc, err := json.Marshal(rule)
	if err != nil {
		return Waiver{}, err
	}

	id := uuid.NewV7()
	if err := a.DB.InsertWaiver(ctx, database.InsertWaiverParams{
		ID:          id,
		RequestedBy: user,
		Reason:      reason,
		Doc:         doc,
		Created:     time.Now().UTC(),
	}); err != nil {
		return Waiver{}, fmt.Errorf("record waiver: %w", err)
	}

	return a.Waiver(ctx, id.String())
}

// Waiver returns one waiver.
func (a *Admin) Waiver(ctx context.Context, id string) (Waiver, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return Waiver{}, waiverNotFound("waiver %s not found", id)
	}

	row, err := a.DB.GetWaiver(ctx, parsed)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Waiver{}, waiverNotFound("waiver %s not found", id)
	}

	if err != nil {
		return Waiver{}, err
	}

	return domainWaiver(row)
}

// WaiverQuery selects waivers: one user's, or everyone's for RequestedBy
// "", in one status or all for Status "".
type WaiverQuery struct {
	RequestedBy string
	Status      WaiverStatus
	Skip        int
	Max         int
}

// Waivers lists a page of waivers, newest first, and counts the waivers the
// query selects.
func (a *Admin) Waivers(ctx context.Context, q WaiverQuery) ([]Waiver, int64, error) {
	total, err := a.DB.CountWaivers(ctx, database.CountWaiversParams{
		RequestedBy: q.RequestedBy,
		Status:      string(q.Status),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("count waivers: %w", err)
	}

	rows, err := a.DB.ListWaivers(ctx, database.ListWaiversParams{
		RequestedBy: q.RequestedBy,
		Status:      string(q.Status),
		Skip:        int32(min(q.Skip, math.MaxInt32)), //nolint:gosec // clamped
		MaxRows:     int32(min(q.Max, math.MaxInt32)),  //nolint:gosec // clamped
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list waivers: %w", err)
	}

	out := make([]Waiver, 0, len(rows))
	for _, r := range rows {
		w, err := domainWaiver(r)
		if err != nil {
			return nil, 0, err
		}

		out = append(out, w)
	}

	return out, total, nil
}

// ApproveWaiver turns a pending waiver into an ignore rule that expires after
// days (at most a year).
func (a *Admin) ApproveWaiver(ctx context.Context, id string, days int, note, user string) (Waiver, error) {
	if days < 1 || days > maxWaiverDays {
		return Waiver{}, invalidWaiver("a waiver lasts 1 to %d days", maxWaiverDays)
	}

	w, err := a.Waiver(ctx, id)
	if err != nil {
		return Waiver{}, err
	}

	if w.Status != WaiverPending {
		return Waiver{}, fmt.Errorf("%w: %s is %s", ErrDecided, id, w.Status)
	}

	now := time.Now().UTC()
	expires := now.AddDate(0, 0, days)

	rule := w.Rule
	rule.Expires = &expires
	rule.Notes = "waiver " + w.ID + " for " + w.RequestedBy + ": " + w.Reason

	defer a.changed()

	if err := a.DB.WithTx(ctx, func(q database.Querier) error {
		created, err := insertIgnoreRule(ctx, q, rule, user)
		if err != nil {
			return err
		}

		return decideWaiver(ctx, q, w.ID, WaiverApproved, note, user, now, &expires, created.ID)
	}); err != nil {
		return Waiver{}, err
	}

	return a.Waiver(ctx, id)
}

// RejectWaiver rejects a pending waiver.
func (a *Admin) RejectWaiver(ctx context.Context, id, note, user string) (Waiver, error) {
	w, err := a.Waiver(ctx, id)
	if err != nil {
		return Waiver{}, err
	}

	if err := decideWaiver(ctx, a.DB, w.ID, WaiverRejected, note, user, time.Now().UTC(), nil, ""); err != nil {
		return Waiver{}, err
	}

	return a.Waiver(ctx, id)
}

func decideWaiver(
	ctx context.Context,
	q database.Querier,
	id string,
	status WaiverStatus,
	note, user string,
	now time.Time,
	expires *time.Time,
	ruleID string,
) error {
	n, err := q.DecideWaiver(ctx, database.DecideWaiverParams{
		Status:       string(status),
		DecidedBy:    user,
		DecisionNote: note,
		Decided:      &now,
		Expires:      expires,
		IgnoreRuleID: ruleID,
		ID:           uuid.MustParse(id),
	})
	if err != nil {
		return fmt.Errorf("decide waiver: %w", err)
	}

	if n == 0 {
		return fmt.Errorf("%w: %s", ErrDecided, id)
	}

	return nil
}

// WithdrawWaiver deletes user's own pending waiver.
func (a *Admin) WithdrawWaiver(ctx context.Context, id, user string) error {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return waiverNotFound("waiver %s not found", id)
	}

	n, err := a.DB.DeleteWaiver(ctx, database.DeleteWaiverParams{
		ID:          parsed,
		RequestedBy: user,
	})
	if err != nil {
		return fmt.Errorf("withdraw waiver: %w", err)
	}

	if n == 0 {
		return waiverNotFound("no pending waiver %s of yours", id)
	}

	return nil
}
