package objectstore

import (
	"net/http"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
)

// SetUploadLimits overrides S3's part limit and the size assumed for a body
// of unknown length.
func (s *S3Store) SetUploadLimits(maxParts, maxUnknownSize int64) {
	s.maxUploadParts = maxParts
	s.maxUnknownUploadSize = maxUnknownSize
}

// ObserveDirectorySyncs calls observe with each directory the store syncs,
// relative to its root, before syncing it.
func (s *FilesystemStore) ObserveDirectorySyncs(observe func(dir string)) {
	syncDir := s.syncDir
	s.syncDir = func(dir string) error {
		observe(dir)

		return syncDir(dir)
	}
}

// NewS3WithHTTPClient is NewS3 sending requests through c.
func NewS3WithHTTPClient(cfg S3Config, c *http.Client) *S3Store {
	return newS3(cfg, c)
}

// ResponseTimeout is how long the store's default HTTP client waits for
// response headers.
func (s *S3Store) ResponseTimeout() time.Duration {
	client, _ := s.client.Options().HTTPClient.(*awshttp.BuildableClient)

	return client.GetTransport().ResponseHeaderTimeout
}

// SetListPageSize makes listings fetch n keys per request.
func (s *S3Store) SetListPageSize(n int32) { s.listPageSize = n }
