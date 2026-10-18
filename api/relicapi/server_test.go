package relicapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/access/audit"
)

func TestHandlerProtocols(t *testing.T) {
	t.Parallel()

	srv := httptest.NewUnstartedServer(Handler(Dependencies{
		Version:  "1.2.3",
		Revision: "abc",
		Log:      slog.New(slog.DiscardHandler),
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	tests := []struct {
		name    string
		options []connecthttp.Option
	}{
		{
			name: "Connect, binary",
		},
		{
			name: "Connect, JSON",
			options: []connecthttp.Option{
				connecthttp.WithProtoJSON(),
			},
		},
		{
			name: "Connect, GET",
			options: []connecthttp.Option{
				connecthttp.WithHTTPGet(),
			},
		},
		{
			name: "gRPC",
			options: []connecthttp.Option{
				connecthttp.WithGRPC(),
			},
		},
		{
			name: "gRPC-Web",
			options: []connecthttp.Option{
				connecthttp.WithGRPCWeb(),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			system := relicv1connect.NewSystemServiceClient(
				connect.NewClient(
					connecthttp.NewTransport(
						srv.Client(),
						srv.URL+"/api",
						tt.options...,
					),
				),
			)

			version, err := system.GetVersion(t.Context(), &relicv1.GetVersionRequest{})
			if err != nil || version.GetVersion() != "1.2.3" {
				t.Errorf("GetVersion = %v, %v", version, err)
			}

			security := relicv1connect.NewSecurityServiceClient(
				connect.NewClient(
					connecthttp.NewTransport(
						srv.Client(),
						srv.URL+"/api",
						tt.options...,
					),
				),
			)

			_, err = security.ListUsers(t.Context(), &relicv1.ListUsersRequest{})
			if connect.CodeOf(err) != connect.CodeUnauthenticated || !strings.HasSuffix(err.Error(), "sign in first") {
				t.Errorf("ListUsers, anonymous = %v, want unauthenticated: sign in first", err)
			}
		})
	}
}

func TestConnectErrorBody(t *testing.T) {
	t.Parallel()

	h := Handler(Dependencies{
		Log: slog.New(slog.DiscardHandler),
	})

	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"/api/relic.v1.SecurityService/ListUsers",
		strings.NewReader("{}"),
	)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("Content-Type") != "application/json" ||
		string(body) != `{"code":"unauthenticated","message":"sign in first"}` {
		t.Errorf("response = %d %q %s", rec.Code, rec.Header().Get("Content-Type"), body)
	}
}

type recordedEvents struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recordedEvents) Record(_ context.Context, e audit.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.events = append(r.events, e)
}

func TestRequestDecodedBeforeAccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
		wantEvents []audit.Event
	}{
		{
			name:       "a denied call is audited with its target",
			body:       `{"name":"bob"}`,
			wantStatus: http.StatusUnauthorized,
			wantCode:   `"code":"unauthenticated"`,
			wantEvents: []audit.Event{
				{
					Actor:   "anonymous",
					Action:  "relic.v1.SecurityService/DeleteUser",
					Target:  "bob",
					Outcome: audit.Denied,
					Detail:  "sign in first",
				},
			},
		},
		{
			name:       "a body that cannot be decoded fails before the access check",
			body:       `{"name":`,
			wantStatus: http.StatusBadRequest,
			wantCode:   `"code":"invalid_argument"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			d := Dependencies{
				Log: slog.New(slog.DiscardHandler),
			}
			recorder := &recordedEvents{}

			server := connect.NewServer(d.serverInterceptors(recorder)...)
			relicv1connect.RegisterSecurityServiceHandler(server, &securityServer{
				d,
			})

			mux := http.NewServeMux()
			connecthttp.Mount(mux, server)

			req := httptest.NewRequestWithContext(
				t.Context(),
				http.MethodPost,
				relicv1connect.SecurityServiceDeleteUserProcedure,
				strings.NewReader(tt.body),
			)
			req.Header.Set("Content-Type", "application/json")

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus || !strings.Contains(rec.Body.String(), tt.wantCode) {
				t.Errorf("response = %d %s, want %d containing %s", rec.Code, rec.Body, tt.wantStatus, tt.wantCode)
			}

			if len(recorder.events) != len(tt.wantEvents) {
				t.Fatalf("events = %+v, want %+v", recorder.events, tt.wantEvents)
			}

			for i, want := range tt.wantEvents {
				if got := recorder.events[i]; got != want {
					t.Errorf("event %d = %+v, want %+v", i, got, want)
				}
			}
		})
	}
}
