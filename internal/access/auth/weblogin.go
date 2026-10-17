package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"relic/internal/platform/database"
	"relic/internal/platform/telemetry"
)

// Web-login errors.
var (
	ErrLoginPending = errors.New("login not approved yet")
	ErrLoginUnknown = errors.New("unknown or expired login session")
	ErrLoginCode    = errors.New("verification code does not match")
)

const webLoginTTL = 10 * time.Minute

const webLoginTokenTTL = time.Hour

// WebLogins signs a command-line client in through the web UI.
type WebLogins struct {
	db     *database.DB
	tokens *Tokens
	now    func() time.Time
}

// NewWebLogins returns a WebLogins issuing tokens from tokens.
func NewWebLogins(db *database.DB, tokens *Tokens) *WebLogins {
	return &WebLogins{
		db:     db,
		tokens: tokens,
		now:    time.Now,
	}
}

// Request registers a session a client created.
func (l *WebLogins) Request(ctx context.Context, session string) error {
	if _, err := uuid.Parse(session); err != nil {
		return fmt.Errorf("%w: session must be a UUID", ErrLoginUnknown)
	}

	if err := l.db.DeleteExpiredWebLoginSessions(ctx, l.now().Add(-webLoginTTL)); err != nil {
		return err
	}

	return l.db.InsertWebLoginSession(ctx, session)
}

// Approve lets username approve a session.
func (l *WebLogins) Approve(ctx context.Context, session, code, username string) error {
	s, err := l.session(ctx, session)
	if err != nil {
		return err
	}

	want := strings.ToLower(s.ID[len(s.ID)-4:])
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(code)), []byte(want)) != 1 {
		return ErrLoginCode
	}

	n, err := l.db.ApproveWebLoginSession(ctx, database.ApproveWebLoginSessionParams{
		ID:       session,
		Username: &username,
	})
	if err != nil {
		return err
	}

	if n == 0 {
		return fmt.Errorf("%w: already approved", ErrLoginUnknown)
	}

	return nil
}

// Poll returns a token once the session is approved, then forgets the
// session so the token is handed out only once.
func (l *WebLogins) Poll(ctx context.Context, session, description string) (IssuedToken, error) {
	s, err := l.session(ctx, session)
	if err != nil {
		return IssuedToken{}, err
	}

	if s.ApprovedBy == nil {
		return IssuedToken{}, ErrLoginPending
	}

	var out IssuedToken

	params := IssueParams{
		Username:    *s.ApprovedBy,
		Scope:       UserScope,
		ExpiresIn:   webLoginTokenTTL,
		Refreshable: true,
		Description: description,
	}

	err = l.db.WithTx(ctx, func(q database.Querier) error {
		n, err := q.DeleteWebLoginSession(ctx, session)
		if err != nil {
			return err
		}

		if n == 0 {
			return ErrLoginUnknown
		}

		out, err = l.tokens.issue(ctx, q, params)

		return err
	})
	if err != nil {
		return IssuedToken{}, err
	}

	telemetry.TokenIssued(params.kind)

	return out, nil
}

func (l *WebLogins) session(ctx context.Context, id string) (database.WebLoginSession, error) {
	s, err := l.db.GetWebLoginSession(ctx, id)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return database.WebLoginSession{}, ErrLoginUnknown
	}

	if err != nil {
		return database.WebLoginSession{}, err
	}

	if l.now().Sub(s.Created) > webLoginTTL {
		return database.WebLoginSession{}, ErrLoginUnknown
	}

	return s, nil
}
