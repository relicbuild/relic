package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/service"
)

const (
	azureBlockSize   = 8 << 20
	azureConcurrency = 4
	// azureClockSkew starts a user delegation SAS early, for clocks behind.
	azureClockSkew = 5 * time.Minute
	// azureSASVersion is the service version presigned URLs are signed
	// for: one Azure Storage and Azurite both verify.
	azureSASVersion = "2026-06-06"
)

// AzureConfig configures an AzureStore.
type AzureConfig struct {
	// AccountURL is the storage account's blob endpoint,
	// https://<account>.blob.core.windows.net.
	AccountURL string
	// ConnectionString authenticates with the account key; without one,
	// the default Azure credential does (workload identity, managed
	// identity, the environment).
	ConnectionString string
	// ArtifactsBucket and TemporaryBucket name the two containers.
	ArtifactsBucket string
	TemporaryBucket string
}

// AzureStore implements Store on Azure Blob Storage.
type AzureStore struct {
	client *azblob.Client
	// sharedKey signs presigned URLs when the account key is configured.
	sharedKey  *azblob.SharedKeyCredential
	blobs, tmp string
}

// NewAzure returns a store for cfg.
func NewAzure(cfg AzureConfig) (*AzureStore, error) {
	s := &AzureStore{
		blobs: cfg.ArtifactsBucket,
		tmp:   cfg.TemporaryBucket,
	}

	var err error

	if cfg.ConnectionString != "" {
		s.client, err = azblob.NewClientFromConnectionString(cfg.ConnectionString, nil)
		if err == nil {
			s.sharedKey, err = connectionStringKey(cfg.ConnectionString)
		}
	} else {
		var cred azcore.TokenCredential

		cred, err = azidentity.NewDefaultAzureCredential(nil)
		if err == nil {
			s.client, err = azblob.NewClient(cfg.AccountURL, cred, nil)
		}
	}

	if err != nil {
		return nil, fmt.Errorf("azure blob storage: %w", err)
	}

	return s, nil
}

// connectionStringKey is the account key credential of a connection string.
func connectionStringKey(connection string) (*azblob.SharedKeyCredential, error) {
	fields := map[string]string{}

	for part := range strings.SplitSeq(connection, ";") {
		if k, v, ok := strings.Cut(part, "="); ok {
			fields[k] = v
		}
	}

	if fields["AccountName"] == "" || fields["AccountKey"] == "" {
		return nil, errors.New("the connection string has no AccountName and AccountKey")
	}

	return azblob.NewSharedKeyCredential(fields["AccountName"], fields["AccountKey"])
}

func (s *AzureStore) locate(key string) (container, name string) {
	if rest, ok := strings.CutPrefix(key, TemporaryKeyPrefix); ok {
		return s.tmp, rest
	}

	return s.blobs, key
}

func (s *AzureStore) blob(key string) *blob.Client {
	container, name := s.locate(key)

	return s.client.ServiceClient().NewContainerClient(container).NewBlobClient(name)
}

// Put implements Store. A failed read of r commits no block list, so the
// key is left as it was.
func (s *AzureStore) Put(ctx context.Context, key string, r io.Reader, _ int64) error {
	container, name := s.locate(key)

	if _, err := s.client.UploadStream(ctx, container, name, r, &azblob.UploadStreamOptions{
		BlockSize:   azureBlockSize,
		Concurrency: azureConcurrency,
	}); err != nil {
		return fmt.Errorf("put %s: %w", key, azureErr(err))
	}

	return nil
}

// Get implements Store.
func (s *AzureStore) Get(ctx context.Context, key string, rng *Range) (io.ReadCloser, Info, error) {
	container, name := s.locate(key)

	opts := &azblob.DownloadStreamOptions{}
	if rng != nil {
		opts.Range = azblob.HTTPRange{
			Offset: rng.Start,
			Count:  rng.End - rng.Start + 1,
		}
	}

	resp, err := s.client.DownloadStream(ctx, container, name, opts)
	if err != nil {
		return nil, Info{}, fmt.Errorf("get %s: %w", key, azureErr(err))
	}

	var size int64
	if resp.ContentLength != nil {
		size = *resp.ContentLength
	}

	return resp.Body, Info{
		Size: size,
	}, nil
}

// Head implements Store.
func (s *AzureStore) Head(ctx context.Context, key string) (Info, error) {
	props, err := s.blob(key).GetProperties(ctx, nil)
	if err != nil {
		return Info{}, fmt.Errorf("head %s: %w", key, azureErr(err))
	}

	var size int64
	if props.ContentLength != nil {
		size = *props.ContentLength
	}

	return Info{
		Size: size,
	}, nil
}

// Copy implements Store by streaming the source through Relic: a server-side
// copy would need the source authorized separately under Entra ID.
func (s *AzureStore) Copy(ctx context.Context, srcKey, dstKey string) error {
	body, _, err := s.Get(ctx, srcKey, nil)
	if err != nil {
		return fmt.Errorf("copy %s to %s: %w", srcKey, dstKey, err)
	}

	defer func() {
		_ = body.Close()
	}()

	return s.Put(ctx, dstKey, body, -1)
}

// Delete implements Store; a missing key is not an error.
func (s *AzureStore) Delete(ctx context.Context, key string) error {
	container, name := s.locate(key)

	_, err := s.client.DeleteBlob(ctx, container, name, nil)
	if err != nil && !bloberror.HasCode(err, bloberror.BlobNotFound) {
		return fmt.Errorf("delete %s: %w", key, azureErr(err))
	}

	return nil
}

// PresignGet implements Store with a read-only SAS: signed with the account
// key, or with a user delegation key under Entra ID.
func (s *AzureStore) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	b := s.blob(key)
	expiry := time.Now().UTC().Add(ttl)

	container, name := s.locate(key)

	if s.sharedKey != nil {
		params, err := sas.BlobSignatureValues{
			Version:    azureSASVersion,
			ExpiryTime: expiry,
			Permissions: (&sas.BlobPermissions{
				Read: true,
			}).String(),
			ContainerName: container,
			BlobName:      name,
		}.SignWithSharedKey(s.sharedKey)
		if err != nil {
			return "", fmt.Errorf("presign %s: %w", key, err)
		}

		return plainSlashes(b.URL()) + "?" + params.Encode(), nil
	}

	start := time.Now().UTC().Add(-azureClockSkew)

	cred, err := s.client.ServiceClient().GetUserDelegationCredential(ctx, service.KeyInfo{
		Start:  new(start.Format(sas.TimeFormat)),
		Expiry: new(expiry.Format(sas.TimeFormat)),
	}, nil)
	if err != nil {
		return "", fmt.Errorf("presign %s: %w", key, azureErr(err))
	}

	params, err := sas.BlobSignatureValues{
		Version:    azureSASVersion,
		Protocol:   sas.ProtocolHTTPS,
		StartTime:  start,
		ExpiryTime: expiry,
		Permissions: (&sas.BlobPermissions{
			Read: true,
		}).String(),
		ContainerName: container,
		BlobName:      name,
	}.SignWithUserDelegation(cred)
	if err != nil {
		return "", fmt.Errorf("presign %s: %w", key, err)
	}

	return plainSlashes(b.URL()) + "?" + params.Encode(), nil
}

// plainSlashes writes the folder separators of a blob URL's path as "/",
// which every service reads as the same blob and canonicalizes alike.
func plainSlashes(u string) string {
	path, query, hasQuery := strings.Cut(u, "?")
	path = strings.ReplaceAll(path, "%2F", "/")

	if hasQuery {
		return path + "?" + query
	}

	return path
}

// List implements Store.
func (s *AzureStore) List(ctx context.Context, prefix string) iter.Seq2[Object, error] {
	return func(yield func(Object, error) bool) {
		container, namePrefix := s.locate(prefix)
		keyPrefix := strings.TrimSuffix(prefix, namePrefix)

		pager := s.client.NewListBlobsFlatPager(container, &azblob.ListBlobsFlatOptions{
			Prefix: &namePrefix,
		})
		for pager.More() {
			page, err := pager.NextPage(ctx)
			if err != nil {
				yield(Object{}, fmt.Errorf("list %s: %w", prefix, azureErr(err)))
				return
			}

			for _, b := range page.Segment.BlobItems {
				o := Object{
					Key: keyPrefix + *b.Name,
				}
				if b.Properties != nil {
					if b.Properties.ContentLength != nil {
						o.Size = *b.Properties.ContentLength
					}

					if b.Properties.LastModified != nil {
						o.Modified = *b.Properties.LastModified
					}
				}

				if !yield(o, nil) {
					return
				}
			}
		}
	}
}

func azureErr(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}

	if bloberror.HasCode(err, bloberror.ContainerNotFound) {
		return errors.Join(ErrNotFound, ErrNoBucket, err)
	}

	if bloberror.HasCode(err, bloberror.BlobNotFound) {
		return errors.Join(ErrNotFound, err)
	}

	if respErr, ok := errors.AsType[*azcore.ResponseError](err); ok {
		if respErr.StatusCode == http.StatusNotFound {
			return errors.Join(ErrNotFound, err)
		}

		if respErr.StatusCode >= 500 || respErr.StatusCode == http.StatusTooManyRequests {
			return errors.Join(ErrTransient, err)
		}
	}

	return err
}
