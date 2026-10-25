package relicapi

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/delivery/release"
	"relic/internal/delivery/replication"
)

func TestAllowed(t *testing.T) {
	t.Parallel()

	anon, user, admin := auth.Principal{}, auth.Principal{
		Username: "u",
	}, auth.Principal{
		Username: "a",
		Admin:    true,
	}
	viewer := auth.Principal{
		Username: "v",
		Viewer:   true,
	}

	tests := []struct {
		name   string
		access relicv1.Access
		p      auth.Principal
		want   connect.Code // 0: allowed
	}{
		{
			"public anonymous",
			relicv1.Access_ACCESS_PUBLIC,
			anon,
			0,
		},
		{
			"authenticated anonymous",
			relicv1.Access_ACCESS_AUTHENTICATED,
			anon,
			connect.CodeUnauthenticated,
		},
		{
			"authenticated user",
			relicv1.Access_ACCESS_AUTHENTICATED,
			user,
			0,
		},
		{
			"admin anonymous",
			relicv1.Access_ACCESS_ADMIN,
			anon,
			connect.CodeUnauthenticated,
		},
		{
			"admin user",
			relicv1.Access_ACCESS_ADMIN,
			user,
			connect.CodePermissionDenied,
		},
		{
			"admin admin",
			relicv1.Access_ACCESS_ADMIN,
			admin,
			0,
		},
		{
			"admin viewer",
			relicv1.Access_ACCESS_ADMIN,
			viewer,
			connect.CodePermissionDenied,
		},
		{
			"viewer anonymous",
			relicv1.Access_ACCESS_VIEWER,
			anon,
			connect.CodeUnauthenticated,
		},
		{
			"viewer user",
			relicv1.Access_ACCESS_VIEWER,
			user,
			connect.CodePermissionDenied,
		},
		{
			"viewer viewer",
			relicv1.Access_ACCESS_VIEWER,
			viewer,
			0,
		},
		{
			"viewer admin",
			relicv1.Access_ACCESS_VIEWER,
			admin,
			0,
		},
		{
			"no rule fails closed",
			relicv1.Access_ACCESS_UNSPECIFIED,
			admin,
			connect.CodePermissionDenied,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := allowed(tt.access, tt.p)

			var ce *connect.Error
			switch {
			case tt.want == 0 && err != nil:
				t.Errorf("err = %v, want allowed", err)
			case tt.want != 0 && (!errors.As(err, &ce) || ce.Code() != tt.want):
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestCodeOf(t *testing.T) {
	t.Parallel()

	if c := codeOf(repository.ErrRepositoryNotFound).Code(); c != connect.CodeNotFound {
		t.Errorf("not found = %v", c)
	}

	if c := codeOf(fmt.Errorf("save: %w", replication.ErrInvalid)).Code(); c != connect.CodeInvalidArgument {
		t.Errorf("invalid replication = %v", c)
	}

	for _, err := range []error{
		repository.ErrSelfCopy,
		fmt.Errorf("%w: archive.rar", repository.ErrUnsupportedArchive),
	} {
		if c := codeOf(err).Code(); c != connect.CodeInvalidArgument {
			t.Errorf("%v = %v, want InvalidArgument", err, c)
		}
	}

	upstreamMismatch := fmt.Errorf(
		"%w: a.txt does not match: %w",
		repository.ErrUpstream,
		repository.ErrChecksumMismatch,
	)
	if c := codeOf(upstreamMismatch).Code(); c != connect.CodeUnavailable {
		t.Errorf("a file that does not match its upstream's checksum = %v, want Unavailable", c)
	}

	if c := codeOf(fmt.Errorf("%w: gate", release.ErrGateNotFound)).Code(); c != connect.CodeNotFound {
		t.Errorf("promotion gate not found = %v", c)
	}

	if c := codeOf(fmt.Errorf("%w: no environment", release.ErrInvalidGate)).Code(); c != connect.CodeInvalidArgument {
		t.Errorf("invalid promotion gate = %v", c)
	}

	internal := codeOf(errors.New("password=hunter2 leaked in a driver error"))
	if internal.Code() != connect.CodeInternal || internal.Message() != "internal error" {
		t.Errorf("unexpected error = %v %q, want Internal without details", internal.Code(), internal.Message())
	}
}
