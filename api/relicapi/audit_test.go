package relicapi

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	relicv1 "relic/api/gen/relic/v1"
)

func TestAuditTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  proto.Message
		want string
	}{
		{
			name: "repository and path",
			msg: &relicv1.DeleteItemRequest{
				Repository: "libs",
				Path:       "/a/b.jar",
			},
			want: "libs/a/b.jar",
		},
		{
			name: "name field",
			msg: &relicv1.DeleteUserRequest{
				Name: "bob",
			},
			want: "bob",
		},
		{
			name: "project before member",
			msg: &relicv1.RemoveProjectMemberRequest{
				Key:  "web",
				Name: "bob",
			},
			want: "web",
		},
		{
			name: "carried message",
			msg: &relicv1.PutUserRequest{
				User: &relicv1.User{
					Name:     "bob",
					Password: "Secret-1",
				},
			},
			want: "bob",
		},
		{
			name: "sign-in session is not named",
			msg: &relicv1.ApproveLoginRequest{
				Session: "0b7e4c1e-5d2f-4c55-9d0e-1a2b3c4d5e6f",
				Code:    "5e6f",
			},
		},
		{
			name: "nothing",
			msg:  &relicv1.CollectGarbageRequest{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := auditTarget(tt.msg); got != tt.want {
				t.Errorf("auditTarget = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAudited(t *testing.T) {
	t.Parallel()

	tests := []struct {
		procedure string
		want      bool
	}{
		{
			procedure: "/relic.v1.RepositoryService/CreateRepository",
			want:      true,
		},
		{
			procedure: "/relic.v1.RepositoryService/UpdateRepository",
			want:      true,
		},
		{
			procedure: "/relic.v1.RepositoryService/DeleteRepository",
			want:      true,
		},
		{
			procedure: "/relic.v1.ProjectService/SetProjectMembers",
			want:      true,
		},
		{
			procedure: "/relic.v1.ProjectService/AddProjectMember",
			want:      true,
		},
		{
			procedure: "/relic.v1.ProjectService/RemoveProjectMember",
			want:      true,
		},
		{
			procedure: "/relic.v1.SecurityService/DeleteUser",
			want:      true,
		},
		{
			procedure: "/relic.v1.SessionService/Login",
			want:      true,
		},
		{
			procedure: "/relic.v1.RepositoryService/GetRepository",
		},
		{
			procedure: "/relic.v1.ArtifactService/DeleteItem",
		},
	}
	for _, tt := range tests {
		t.Run(tt.procedure, func(t *testing.T) {
			t.Parallel()

			name := strings.ReplaceAll(strings.TrimPrefix(tt.procedure, "/"), "/", ".")

			method, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(name))
			if err != nil {
				t.Fatal(err)
			}

			if got := audited(connect.Spec{
				Procedure: tt.procedure,
				Schema:    method,
			}); got != tt.want {
				t.Errorf("audited = %v, want %v", got, tt.want)
			}
		})
	}
}
