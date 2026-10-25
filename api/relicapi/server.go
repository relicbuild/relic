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

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/connect/v2/connectproto"
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
	DownloadStats          *usage.ItemDownloads
	SystemStatus           *systemstatus.Service
	HealthChecks           []HealthCheck
	ClientEndpoints        func(origin string, r repository.Repository) (packageManager, containerRegistry string)
	QueueGarbageCollection func(context.Context) error
	QueueStorageCheck      func(context.Context) (int64, error)
	QueueReplication       func(context.Context, string) error
	QueueGraphScan         func(context.Context, uuid.UUID) error
	// GraphScanWorkers reports whether a worker runs the graph scans
	// QueueGraphScan queues.
	GraphScanWorkers func(context.Context) (bool, error)
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
	Interceptors []connect.ServerInterceptor
}

func (d Dependencies) auditRecorder() audit.Recorder {
	if d.Audit == nil {
		return nil
	}

	return d.Audit
}

// serverInterceptors run on every call, the first outermost.
func (d Dependencies) serverInterceptors(recorder audit.Recorder) []connect.ServerInterceptor {
	return append(
		slices.Clone(d.Interceptors),
		receiveRequest(),
		auditInterceptor(recorder),
		accessInterceptor(),
		errorInterceptor(d.Log),
	)
}

// maxRequestBytes bounds a Relic API request message.
const maxRequestBytes = 64 << 20

// Handler returns the API, to be mounted at Prefix.
func Handler(d Dependencies) http.Handler {
	server := connect.NewServer(d.serverInterceptors(d.auditRecorder())...)
	relicv1connect.RegisterSystemServiceHandler(server, &systemServer{
		d,
	})
	relicv1connect.RegisterSessionServiceHandler(server, &sessionServer{
		d,
	})
	relicv1connect.RegisterRepositoryServiceHandler(server, &repositoryServer{
		d,
	})
	relicv1connect.RegisterSecurityServiceHandler(server, &securityServer{
		d,
	})
	relicv1connect.RegisterLdapServiceHandler(server, &ldapServer{
		d,
	})
	relicv1connect.RegisterReplicationServiceHandler(server, &replicationServer{
		d,
	})
	relicv1connect.RegisterCleanupServiceHandler(server, &cleanupServer{
		d,
	})
	relicv1connect.RegisterHoldServiceHandler(server, &holdServer{
		d,
	})
	relicv1connect.RegisterAuditServiceHandler(server, &auditServer{
		d,
	})
	relicv1connect.RegisterIdentityProviderServiceHandler(server, &identityProviderServer{
		d,
	})
	relicv1connect.RegisterWorkloadIdentityServiceHandler(server, &workloadIdentityServer{
		d,
	})
	relicv1connect.RegisterQuarantineServiceHandler(server, &quarantineServer{
		d,
	})
	relicv1connect.RegisterWaiverServiceHandler(server, &waiverServer{
		d,
	})
	relicv1connect.RegisterPromotionGateServiceHandler(server, &promotionGateServer{
		d,
	})
	relicv1connect.RegisterPolicyDecisionServiceHandler(server, &policyDecisionServer{
		d,
	})
	relicv1connect.RegisterWebhookServiceHandler(server, &webhookServer{
		d,
	})
	relicv1connect.RegisterProjectServiceHandler(server, &projectServer{
		d,
	})
	relicv1connect.RegisterAgentRegistryServiceHandler(server, &agentRegistryServer{
		d,
	})
	relicv1connect.RegisterBuildServiceHandler(server, &buildServer{
		d,
	})
	relicv1connect.RegisterArtifactServiceHandler(server, &artifactServer{
		d,
	})
	relicv1connect.RegisterScanServiceHandler(server, &scanServer{
		d,
	})
	relicv1connect.RegisterPackageServiceHandler(server, &packageServer{
		d,
	})
	relicv1connect.RegisterTrashServiceHandler(server, &trashServer{
		d,
	})
	relicv1connect.RegisterReleaseServiceHandler(server, &releaseServer{
		d,
	})
	relicv1connect.RegisterEvidenceServiceHandler(server, &evidenceServer{
		d,
	})
	relicv1connect.RegisterSigningKeyServiceHandler(server, &keyServer{
		d,
	})

	api := http.NewServeMux()
	connecthttp.Mount(
		api,
		server,
		connecthttp.WithCodecs(
			connectproto.NewBinaryCodec(),
			strictJSON{},
		),
		connecthttp.WithReadMaxBytes(maxRequestBytes),
	)

	err := vanguard.Mount(
		api,
		server,
		vanguard.WithCodec(strictRESTJSON),
		vanguard.WithMaxReadBytes(maxRequestBytes),
	)
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
	mux.Handle(Prefix, restDecodeErrors(connectPaths(api)))

	return withRequest(mux)
}

// connectPaths routes Connect, gRPC and gRPC-Web requests under Prefix to
// their procedures; REST requests keep their path.
func connectPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, Prefix+"relic.v1.") {
			r2 := r.Clone(r.Context())
			r2.URL.Path = strings.TrimPrefix(r.URL.Path, strings.TrimSuffix(Prefix, "/"))
			r2.URL.RawPath = ""
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
