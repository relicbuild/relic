package relicapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/audit"
	"relic/internal/access/auth"
	"relic/internal/access/ldap"
	"relic/internal/access/project"
	"relic/internal/access/security"
	"relic/internal/access/sso"
	"relic/internal/access/workload"
	"relic/internal/artifacts/cleanup"
	"relic/internal/artifacts/formats/agentregistry"
	"relic/internal/artifacts/repository"
	"relic/internal/artifacts/signingkey"
	"relic/internal/delivery/buildinfo"
	"relic/internal/delivery/evidence"
	"relic/internal/delivery/release"
	"relic/internal/delivery/replication"
	"relic/internal/delivery/webhook"
	"relic/internal/platform/objectstore"
	"relic/internal/scanning/cvecatalog"
	"relic/internal/scanning/scan"
	"relic/internal/scanning/scan/blocking"
	"relic/internal/scanning/scan/policy"
)

func accessOf(spec connect.Spec) relicv1.Access {
	md, ok := spec.Schema.(protoreflect.MethodDescriptor)
	if !ok {
		return relicv1.Access_ACCESS_UNSPECIFIED
	}

	a, _ := proto.GetExtension(md.Options(), relicv1.E_Access).(relicv1.Access)

	return a
}

func accessInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			access := accessOf(req.Spec())
			err := allowed(access, auth.FromContext(ctx))
			trace.SpanFromContext(ctx).SetAttributes(
				attribute.String("relic.access", access.String()), attribute.Bool("relic.access.allowed", err == nil))

			if err != nil {
				return nil, err
			}

			return next(ctx, req)
		}
	}
}

func allowed(a relicv1.Access, p auth.Principal) error {
	switch a {
	case relicv1.Access_ACCESS_PUBLIC:
		return nil
	case relicv1.Access_ACCESS_AUTHENTICATED:
		if p.Anonymous() {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("sign in first"))
		}

		return nil
	case relicv1.Access_ACCESS_ADMIN:
		if p.Anonymous() {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("sign in first"))
		}

		if !p.Admin {
			return connect.NewError(connect.CodePermissionDenied, errors.New("administrators only"))
		}

		return nil
	case relicv1.Access_ACCESS_VIEWER:
		if p.Anonymous() {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("sign in first"))
		}

		if !p.Admin && !p.Viewer {
			return connect.NewError(connect.CodePermissionDenied, errors.New("viewers and administrators only"))
		}

		return nil
	default:
		return connect.NewError(connect.CodePermissionDenied, errors.New("method has no access rule"))
	}
}

func errorInterceptor(log *slog.Logger) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			resp, err := next(ctx, req)
			if err == nil {
				return resp, nil
			}

			ce := codeOf(err)
			if ce.Code() == connect.CodeInternal {
				log.ErrorContext(ctx, "relic api", "procedure", req.Spec().Procedure, "error", err)
			}

			return nil, ce
		}
	}
}

var notFound = []error{
	repository.ErrNotFound,
	repository.ErrRepositoryNotFound,
	security.ErrNotFound,
	ldap.ErrNotFound,
	policy.ErrNotFound,
	replication.ErrNotConfigured,
	cleanup.ErrNotFound,
	scan.ErrNoScan,
	cvecatalog.ErrUnknown,
	buildinfo.ErrNotFound,
	release.ErrNotFound,
	release.ErrGateNotFound,
	evidence.ErrKeyNotFound,
	signingkey.ErrNotFound,
	auth.ErrTokenNotFound,
	auth.ErrUnknownUser,
	sso.ErrNotFound,
	workload.ErrNotFound,
	webhook.ErrNotFound,
	project.ErrNotFound,
	project.ErrMemberNotFound,
	agentregistry.ErrNotFound,
}

func codeOf(err error) *connect.Error {
	if ce, ok := errors.AsType[*connect.Error](err); ok {
		return ce
	}

	if errors.Is(err, repository.ErrContentMissing) || errors.Is(err, repository.ErrContentCorrupt) {
		return connect.NewError(connect.CodeDataLoss, errors.New("stored content is missing or damaged"))
	}

	for _, nf := range notFound {
		if errors.Is(err, nf) {
			return connect.NewError(connect.CodeNotFound, err)
		}
	}

	switch {
	case errors.Is(err, repository.ErrRepositoryExists),
		errors.Is(err, security.ErrExists),
		errors.Is(err, policy.ErrExists),
		errors.Is(err, release.ErrExists),
		errors.Is(err, evidence.ErrKeyExists):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, repository.ErrInvalidRepository),
		errors.Is(err, repository.ErrInvalidPath),
		errors.Is(err, security.ErrInvalid),
		errors.Is(err, ldap.ErrInvalid),
		errors.Is(err, policy.ErrInvalid),
		errors.Is(err, policy.ErrInvalidWaiver),
		errors.Is(err, replication.ErrInvalid),
		errors.Is(err, cleanup.ErrInvalid),
		errors.Is(err, audit.ErrInvalid),
		errors.Is(err, sso.ErrInvalid),
		errors.Is(err, workload.ErrInvalid),
		errors.Is(err, webhook.ErrInvalid),
		errors.Is(err, project.ErrInvalid),
		errors.Is(err, agentregistry.ErrInvalid),
		errors.Is(err, release.ErrInvalid),
		errors.Is(err, release.ErrInvalidGate),
		errors.Is(err, evidence.ErrInvalid),
		errors.Is(err, buildinfo.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, repository.ErrChecksumMismatch),
		errors.Is(err, repository.ErrNotAFile),
		errors.Is(err, repository.ErrSelfCopy),
		errors.Is(err, repository.ErrUnsupportedArchive):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, repository.ErrForbidden),
		errors.Is(err, repository.ErrCoolingDown),
		errors.Is(err, release.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, blocking.ErrNotQuarantined):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, blocking.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, policy.ErrInUse),
		errors.Is(err, policy.ErrDecided),
		errors.Is(err, release.ErrRefused),
		errors.Is(err, repository.ErrNotLocal),
		errors.Is(err, repository.ErrConflict),
		errors.Is(err, repository.ErrHeld),
		errors.Is(err, repository.ErrImmutable),
		errors.Is(err, release.ErrNotDraft),
		errors.Is(err, signingkey.ErrKind):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, repository.ErrUpstream):
		return connect.NewError(connect.CodeUnavailable, err)
	case errors.Is(err, repository.ErrTooLarge), errors.Is(err, repository.ErrQuotaExceeded):
		return connect.NewError(connect.CodeResourceExhausted, err)
	case errors.Is(err, objectstore.ErrStorageFull):
		return connect.NewError(connect.CodeResourceExhausted, objectstore.ErrStorageFull)
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, err)
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	}

	return connect.NewError(connect.CodeInternal, errors.New("internal error"))
}
