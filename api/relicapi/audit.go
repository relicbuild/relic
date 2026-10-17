package relicapi

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/audit"
	"relic/internal/access/auth"
)

// auditedMethods are audited although they are not administrator methods:
// sign-ins, tokens, repositories and project members.
var auditedMethods = map[string]bool{
	"/relic.v1.SessionService/Login":               true,
	"/relic.v1.SessionService/Logout":              true,
	"/relic.v1.SessionService/ApproveLogin":        true,
	"/relic.v1.SecurityService/CreateToken":        true,
	"/relic.v1.SecurityService/RevokeToken":        true,
	"/relic.v1.RepositoryService/CreateRepository": true,
	"/relic.v1.RepositoryService/UpdateRepository": true,
	"/relic.v1.RepositoryService/DeleteRepository": true,
	"/relic.v1.ProjectService/SetProjectMembers":   true,
	"/relic.v1.ProjectService/AddProjectMember":    true,
	"/relic.v1.ProjectService/RemoveProjectMember": true,
}

type auditServer struct {
	d Dependencies
}

func (s *auditServer) ListAuditEvents(
	ctx context.Context,
	req *connect.Request[relicv1.ListAuditEventsRequest],
) (*connect.Response[relicv1.ListAuditEventsResponse], error) {
	if s.d.Audit == nil {
		return nil, unimplemented("the audit log")
	}

	m := req.Msg

	outcome, known := toDomain[audit.Outcome](m.GetOutcome())
	if !known && m.GetOutcome() != relicv1.AuditOutcome_AUDIT_OUTCOME_UNSPECIFIED {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown outcome %d", m.GetOutcome()))
	}

	f := audit.Filter{
		Actor:    m.GetActor(),
		Action:   m.GetAction(),
		Target:   m.GetTarget(),
		Outcome:  outcome,
		Page:     int(m.GetPage()),
		PageSize: int(m.GetPageSize()),
	}
	if m.GetSince() != nil {
		f.Since = m.GetSince().AsTime()
	}

	if m.GetUntil() != nil {
		f.Until = m.GetUntil().AsTime()
	}

	events, total, err := s.d.Audit.List(ctx, f)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListAuditEventsResponse{
		Events: make([]*relicv1.AuditEvent, 0, len(events)),
		Total:  total,
	}
	for _, e := range events {
		out.Events = append(out.Events, &relicv1.AuditEvent{
			Id:      e.ID,
			Time:    timestamppb.New(e.Time),
			Actor:   e.Actor,
			Action:  e.Action,
			Target:  e.Target,
			Outcome: fromDomain[relicv1.AuditOutcome](e.Outcome),
			Client:  e.Client,
			Detail:  e.Detail,
		})
	}

	return connect.NewResponse(out), nil
}

func audited(spec connect.Spec) bool {
	return accessOf(spec) == relicv1.Access_ACCESS_ADMIN || auditedMethods[spec.Procedure]
}

func auditInterceptor(a audit.Recorder) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			resp, err := next(ctx, req)
			if a == nil || !audited(req.Spec()) {
				return resp, err
			}

			msg, _ := req.Any().(proto.Message)

			e := audit.Event{
				Actor:   auth.FromContext(ctx).Name(),
				Action:  strings.TrimPrefix(req.Spec().Procedure, "/"),
				Target:  auditTarget(msg),
				Outcome: rpcOutcome(err),
			}
			if login, ok := msg.(*relicv1.LoginRequest); ok {
				e.Actor = login.GetUsername()
			}

			if err != nil {
				e.Detail = codeOf(err).Message()
			}

			a.Record(ctx, e)

			return resp, err
		}
	}
}

func rpcOutcome(err error) audit.Outcome {
	if err == nil {
		return audit.Success
	}

	switch codeOf(err).Code() {
	case connect.CodePermissionDenied, connect.CodeUnauthenticated:
		return audit.Denied
	default:
		return audit.Failure
	}
}

// targetFields name what a request acts on, in order of preference; secrets
// (passwords, sign-in session IDs and codes) are never among them.
var targetFields = []protoreflect.Name{
	"key",
	"name",
	"id",
	"username",
	"repository",
	"kid",
	"description",
	"path",
}

// carriedTargetFields name the message a request carries (a project by its
// key, a replication by its repository), in order of preference.
var carriedTargetFields = []protoreflect.Name{
	"key",
	"name",
	"repository",
	"id",
}

// auditTarget names what msg acts on: its repository and path, a naming
// field, or the name of the message it carries (a user, group, policy, …).
func auditTarget(msg proto.Message) string {
	if msg == nil {
		return ""
	}

	m := msg.ProtoReflect()
	if repo, path := stringField(m, "repository"), stringField(m, "path"); repo != "" && path != "" {
		return repo + "/" + strings.TrimPrefix(path, "/")
	}

	for _, name := range targetFields {
		if v := stringField(m, name); v != "" {
			return v
		}
	}

	fields := m.Descriptor().Fields()
	for i := range fields.Len() {
		f := fields.Get(i)
		if f.Kind() != protoreflect.MessageKind || f.IsList() || f.IsMap() || !m.Has(f) {
			continue
		}

		carried := m.Get(f).Message()
		for _, name := range carriedTargetFields {
			if v := stringField(carried, name); v != "" {
				return v
			}
		}
	}

	return ""
}

func stringField(m protoreflect.Message, name protoreflect.Name) string {
	f := m.Descriptor().Fields().ByName(name)
	if f == nil || f.Kind() != protoreflect.StringKind || f.IsList() {
		return ""
	}

	return m.Get(f).String()
}
