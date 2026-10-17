package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTrustProxies(t *testing.T) {
	t.Parallel()

	trusted, err := ParsePrefixes("10.0.0.0/8, 192.168.1.5")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		peer       string
		xff, proto string
		wantClient string
		wantProto  string
	}{
		{
			"untrusted peer loses the headers",
			"203.0.113.9:1234",
			"1.2.3.4",
			"https",
			"203.0.113.9",
			"",
		},
		{
			"trusted proxy",
			"10.1.2.3:1234",
			"198.51.100.7",
			"https",
			"198.51.100.7",
			"https",
		},
		{
			"trusted single address",
			"192.168.1.5:1234",
			"198.51.100.7",
			"https",
			"198.51.100.7",
			"https",
		},
		{
			"chain of trusted proxies",
			"10.1.2.3:1234",
			"198.51.100.7, 10.9.9.9",
			"https",
			"198.51.100.7",
			"https",
		},
		{
			"spoofed left-most entry is ignored",
			"10.1.2.3:1234",
			"6.6.6.6, 198.51.100.7",
			"",
			"198.51.100.7",
			"",
		},
		{
			"trusted proxy without XFF",
			"10.1.2.3:1234",
			"",
			"https",
			"10.1.2.3",
			"https",
		},
		{
			"garbage XFF keeps the peer",
			"10.1.2.3:1234",
			"not-an-ip",
			"",
			"10.1.2.3",
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var client, proto string

			h := TrustProxies(trusted, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				client, proto = ClientIP(r).String(), r.Header.Get("X-Forwarded-Proto")
			}))
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)

			req.RemoteAddr = tt.peer
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}

			if tt.proto != "" {
				req.Header.Set("X-Forwarded-Proto", tt.proto)
			}

			h.ServeHTTP(httptest.NewRecorder(), req)

			if client != tt.wantClient || proto != tt.wantProto {
				t.Errorf("client, proto = %s, %q; want %s, %q", client, proto, tt.wantClient, tt.wantProto)
			}
		})
	}
}

func TestParsePrefixes(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{
		"10.0.0.0/33",
		"nope",
		"10.0.0.1/8/1",
	} {
		if _, err := ParsePrefixes(bad); err == nil {
			t.Errorf("ParsePrefixes(%q) accepted", bad)
		}
	}

	if p, err := ParsePrefixes(" ,::1, fc00::/7"); err != nil || len(p) != 2 {
		t.Errorf("ParsePrefixes = %v, %v", p, err)
	}
}

func TestOrigin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		forwardedProto string
		want           string
	}{
		{
			name: "plain http",
			want: "http://relic.example.com",
		},
		{
			name:           "https through a proxy",
			forwardedProto: "HTTPS",
			want:           "https://relic.example.com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := httptest.NewRequestWithContext(
				t.Context(),
				http.MethodGet,
				"http://relic.example.com/scim/v2/Users",
				nil,
			)
			if tt.forwardedProto != "" {
				r.Header.Set("X-Forwarded-Proto", tt.forwardedProto)
			}

			if got := Origin(r); got != tt.want {
				t.Errorf("Origin = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHSTS(t *testing.T) {
	t.Parallel()

	h := HSTS(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	for _, tt := range []struct {
		name  string
		proto string
		want  string
	}{
		{
			"https",
			"https",
			"max-age=31536000",
		},
		{
			"http",
			"",
			"",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			if tt.proto != "" {
				req.Header.Set("X-Forwarded-Proto", tt.proto)
			}

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if got := rec.Header().Get("Strict-Transport-Security"); got != tt.want {
				t.Errorf("Strict-Transport-Security = %q; want %q", got, tt.want)
			}
		})
	}
}
