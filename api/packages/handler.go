// Package packages serves the package managers' own protocols (npm, PyPI,
// Maven and the raw repository layout, Go, NuGet, RubyGems, Cargo, Conan,
// Helm, Terraform, Hugging Face, Nix, Debian, RPM, Alpine and the container
// registry) over Relic's repositories.
package packages

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"

	"relic/internal/access/auth"
	"relic/internal/access/security"
	"relic/internal/artifacts/formats/alpine"
	"relic/internal/artifacts/formats/cargo"
	"relic/internal/artifacts/formats/conan"
	"relic/internal/artifacts/formats/debian"
	"relic/internal/artifacts/formats/docker"
	"relic/internal/artifacts/formats/gems"
	"relic/internal/artifacts/formats/gomod"
	"relic/internal/artifacts/formats/helm"
	"relic/internal/artifacts/formats/huggingface"
	"relic/internal/artifacts/formats/maven"
	"relic/internal/artifacts/formats/nix"
	"relic/internal/artifacts/formats/npm"
	"relic/internal/artifacts/formats/nuget"
	"relic/internal/artifacts/formats/pypi"
	"relic/internal/artifacts/formats/rpm"
	"relic/internal/artifacts/formats/terraform"
	"relic/internal/artifacts/repository"
	"relic/internal/artifacts/signingkey"
	"relic/internal/artifacts/usage"
	"relic/internal/platform/httperror"
	"relic/internal/platform/httpserver"
	"relic/internal/platform/localdisk"
	"relic/internal/platform/objectstore"
	"relic/internal/scanning/scan/blocking"
)

// Dependencies are the services the protocols are served from.
type Dependencies struct {
	Repositories       *repository.Service
	Security           *security.Service
	Tokens             *auth.Tokens
	Authenticator      *auth.Authenticator
	Npm                *npm.Service
	Maven              *maven.Service
	Docker             *docker.Service
	Go                 *gomod.Service
	PyPI               *pypi.Service
	Helm               *helm.Service
	NuGet              *nuget.Service
	Gems               *gems.Service
	Cargo              *cargo.Service
	Conan              *conan.Service
	HuggingFace        *huggingface.Service
	Alpine             *alpine.Service
	Debian             *debian.Service
	RPM                *rpm.Service
	Nix                *nix.Service
	TerraformProviders *terraform.Service
	Blocker            *blocking.Blocker
	Downloads          *usage.ItemDownloads
	Log                *slog.Logger
}

// Handler serves the package protocols.
type Handler struct {
	repos       *repository.Service
	security    *security.Service
	tokens      *auth.Tokens
	authn       *auth.Authenticator
	npm         *npm.Service
	maven       *maven.Service
	docker      *docker.Service
	gomod       *gomod.Service
	pypi        *pypi.Service
	helm        *helm.Service
	nuget       *nuget.Service
	gems        *gems.Service
	cargo       *cargo.Service
	conan       *conan.Service
	hf          *huggingface.Service
	alpine      *alpine.Service
	debian      *debian.Service
	rpm         *rpm.Service
	nix         *nix.Service
	tfProviders *terraform.Service
	blocker     *blocking.Blocker
	downloads   *usage.ItemDownloads
	buffers     *bufferPool
	log         *slog.Logger
}

// New returns a Handler serving from d.
func New(d Dependencies) *Handler {
	return &Handler{
		repos:       d.Repositories,
		security:    d.Security,
		tokens:      d.Tokens,
		authn:       d.Authenticator,
		npm:         d.Npm,
		maven:       d.Maven,
		docker:      d.Docker,
		gomod:       d.Go,
		pypi:        d.PyPI,
		helm:        d.Helm,
		nuget:       d.NuGet,
		gems:        d.Gems,
		cargo:       d.Cargo,
		conan:       d.Conan,
		hf:          d.HuggingFace,
		alpine:      d.Alpine,
		debian:      d.Debian,
		rpm:         d.RPM,
		nix:         d.Nix,
		tfProviders: d.TerraformProviders,
		blocker:     d.Blocker,
		downloads:   d.Downloads,
		buffers:     newBufferPool(bufferBudget(debug.SetMemoryLimit(-1))),
		log:         d.Log,
	}
}

// Mount is where a protocol is served and what the API serving it there
// adds to it.
type Mount struct {
	Prefix          string
	Files           func(w http.ResponseWriter, r *http.Request, escapedRepoPath string)
	FileHeaders     func(h http.Header, item repository.Item)
	RegistryHeaders func(h http.Header, repo string)
}

func (m Mount) fileHeaders(h http.Header, item repository.Item) {
	if m.FileHeaders != nil {
		m.FileHeaders(h, item)
	}
}

func origin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}

	return scheme + "://" + r.Host
}

func (m Mount) base(r *http.Request) string {
	return origin(r) + strings.TrimSuffix(m.Prefix, "/")
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}

	return strings.Join(parts, "/")
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := http.StatusInternalServerError, "internal server error"

	switch {
	case errors.Is(err, repository.ErrForbidden), errors.Is(err, repository.ErrCoolingDown):
		status, msg = http.StatusForbidden, err.Error()
	case errors.Is(err, repository.ErrRepositoryNotFound), errors.Is(err, repository.ErrNotFound):
		status, msg = http.StatusNotFound, err.Error()
	case errors.Is(err, repository.ErrRepositoryExists), errors.Is(err, repository.ErrRepositoryKeyCaseConflict),
		errors.Is(err, repository.ErrConflict),
		errors.Is(err, repository.ErrHeld), errors.Is(err, repository.ErrImmutable):
		status, msg = http.StatusConflict, err.Error()
	case errors.Is(err, repository.ErrInvalidPath),
		errors.Is(err, repository.ErrInvalidProperty),
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
	case errors.Is(err, repository.ErrChecksumMismatch), errors.Is(err, signingkey.ErrKind):
		status, msg = http.StatusConflict, err.Error()
	case errors.Is(err, repository.ErrTooLarge), errors.Is(err, repository.ErrQuotaExceeded):
		status, msg = http.StatusRequestEntityTooLarge, err.Error()
	case errors.Is(err, objectstore.ErrStorageFull):
		status, msg = http.StatusInsufficientStorage, objectstore.ErrStorageFull.Error()
	case errors.Is(err, localdisk.ErrFull):
		status, msg = http.StatusInsufficientStorage, localdisk.ErrFull.Error()
	case errors.Is(err, repository.ErrContentMissing):
		msg = repository.ErrContentMissing.Error()
	default:
		h.log.ErrorContext(r.Context(), "request failed", "request_id", httpserver.RequestID(r.Context()), "error", err)
	}

	httperror.Write(w, r, status, msg)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
