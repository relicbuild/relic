package relicapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"connectrpc.com/connect"

	"relic/internal/access/audit"
	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/scanning/scan"
)

// SBOMPath is where an item's SBOM is exported (GET, ?format=cyclonedx,
// spdx or syft) and imported (PUT): SBOMPath + "<repository>/<path>".
const SBOMPath = "/api/v1/sbom/"

var sbomFormats = map[scan.SBOMFormat]struct {
	contentType, extension string
}{
	scan.CycloneDXJSON: {
		contentType: "application/vnd.cyclonedx+json",
		extension:   ".cdx.json",
	},
	scan.SPDXJSON: {
		contentType: "application/spdx+json",
		extension:   ".spdx.json",
	},
	scan.SyftJSON: {
		contentType: "application/json",
		extension:   ".syft.json",
	},
}

func (d Dependencies) sbom(w http.ResponseWriter, r *http.Request) {
	key, p, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, SBOMPath), "/")

	p = cleanPath(p)
	if key == "" || p == "" {
		writeHTTPError(
			w,
			connect.NewError(connect.CodeInvalidArgument, errors.New("expected "+SBOMPath+"<repository>/<path>")),
		)

		return
	}

	if auth.FromContext(r.Context()).Anonymous() {
		writeHTTPError(w, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in first")))
		return
	}

	switch r.Method {
	case http.MethodGet:
		d.exportSBOM(w, r, key, p)
	case http.MethodPut:
		d.importSBOM(w, r, key, p)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeHTTPError(w, connect.NewError(connect.CodeUnimplemented, errors.New("method not allowed")))
	}
}

func (d Dependencies) exportSBOM(w http.ResponseWriter, r *http.Request, key, p string) {
	ctx := r.Context()

	format := scan.SBOMFormat(r.URL.Query().Get("format"))
	if format == "" {
		format = scan.CycloneDXJSON
	}

	kind, ok := sbomFormats[format]
	if !ok {
		writeHTTPError(
			w,
			connect.NewError(connect.CodeInvalidArgument, errors.New("format is cyclonedx, spdx or syft")),
		)

		return
	}

	if err := can(ctx, d, key, p, auth.ActionRead); err != nil {
		writeHTTPError(w, codeOf(err))
		return
	}

	it, err := d.Repositories.Stat(ctx, key, p)
	if err != nil {
		writeHTTPError(w, d.logged(r, err))
		return
	}

	target, _, found, err := scanTarget(ctx, d, it)
	if err != nil {
		writeHTTPError(w, d.logged(r, err))
		return
	}

	var a scan.ArtifactScan
	if found {
		a, err = d.ScanResults.Artifact(ctx, target.Checksums.SHA256)
	}

	if !found || errors.Is(err, scan.ErrNoScan) || (err == nil && a.SBOM == nil) {
		writeHTTPError(w, connect.NewError(connect.CodeNotFound, errors.New("no SBOM for this item")))
		return
	}

	if err != nil {
		writeHTTPError(w, d.logged(r, err))
		return
	}

	doc, err := scan.ExportSBOM(a.SBOM, format, key+"/"+p, time.Now())
	if errors.Is(err, scan.ErrSBOM) {
		writeHTTPError(w, connect.NewError(connect.CodeFailedPrecondition, err))
		return
	}

	if err != nil {
		writeHTTPError(w, d.logged(r, err))
		return
	}

	w.Header().Set("Content-Type", kind.contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", path.Base(p)+kind.extension))
	_, _ = w.Write(doc) //nolint:gosec // G705: an SBOM document, served as an attachment
}

func (d Dependencies) importSBOM(w http.ResponseWriter, r *http.Request, key, p string) {
	err := d.storeSBOM(w, r, key, p)
	d.recordSBOMImport(r.Context(), key+"/"+p, err)

	if err != nil {
		writeHTTPError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (d Dependencies) storeSBOM(w http.ResponseWriter, r *http.Request, key, p string) *connect.Error {
	ctx := r.Context()
	if err := can(ctx, d, key, p, auth.ActionWrite); err != nil {
		return codeOf(err)
	}

	it, err := d.Repositories.Stat(ctx, key, p)
	if err != nil {
		return d.logged(r, err)
	}

	target, _, found, err := scanTarget(ctx, d, it)
	if err != nil {
		return d.logged(r, err)
	}

	if !found {
		return connect.NewError(connect.CodeInvalidArgument, errors.New(key+"/"+p+" is a folder"))
	}

	if err := canDeployEveryCopy(ctx, d, target.Checksums.SHA256); err != nil {
		return d.logged(r, err)
	}

	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, scan.MaxSBOMSize))
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("read the SBOM: %w", err))
	}

	err = d.ScanResults.ImportSBOM(ctx, target.Checksums.SHA256, target.Name, raw)
	if errors.Is(err, scan.ErrSBOM) {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}

	if err != nil {
		return d.logged(r, err)
	}

	if d.QueueRematch != nil {
		if err := d.QueueRematch(ctx, target.Checksums.SHA256); err != nil {
			d.Log.ErrorContext(ctx, "imported SBOM not queued for matching", "item", key+"/"+p, "error", err)

			return connect.NewError(
				connect.CodeUnavailable,
				errors.New("the SBOM is stored but not matched yet: import it again"),
			)
		}
	}

	return nil
}

func canDeployEveryCopy(ctx context.Context, d Dependencies, sha256Hex string) error {
	if auth.FromContext(ctx).Admin {
		return nil
	}

	copies, err := d.Repositories.FilesWithContent(ctx, repository.Checksums{
		SHA256: sha256Hex,
	})
	if err != nil {
		return err
	}

	denied := connect.NewError(
		connect.CodePermissionDenied,
		errors.New("this content is also stored where you may not deploy: ask an administrator to import its SBOM"),
	)
	if len(copies) >= repository.FilesWithContentLimit {
		return denied
	}

	for _, it := range copies {
		if err := can(ctx, d, it.Repository, cleanPath(it.Path()), auth.ActionWrite); err != nil {
			if errors.Is(err, repository.ErrNotFound) || codeOf(err).Code() == connect.CodePermissionDenied {
				return denied
			}

			return err
		}
	}

	return nil
}

func (d Dependencies) recordSBOMImport(ctx context.Context, target string, err *connect.Error) {
	if d.Audit == nil {
		return
	}

	e := audit.Event{
		Actor:   auth.FromContext(ctx).Name(),
		Action:  "sbom.import",
		Target:  target,
		Outcome: audit.Success,
	}
	if err != nil {
		e.Outcome, e.Detail = rpcOutcome(err), err.Message()
	}

	d.Audit.Record(ctx, e)
}
