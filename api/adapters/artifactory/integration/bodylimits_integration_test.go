//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUploadLimits(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/nuget-local",
			body:   `{"rclass":"local","packageType":"nuget"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/pypi-local",
			body:   `{"rclass":"local","packageType":"pypi"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/security/users/eve",
			body:   `{"email":"e@example.com","password":"Eve-pass-1"}`,
		},
	)

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("package", "p.nupkg")
	_, _ = fw.Write([]byte("not a package"))
	_ = mw.Close()

	resp, _ := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/nuget/nuget-local",
			body:   body.String(),
			noAuth: true,
			headers: map[string]string{
				"Content-Type":  mw.FormDataContentType(),
				"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("eve:Eve-pass-1")),
			},
		},
	)
	if resp.StatusCode != 403 {
		t.Errorf("NuGet push without write = %d, want 403 before the package is read", resp.StatusCode)
	}

	body.Reset()

	mw = multipart.NewWriter(&body)
	for i := range 300 {
		_ = mw.WriteField(fmt.Sprintf("f%d", i), "x")
	}

	_ = mw.Close()
	if resp, _ := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/pypi/pypi-local",
			body:   body.String(),
			headers: map[string]string{
				"Content-Type": mw.FormDataContentType(),
			},
		},
	); resp.StatusCode != 400 {
		t.Errorf("PyPI upload with 300 fields = %d, want 400", resp.StatusCode)
	}

	for _, path := range []string{
		"/artifactory/api/build",
		"/artifactory/api/repositories/cut-short",
		"/artifactory/api/search/aql",
		"/artifactory/api/security/users/cut-short",
	} {
		method := "PUT"
		if strings.HasSuffix(path, "aql") {
			method = "POST"
		}

		if status := truncatedBodyStatus(t, srv, method, path); status != 400 {
			t.Errorf("%s %s with a body cut short = %d, want 400", method, path, status)
		}
	}
}

func truncatedBodyStatus(t *testing.T, srv *httptest.Server, method, path string) int {
	t.Helper()

	var dialer net.Dialer

	conn, err := dialer.DialContext(t.Context(), "tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = conn.Close()
	}()

	request := method + " " + path + " HTTP/1.1\r\n" +
		"Host: relic\r\n" +
		"Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("admin:password")) + "\r\n" +
		"Content-Type: application/json\r\n" +
		"Content-Length: 100\r\n" +
		"\r\n" +
		`{"name":`
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}

	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	return resp.StatusCode
}
