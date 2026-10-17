// Package relicapi serves Relic's own API (api/proto/relic/v1) with
// ConnectRPC: the Connect protocol (JSON or binary over HTTP), gRPC and
// gRPC-Web, under /api/.
package relicapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"uuid"

	"connectrpc.com/connect"
	"connectrpc.com/vanguard"

	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/access/audit"
	"relic/internal/access/auth"
	"relic/internal/access/ldap"
	"relic/internal/access/project"
	"relic/internal/access/security"
	"relic/internal/access/sso"
	"relic/internal/access/workload"
	"relic/internal/artifacts/cleanup"
	"relic/internal/artifacts/downloadstats"
	"relic/internal/artifacts/formats/agentregistry"
	"relic/internal/artifacts/formats/docker"
	"relic/internal/artifacts/formats/helm"
	"relic/internal/artifacts/formats/npm"
	"relic/internal/artifacts/formats/pypi"
	"relic/internal/artifacts/repository"
	"relic/internal/artifacts/signingkey"
	"relic/internal/artifacts/usage"
	"relic/internal/delivery/buildinfo"
	"relic/internal/delivery/evidence"
	"relic/internal/delivery/release"
	"relic/internal/delivery/replication"
	"relic/internal/delivery/webhook"
	"relic/internal/scanning/cvecatalog"
	"relic/internal/scanning/endoflife"
	"relic/internal/scanning/malicious"
	"relic/internal/scanning/scan"
	"relic/internal/scanning/scan/blocking"
	"relic/internal/scanning/scan/policy"
	"relic/internal/systemstatus"
)

// Prefix is where the API is mounted; a method's path is
// Prefix + "relic.v1.<Service>/<Method>".
const Prefix = "/api/"

// HealthCheck probes one dependency; Check returns nil when it answers.
type HealthCheck struct {
	Name  string
	Check func(context.Context) error
}

// Dependencies are the services the API is built on.
type Dependencies struct {
	Version, Revision string
	// SignInNote is shown on the sign-in page; none when empty.
	SignInNote string

	Authenticator          *auth.Authenticator
	Tokens                 *auth.Tokens
	WebLogins              *auth.WebLogins
	Repositories           *repository.Service
	Builds                 *buildinfo.Service
	Releases               *release.Service
	Evidence               *evidence.Service
	SigningKeys            *signingkey.Service
	Security               *security.Service
	LDAP                   *ldap.Service
	Replications           *replication.Service
	Cleanup                *cleanup.Service
	ScanResults            *scan.Results
	Scans                  *scan.Service
	CVEs                   *cvecatalog.Catalog
	ScanPolicies           *policy.Admin
	PolicyDecisions        *policy.Decisions
	Webhooks               *webhook.Service
	Projects               *project.Service
	AgentRegistry          *agentregistry.Service
	Usage                  *usage.Service
	ContainerImages        *docker.Service
	NpmPackages            *npm.Service
	PythonPackages         *pypi.Service
	MaliciousPackages      *malicious.Catalog
	EndOfLife              *endoflife.Catalog
	EndOfLifeData          *endoflife.Refresher
	HelmCharts             *helm.Service
	DownloadBlocker        *blocking.Blocker
	DownloadStats          *downloadstats.Recorder
	SystemStatus           *systemstatus.Service
	HealthChecks           []HealthCheck
	ClientEndpoints        func(origin string, r repository.Repository) (packageManager, containerRegistry string)
	QueueGarbageCollection func(context.Context) error
	QueueStorageCheck      func(context.Context) (int64, error)
	QueueReplication       func(context.Context, string) error
	QueueGraphScan         func(context.Context, uuid.UUID) error
	// QueueRematch matches a content's SBOM again.
	QueueRematch                     func(context.Context, string) error
	VulnerabilityDatabases           *scan.DatabaseImports
	QueueVulnerabilityDatabaseImport func(context.Context, string) error
	// QueueOperationalRiskCheck assesses stored scans again after the
	// end-of-life data changed.
	QueueOperationalRiskCheck func(context.Context) error

	Audit *audit.Service
	SSO   *sso.Service
	// Workloads exchange CI jobs' OIDC tokens for Relic tokens.
	Workloads *workload.Service

	Log *slog.Logger
	// Interceptors run before the access check.
	Interceptors []connect.Interceptor
}

func (d Dependencies) auditRecorder() audit.Recorder {
	if d.Audit == nil {
		return nil
	}

	return d.Audit
}

// Handler returns the API, to be mounted at Prefix.
func Handler(d Dependencies) http.Handler {
	opts := connect.WithHandlerOptions(
		connect.WithInterceptors(
			append(
				slices.Clone(d.Interceptors),
				auditInterceptor(d.auditRecorder()),
				accessInterceptor(),
				errorInterceptor(d.Log),
			)...,
		),
		strictJSONCodecs(),
	)

	var services []*vanguard.Service

	mount := func(path string, h http.Handler) {
		services = append(services, vanguard.NewService(path, h))
	}
	mount(relicv1connect.NewSystemServiceHandler(&systemServer{
		d,
	}, opts))
	mount(relicv1connect.NewSessionServiceHandler(&sessionServer{
		d,
	}, opts))
	mount(relicv1connect.NewRepositoryServiceHandler(&repositoryServer{
		d,
	}, opts))
	mount(relicv1connect.NewSecurityServiceHandler(&securityServer{
		d,
	}, opts))
	mount(relicv1connect.NewLdapServiceHandler(&ldapServer{
		d,
	}, opts))
	mount(relicv1connect.NewReplicationServiceHandler(&replicationServer{
		d,
	}, opts))
	mount(relicv1connect.NewCleanupServiceHandler(&cleanupServer{
		d,
	}, opts))
	mount(relicv1connect.NewHoldServiceHandler(&holdServer{
		d,
	}, opts))
	mount(relicv1connect.NewAuditServiceHandler(&auditServer{
		d,
	}, opts))
	mount(relicv1connect.NewIdentityProviderServiceHandler(&identityProviderServer{
		d,
	}, opts))
	mount(relicv1connect.NewWorkloadIdentityServiceHandler(&workloadIdentityServer{
		d,
	}, opts))
	mount(relicv1connect.NewQuarantineServiceHandler(&quarantineServer{
		d,
	}, opts))
	mount(relicv1connect.NewWaiverServiceHandler(&waiverServer{
		d,
	}, opts))
	mount(relicv1connect.NewPromotionGateServiceHandler(&promotionGateServer{
		d,
	}, opts))
	mount(relicv1connect.NewPolicyDecisionServiceHandler(&policyDecisionServer{
		d,
	}, opts))
	mount(relicv1connect.NewWebhookServiceHandler(&webhookServer{
		d,
	}, opts))
	mount(relicv1connect.NewProjectServiceHandler(&projectServer{
		d,
	}, opts))
	mount(relicv1connect.NewAgentRegistryServiceHandler(&agentRegistryServer{
		d,
	}, opts))
	mount(relicv1connect.NewBuildServiceHandler(&buildServer{
		d,
	}, opts))
	mount(relicv1connect.NewArtifactServiceHandler(&artifactServer{
		d,
	}, opts))
	mount(relicv1connect.NewScanServiceHandler(&scanServer{
		d,
	}, opts))
	mount(relicv1connect.NewPackageServiceHandler(&packageServer{
		d,
	}, opts))
	mount(relicv1connect.NewTrashServiceHandler(&trashServer{
		d,
	}, opts))
	mount(relicv1connect.NewReleaseServiceHandler(&releaseServer{
		d,
	}, opts))
	mount(relicv1connect.NewEvidenceServiceHandler(&evidenceServer{
		d,
	}, opts))
	mount(relicv1connect.NewSigningKeyServiceHandler(&keyServer{
		d,
	}, opts))

	api, err := vanguard.NewTranscoder(services)
	if err != nil {
		panic(fmt.Sprintf("relic api: %v", err))
	}

	mux := http.NewServeMux()
	mux.HandleFunc(FilesPath, d.files)
	mux.HandleFunc(ArchivePath, d.archiveEntry)
	mux.HandleFunc(SBOMPath, d.sbom)
	mux.HandleFunc(MCPRegistryPath, d.mcpRegistry)
	mux.HandleFunc("POST "+ReleaseImportPath, d.importRelease)
	mux.HandleFunc("PUT "+VulnerabilityDatabasePath, d.importVulnerabilityDatabase)
	mux.HandleFunc("PUT "+EndOfLifeDataPath, d.importEndOfLifeData)
	mux.HandleFunc("GET "+SSOPath+"{provider}/start", d.ssoStart)
	mux.HandleFunc("GET "+SSOPath+"{provider}/callback", d.ssoCallback)
	mux.HandleFunc("POST "+SSOPath+"{provider}/acs", d.ssoAssertion)
	mux.HandleFunc("GET "+SSOPath+"{provider}/metadata", d.ssoMetadata)
	mux.Handle(Prefix, connectPaths(api))

	return withRequest(mux)
}

// connectPaths routes Connect requests under Prefix, and hands REST requests
// their path as sent: the transcoder unescapes path variables itself, so a
// name holding an escaped '/' stays one segment.
func connectPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if escaped := r.URL.EscapedPath(); escaped != r.URL.Path {
			r2 := r.Clone(r.Context())
			r2.URL.Path, r2.URL.RawPath = escaped, ""
			r = r2
		}

		if strings.HasPrefix(r.URL.Path, Prefix+"relic.v1.") {
			r2 := r.Clone(r.Context())
			r2.URL.Path = strings.TrimPrefix(r.URL.Path, strings.TrimSuffix(Prefix, "/"))
			r2.URL.RawPath = ""

			ct := r2.Header.Get("Content-Type")
			if r2.Header.Get("Connect-Protocol-Version") == "" && !strings.HasPrefix(ct, "application/grpc") &&
				!strings.HasPrefix(ct, "application/connect+") {
				r2.Header.Set("Connect-Protocol-Version", "1")
			}

			r = r2
		}

		next.ServeHTTP(w, r)
	})
}

type requestKey struct{}

func withRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestKey{}, r)))
	})
}

func httpRequest(ctx context.Context) *http.Request {
	r, _ := ctx.Value(requestKey{}).(*http.Request)
	return r
}
