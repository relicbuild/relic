package main

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"

	"relic/api/gen/relic/v1/relicv1connect"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
)

type client struct {
	url  string
	http *http.Client

	artifacts          relicv1connect.ArtifactServiceClient
	builds             relicv1connect.BuildServiceClient
	evidence           relicv1connect.EvidenceServiceClient
	ldap               relicv1connect.LdapServiceClient
	packages           relicv1connect.PackageServiceClient
	releases           relicv1connect.ReleaseServiceClient
	replications       relicv1connect.ReplicationServiceClient
	cleanup            relicv1connect.CleanupServiceClient
	holds              relicv1connect.HoldServiceClient
	audit              relicv1connect.AuditServiceClient
	identityProviders  relicv1connect.IdentityProviderServiceClient
	workloadIdentities relicv1connect.WorkloadIdentityServiceClient
	quarantine         relicv1connect.QuarantineServiceClient
	waiver             relicv1connect.WaiverServiceClient
	promotionGates     relicv1connect.PromotionGateServiceClient
	policyDecisions    relicv1connect.PolicyDecisionServiceClient
	webhooks           relicv1connect.WebhookServiceClient
	projects           relicv1connect.ProjectServiceClient
	agentRegistry      relicv1connect.AgentRegistryServiceClient
	repositories       relicv1connect.RepositoryServiceClient
	scans              relicv1connect.ScanServiceClient
	security           relicv1connect.SecurityServiceClient
	sessions           relicv1connect.SessionServiceClient
	signingKeys        relicv1connect.SigningKeyServiceClient
	system             relicv1connect.SystemServiceClient
}

type credentials struct {
	token, user, password string
	next                  http.RoundTripper
}

func (c credentials) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())

	switch {
	case c.token != "":
		r.Header.Set("Authorization", "Bearer "+c.token)
	case c.user != "":
		r.SetBasicAuth(c.user, c.password)
	}

	return c.next.RoundTrip(r)
}

func newClient(url, certificateAuthority string, creds credentials) *client {
	creds.next = http.DefaultTransport
	if certificateAuthority != "" {
		creds.next = trustingTransport(certificateAuthority)
	}
	// No overall timeout: uploads and downloads may be large.
	hc := &http.Client{
		Transport: creds,
	}
	api := url + "/api"

	rpc := connect.NewClient(
		connecthttp.NewTransport(
			hc,
			api,
			connecthttp.WithReadMaxBytes(0),
		),
	)

	return &client{
		url:                url,
		http:               hc,
		artifacts:          relicv1connect.NewArtifactServiceClient(rpc),
		builds:             relicv1connect.NewBuildServiceClient(rpc),
		evidence:           relicv1connect.NewEvidenceServiceClient(rpc),
		ldap:               relicv1connect.NewLdapServiceClient(rpc),
		packages:           relicv1connect.NewPackageServiceClient(rpc),
		releases:           relicv1connect.NewReleaseServiceClient(rpc),
		replications:       relicv1connect.NewReplicationServiceClient(rpc),
		cleanup:            relicv1connect.NewCleanupServiceClient(rpc),
		holds:              relicv1connect.NewHoldServiceClient(rpc),
		audit:              relicv1connect.NewAuditServiceClient(rpc),
		identityProviders:  relicv1connect.NewIdentityProviderServiceClient(rpc),
		workloadIdentities: relicv1connect.NewWorkloadIdentityServiceClient(rpc),
		quarantine:         relicv1connect.NewQuarantineServiceClient(rpc),
		waiver:             relicv1connect.NewWaiverServiceClient(rpc),
		promotionGates:     relicv1connect.NewPromotionGateServiceClient(rpc),
		policyDecisions:    relicv1connect.NewPolicyDecisionServiceClient(rpc),
		webhooks:           relicv1connect.NewWebhookServiceClient(rpc),
		projects:           relicv1connect.NewProjectServiceClient(rpc),
		agentRegistry:      relicv1connect.NewAgentRegistryServiceClient(rpc),
		repositories:       relicv1connect.NewRepositoryServiceClient(rpc),
		scans:              relicv1connect.NewScanServiceClient(rpc),
		security:           relicv1connect.NewSecurityServiceClient(rpc),
		sessions:           relicv1connect.NewSessionServiceClient(rpc),
		signingKeys:        relicv1connect.NewSigningKeyServiceClient(rpc),
		system:             relicv1connect.NewSystemServiceClient(rpc),
	}
}

func (g *globals) dial() (*client, error) {
	target, err := g.target()
	if err != nil {
		return nil, err
	}

	return newClient(target.url, target.certificateAuthority, target.credentials()), nil
}

func trustingTransport(certificateAuthority string) *http.Transport {
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}

	roots.AppendCertsFromPEM([]byte(certificateAuthority))

	transport, _ := http.DefaultTransport.(*http.Transport)
	transport = transport.Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    roots,
	}

	return transport
}
