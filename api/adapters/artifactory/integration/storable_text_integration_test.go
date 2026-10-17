//go:build integration

package integration

import "testing"

func TestTextNeitherEngineStoresIsRefused(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/gen",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/gen/docs/p.txt",
		body:   "x",
	})

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{
			name:   "a file name with invalid UTF-8",
			method: "PUT",
			path:   "/artifactory/gen/bad/a%FFb.txt",
			body:   "hi",
			want:   400,
		},
		{
			name:   "a property value with NUL",
			method: "PUT",
			path:   "/artifactory/api/storage/gen/docs/p.txt?properties=k1=a%00b",
			want:   400,
		},
		{
			name:   "a property value with invalid UTF-8",
			method: "PUT",
			path:   "/artifactory/api/storage/gen/docs/p.txt?properties=k2=a%FFb",
			want:   400,
		},
		{
			name:   "a property value in UTF-8",
			method: "PUT",
			path:   "/artifactory/api/storage/gen/docs/p.txt?properties=k3=Stra%C3%9Fe",
			want:   204,
		},
		{
			name:   "repository settings with NUL",
			method: "PUT",
			path:   "/artifactory/api/repositories/ext1",
			body:   `{"rclass":"local","packageType":"generic","zzzCustom":{"s":"x\u0000y"}}`,
			want:   400,
		},
	}
	for _, tt := range tests {
		if resp, body := do(
			t,
			srv,
			call{
				method: tt.method,
				path:   tt.path,
				body:   tt.body,
			},
		); resp.StatusCode != tt.want {
			t.Errorf("%s = %d %s, want %d", tt.name, resp.StatusCode, body, tt.want)
		}
	}
}
