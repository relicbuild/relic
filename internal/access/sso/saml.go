package sso

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/crewjam/saml"
	dsig "github.com/russellhaering/goxmldsig"

	"relic/internal/platform/telemetry"
)

const maxMetadataSize = 1 << 20

// emailAttributes name an email address in SAML assertions.
var emailAttributes = []string{
	"email",
	"mail",
	"urn:oid:0.9.2342.19200300.100.1.3",
	"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress",
}

// Start begins a sign-in through provider name and returns the URL to send
// the browser to, the state the provider will send back, and the
// provider's protocol. base is the provider's URL on Relic,
// .../api/v1/sso/<name>; returnTo is where to go after.
func (s *Service) Start(ctx context.Context, name, base, returnTo string) (string, string, Protocol, error) {
	p, err := s.Provider(ctx, name)
	if errors.Is(err, ErrNotFound) {
		return "", "", "", refused("there is no identity provider " + name)
	}

	if err != nil {
		return "", "", "", err
	}

	if p.Protocol() == OIDC {
		redirect, state, err := s.StartOIDC(ctx, name, base+"/callback", returnTo)
		return redirect, state, OIDC, err
	}

	redirect, state, err := s.startSAML(ctx, name, base, returnTo)

	return redirect, state, SAML, err
}

func (s *Service) startSAML(ctx context.Context, name, base, returnTo string) (string, string, error) {
	p, err := s.enabled(ctx, name, SAML)
	if err != nil {
		return "", "", err
	}

	sp, err := s.serviceProvider(ctx, p, base)
	if err != nil {
		return "", "", err
	}

	req, err := sp.MakeAuthenticationRequest(
		sp.GetSSOBindingLocation(saml.HTTPRedirectBinding),
		saml.HTTPRedirectBinding,
		saml.HTTPPostBinding,
	)
	if err != nil {
		return "", "", fmt.Errorf("make SAML request for %s: %w", name, err)
	}

	state, err := s.saveRequest(ctx, p.Name, "", req.ID, returnTo)
	if err != nil {
		return "", "", err
	}

	redirect, err := req.Redirect(state, sp)
	if err != nil {
		return "", "", fmt.Errorf("sign SAML request for %s: %w", name, err)
	}

	return redirect.String(), state, nil
}

// FinishSAML completes a sign-in from the identity provider's POSTed
// SAMLResponse: it checks the response's signature, audience, destination,
// validity and that it answers the request with this state, then
// provisions the user. It returns the username and where to go after.
func (s *Service) FinishSAML(ctx context.Context, name, base, state, samlResponse string) (string, string, error) {
	username, returnTo, err := s.finishSAML(ctx, name, base, state, samlResponse)
	telemetry.AuthAttempt("saml", signInResult(err))

	return username, returnTo, err
}

func (s *Service) finishSAML(ctx context.Context, name, base, state, samlResponse string) (string, string, error) {
	req, err := s.takeRequest(ctx, name, state)
	if err != nil {
		return "", "", err
	}

	p, err := s.enabled(ctx, name, SAML)
	if err != nil {
		return "", "", err
	}

	sp, err := s.serviceProvider(ctx, p, base)
	if err != nil {
		return "", "", err
	}

	raw, err := base64.StdEncoding.DecodeString(samlResponse)
	if err != nil {
		return "", "", refused("the SAML response is not base64")
	}

	assertion, err := sp.ParseXMLResponse(raw, []string{
		req.Nonce,
	}, sp.AcsURL)
	if err != nil {
		var invalid *saml.InvalidResponseError
		if errors.As(err, &invalid) {
			err = invalid.PrivateErr
		}

		s.log.WarnContext(ctx, "SAML response refused", "provider", name, "error", err)

		return "", "", refused("the identity provider's SAML response is not valid")
	}

	id, err := assertedIdentity(p, assertion)
	if err != nil {
		return "", "", err
	}

	username, err := s.provision(ctx, p, id)

	return username, req.Redirect, err
}

// SAMLMetadata returns Relic's service-provider metadata for provider name,
// to give the identity provider.
func (s *Service) SAMLMetadata(ctx context.Context, name, base string) ([]byte, error) {
	p, err := s.Provider(ctx, name)
	if err != nil {
		return nil, err
	}

	if p.SAML == nil {
		return nil, fmt.Errorf("%w: %s is not a SAML provider", ErrNotFound, name)
	}

	sp, err := s.serviceProvider(ctx, p, base)
	if err != nil {
		return nil, err
	}

	out, err := xml.MarshalIndent(sp.Metadata(), "", "  ")
	if err != nil {
		return nil, err
	}

	return append([]byte(xml.Header), out...), nil
}

func (s *Service) serviceProvider(ctx context.Context, p Provider, base string) (*saml.ServiceProvider, error) {
	key, cert, err := parseKeyPair(p.SAML.Key, p.SAML.Certificate)
	if err != nil {
		return nil, fmt.Errorf("identity provider %s: %w", p.Name, err)
	}

	idp, err := s.idpMetadata(ctx, p)
	if err != nil {
		return nil, err
	}

	metadataURL, err := url.Parse(base + "/metadata")
	if err != nil {
		return nil, err
	}

	acsURL, err := url.Parse(base + "/acs")
	if err != nil {
		return nil, err
	}

	return &saml.ServiceProvider{
		EntityID:          metadataURL.String(),
		Key:               key,
		Certificate:       cert,
		HTTPClient:        s.client,
		MetadataURL:       *metadataURL,
		AcsURL:            *acsURL,
		IDPMetadata:       idp,
		AuthnNameIDFormat: saml.UnspecifiedNameIDFormat,
		SignatureMethod:   dsig.RSASHA256SignatureMethod,
	}, nil
}

func parseKeyPair(keyPEM, certPEM string) (crypto.Signer, *x509.Certificate, error) {
	kb, _ := pem.Decode([]byte(keyPEM))
	cb, _ := pem.Decode([]byte(certPEM))

	if kb == nil || cb == nil {
		return nil, nil, errors.New("the SAML key pair is missing; save the provider again")
	}

	parsed, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse SAML key: %w", err)
	}

	key, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, nil, errors.New("the SAML key cannot sign")
	}

	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse SAML certificate: %w", err)
	}

	return key, cert, nil
}

func (s *Service) idpMetadata(ctx context.Context, p Provider) (*saml.EntityDescriptor, error) {
	raw := []byte(p.SAML.MetadataXML)
	if len(raw) == 0 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.SAML.MetadataURL, nil)
		if err != nil {
			return nil, fmt.Errorf("%w: metadata URL %q", ErrInvalid, p.SAML.MetadataURL)
		}

		resp, err := s.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch the metadata of identity provider %s: %w", p.Name, err)
		}

		defer func() {
			_ = resp.Body.Close()
		}()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetch the metadata of identity provider %s: %s", p.Name, resp.Status)
		}

		if raw, err = io.ReadAll(io.LimitReader(resp.Body, maxMetadataSize)); err != nil {
			return nil, fmt.Errorf("read the metadata of identity provider %s: %w", p.Name, err)
		}
	}

	return parseIDPMetadata(raw)
}

// parseIDPMetadata reads an EntityDescriptor, or the first one with an
// identity provider in an EntitiesDescriptor.
func parseIDPMetadata(raw []byte) (*saml.EntityDescriptor, error) {
	var entity saml.EntityDescriptor
	if err := xml.Unmarshal(raw, &entity); err == nil && len(entity.IDPSSODescriptors) > 0 {
		return &entity, nil
	}

	var entities saml.EntitiesDescriptor
	if err := xml.Unmarshal(raw, &entities); err != nil {
		return nil, fmt.Errorf("%w: the identity provider's metadata does not parse: %w", ErrInvalid, err)
	}

	for i := range entities.EntityDescriptors {
		if len(entities.EntityDescriptors[i].IDPSSODescriptors) > 0 {
			return &entities.EntityDescriptors[i], nil
		}
	}

	return nil, fmt.Errorf("%w: the metadata describes no identity provider", ErrInvalid)
}

func assertedIdentity(p Provider, a *saml.Assertion) (identity, error) {
	values := map[string][]string{}

	for _, statement := range a.AttributeStatements {
		for _, attr := range statement.Attributes {
			for _, v := range attr.Values {
				for _, key := range []string{
					attr.Name,
					attr.FriendlyName,
				} {
					if key != "" {
						values[key] = append(values[key], v.Value)
					}
				}
			}
		}
	}

	first := func(names ...string) string {
		for _, n := range names {
			for _, v := range values[n] {
				if v != "" {
					return v
				}
			}
		}

		return ""
	}

	var username string
	if p.UsernameClaim != "" {
		username = first(p.UsernameClaim)
	} else if a.Subject != nil && a.Subject.NameID != nil {
		username = a.Subject.NameID.Value
	}

	if username == "" {
		return identity{}, refused("the SAML assertion names no user")
	}

	id := identity{
		username: strings.TrimSpace(username),
		email:    first(emailAttributes...),
	}
	if p.GroupsClaim != "" {
		id.groups = values[p.GroupsClaim]
	}

	return id, nil
}
