package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"slices"
	"syscall"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

func responseError(status int) error {
	return &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{
			Response: &http.Response{
				StatusCode: status,
			},
		},
		Err: errors.New("response"),
	}
}

func TestS3ErrorClasses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want error
	}{
		{
			name: "no such key",
			err: &smithy.GenericAPIError{
				Code: "NoSuchKey",
			},
			want: ErrNotFound,
		},
		{
			name: "no such bucket",
			err: &smithy.GenericAPIError{
				Code: "NoSuchBucket",
			},
			want: ErrNoBucket,
		},
		{
			name: "slow down",
			err: &smithy.GenericAPIError{
				Code: "SlowDown",
			},
			want: ErrTransient,
		},
		{
			name: "503",
			err:  responseError(http.StatusServiceUnavailable),
			want: ErrTransient,
		},
		{
			name: "429",
			err:  responseError(http.StatusTooManyRequests),
			want: ErrTransient,
		},
		{
			name: "network",
			err: &net.OpError{
				Op:  "dial",
				Err: syscall.ECONNREFUSED,
			},
			want: ErrTransient,
		},
		{
			name: "deadline",
			err:  fmt.Errorf("get: %w", context.DeadlineExceeded),
			want: ErrTransient,
		},
		{
			name: "access denied",
			err: &smithy.GenericAPIError{
				Code: "AccessDenied",
			},
		},
		{
			name: "cancelled by the caller",
			err:  context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := mapErr(tt.err)
			if !errors.Is(got, tt.err) {
				t.Errorf("mapErr(%v) = %v, want it to wrap the original error", tt.err, got)
			}

			for _, class := range []error{
				ErrNotFound,
				ErrNoBucket,
				ErrTransient,
				ErrStorageFull,
			} {
				if want := errors.Is(tt.want, class); errors.Is(got, class) != want {
					t.Errorf("mapErr(%v) is %v: %v, want %v", tt.err, class, !want, want)
				}
			}
		})
	}
}

func TestCloudErrorClasses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mapped    error
		wantClass []error
	}{
		{
			name: "an Azure container that does not exist",
			mapped: azureErr(&azcore.ResponseError{
				ErrorCode:  string(bloberror.ContainerNotFound),
				StatusCode: http.StatusNotFound,
			}),
			wantClass: []error{
				ErrNotFound,
				ErrNoBucket,
			},
		},
		{
			name: "an Azure blob that does not exist",
			mapped: azureErr(&azcore.ResponseError{
				ErrorCode:  string(bloberror.BlobNotFound),
				StatusCode: http.StatusNotFound,
			}),
			wantClass: []error{
				ErrNotFound,
			},
		},
		{
			name:   "a Google Cloud Storage bucket that does not exist",
			mapped: gcsErr(storage.ErrBucketNotExist),
			wantClass: []error{
				ErrNotFound,
				ErrNoBucket,
			},
		},
		{
			name:   "a Google Cloud Storage object that does not exist",
			mapped: gcsErr(storage.ErrObjectNotExist),
			wantClass: []error{
				ErrNotFound,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for _, class := range []error{
				ErrNotFound,
				ErrNoBucket,
				ErrTransient,
			} {
				if want := slices.Contains(tt.wantClass, class); errors.Is(tt.mapped, class) != want {
					t.Errorf("%v is %v: %v, want %v", tt.mapped, class, !want, want)
				}
			}
		})
	}
}

func TestFilesystemErrorClasses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want error
	}{
		{
			name: "missing",
			err:  fs.ErrNotExist,
			want: ErrNotFound,
		},
		{
			name: "disk full",
			err:  syscall.ENOSPC,
			want: ErrStorageFull,
		},
		{
			name: "quota",
			err:  syscall.EDQUOT,
			want: ErrStorageFull,
		},
		{
			name: "permission denied",
			err:  fs.ErrPermission,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := mapFSErr(fmt.Errorf("write: %w", tt.err))
			if !errors.Is(got, tt.err) {
				t.Errorf("mapFSErr(%v) = %v, want it to wrap the original error", tt.err, got)
			}

			for _, class := range []error{
				ErrNotFound,
				ErrNoBucket,
				ErrTransient,
				ErrStorageFull,
			} {
				if want := errors.Is(tt.want, class); errors.Is(got, class) != want {
					t.Errorf("mapFSErr(%v) is %v: %v, want %v", tt.err, class, !want, want)
				}
			}
		})
	}
}

func TestS3StoreBoundsTheWaitForResponses(t *testing.T) {
	t.Parallel()

	s := NewS3(S3Config{
		Endpoint: "http://127.0.0.1:1",
		Region:   "us-east-1",
	})
	if got := s.ResponseTimeout(); got != responseTimeout {
		t.Errorf("response header timeout = %v, want %v", got, responseTimeout)
	}
}
