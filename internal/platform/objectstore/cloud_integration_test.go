//go:build integration

package objectstore_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/api/option"

	"relic/internal/platform/objectstore"
	"relic/internal/platform/objectstore/objectstoretest"
)

var containers atomic.Int64

func startContainer(t *testing.T, image, port string, opts ...testcontainers.ContainerCustomizer) string {
	t.Helper()

	ctx := context.Background()

	c, err := testcontainers.Run(ctx, image, append(opts,
		testcontainers.WithExposedPorts(port),
		testcontainers.WithWaitStrategy(wait.ForListeningPort(port).WithStartupTimeout(2*time.Minute)),
	)...)
	if err != nil {
		t.Fatalf("start %s: %v", image, err)
	}

	t.Cleanup(func() {
		_ = testcontainers.TerminateContainer(c)
	})

	host, _ := c.Host(ctx)
	mapped, _ := c.MappedPort(ctx, port)

	return host + ":" + mapped.Port()
}

func TestAzureStore(t *testing.T) {
	t.Parallel()

	key := make([]byte, 64)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}

	account, accountKey := "relictest", base64.StdEncoding.EncodeToString(key)

	addr := startContainer(
		t,
		"mcr.microsoft.com/azure-storage/azurite:3.37.0",
		"10000/tcp",
		testcontainers.WithEnv(map[string]string{
			"AZURITE_ACCOUNTS": account + ":" + accountKey,
		}),
		testcontainers.WithCmd("azurite-blob", "--blobHost", "0.0.0.0", "--skipApiVersionCheck", "--loose"),
	)
	connection := "DefaultEndpointsProtocol=http;AccountName=" + account + ";AccountKey=" + accountKey +
		";BlobEndpoint=http://" + addr + "/" + account + ";"

	client, err := azblob.NewClientFromConnectionString(connection, nil)
	if err != nil {
		t.Fatal(err)
	}

	objectstoretest.RunContract(t, func(t *testing.T) objectstore.Store {
		t.Helper()

		n := strconv.FormatInt(containers.Add(1), 10)
		blobs, tmp := "blobs-"+n, "tmp-"+n

		for _, c := range []string{
			blobs,
			tmp,
		} {
			if _, err := client.CreateContainer(t.Context(), c, nil); err != nil {
				t.Fatalf("create container %s: %v", c, err)
			}
		}

		s, err := objectstore.NewAzure(objectstore.AzureConfig{
			ConnectionString: connection,
			ArtifactsBucket:  blobs,
			TemporaryBucket:  tmp,
		})
		if err != nil {
			t.Fatal(err)
		}

		return s
	})

	t.Run("presigned GET", func(t *testing.T) {
		if _, err := client.CreateContainer(t.Context(), "presign", nil); err != nil {
			t.Fatal(err)
		}

		s, err := objectstore.NewAzure(objectstore.AzureConfig{
			ConnectionString: connection,
			ArtifactsBucket:  "presign",
			TemporaryBucket:  "presign",
		})
		if err != nil {
			t.Fatal(err)
		}

		presignedGet(t, s)
	})
}

// presignedGet stores an object and fetches it through the URL s presigns,
// without credentials.
func presignedGet(t *testing.T, s objectstore.Store) {
	t.Helper()

	ctx := t.Context()
	key := objectstore.Key("ab" + strings.Repeat("1", 62))

	if err := s.Put(ctx, key, strings.NewReader("signed"), 6); err != nil {
		t.Fatal(err)
	}

	url, err := s.PresignGet(ctx, key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "signed" {
		t.Errorf("presigned GET %s = %d %q", url, resp.StatusCode, body)
	}
}

func TestGCSStore(t *testing.T) {
	t.Parallel()

	addr := startContainer(
		t,
		"fsouza/fake-gcs-server:1.56.1",
		"4443/tcp",
		testcontainers.WithCmd("-scheme", "http", "-port", "4443", "-backend", "memory"),
	)
	endpoint := "http://" + addr

	req, _ := http.NewRequestWithContext(
		t.Context(),
		http.MethodPut,
		endpoint+"/_internal/config",
		strings.NewReader(`{"externalUrl": "`+endpoint+`"}`),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	client, err := storage.NewClient(
		t.Context(),
		option.WithEndpoint(endpoint+"/storage/v1/"),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatal(err)
	}

	objectstoretest.RunContract(t, func(t *testing.T) objectstore.Store {
		t.Helper()

		n := strconv.FormatInt(containers.Add(1), 10)
		blobs, tmp := "blobs-"+n, "tmp-"+n

		for _, b := range []string{
			blobs,
			tmp,
		} {
			if err := client.Bucket(b).Create(t.Context(), "relic-test", nil); err != nil {
				t.Fatalf("create bucket %s: %v", b, err)
			}
		}

		s, err := objectstore.NewGCS(t.Context(), objectstore.GCSConfig{
			Endpoint:        endpoint,
			ArtifactsBucket: blobs,
			TemporaryBucket: tmp,
		})
		if err != nil {
			t.Fatal(err)
		}

		return s
	})
}
