package ssotest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestOIDCTokenBeforeSignIn(t *testing.T) {
	t.Parallel()

	o := NewOIDC(t, "relic", "client-secret-1")
	verifier := "verifier-of-at-least-forty-three-characters-long"
	sum := sha256.Sum256([]byte(verifier))
	redirect := "https://relic.example.com/callback"

	authorize := o.URL + "/authorize?" + url.Values{
		"client_id": []string{
			"relic",
		},
		"code_challenge_method": []string{
			"S256",
		},
		"code_challenge": []string{
			base64.RawURLEncoding.EncodeToString(sum[:]),
		},
		"redirect_uri": []string{
			redirect,
		},
	}.Encode()

	browser := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, authorize, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	back, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"grant_type": []string{
			"authorization_code",
		},
		"code": []string{
			back.Query().Get("code"),
		},
		"redirect_uri": []string{
			redirect,
		},
		"code_verifier": []string{
			verifier,
		},
	}

	req, err = http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		o.URL+"/token",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("relic", "client-secret-1")

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("token request before SignIn: %v", err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	var answer map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil || resp.StatusCode != http.StatusBadRequest ||
		answer["error"] != "access_denied" {
		t.Errorf("token before SignIn = %d %v, %v; want 400 access_denied", resp.StatusCode, answer, err)
	}

	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("token error Content-Type = %q, want application/json", got)
	}
}
