package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	tmtypes "github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

const maxSingleCopy = 5 << 30

const (
	uploadPartSize    int64 = 5 << 20
	uploadConcurrency       = 2
	maxUploadParts    int64 = 10000
	// maxUnknownUploadSize keeps the parts of an upload within s3UploadMemory.
	maxUnknownUploadSize int64 = 64 << 30
	abortTimeout               = 30 * time.Second
	// responseTimeout bounds the wait for a response's headers; a large
	// server-side copy answers within it.
	responseTimeout = 5 * time.Minute
)

const minCopyPartSize int64 = 512 << 20

// S3Config configures an S3Store.
type S3Config struct {
	// Endpoint is an S3-compatible service's URL; empty uses AWS's own
	// endpoints with virtual-hosted URLs.
	Endpoint string
	Region   string
	// AccessKey and SecretKey are a static key; without one, Credentials
	// signs requests.
	AccessKey string
	SecretKey string
	// Credentials is the AWS default credential chain (IRSA, EKS Pod
	// Identity, the instance or task role, the environment); Open sets it.
	Credentials aws.CredentialsProvider
	// ServerSideEncryption (AES256 or aws:kms), its KMS key and StorageClass
	// apply to every write.
	ServerSideEncryption string
	SSEKMSKeyID          string
	StorageClass         string
	ArtifactsBucket      string
	TemporaryBucket      string
	MaxConcurrentUploads int
	ChecksumWhenRequired bool
}

// S3Store implements Store on an S3-compatible service with path-style URLs.
type S3Store struct {
	client   *s3.Client
	presign  *s3.PresignClient
	uploader *transfermanager.Client
	uploads  uploadGate
	blobs    string
	tmp      string

	maxUploadParts       int64
	maxUnknownUploadSize int64
	listPageSize         int32

	encryption   types.ServerSideEncryption
	kmsKeyID     string
	storageClass types.StorageClass
}

func (s *S3Store) kmsKey() *string {
	if s.kmsKeyID == "" {
		return nil
	}

	return aws.String(s.kmsKeyID)
}

// NewS3 returns a store for cfg.
func NewS3(cfg S3Config) *S3Store {
	return newS3(cfg, nil)
}

func newS3(cfg S3Config, httpClient aws.HTTPClient) *S3Store {
	checksums := aws.RequestChecksumCalculationWhenSupported
	validation := aws.ResponseChecksumValidationWhenSupported

	if cfg.ChecksumWhenRequired {
		checksums = aws.RequestChecksumCalculationWhenRequired
		validation = aws.ResponseChecksumValidationWhenRequired
	}

	if httpClient == nil {
		httpClient = awshttp.NewBuildableClient().WithTransportOptions(func(t *http.Transport) {
			t.ResponseHeaderTimeout = responseTimeout
		})
	}

	creds := cfg.Credentials
	if cfg.AccessKey != "" {
		creds = credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")
	}

	var endpoint *string
	if cfg.Endpoint != "" {
		endpoint = aws.String(cfg.Endpoint)
	}

	client := s3.New(s3.Options{
		Region:                     cfg.Region,
		BaseEndpoint:               endpoint,
		UsePathStyle:               cfg.Endpoint != "",
		Credentials:                creds,
		HTTPClient:                 httpClient,
		RequestChecksumCalculation: checksums,
		ResponseChecksumValidation: validation,
	})

	return &S3Store{
		client:  client,
		presign: s3.NewPresignClient(client),
		uploader: transfermanager.New(client, func(o *transfermanager.Options) {
			o.PartSizeBytes = uploadPartSize
			o.MultipartUploadThreshold = uploadPartSize
			o.Concurrency = uploadConcurrency
			o.FailTimeout = abortTimeout
			o.RequestChecksumCalculation = checksums
		}),
		uploads: newUploadGate(cfg.MaxConcurrentUploads),
		blobs:   cfg.ArtifactsBucket,
		tmp:     cfg.TemporaryBucket,

		encryption:   types.ServerSideEncryption(cfg.ServerSideEncryption),
		kmsKeyID:     cfg.SSEKMSKeyID,
		storageClass: types.StorageClass(cfg.StorageClass),

		maxUploadParts:       maxUploadParts,
		maxUnknownUploadSize: maxUnknownUploadSize,
	}
}

func (s *S3Store) locate(key string) (bucket, object string) {
	if rest, ok := strings.CutPrefix(key, TemporaryKeyPrefix); ok {
		return s.tmp, rest
	}

	return s.blobs, key
}

// R2 requires every part but the last to be the same size.
func (s *S3Store) partSize(size int64) int64 {
	if size <= 0 {
		size = s.maxUnknownUploadSize
	}

	part := max(uploadPartSize, (size+s.maxUploadParts-1)/s.maxUploadParts)

	return (part + 1<<20 - 1) &^ (1<<20 - 1)
}

func (s *S3Store) copyPartSize(size int64) int64 {
	part := max(minCopyPartSize, (size+s.maxUploadParts-1)/s.maxUploadParts)

	return (part + 1<<20 - 1) &^ (1<<20 - 1)
}

// Put implements Store.
func (s *S3Store) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	release, err := s.uploads.acquire(ctx, key)
	if err != nil {
		return err
	}
	defer release()

	bucket, object := s.locate(key)
	in := &transfermanager.UploadObjectInput{
		Bucket:               &bucket,
		Key:                  &object,
		Body:                 r,
		ServerSideEncryption: tmtypes.ServerSideEncryption(s.encryption),
		SSEKMSKeyID:          s.kmsKey(),
		StorageClass:         tmtypes.StorageClass(s.storageClass),
	}

	if size > 0 {
		in.ContentLength = aws.Int64(size)
	}

	part := s.partSize(size)
	if _, err := s.uploader.UploadObject(
		ctx,
		in,
		func(o *transfermanager.Options) {
			o.PartSizeBytes = part
			o.MultipartUploadThreshold = part
			o.MaxUploadParts = s.maxUploadParts
		},
	); err != nil {
		return fmt.Errorf("put %s: %w", key, mapErr(err))
	}

	return nil
}

// Get implements Store.
func (s *S3Store) Get(ctx context.Context, key string, rng *Range) (io.ReadCloser, Info, error) {
	bucket, object := s.locate(key)

	in := &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &object,
	}
	if rng != nil {
		in.Range = aws.String(fmt.Sprintf("bytes=%d-%d", rng.Start, rng.End))
	}

	out, err := s.client.GetObject(ctx, in)
	if err != nil {
		return nil, Info{}, fmt.Errorf("get %s: %w", key, mapErr(err))
	}

	return out.Body, Info{
		Size: aws.ToInt64(out.ContentLength),
	}, nil
}

// Head implements Store.
func (s *S3Store) Head(ctx context.Context, key string) (Info, error) {
	bucket, object := s.locate(key)

	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &bucket,
		Key:    &object,
	})
	if err != nil {
		return Info{}, fmt.Errorf("head %s: %w", key, mapErr(err))
	}

	return Info{
		Size: aws.ToInt64(out.ContentLength),
	}, nil
}

// Copy implements Store, using a multipart copy above S3's 5 GiB CopyObject limit.
func (s *S3Store) Copy(ctx context.Context, srcKey, dstKey string) error {
	info, err := s.Head(ctx, srcKey)
	if err != nil {
		return err
	}

	srcBucket, srcObject := s.locate(srcKey)
	dstBucket, dstObject := s.locate(dstKey)

	source := url.PathEscape(srcBucket) + "/" + escapeKey(srcObject)
	if info.Size <= maxSingleCopy {
		_, err := s.client.CopyObject(
			ctx,
			&s3.CopyObjectInput{
				Bucket:               &dstBucket,
				Key:                  &dstObject,
				CopySource:           &source,
				ServerSideEncryption: s.encryption,
				SSEKMSKeyId:          s.kmsKey(),
				StorageClass:         s.storageClass,
			},
		)
		if err != nil {
			return fmt.Errorf("copy %s to %s: %w", srcKey, dstKey, mapErr(err))
		}

		return nil
	}

	return s.multipartCopy(ctx, source, dstBucket, dstObject, info.Size)
}

func (s *S3Store) multipartCopy(ctx context.Context, source, bucket, object string, size int64) (err error) {
	created, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket:               &bucket,
		Key:                  &object,
		ServerSideEncryption: s.encryption,
		SSEKMSKeyId:          s.kmsKey(),
		StorageClass:         s.storageClass,
	})
	if err != nil {
		return fmt.Errorf("start multipart copy to %s: %w", object, mapErr(err))
	}
	defer func() {
		if err != nil {
			abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), abortTimeout)
			defer cancel()

			_, abortErr := s.client.AbortMultipartUpload(
				abortCtx,
				&s3.AbortMultipartUploadInput{
					Bucket:   &bucket,
					Key:      &object,
					UploadId: created.UploadId,
				},
			)
			err = errors.Join(err, abortErr)
		}
	}()

	var parts []types.CompletedPart

	part := s.copyPartSize(size)

	for n, start := int32(1), int64(0); start < size; n, start = n+1, start+part {
		end := min(start+part, size) - 1

		out, err := s.client.UploadPartCopy(ctx, &s3.UploadPartCopyInput{
			Bucket:          &bucket,
			Key:             &object,
			UploadId:        created.UploadId,
			PartNumber:      aws.Int32(n),
			CopySource:      &source,
			CopySourceRange: aws.String(fmt.Sprintf("bytes=%d-%d", start, end)),
		})
		if err != nil {
			return fmt.Errorf("copy part %d to %s: %w", n, object, mapErr(err))
		}

		parts = append(parts, types.CompletedPart{
			ETag:       out.CopyPartResult.ETag,
			PartNumber: aws.Int32(n),
		})
	}

	_, err = s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   &bucket,
		Key:      &object,
		UploadId: created.UploadId,
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: parts,
		},
	})
	if err != nil {
		return fmt.Errorf("complete multipart copy to %s: %w", object, mapErr(err))
	}

	return nil
}

// Delete implements Store.
func (s *S3Store) Delete(ctx context.Context, key string) error {
	bucket, object := s.locate(key)
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &bucket,
		Key:    &object,
	}); err != nil {
		return fmt.Errorf("delete %s: %w", key, mapErr(err))
	}

	return nil
}

// List implements Store.
func (s *S3Store) List(ctx context.Context, prefix string) iter.Seq2[Object, error] {
	return func(yield func(Object, error) bool) {
		bucket, objectPrefix := s.locate(prefix)
		keyPrefix := strings.TrimSuffix(prefix, objectPrefix)

		in := &s3.ListObjectsV2Input{
			Bucket: &bucket,
			Prefix: &objectPrefix,
		}
		if s.listPageSize > 0 {
			in.MaxKeys = &s.listPageSize
		}

		pages := s3.NewListObjectsV2Paginator(s.client, in)
		for pages.HasMorePages() {
			page, err := pages.NextPage(ctx)
			if err != nil {
				yield(Object{}, fmt.Errorf("list %s: %w", prefix, mapErr(err)))
				return
			}

			for _, o := range page.Contents {
				if !yield(Object{
					Key:      keyPrefix + aws.ToString(o.Key),
					Size:     aws.ToInt64(o.Size),
					Modified: aws.ToTime(o.LastModified),
				}, nil) {
					return
				}
			}
		}
	}
}

// PresignGet implements Store.
func (s *S3Store) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	bucket, object := s.locate(key)

	req, err := s.presign.PresignGetObject(
		ctx,
		&s3.GetObjectInput{
			Bucket: &bucket,
			Key:    &object,
		},
		s3.WithPresignExpires(ttl),
	)
	if err != nil {
		return "", fmt.Errorf("presign %s: %w", key, err)
	}

	return req.URL, nil
}

func escapeKey(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}

	return strings.Join(parts, "/")
}

func mapErr(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}

	if apiErr, ok := errors.AsType[smithy.APIError](err); ok {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return errors.Join(ErrNotFound, err)
		case "NoSuchBucket":
			return errors.Join(ErrNoBucket, err)
		case "SlowDown", "ServiceUnavailable", "InternalError", "RequestTimeout", "Throttling", "ThrottlingException":
			return errors.Join(ErrTransient, err)
		}
	}

	if respErr, ok := errors.AsType[*smithyhttp.ResponseError](err); ok {
		if status := respErr.HTTPStatusCode(); status >= 500 || status == http.StatusTooManyRequests {
			return errors.Join(ErrTransient, err)
		}
	}

	if _, ok := errors.AsType[net.Error](err); ok || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.ErrUnexpectedEOF) {
		return errors.Join(ErrTransient, err)
	}

	return err
}
