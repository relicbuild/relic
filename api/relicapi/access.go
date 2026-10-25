package relicapi

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"connectrpc.com/connect/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

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

func accessInterceptor() connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			access := accessOf(spec)
			err := allowed(access, auth.FromContext(ctx))
			trace.SpanFromContext(ctx).SetAttributes(
				attribute.String("relic.access", access.String()), attribute.Bool("relic.access.allowed", err == nil))

			if err != nil {
				return err
			}

			return next(ctx, spec, stream)
		}
	}
}

func allowed(a relicv1.Access, p auth.Principal) error {
	switch a {
	case relicv1.Access_ACCESS_PUBLIC:
		return nil
	case relicv1.Access_ACCESS_AUTHENTICATED:
		if p.Anonymous() {
			return connect.NewError(connect.CodeUnauthenticated, "sign in first")
		}

		return nil
	case relicv1.Access_ACCESS_ADMIN:
		if p.Anonymous() {
			return connect.NewError(connect.CodeUnauthenticated, "sign in first")
		}

		if !p.Admin {
			return connect.NewError(connect.CodePermissionDenied, "administrators only")
		}

		return nil
	case relicv1.Access_ACCESS_VIEWER:
		if p.Anonymous() {
			return connect.NewError(connect.CodeUnauthenticated, "sign in first")
		}

		if !p.Admin && !p.Viewer {
			return connect.NewError(connect.CodePermissionDenied, "viewers and administrators only")
		}

		return nil
	default:
		return connect.NewError(connect.CodePermissionDenied, "method has no access rule")
	}
}

func errorInterceptor(log *slog.Logger) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			err := next(ctx, spec, stream)
			if err == nil {
				return nil
			}

			ce := codeOf(err)
			if ce.Code() == connect.CodeInternal {
				log.ErrorContext(ctx, "relic api", "procedure", spec.Procedure, "error", err)
			}

			return ce
		}
	}
}

type requestMessageKey struct{}

// receiveRequest decodes a unary call's request before the interceptors
// after it run, so they see the request and a body that cannot be decoded
// fails before the access check.
func receiveRequest() connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			request := unaryRequestMessage(spec)
			if request == nil {
				return next(ctx, spec, stream)
			}

			if err := stream.Receive(request); err != nil {
				return err
			}

			ctx = context.WithValue(ctx, requestMessageKey{}, request)

			return next(ctx, spec, &receivedStream{
				ServerStream: stream,
				request:      request,
			})
		}
	}
}

func unaryRequestMessage(spec connect.Spec) proto.Message {
	md, ok := spec.Schema.(protoreflect.MethodDescriptor)
	if !ok || spec.StreamType != connect.StreamTypeUnary {
		return nil
	}

	messageType, err := protoregistry.GlobalTypes.FindMessageByName(md.Input().FullName())
	if err != nil {
		return nil
	}

	return messageType.New().Interface()
}

func requestMessage(ctx context.Context) proto.Message {
	msg, _ := ctx.Value(requestMessageKey{}).(proto.Message)

	return msg
}

// receivedStream hands the handler the request receiveRequest decoded.
type receivedStream struct {
	connect.ServerStream

	request  proto.Message
	received bool
}

func (s *receivedStream) Receive(msg any) error {
	if s.received {
		return io.EOF
	}

	dst, ok := msg.(proto.Message)
	if !ok {
		return connect.Errorf(connect.CodeInternal, "%T is not a protobuf message", msg)
	}

	proto.Merge(dst, s.request)
	s.received = true

	return nil
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
	auth.ErrUnknownGroup,
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
		return connect.NewError(connect.CodeDataLoss, "stored content is missing or damaged")
	}

	for _, nf := range notFound {
		if errors.Is(err, nf) {
			return connect.NewError(connect.CodeNotFound, err.Error()).WithCause(err)
		}
	}

	switch {
	case errors.Is(err, repository.ErrUpstream):
		return connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	case errors.Is(err, repository.ErrRepositoryExists),
		errors.Is(err, repository.ErrRepositoryKeyCaseConflict),
		errors.Is(err, security.ErrExists),
		errors.Is(err, policy.ErrExists),
		errors.Is(err, release.ErrExists),
		errors.Is(err, evidence.ErrKeyExists):
		return connect.NewError(connect.CodeAlreadyExists, err.Error()).WithCause(err)
	case errors.Is(err, repository.ErrInvalidRepository),
		errors.Is(err, repository.ErrInvalidPath),
		errors.Is(err, repository.ErrInvalidProperty),
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
		return connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err)
	case errors.Is(err, repository.ErrChecksumMismatch),
		errors.Is(err, repository.ErrNotAFile),
		errors.Is(err, repository.ErrSelfCopy),
		errors.Is(err, repository.ErrUnsupportedArchive):
		return connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err)
	case errors.Is(err, repository.ErrForbidden),
		errors.Is(err, repository.ErrCoolingDown),
		errors.Is(err, release.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, err.Error()).WithCause(err)
	case errors.Is(err, blocking.ErrNotQuarantined):
		return connect.NewError(connect.CodeFailedPrecondition, err.Error()).WithCause(err)
	case errors.Is(err, blocking.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err)
	case errors.Is(err, policy.ErrInUse),
		errors.Is(err, project.ErrHasRepositories),
		errors.Is(err, policy.ErrDecided),
		errors.Is(err, release.ErrRefused),
		errors.Is(err, repository.ErrNotLocal),
		errors.Is(err, repository.ErrConflict),
		errors.Is(err, repository.ErrHeld),
		errors.Is(err, repository.ErrImmutable),
		errors.Is(err, release.ErrNotDraft),
		errors.Is(err, signingkey.ErrKind),
		errors.Is(err, signingkey.ErrInUse):
		return connect.NewError(connect.CodeFailedPrecondition, err.Error()).WithCause(err)
	case errors.Is(err, repository.ErrTooLarge), errors.Is(err, repository.ErrQuotaExceeded):
		return connect.NewError(connect.CodeResourceExhausted, err.Error()).WithCause(err)
	case errors.Is(err, objectstore.ErrStorageFull):
		return connect.NewError(connect.CodeResourceExhausted, objectstore.ErrStorageFull.Error()).
			WithCause(objectstore.ErrStorageFull)
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, err.Error()).WithCause(err)
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err.Error()).WithCause(err)
	}

	return connect.NewError(connect.CodeInternal, "internal error")
}
