package relicapi

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/scanning/endoflife"
	"relic/internal/scanning/scan"
)

// EndOfLifeDataPath takes a dump of endoflife.date's products: PUT it as the
// body. GET it for what the server holds.
const EndOfLifeDataPath = "/api/v1/end-of-life-data"

const maxEndOfLifeDump = 64 << 20

func operationalRiskMessage(r scan.OperationalRisk) *relicv1.OperationalRisk {
	return &relicv1.OperationalRisk{
		Kind:           r.Kind,
		Severity:       severityMessage(r.Severity),
		Purl:           r.PURL,
		PackageName:    r.PackageName,
		PackageVersion: r.PackageVersion,
		Product:        r.Product,
		Cycle:          r.Cycle,
		EolFrom:        r.EOLFrom,
		Detail:         r.Detail,
	}
}

func (s *scanServer) ListOperationalRisks(
	ctx context.Context,
	req *relicv1.ListOperationalRisksRequest,
) (*relicv1.ListOperationalRisksResponse, error) {
	switch req.GetKind() {
	case "", "end_of_life", "deprecated", "unmaintained":
	default:
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			"kind is end_of_life, deprecated or unmaintained",
		)
	}

	n, size := page(req.GetPage(), req.GetPageSize())

	rows, total, err := s.d.ScanResults.RisksPage(ctx, scan.RisksQuery{
		Kind:       req.GetKind(),
		Query:      req.GetQuery(),
		Repository: req.GetRepository(),
		Skip:       (n - 1) * size,
		Max:        size,
	})
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListOperationalRisksResponse{
		Total: total,
		Risks: make([]*relicv1.OperationalRisk, 0, len(rows)),
	}
	for _, r := range rows {
		m := operationalRiskMessage(r.OperationalRisk)
		m.Repository = r.Repository
		m.Path = strings.TrimPrefix(path.Join(r.Path, r.Name), "./")
		out.Risks = append(out.Risks, m)
	}

	return out, nil
}

func (s *scanServer) GetEndOfLifeStatus(
	ctx context.Context,
	_ *relicv1.GetEndOfLifeStatusRequest,
) (*relicv1.GetEndOfLifeStatusResponse, error) {
	if s.d.EndOfLife == nil {
		return nil, connect.NewError(connect.CodeUnavailable, "scanning is not available")
	}

	st, err := s.d.EndOfLife.Status(ctx)
	if err != nil {
		return nil, err
	}

	out := &relicv1.GetEndOfLifeStatusResponse{
		Source:   st.Source,
		Products: int32(min(st.Products, 1<<30)), //nolint:gosec // bounded
	}
	if st.Source != "" {
		out.Fetched, out.Changed = ts(&st.Fetched), ts(&st.Changed)
	}

	return out, nil
}

func (d Dependencies) importEndOfLifeData(w http.ResponseWriter, r *http.Request) {
	p := auth.FromContext(r.Context())

	switch {
	case p.Anonymous():
		writeHTTPError(w, connect.NewError(connect.CodeUnauthenticated, "sign in first"))

		return
	case !p.Admin:
		writeHTTPError(
			w,
			connect.NewError(
				connect.CodePermissionDenied,
				"importing end-of-life data needs an administrator",
			),
		)

		return
	case d.EndOfLifeData == nil:
		writeHTTPError(w, connect.NewError(connect.CodeUnavailable, "scanning is not available"))

		return
	}

	n, err := d.EndOfLifeData.Import(r.Context(), http.MaxBytesReader(w, r.Body, maxEndOfLifeDump))
	if errors.Is(err, endoflife.ErrInvalidDump) {
		writeHTTPError(w, connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err))

		return
	}

	if err != nil {
		writeHTTPError(w, d.logged(r, err))

		return
	}

	if d.QueueOperationalRiskCheck != nil {
		if err := d.QueueOperationalRiskCheck(r.Context()); err != nil {
			d.Log.WarnContext(r.Context(), "end-of-life data imported; scans are assessed again on the next daily run",
				"error", err)
		}
	}

	d.Log.InfoContext(r.Context(), "end-of-life data imported", "user", p.Name(), "products", n)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"products":` + strconv.Itoa(n) + "}\n"))
}
