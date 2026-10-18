package relicapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
)

const sessionTTL = 12 * time.Hour

type sessionServer struct {
	d Dependencies
}

func principalOf(p auth.Principal) *relicv1.Principal {
	return &relicv1.Principal{
		Username: p.Username,
		Admin:    p.Admin,
		Viewer:   p.Viewer,
		Groups:   p.Groups,
	}
}

func (s *sessionServer) Login(
	ctx context.Context,
	req *connect.Request[relicv1.LoginRequest],
) (*connect.Response[relicv1.LoginResponse], error) {
	r := httpRequest(ctx)
	if s.d.Authenticator == nil || s.d.Tokens == nil || r == nil {
		return nil, unimplemented("sign-in")
	}
	// A cross-site form cannot add the header without a CORS preflight,
	// which Relic never grants.
	if req.Header().Get(auth.UIHeader) == "" {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("missing "+auth.UIHeader+" header"))
	}

	p, err := s.d.Authenticator.Password(r, req.Msg.GetUsername(), req.Msg.GetPassword())

	var throttled *auth.ThrottledError
	switch {
	case errors.Is(err, auth.ErrBadCredentials):
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid username or password"))
	case errors.As(err, &throttled):
		return nil, connect.NewError(connect.CodeResourceExhausted, err)
	case err != nil:
		return nil, err
	}

	cookie, expires, err := s.d.uiSession(r, p)
	if err != nil {
		return nil, err
	}

	resp := connect.NewResponse(
		&relicv1.LoginResponse{
			Principal: principalOf(p),
			Expires:   timestamppb.New(expires),
		},
	)
	resp.Header().Add("Set-Cookie", cookie.String())

	return resp, nil
}

// uiSession issues a session token for p and returns its cookie and when it
// expires.
func (d Dependencies) uiSession(r *http.Request, p auth.Principal) (*http.Cookie, time.Time, error) {
	now := time.Now()

	tok, err := d.Tokens.Issue(r.Context(), p.LimitIssue(auth.IssueParams{
		Username:    p.Username,
		Scope:       auth.UserScope,
		ExpiresIn:   sessionTTL,
		Description: "ui session",
	}, now))
	if err != nil {
		return nil, time.Time{}, err
	}

	return sessionCookie(r, tok.AccessToken, int(tok.ExpiresIn/time.Second)), now.Add(tok.ExpiresIn), nil
}

func (s *sessionServer) Logout(
	ctx context.Context,
	req *connect.Request[relicv1.LogoutRequest],
) (*connect.Response[relicv1.LogoutResponse], error) {
	r := httpRequest(ctx)
	if s.d.Tokens == nil || r == nil {
		return nil, unimplemented("sign-out")
	}

	if c, err := r.Cookie(auth.SessionCookie); err == nil && c.Value != "" {
		if err := s.d.Tokens.Revoke(ctx, c.Value); err != nil {
			return nil, err
		}
	}

	if token, ok := auth.BearerToken(r.Header.Get("Authorization")); ok {
		if err := s.d.Tokens.Revoke(ctx, token); err != nil {
			return nil, err
		}
	}

	resp := connect.NewResponse(&relicv1.LogoutResponse{})
	resp.Header().Add("Set-Cookie", sessionCookie(r, "", -1).String())

	return resp, nil
}

func (s *sessionServer) GetCurrentUser(
	ctx context.Context,
	_ *connect.Request[relicv1.GetCurrentUserRequest],
) (*connect.Response[relicv1.GetCurrentUserResponse], error) {
	return connect.NewResponse(&relicv1.GetCurrentUserResponse{
		Principal: principalOf(auth.FromContext(ctx)),
	}), nil
}

func sessionCookie(r *http.Request, value string, maxAge int) *http.Cookie {
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")

	return &http.Cookie{ //nolint:gosec // G124: Secure is off only for plain-HTTP development
		Name:     auth.SessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}

func (s *sessionServer) ApproveLogin(
	ctx context.Context,
	req *connect.Request[relicv1.ApproveLoginRequest],
) (*connect.Response[relicv1.ApproveLoginResponse], error) {
	if s.d.WebLogins == nil {
		return nil, unimplemented("sign-in approval")
	}

	approver := auth.FromContext(ctx)
	if approver.Scoped || approver.WorkloadIdentity != "" {
		return nil, connect.NewError(
			connect.CodePermissionDenied,
			errors.New("a token scoped to groups or from a workload identity cannot approve a sign-in"),
		)
	}

	err := s.d.WebLogins.Approve(ctx, req.Msg.GetSession(), req.Msg.GetCode(), approver.Username)
	switch {
	case errors.Is(err, auth.ErrLoginCode):
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			errors.New("the code does not match the one shown in your terminal"),
		)
	case errors.Is(err, auth.ErrLoginUnknown):
		return nil, connect.NewError(
			connect.CodeNotFound,
			errors.New("this sign-in request is unknown or has expired; sign in from the client again"),
		)
	case err != nil:
		return nil, err
	}

	return connect.NewResponse(&relicv1.ApproveLoginResponse{}), nil
}

func (s *sessionServer) StartWebLogin(
	ctx context.Context,
	req *connect.Request[relicv1.StartWebLoginRequest],
) (*connect.Response[relicv1.StartWebLoginResponse], error) {
	if s.d.WebLogins == nil {
		return nil, unimplemented("sign-in in the browser")
	}

	session := req.Msg.GetSession()
	if err := s.d.WebLogins.Request(ctx, session); errors.Is(err, auth.ErrLoginUnknown) {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	} else if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.StartWebLoginResponse{
		ApprovePath: "/login?" + url.Values{
			"cliSession": []string{
				session,
			},
			"cliName": []string{
				"relic CLI",
			},
		}.Encode(),
		Code: strings.ToLower(session[len(session)-4:]),
	}), nil
}

func (s *sessionServer) PollWebLogin(
	ctx context.Context,
	req *connect.Request[relicv1.PollWebLoginRequest],
) (*connect.Response[relicv1.PollWebLoginResponse], error) {
	if s.d.WebLogins == nil {
		return nil, unimplemented("sign-in in the browser")
	}

	description := req.Msg.GetDescription()
	if description == "" {
		description = "relic CLI"
	}

	tok, err := s.d.WebLogins.Poll(ctx, req.Msg.GetSession(), description)

	switch {
	case errors.Is(err, auth.ErrLoginPending):
		return connect.NewResponse(&relicv1.PollWebLoginResponse{}), nil
	case errors.Is(err, auth.ErrLoginUnknown):
		return nil, connect.NewError(
			connect.CodeNotFound,
			errors.New("the sign-in is unknown or has expired; start again"),
		)
	case err != nil:
		return nil, err
	}

	return connect.NewResponse(&relicv1.PollWebLoginResponse{
		Approved:         true,
		AccessToken:      tok.AccessToken,
		ExpiresInSeconds: int64(tok.ExpiresIn / time.Second),
	}), nil
}

func (s *sessionServer) GetSignInNote(
	_ context.Context,
	_ *connect.Request[relicv1.GetSignInNoteRequest],
) (*connect.Response[relicv1.GetSignInNoteResponse], error) {
	return connect.NewResponse(&relicv1.GetSignInNoteResponse{
		Note: s.d.SignInNote,
	}), nil
}
