package httpserver

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"relic/internal/access/auth"
)

func TestRouterAuditsWrites(t *testing.T) {
	t.Parallel()

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	rt := NewRouter()
	rt.Handle("/admin/{name}", auth.Admin, ok)
	rt.Handle("/open", auth.Public, ok)
	rt.HandleAudited("POST /tokens", auth.Authenticated, ok)

	type audited struct {
		pattern string
		status  int
	}

	var got []audited

	rt.SetAuditHook(func(_ *http.Request, route Route, status int) {
		got = append(got, audited{
			pattern: route.Pattern,
			status:  status,
		})
	})

	admin := auth.Principal{
		Username: "admin",
		Admin:    true,
	}
	user := auth.Principal{
		Username: "bob",
	}

	tests := []struct {
		method, path string
		p            auth.Principal
		want         []audited
	}{
		{
			method: http.MethodPut,
			path:   "/admin/x",
			p:      admin,
			want: []audited{
				{
					pattern: "/admin/{name}",
					status:  http.StatusCreated,
				},
			},
		},
		{
			method: http.MethodDelete,
			path:   "/admin/x",
			p:      user,
			want: []audited{
				{
					pattern: "/admin/{name}",
					status:  http.StatusForbidden,
				},
			},
		},
		{
			method: http.MethodGet,
			path:   "/admin/x",
			p:      admin,
		},
		{
			method: http.MethodPost,
			path:   "/open",
			p:      user,
		},
		{
			method: http.MethodPost,
			path:   "/tokens",
			p:      user,
			want: []audited{
				{
					pattern: "POST /tokens",
					status:  http.StatusCreated,
				},
			},
		},
	}
	for _, tt := range tests {
		got = nil
		r := httptest.NewRequestWithContext(auth.WithPrincipal(t.Context(), tt.p), tt.method, tt.path, nil)
		rt.ServeHTTP(httptest.NewRecorder(), r)

		if !slices.Equal(got, tt.want) {
			t.Errorf("%s %s audited %v, want %v", tt.method, tt.path, got, tt.want)
		}
	}
}

func TestRouterAuditsAWriteThatPanics(t *testing.T) {
	t.Parallel()

	rt := NewRouter()
	rt.Handle("/admin/{name}", auth.Admin, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("the handler broke")
	}))

	var statuses []int

	rt.SetAuditHook(func(_ *http.Request, _ Route, status int) {
		statuses = append(statuses, status)
	})

	r := httptest.NewRequestWithContext(auth.WithPrincipal(t.Context(), auth.Principal{
		Username: "admin",
		Admin:    true,
	}), http.MethodDelete, "/admin/x", nil)

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the handler's panic did not reach the recovery middleware")
			}
		}()

		rt.ServeHTTP(httptest.NewRecorder(), r)
	}()

	if !slices.Equal(statuses, []int{
		http.StatusInternalServerError,
	}) {
		t.Errorf("audited statuses = %v, want one 500", statuses)
	}
}
