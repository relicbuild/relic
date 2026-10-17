package artifactory

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"relic/api/adapters/artifactory/wire"
	"relic/api/packages"
	"relic/internal/access/auth"
	"relic/internal/access/security"
	"relic/internal/artifacts/downloadstats"
	"relic/internal/artifacts/formats/docker"
	"relic/internal/artifacts/itemquery"
	"relic/internal/artifacts/repository"
	"relic/internal/artifacts/signingkey"
	"relic/internal/delivery/buildinfo"
	"relic/internal/delivery/replication"
	"relic/internal/platform/httperror"
	"relic/internal/platform/httpserver"
	"relic/internal/platform/objectstore"
	"relic/internal/platform/sealing"
	"relic/internal/platform/telemetry"
)

// Handler serves the adapter's REST API.
type Handler struct {
	repos        *repository.Service
	builds       *buildinfo.Service
	security     *security.Service
	tokens       *auth.Tokens
	enqueue      func(ctx context.Context, repoKey string) error
	secrets      *sealing.Box
	replications *replication.Service
	packages     *packages.Handler
	downloads    *downloadstats.Recorder
	docker       *docker.Service
	keys         *signingkey.Service
	aql          itemquery.Querier
	log          *slog.Logger
}

// New returns a Handler.
func New(
	repos *repository.Service,
	builds *buildinfo.Service,
	sec *security.Service,
	tokens *auth.Tokens,
	q itemquery.Querier,
	log *slog.Logger,
) *Handler {
	return &Handler{
		repos:    repos,
		builds:   builds,
		security: sec,
		tokens:   tokens,
		aql:      q,
		log:      log,
	}
}

// SetPackages wires the package protocols, the container images Docker
// promotion moves and the signing keys the key pair endpoints serve.
func (h *Handler) SetPackages(p *packages.Handler, images *docker.Service, keys *signingkey.Service) {
	h.packages, h.docker, h.keys = p, images, keys
}

// SetDownloads wires the download counts storage answers report.
func (h *Handler) SetDownloads(r *downloadstats.Recorder) { h.downloads = r }

// SetSecrets sets the box that seals stored credentials and the passwords
// clients keep in place of their own.
func (h *Handler) SetSecrets(b *sealing.Box) { h.secrets = b }

// SetReplicationQueue sets how replication runs are queued.
func (h *Handler) SetReplicationQueue(enqueue func(ctx context.Context, repoKey string) error) {
	h.enqueue = enqueue
}

// Register mounts the API on rt.
func (h *Handler) Register(rt *httpserver.Router) {
	rt.HandleFunc("GET /ui/", auth.Public, uiRedirect)
	registerNotProvided(rt)
	rt.HandleFunc("GET /artifactory/api/system/ping", auth.Public, h.ping)
	rt.HandleFunc("GET /artifactory/api/system/version", auth.Authenticated, h.version)
	rt.HandleFunc("POST /artifactory/api/system/usage", auth.Authenticated, h.usage)
	rt.HandleFunc("POST /jfconnect/api/v1/backoffice/metrics/log", auth.Authenticated, h.jfconnectMetrics)
	rt.HandleFunc("GET /artifactory/api/v1/uploads/config", auth.Authenticated, h.uploadsConfig)
	rt.HandleFunc("GET /artifactory/api/security/encryptedPassword", auth.Authenticated, h.encryptedPassword)
	// Public: refreshing carries no credentials; the handler authorizes creation.
	rt.HandleAudited("POST /artifactory/api/security/token", auth.Public, http.HandlerFunc(h.createToken))

	rt.HandleFunc("GET /artifactory/api/system/service_id", auth.Authenticated, h.serviceID)

	rt.HandleFunc("GET /artifactory/api/security/users", auth.Admin, h.listUsers)
	rt.HandleFunc("GET /artifactory/api/security/users/{name}", auth.Admin, h.getUser)
	rt.HandleFunc("PUT /artifactory/api/security/users/{name}", auth.Admin, h.putUser)
	rt.HandleFunc("POST /artifactory/api/security/users/{name}", auth.Admin, h.updateUser)
	rt.HandleFunc("DELETE /artifactory/api/security/users/{name}", auth.Admin, h.deleteUser)
	rt.HandleFunc("GET /artifactory/api/security/groups", auth.Admin, h.listGroups)
	rt.HandleFunc("GET /artifactory/api/security/groups/{name}", auth.Admin, h.getGroup)
	rt.HandleFunc("PUT /artifactory/api/security/groups/{name}", auth.Admin, h.putGroup)
	rt.HandleFunc("POST /artifactory/api/security/groups/{name}", auth.Admin, h.updateGroup)
	rt.HandleFunc("DELETE /artifactory/api/security/groups/{name}", auth.Admin, h.deleteGroup)
	rt.HandleFunc("GET /artifactory/api/v2/security/permissions", auth.Admin, h.listPermissionTargets)
	rt.HandleFunc("GET /artifactory/api/v2/security/permissions/{name}", auth.Admin, h.getPermissionTarget)
	rt.HandleFunc("POST /artifactory/api/v2/security/permissions/{name}", auth.Admin, h.createPermissionTarget)
	rt.HandleFunc("PUT /artifactory/api/v2/security/permissions/{name}", auth.Admin, h.updatePermissionTarget)
	rt.HandleFunc("DELETE /artifactory/api/v2/security/permissions/{name}", auth.Admin, h.deletePermissionTarget)

	rt.HandleFunc("GET /artifactory/api/repositories", auth.Authenticated, h.listRepositories)
	rt.HandleFunc("GET /artifactory/api/repositories/{$}", auth.Authenticated, h.listRepositories)
	// Admin only: remote repository configs carry upstream credentials.
	rt.HandleFunc("GET /artifactory/api/repositories/{key}", auth.Admin, h.getRepository)
	rt.HandleFunc("PUT /artifactory/api/repositories/{key}", auth.Admin, h.createRepository)
	rt.HandleFunc("POST /artifactory/api/repositories/{key}", auth.Admin, h.updateRepository)
	rt.HandleFunc("DELETE /artifactory/api/repositories/{key}", auth.Admin, h.deleteRepository)

	rt.HandleFunc("PUT /artifactory/api/v2/repositories/batch", auth.Admin, h.batchRepositories)
	rt.HandleFunc("POST /artifactory/api/v2/repositories/batch", auth.Admin, h.batchRepositories)
	rt.HandleFunc("GET /artifactory/api/replications/{repoKey}", auth.Admin, h.getReplication)
	rt.HandleFunc("PUT /artifactory/api/replications/{repoKey}", auth.Admin, h.putReplication)
	rt.HandleFunc("POST /artifactory/api/replications/{repoKey}", auth.Admin, h.updateReplication)
	rt.HandleFunc("DELETE /artifactory/api/replications/{repoKey}", auth.Admin, h.deleteReplication)
	rt.HandleFunc("GET /artifactory/api/replication/{repoKey}", auth.Admin, h.replicationStatus)
	rt.HandleFunc("POST /artifactory/api/replication/execute/{repoKey}", auth.Admin, h.executeReplication)

	rt.HandleFunc("POST /artifactory/api/search/aql", auth.Authenticated, h.searchAQL)
	rt.HandleFunc("GET /artifactory/api/storage/{path...}", auth.Authenticated, h.storageGet)
	rt.HandleFunc("PUT /artifactory/api/storage/{path...}", auth.Authenticated, h.storageSetProperties)
	rt.HandleFunc("DELETE /artifactory/api/storage/{path...}", auth.Authenticated, h.storageDeleteProperties)
	rt.HandleFunc(
		"GET /artifactory/api/security/keypair/public/repositories/{repo}",
		auth.Authenticated,
		h.repositoryPublicKey,
	)
	rt.HandleFunc("GET /artifactory/api/security/keypair/{name}/public", auth.Authenticated, h.keyPairPublic)
	rt.HandleFunc("GET /artifactory/api/gpg/key/public", auth.Authenticated, h.gpgPublicKey)
	rt.HandleFunc("POST /artifactory/api/trash/restore/{path...}", auth.Admin, h.restoreTrash)
	rt.HandleFunc("POST /artifactory/api/trash/empty", auth.Admin, h.emptyTrash)
	rt.HandleFunc("DELETE /artifactory/api/trash/clean/{path...}", auth.Admin, h.cleanTrash)
	rt.HandleFunc("POST /artifactory/api/copy/{path...}", auth.Authenticated, h.copyItem)
	rt.HandleFunc("POST /artifactory/api/move/{path...}", auth.Authenticated, h.moveItem)

	rt.HandleFunc("PUT /artifactory/api/build", auth.Authenticated, h.publishBuild)
	rt.HandleFunc("GET /artifactory/api/build", auth.Authenticated, h.getBuild)
	rt.HandleFunc("GET /artifactory/api/build/{rest...}", auth.Authenticated, h.getBuild)
	rt.HandleFunc("POST /artifactory/api/build/promote/{name}/{number}", auth.Authenticated, h.promoteBuild)
	rt.HandleFunc("POST /artifactory/api/build/retention/{name...}", auth.Authenticated, h.discardBuilds)
	rt.HandleFunc("POST /artifactory/api/build/delete", auth.Authenticated, h.deleteBuilds)
	rt.HandleFunc("GET /artifactory/api/builds/buildArtifacts/{rest...}", auth.Authenticated, h.buildArtifacts)

	rt.HandleFunc("GET /artifactory/api/npm/auth", auth.Authenticated, h.npmAuth)
	h.packages.RegisterNpm(rt, h.mount("/artifactory/api/npm/"))
	h.packages.RegisterTerraform(rt, h.mount("/artifactory/api/terraform/"))
	h.packages.RegisterGems(rt, h.mount("/artifactory/api/gems/"))
	h.packages.RegisterCargo(rt, h.mount("/artifactory/api/cargo/"))
	h.packages.RegisterNix(rt, h.mount("/artifactory/api/nix/"))
	h.packages.RegisterConan(rt, h.mount("/artifactory/api/conan/"))
	h.packages.RegisterHuggingFace(rt, h.mount("/artifactory/api/huggingfaceml/"))
	h.packages.RegisterNuGet(rt, h.mount("/artifactory/api/nuget/"))
	h.packages.RegisterHelm(rt, h.mount("/artifactory/api/helm/"))
	h.packages.RegisterPyPI(rt, h.mount("/artifactory/api/pypi/"))
	h.packages.RegisterGo(rt, h.mount("/artifactory/api/go/"))
	rt.HandleFunc("/artifactory/api/docker/", auth.Public, h.dockerAPI)

	rt.HandleFunc(curationAuditPrefix, auth.Authenticated, curationAudit(rt))
	rt.HandleFunc("/artifactory/", auth.Authenticated, h.files)
}

// Unexpected errors are logged with the request ID and reported as 500 without details.
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := http.StatusInternalServerError, "internal server error"

	if code, ok := isSecurityError(err); ok {
		wire.WriteError(w, code, err.Error())
		return
	}

	if code, ok := isBuildError(err); ok {
		wire.WriteError(w, code, err.Error())
		return
	}

	switch {
	case errors.Is(err, repository.ErrForbidden), errors.Is(err, repository.ErrCoolingDown):
		status, msg = http.StatusForbidden, err.Error()
	case errors.Is(err, repository.ErrRepositoryNotFound), errors.Is(err, repository.ErrNotFound):
		status, msg = http.StatusNotFound, err.Error()
	case errors.Is(err, repository.ErrRepositoryExists), errors.Is(err, repository.ErrConflict),
		errors.Is(err, repository.ErrHeld), errors.Is(err, repository.ErrImmutable):
		status, msg = http.StatusConflict, err.Error()
	case errors.Is(err, repository.ErrInvalidPath),
		errors.Is(err, repository.ErrInvalidRepository),
		errors.Is(err, repository.ErrNotLocal),
		errors.Is(err, repository.ErrNotAFile),
		errors.Is(err, repository.ErrSelfCopy),
		errors.Is(err, repository.ErrUnsupportedArchive):
		status, msg = http.StatusBadRequest, err.Error()
	case errors.Is(err, repository.ErrUpstream):
		status, msg = http.StatusBadGateway, "remote repository upstream is unavailable"

		h.log.WarnContext(
			r.Context(),
			"remote upstream failed",
			"request_id",
			httpserver.RequestID(r.Context()),
			"error",
			err,
		)
	case errors.Is(err, repository.ErrChecksumMismatch):
		status, msg = http.StatusConflict, err.Error()
	case errors.Is(err, repository.ErrTooLarge), errors.Is(err, repository.ErrQuotaExceeded):
		status, msg = http.StatusRequestEntityTooLarge, err.Error()
	case errors.Is(err, objectstore.ErrStorageFull):
		status, msg = http.StatusInsufficientStorage, objectstore.ErrStorageFull.Error()
	default:
		h.log.ErrorContext(r.Context(), "request failed", "request_id", httpserver.RequestID(r.Context()), "error", err)
	}

	wire.WriteError(w, status, msg)
}

// PathPrefixes are the paths the adapter's APIs serve.
var PathPrefixes = []string{
	"/artifactory/",
	"/xray/",
	"/access/",
	"/lifecycle/",
	"/evidence/",
	"/v2/",
	"/jfconnect/",
	"/onemodel/",
	"/.well-known/",
}

var families = map[string]string{
	"/artifactory/": "packages",
	"/.well-known/": "packages",
	"/v2/":          "container-registry",
	"/xray/":        "scanning",
	"/access/":      "access",
	"/jfconnect/":   "access",
	"/lifecycle/":   "releases",
	"/evidence/":    "evidence",
	"/onemodel/":    "evidence",
}

// RegisterPaths makes the HTTP layer answer errors under PathPrefixes in the
// adapter's error envelope, and count their requests toward Relic's API families.
func RegisterPaths() {
	for _, p := range PathPrefixes {
		httperror.Register(p, wire.WriteError)
		telemetry.RegisterFamily(p, families[p])
	}
}

// ConfigureAuth teaches the authenticator what the adapter's clients send.
func ConfigureAuth(a *auth.Authenticator) {
	a.AcceptAPIKey("X-NuGet-ApiKey", "/artifactory/api/nuget/")
	a.AllowExpiredBearer("/artifactory/api/security/token")
	a.AllowExpiredBearer("/access/api/v1/tokens")
}

// RouteTag groups the adapter's routes in the route inventory by API and
// resource.
func RouteTag(pattern string) string {
	segs := strings.Split(strings.Trim(pattern, "/"), "/")

	at := func(i int) string {
		if i < len(segs) && !strings.HasPrefix(segs[i], "{") {
			return segs[i]
		}

		return ""
	}
	switch at(0) {
	case "artifactory":
		if at(1) == "api" && at(2) != "" {
			return "Artifactory: " + at(2)
		}

		return "Artifactory: files"
	case "v2":
		return "Docker registry"
	case "xray", "access", "lifecycle", "evidence":
		return strings.ToUpper(at(0)[:1]) + at(0)[1:]
	case "onemodel":
		return "Evidence"
	case "jfconnect":
		return "JFrog Connect"
	case ".well-known":
		return "Terraform"
	}

	return ""
}

var notProvided = map[string]string{
	"/pipelines/":    "JFrog Pipelines",
	"/worker/":       "JFrog Workers",
	"/apptrust/":     "JFrog AppTrust",
	"/mc/":           "JFrog Mission Control",
	"/distribution/": "JFrog Distribution",
}

func registerNotProvided(rt *httpserver.Router) {
	for prefix, product := range notProvided {
		rt.HandleFunc(prefix, auth.Authenticated, func(w http.ResponseWriter, _ *http.Request) {
			wire.WriteError(w, http.StatusNotImplemented, "Relic does not provide "+product)
		})
	}
}

var signInParams = map[string]string{
	"jfClientSession": "cliSession",
	"jfClientName":    "cliName",
}

func uiRedirect(w http.ResponseWriter, r *http.Request) {
	// TrimLeft keeps "/ui//host" and "/ui/\host" from becoming a
	// protocol-relative redirect to another host.
	target := "/" + strings.TrimLeft(strings.TrimPrefix(r.URL.Path, "/ui/"), "/\\")
	q := url.Values{}

	for k, v := range r.URL.Query() {
		switch renamed, ok := signInParams[k]; {
		case ok:
			q[renamed] = v
		case strings.HasPrefix(k, "jfClient"):
		default:
			q[k] = v
		}
	}

	if len(q) > 0 {
		target += "?" + q.Encode()
	}

	http.Redirect(w, r, target, http.StatusMovedPermanently) //nolint:gosec // G710: a path on this host, see above
}

const curationAuditPrefix = "/artifactory/api/curation/audit/"

func curationAudit(rt *httpserver.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/artifactory/" + strings.TrimPrefix(r.URL.Path, curationAuditPrefix)
		r2.URL.RawPath = ""
		rt.ServeHTTP(w, r2)
	}
}
