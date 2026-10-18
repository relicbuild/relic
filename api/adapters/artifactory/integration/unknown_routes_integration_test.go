//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestUnknownRoutesAnswerTheErrorEnvelope(t *testing.T) {
	t.Parallel()

	srv := server(t)

	for _, c := range []call{
		{
			method: http.MethodGet,
			path:   "/lifecycle/api/v2/release_bundle/names",
		},
		{
			method: http.MethodGet,
			path:   "/access/api/v1/nothing",
		},
		{
			method: http.MethodPost,
			path:   "/access/api/v1/nothing",
		},
		{
			method: http.MethodGet,
			path:   "/xray/api/v1/unknown",
		},
		{
			method: http.MethodGet,
			path:   "/xray/api/v1/xsc/system/version",
		},
		{
			method: http.MethodGet,
			path:   "/evidence/api/v1/nope",
		},
		{
			method: http.MethodGet,
			path:   "/onemodel/api/v1/nope",
		},
	} {
		resp, body := do(t, srv, c)

		var envelope struct {
			Errors []struct {
				Status  int    `json:"status"`
				Message string `json:"message"`
			} `json:"errors"`
		}
		if resp.StatusCode != http.StatusNotFound || resp.Header.Get("Content-Type") != "application/json" ||
			json.Unmarshal([]byte(body), &envelope) != nil || len(envelope.Errors) != 1 ||
			envelope.Errors[0].Status != http.StatusNotFound {
			t.Errorf("%s %s = %d %s %q", c.method, c.path, resp.StatusCode, resp.Header.Get("Content-Type"), body)
		}
	}

	for _, method := range []string{
		http.MethodGet,
		http.MethodPut,
	} {
		resp, body := do(t, srv, call{
			method: method,
			path:   "/artifactory/api/storageinfo",
		})
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "no such endpoint") {
			t.Errorf("%s an Artifactory endpoint Relic lacks = %d %s", method, resp.StatusCode, body)
		}
	}
}
