package packages

import (
	"net/http"

	"relic/internal/access/auth"
	"relic/internal/platform/httpserver"
	"relic/internal/platform/telemetry"
)

func mounted(m Mount, serve func(http.ResponseWriter, *http.Request, Mount)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, m)
	}
}

// RegisterNpm serves the npm registry protocol.
func (h *Handler) RegisterNpm(rt *httpserver.Router, m Mount) {
	// npm login sends its credentials in the body, without Authorization.
	rt.HandleFunc("PUT "+m.Prefix+"{repo}/-/user/{user}", auth.Public, h.npmLogin)
	rt.HandleFunc(m.Prefix, auth.Authenticated, h.visibleListings(mounted(m, h.npmAPI)))
}

// RegisterTerraform serves the module registry and the provider network mirror at <prefix><repo>/providers/.
func (h *Handler) RegisterTerraform(rt *httpserver.Router, m Mount) {
	rt.HandleFunc("GET "+terraformModules(m), auth.Authenticated, mounted(m, h.terraformModule))
	rt.HandleFunc("GET "+terraformArchives(m), auth.Public, mounted(m, h.terraformArchive))
	rt.HandleFunc("GET "+providerArchives(m), auth.Public, mounted(m, h.terraformProviderArchive))
	// <repo>/providers/...: the routes above are more specific.
	rt.HandleFunc("GET "+m.Prefix, auth.Authenticated, h.visibleListings(mounted(m, h.terraformProviders)))
}

// RegisterGems serves RubyGems.
func (h *Handler) RegisterGems(rt *httpserver.Router, m Mount) {
	rt.HandleFunc(m.Prefix, auth.Authenticated, h.visibleListings(mounted(m, h.gemsAPI)))
}

// RegisterCargo serves Cargo's sparse registry.
func (h *Handler) RegisterCargo(rt *httpserver.Router, m Mount) {
	rt.HandleFunc(m.Prefix, auth.Authenticated, h.visibleListings(mounted(m, h.cargoAPI)))
}

// RegisterNix serves Nix binary caches.
func (h *Handler) RegisterNix(rt *httpserver.Router, m Mount) {
	rt.HandleFunc(m.Prefix, auth.Authenticated, mounted(m, h.nixAPI))
}

// RegisterConan serves Conan.
func (h *Handler) RegisterConan(rt *httpserver.Router, m Mount) {
	rt.HandleFunc("GET "+m.Prefix+"{repo}/v1/ping", auth.Public, h.conanPing)
	rt.HandleFunc(m.Prefix, auth.Authenticated, h.visibleListings(mounted(m, h.conanAPI)))
}

// RegisterHuggingFace serves the Hugging Face Hub API.
func (h *Handler) RegisterHuggingFace(rt *httpserver.Router, m Mount) {
	rt.HandleFunc("PUT "+m.Prefix+"{repo}/lfs/{oid}", auth.Public, h.hfLFSUpload)
	// The Xet CAS authenticates its own short-lived tokens.
	cas := m.Prefix + "{repo}" + hfXetCASPath
	rt.HandleFunc("POST "+cas+"/v1/xorbs/{prefix}/{hash}", auth.Public, h.hfXetUploadXorb)
	rt.HandleFunc("HEAD "+cas+"/v1/xorbs/{prefix}/{hash}", auth.Public, h.hfXetHeadXorb)
	rt.HandleFunc("POST "+cas+"/v1/shards", auth.Public, h.hfXetUploadShard)
	rt.HandleFunc(cas+"/", auth.Public, h.hfXetNotServed)
	rt.HandleFunc(m.Prefix, auth.Authenticated, mounted(m, h.hfAPI))
}

// RegisterNuGet serves NuGet feeds.
func (h *Handler) RegisterNuGet(rt *httpserver.Router, m Mount) {
	rt.HandleFunc(m.Prefix, auth.Authenticated, h.visibleListings(mounted(m, h.nugetAPI)))
}

// RegisterHelm serves Helm chart repositories.
func (h *Handler) RegisterHelm(rt *httpserver.Router, m Mount) {
	rt.HandleFunc(m.Prefix, auth.Authenticated, mounted(m, h.helmAPI))
}

// RegisterPyPI serves the Python package index.
func (h *Handler) RegisterPyPI(rt *httpserver.Router, m Mount) {
	rt.HandleFunc(m.Prefix, auth.Authenticated, h.visibleListings(mounted(m, h.pypiAPI)))
}

// RegisterGo serves the Go module proxy.
func (h *Handler) RegisterGo(rt *httpserver.Router, m Mount) {
	rt.HandleFunc(m.Prefix, auth.Authenticated, h.visibleListings(mounted(m, h.goAPI)))
}

// RegisterContainerRegistry serves the container registry at m (/v2/).
func (h *Handler) RegisterContainerRegistry(rt *httpserver.Router, m Mount) {
	rt.HandleFunc(m.Prefix, auth.Public, mounted(m, h.dockerV2))
}

// Register serves every protocol at Relic's own mounts, /<format>/<repo>/,
// and the container registry at registry.
func (h *Handler) Register(rt *httpserver.Router, registry Mount) {
	h.RegisterNpm(rt, h.native("/npm/"))
	h.RegisterPyPI(rt, h.native("/pypi/"))
	h.RegisterNuGet(rt, h.native("/nuget/"))
	h.RegisterGems(rt, h.native("/gems/"))
	h.RegisterCargo(rt, h.native("/cargo/"))
	h.RegisterConan(rt, h.native("/conan/"))
	h.RegisterNix(rt, h.native("/nix/"))
	h.RegisterHuggingFace(rt, h.native("/huggingface/"))
	h.RegisterHelm(rt, h.native("/helm/"))
	h.RegisterGo(rt, h.native("/go/"))

	terraform := h.native("/terraform/")
	h.RegisterTerraform(rt, terraform)
	// terraform reads the discovery document before authenticating.
	rt.HandleFunc("GET /.well-known/terraform.json", auth.Public, mounted(terraform, h.terraformDiscovery))

	for _, prefix := range rawPrefixes {
		rt.HandleFunc(prefix, auth.Authenticated, mounted(h.native(prefix), h.rawAPI))
	}

	h.RegisterContainerRegistry(rt, registry)
}

var rawPrefixes = []string{
	"/maven/",
	"/gradle/",
	"/debian/",
	"/rpm/",
	"/alpine/",
}

// NativePrefixes are the paths Register serves the protocols under (the
// container registry's aside).
var NativePrefixes = append([]string{
	"/npm/",
	"/pypi/",
	"/nuget/",
	"/gems/",
	"/cargo/",
	"/conan/",
	"/nix/",
	"/huggingface/",
	"/helm/",
	"/go/",
	"/terraform/",
}, rawPrefixes...)

func (h *Handler) native(prefix string) Mount {
	var m Mount

	m = Mount{
		Prefix: prefix,
		Files: func(w http.ResponseWriter, r *http.Request, escapedRepoPath string) {
			h.serveRaw(w, r, m, escapedRepoPath)
		},
	}

	return m
}

// ConfigureAuth teaches the authenticator what the package clients send at Relic's mounts.
func ConfigureAuth(a *auth.Authenticator) {
	a.AcceptAPIKey("X-NuGet-ApiKey", "/nuget/")
	a.LeaveBearer(hfXetTokenPrefix)
}

// RegisterPaths counts the requests under Relic's mounts toward the
// packages family in the HTTP metrics.
func RegisterPaths() {
	for _, p := range NativePrefixes {
		telemetry.RegisterFamily(p, "packages")
	}
}
