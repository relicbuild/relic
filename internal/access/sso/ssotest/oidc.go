// Package ssotest runs identity providers in tests.
package ssotest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// OIDC is an OpenID Connect provider that signs in whoever User names,
// without asking.
type OIDC struct {
	*httptest.Server

	ClientID     string
	ClientSecret string

	mu    sync.Mutex
	user  map[string]any
	codes map[string]grant
	key   *rsa.PrivateKey
	// audience and nonce override the ID token's when set.
	audience string
	nonce    string
}

type grant struct {
	clientID, redirectURI, nonce, challenge string
}

// NewOIDC starts a provider for one client.
func NewOIDC(t *testing.T, clientID, clientSecret string) *OIDC {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	o := &OIDC{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		codes:        map[string]grant{},
		key:          key,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", o.discovery)
	mux.HandleFunc("GET /keys", o.keys)
	mux.HandleFunc("GET /authorize", o.authorize)
	mux.HandleFunc("POST /token", o.token)
	o.Server = httptest.NewServer(mux)
	t.Cleanup(o.Close)

	return o
}

// SignIn makes the provider sign in a user with these claims.
func (o *OIDC) SignIn(claims map[string]any) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.user = maps.Clone(claims)
}

// Tamper makes the provider issue ID tokens for another audience or with
// another nonce; empty values stop it.
func (o *OIDC) Tamper(audience, nonce string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.audience, o.nonce = audience, nonce
}

// Mint signs a token with these claims as the provider; iss defaults to the
// provider's URL, and iat and exp to now and an hour later.
func (o *OIDC) Mint(t *testing.T, claims map[string]any) string {
	t.Helper()

	c := maps.Clone(claims)
	if _, ok := c["iss"]; !ok {
		c["iss"] = o.URL
	}

	if _, ok := c["iat"]; !ok {
		c["iat"] = time.Now().Unix()
	}

	if _, ok := c["exp"]; !ok {
		c["exp"] = time.Now().Add(time.Hour).Unix()
	}

	signer, err := jose.NewSigner(jose.SigningKey{
		Algorithm: jose.RS256,
		Key: jose.JSONWebKey{
			Key:   o.key,
			KeyID: "test",
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	token, err := jwt.Signed(signer).Claims(c).Serialize()
	if err != nil {
		t.Fatal(err)
	}

	return token
}

// JWKS is the provider's public key set, as JSON.
func (o *OIDC) JWKS(t *testing.T) string {
	t.Helper()

	out, err := json.Marshal(jose.JSONWebKeySet{
		Keys: []jose.JSONWebKey{
			{
				Key:       &o.key.PublicKey,
				KeyID:     "test",
				Algorithm: "RS256",
				Use:       "sig",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	return string(out)
}

func (o *OIDC) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                 o.URL,
		"authorization_endpoint": o.URL + "/authorize",
		"token_endpoint":         o.URL + "/token",
		"jwks_uri":               o.URL + "/keys",
		"response_types_supported": []string{
			"code",
		},
		"subject_types_supported": []string{
			"public",
		},
		"id_token_signing_alg_values_supported": []string{
			"RS256",
		},
		"code_challenge_methods_supported": []string{
			"S256",
		},
	})
}

func (o *OIDC) keys(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, jose.JSONWebKeySet{
		Keys: []jose.JSONWebKey{
			{
				Key:       &o.key.PublicKey,
				KeyID:     "test",
				Algorithm: "RS256",
				Use:       "sig",
			},
		},
	})
}

func (o *OIDC) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != o.ClientID || q.Get("code_challenge_method") != "S256" {
		http.Error(w, "bad authorization request", http.StatusBadRequest)
		return
	}

	code := rand.Text()

	o.mu.Lock()
	o.codes[code] = grant{
		clientID:    q.Get("client_id"),
		redirectURI: q.Get("redirect_uri"),
		nonce:       q.Get("nonce"),
		challenge:   q.Get("code_challenge"),
	}
	o.mu.Unlock()

	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}

	values := back.Query()
	values.Set("code", code)
	values.Set("state", q.Get("state"))
	back.RawQuery = values.Encode()
	//nolint:gosec // G710: a test provider sends the browser back to the client under test
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (o *OIDC) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}

	o.mu.Lock()
	g, found := o.codes[r.PostForm.Get("code")]
	delete(o.codes, r.PostForm.Get("code"))
	user := maps.Clone(o.user)
	audience, nonce := o.ClientID, g.nonce

	if o.audience != "" {
		audience = o.audience
	}

	if o.nonce != "" {
		nonce = o.nonce
	}
	o.mu.Unlock()

	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))

	switch {
	case !found, id != o.ClientID, secret != o.ClientSecret, g.redirectURI != r.PostForm.Get("redirect_uri"):
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{
			"error": "invalid_grant",
		})

		return
	case base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge:
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{
			"error": "invalid_grant",
		})

		return
	case user == nil:
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{
			"error":             "access_denied",
			"error_description": "no user signed in: call SignIn first",
		})

		return
	}

	now := time.Now()
	user["iss"] = o.URL
	user["aud"] = audience
	user["nonce"] = nonce
	user["iat"] = now.Unix()
	user["exp"] = now.Add(time.Hour).Unix()

	if _, ok := user["sub"]; !ok {
		user["sub"] = "subject"
	}

	signer, err := jose.NewSigner(jose.SigningKey{
		Algorithm: jose.RS256,
		Key: jose.JSONWebKey{
			Key:   o.key,
			KeyID: "test",
		},
	}, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	idToken, err := jwt.Signed(signer).Claims(user).Serialize()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"access_token": rand.Text(),
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     idToken,
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	writeJSONStatus(w, http.StatusOK, v)
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
