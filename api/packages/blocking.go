package packages

import (
	"context"
	"net/http"

	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/httperror"
)

func (h *Handler) allowDownload(w http.ResponseWriter, r *http.Request, item repository.Item) bool {
	if item.IsFolder() {
		return true
	}

	if h.blocker == nil {
		h.countDownload(r, item)
		return true
	}

	d, err := h.blocker.Check(r.Context(), item.Repository, item.Path(), item.Checksums.SHA256)
	if err != nil {
		h.writeError(w, r, err)
		return false
	}

	if d.Blocked {
		for k := range w.Header() {
			delete(w.Header(), k)
		}

		httperror.Write(
			w,
			r,
			http.StatusForbidden,
			"A Relic Scan policy blocks the download of "+item.Repository+"/"+item.Path()+": "+d.Reason,
		)

		return false
	}

	h.countDownload(r, item)

	return true
}

// RefuseCopy refuses to copy or move file when a download of it would be
// refused (repository.CopyInput.RefuseFile).
func (h *Handler) RefuseCopy(ctx context.Context, file repository.Item) error {
	if h.blocker == nil {
		return nil
	}

	return h.blocker.RefuseCopy(ctx, file)
}

func (h *Handler) countDownload(r *http.Request, item repository.Item) {
	if r.Method == http.MethodGet && r.Header.Get("Range") == "" {
		h.repos.RecordDownload(r.Context(), item)
		h.downloads.Download(item.ID, item.Repository, auth.FromContext(r.Context()).Name())
	}
}
