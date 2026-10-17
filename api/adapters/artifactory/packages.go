package artifactory

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"relic/api/adapters/artifactory/wire"
	"relic/api/packages"
	"relic/internal/access/auth"
	"relic/internal/artifacts/formats/alpine"
	"relic/internal/artifacts/formats/debian"
	"relic/internal/artifacts/formats/docker"
	"relic/internal/artifacts/formats/rpm"
	"relic/internal/artifacts/repository"
	"relic/internal/artifacts/signingkey"
)

func (h *Handler) mount(prefix string) packages.Mount {
	return packages.Mount{
		Prefix:          prefix,
		Files:           h.rawFiles,
		FileHeaders:     fileHeaders,
		RegistryHeaders: registryHeaders,
	}
}

func (h *Handler) rawFiles(w http.ResponseWriter, r *http.Request, escapedRepoPath string) {
	r2 := r.Clone(r.Context())
	r2.URL.RawPath = "/artifactory/" + escapedRepoPath
	r2.URL.Path, _ = url.PathUnescape(r2.URL.RawPath)
	h.files(w, r2)
}

func fileHeaders(hdr http.Header, item repository.Item) {
	hdr.Set("X-Artifactory-Filename", item.Name)
}

func registryHeaders(hdr http.Header, repo string) {
	hdr.Set("X-Artifactory-Docker-Registry", repo)
}

// RegistryMount is the container registry's mount.
func (h *Handler) RegistryMount() packages.Mount { return h.mount(registryPrefix) }

func (h *Handler) npmAuth(w http.ResponseWriter, r *http.Request) {
	p := auth.FromContext(r.Context())

	secret := ""
	if _, password, ok := r.BasicAuth(); ok {
		secret = password
	} else if token, ok := auth.BearerToken(r.Header.Get("Authorization")); ok {
		secret = token
	}

	cred := base64.StdEncoding.EncodeToString([]byte(p.Username + ":" + secret))
	writeText(w, http.StatusOK, "_auth = "+cred+"\nalways-auth = true\nemail = "+p.Username+"@relic.local\n")
}

func (h *Handler) dockerAPI(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/artifactory/api/docker/")

	repoKey, rest, ok := strings.Cut(rest, "/")
	if rest == "v2/promote" && r.Method == http.MethodPost {
		h.dockerPromote(w, r, repoKey)
		return
	}

	rest, found := strings.CutPrefix(rest, "v2/")
	if !ok || !found || repoKey == "" {
		packages.RegistryError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")
		return
	}

	h.packages.ServeRegistry(
		w,
		r,
		h.mount("/artifactory/api/docker/"),
		"/artifactory/api/docker/"+repoKey+"/v2/",
		repoKey,
		rest,
	)
}

type dockerPromoteRequest struct {
	TargetRepo             string `json:"targetRepo"`
	DockerRepository       string `json:"dockerRepository"`
	TargetDockerRepository string `json:"targetDockerRepository"`
	Tag                    string `json:"tag"`
	TargetTag              string `json:"targetTag"`
	Copy                   bool   `json:"copy"`
}

func (h *Handler) dockerPromote(w http.ResponseWriter, r *http.Request, source string) {
	var req dockerPromoteRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		wire.WriteError(w, http.StatusBadRequest, "invalid promotion request")
		return
	}

	valid := docker.ValidImage(req.DockerRepository) &&
		(req.TargetDockerRepository == "" || docker.ValidImage(req.TargetDockerRepository)) &&
		(req.Tag == "" || docker.ValidTag(req.Tag)) && (req.TargetTag == "" || docker.ValidTag(req.TargetTag))
	if !valid || req.TargetRepo == "" {
		wire.WriteError(w, http.StatusBadRequest, "invalid promotion request")
		return
	}

	srcAction := auth.ActionDelete
	if req.Copy {
		srcAction = auth.ActionRead
	}

	targetImage := cmp.Or(req.TargetDockerRepository, req.DockerRepository)
	if !h.packages.RequireRepo(
		w,
		r,
		packages.RepoCheck{
			Repo:   source,
			Path:   req.DockerRepository,
			Action: srcAction,
		},
		packages.RepoCheck{
			Repo:   req.TargetRepo,
			Path:   targetImage,
			Action: auth.ActionWrite,
		},
	) {
		return
	}

	err := h.docker.Promote(r.Context(), docker.PromoteInput{
		SourceRepository: source,
		TargetRepository: req.TargetRepo,
		Image:            req.DockerRepository,
		TargetImage:      targetImage,
		Tag:              req.Tag,
		TargetTag:        req.TargetTag,
		Copy:             req.Copy,
		User:             auth.FromContext(r.Context()).Name(),
	})
	switch {
	case errors.Is(err, docker.ErrManifestUnknown):
		wire.WriteError(w, http.StatusNotFound, "The tag does not exist.")
	case err != nil:
		h.writeError(w, r, err)
	default:
		wire.WriteJSON(w, http.StatusOK, map[string]string{
			"message": "Image promoted.",
		})
	}
}

func (h *Handler) keyPairPublic(w http.ResponseWriter, r *http.Request) {
	h.writePublicKey(w, r, r.PathValue("name"))
}

func (h *Handler) gpgPublicKey(w http.ResponseWriter, r *http.Request) {
	if _, _, err := h.keys.GPG(r.Context(), signingkey.DefaultGPG); err != nil {
		h.writeError(w, r, err)
		return
	}

	h.writePublicKey(w, r, signingkey.DefaultGPG)
}

func (h *Handler) writePublicKey(w http.ResponseWriter, r *http.Request, name string) {
	pub, err := h.keys.PublicKey(r.Context(), name)
	if errors.Is(err, signingkey.ErrNotFound) {
		wire.WriteError(w, http.StatusNotFound, "Key pair "+name+" not found")
		return
	}

	if err != nil {
		h.writeError(w, r, err)
		return
	}

	writeText(w, http.StatusOK, pub)
}

func (h *Handler) repositoryPublicKey(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("repo")
	if !h.packages.RequireRepo(w, r, packages.RepoCheck{
		Repo:   key,
		Path:   "",
		Action: auth.ActionRead,
	}) {
		return
	}

	rp, err := h.repos.GetRepository(r.Context(), key)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	ref := rp.KeyPairRef()
	if ref == "" {
		wire.WriteError(w, http.StatusNotFound, "No key pair is associated with repository "+key)
		return
	}

	var pem string

	switch rp.Format {
	case alpine.Format:
		_, pem, err = h.keys.RSA(r.Context(), ref)
	case debian.Format, rpm.Format:
		_, pem, err = h.keys.GPG(r.Context(), ref)
	default:
		pem, err = h.keys.PublicKey(r.Context(), ref)
	}

	if errors.Is(err, signingkey.ErrNotFound) {
		wire.WriteError(w, http.StatusNotFound, "Key pair "+ref+" not found")
		return
	}

	if err != nil {
		h.writeError(w, r, err)
		return
	}

	writeText(w, http.StatusOK, pem)
}
