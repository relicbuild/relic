package ssotest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/xml"
	"html"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/logger"
)

// SAML is a SAML identity provider that signs in whoever SignIn names,
// without asking. Its single sign-on endpoint answers with the HTML form
// that would post the response to the service provider; Form reads it.
type SAML struct {
	*httptest.Server

	idp *saml.IdentityProvider

	mu        sync.Mutex
	session   saml.Session
	providers map[string]*saml.EntityDescriptor
}

// NewSAML starts an identity provider.
func NewSAML(t *testing.T) *SAML {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "test idp",
		},
		NotBefore: time.Now().Add(-time.Hour),
		NotAfter:  time.Now().Add(time.Hour),
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	s := &SAML{
		providers: map[string]*saml.EntityDescriptor{},
	}
	s.Server = httptest.NewUnstartedServer(nil)
	at := func(path string) url.URL {
		return url.URL{
			Scheme: "http",
			Host:   s.Listener.Addr().String(),
			Path:   path,
		}
	}

	s.idp = &saml.IdentityProvider{
		Key:                     key,
		Signer:                  key,
		Logger:                  logger.DefaultLogger,
		Certificate:             cert,
		MetadataURL:             at("/metadata"),
		SSOURL:                  at("/sso"),
		ServiceProviderProvider: s,
		SessionProvider:         s,
	}
	s.Config.Handler = s.idp.Handler()
	s.Start()
	t.Cleanup(s.Close)

	return s
}

// MetadataURL is where the provider serves its metadata.
func (s *SAML) MetadataURL() string { return s.URL + "/metadata" }

// Metadata is the provider's metadata.
func (s *SAML) Metadata(t *testing.T) string {
	t.Helper()

	out, err := xml.Marshal(s.idp.Metadata())
	if err != nil {
		t.Fatal(err)
	}

	return string(out)
}

// Trust registers a service provider's metadata; without encrypt, the
// provider sends it plain assertions.
func (s *SAML) Trust(t *testing.T, metadata []byte, encrypt bool) {
	t.Helper()

	var ed saml.EntityDescriptor
	if err := xml.Unmarshal(metadata, &ed); err != nil {
		t.Fatal(err)
	}

	for i := range ed.SPSSODescriptors {
		if !encrypt {
			ed.SPSSODescriptors[i].KeyDescriptors = slices.DeleteFunc(
				ed.SPSSODescriptors[i].KeyDescriptors,
				func(k saml.KeyDescriptor) bool {
					return k.Use == "encryption"
				},
			)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.providers[ed.EntityID] = &ed
}

// SignIn makes the provider sign in this session's user.
func (s *SAML) SignIn(session saml.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.session = session
}

// GetServiceProvider implements saml.ServiceProviderProvider.
func (s *SAML) GetServiceProvider(_ *http.Request, id string) (*saml.EntityDescriptor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ed, ok := s.providers[id]
	if !ok {
		return nil, os.ErrNotExist
	}

	return ed, nil
}

// GetSession implements saml.SessionProvider.
func (s *SAML) GetSession(_ http.ResponseWriter, _ *http.Request, _ *saml.IdpAuthnRequest) *saml.Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	session := s.session
	session.ID = rand.Text()
	session.CreateTime = time.Now()
	session.ExpireTime = time.Now().Add(time.Hour)
	session.Index = rand.Text()

	return &session
}

var formInput = regexp.MustCompile(`name="(SAMLResponse|RelayState)" value="([^"]*)"`)

// Form reads the action and fields of the form the provider answered with.
func Form(t *testing.T, page string) (string, url.Values) {
	t.Helper()

	action := regexp.MustCompile(`action="([^"]*)"`).FindStringSubmatch(page)
	if action == nil {
		t.Fatalf("no form in %q", page)
	}

	fields := url.Values{}
	for _, m := range formInput.FindAllStringSubmatch(page, -1) {
		fields.Set(m[1], html.UnescapeString(m[2]))
	}

	return html.UnescapeString(action[1]), fields
}
