// Package artifactory serves the adapter's REST API.
package artifactory

import (
	"io"
	"net/http"

	"relic/api/adapters/artifactory/wire"
	"relic/internal/access/auth"
)

// Version is the version this API reports.
const Version = "7.136.0"

type versionResponse struct {
	Version          string            `json:"version"`
	Revision         string            `json:"revision"`
	ServicesVersions map[string]string `json:"servicesVersions"`
	Addons           []string          `json:"addons"`
	License          string            `json:"license"`
	Entitlements     map[string]bool   `json:"entitlements"`
}

func (h *Handler) ping(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	_, _ = io.WriteString(w, "OK")
}

func (h *Handler) version(w http.ResponseWriter, _ *http.Request) {
	wire.WriteJSON(w, http.StatusOK, versionResponse{
		Version:          Version,
		Revision:         "relic",
		ServicesVersions: map[string]string{},
		Addons: []string{
			"build",
		},
		License: "relic",
		Entitlements: map[string]bool{
			"EVENT_BASED_PULL_REPLICATION": false,
			"SMART_REMOTE_TARGET_FOR_EDGE": false,
			"REPO_REPLICATION":             true,
			"MULTIPUSH_REPLICATION":        false,
		},
	})
}

const maxUsageBytes = 64 << 10

func (h *Handler) usage(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, maxUsageBytes))
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) jfconnectMetrics(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, maxUsageBytes))
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) uploadsConfig(w http.ResponseWriter, _ *http.Request) {
	wire.WriteJSON(w, http.StatusOK, map[string]bool{
		"supported": false,
	})
}

func (h *Handler) encryptedPassword(w http.ResponseWriter, r *http.Request) {
	username, password, ok := r.BasicAuth()
	if !ok {
		wire.WriteError(w, http.StatusConflict, "Password encryption requires Basic authentication")
		return
	}

	sealed, err := auth.SealPassword(h.secrets, username, password)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	writeText(w, http.StatusOK, sealed)
}
