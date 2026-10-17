//go:build integration

package objectstore_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"relic/internal/platform/objectstore"
	"relic/internal/platform/objectstore/objectstoretest"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

var endpoint string

var buckets atomic.Int64

func TestMain(m *testing.M) {
	ctx := context.Background()

	c, err := testcontainers.Run(ctx, "chrislusf/seaweedfs:4.48",
		testcontainers.WithCmd("server", "-s3", "-s3.port=8333", "-ip.bind=0.0.0.0",
			"-master.volumeSizeLimitMB=64", "-volume.max=200"),
		testcontainers.WithExposedPorts("8333/tcp"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("8333/tcp").WithStartupTimeout(2*time.Minute)),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start seaweedfs:", err)
		os.Exit(1)
	}

	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "8333/tcp")
	endpoint = "http://" + host + ":" + port.Port()
	code := m.Run()
	_ = testcontainers.TerminateContainer(c)

	os.Exit(code)
}

func newStore(t *testing.T) *objectstore.S3Store {
	t.Helper()

	return newStoreWith(t, 0)
}

func newStoreWith(t *testing.T, maxConcurrentUploads int) *objectstore.S3Store {
	t.Helper()

	s, _, _ := newStoreAndClient(t, maxConcurrentUploads)

	return s
}

func newStoreAndClient(t *testing.T, maxConcurrentUploads int) (*objectstore.S3Store, *s3.Client, string) {
	t.Helper()
	ctx := t.Context()
	client := s3.New(s3.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(endpoint),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider("any", "any", ""),
	})
	n := strconv.FormatInt(buckets.Add(1), 10)

	blobs, tmp := "blobs-"+n, "tmp-"+n
	for _, b := range []string{
		blobs,
		tmp,
	} {
		deadline := time.Now().Add(time.Minute)

		for {
			_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
				Bucket: aws.String(b),
			})
			if err == nil || time.Now().After(deadline) {
				if err != nil {
					t.Fatalf("create bucket %s: %v", b, err)
				}

				break
			}

			time.Sleep(time.Second)
		}
	}

	return objectstore.NewS3(
		objectstore.S3Config{
			Endpoint:             endpoint,
			Region:               "us-east-1",
			AccessKey:            "any",
			SecretKey:            "any",
			ArtifactsBucket:      blobs,
			TemporaryBucket:      tmp,
			MaxConcurrentUploads: maxConcurrentUploads,
		},
	), client, tmp
}

func TestS3StoreRecordsItsLayoutOnceItsBucketExists(t *testing.T) {
	t.Parallel()

	_, client, _ := newStoreAndClient(t, 0)
	missing := "later-" + strconv.FormatInt(buckets.Add(1), 10)
	store := objectstore.NewS3(objectstore.S3Config{
		Endpoint:        endpoint,
		Region:          "us-east-1",
		AccessKey:       "any",
		SecretKey:       "any",
		ArtifactsBucket: missing,
		TemporaryBucket: missing + "-tmp",
	})

	if err := objectstore.RecordLayout(t.Context(), store); !errors.Is(err, objectstore.ErrNoBucket) {
		t.Fatalf("RecordLayout without the bucket = %v, want ErrNoBucket", err)
	}

	if _, err := store.Head(t.Context(), "readyz/probe"); !errors.Is(err, objectstore.ErrNotFound) {
		t.Errorf("Head without the bucket = %v, want ErrNotFound, which readiness reads as ready", err)
	}

	awaited := make(chan error, 1)

	go func() {
		awaited <- objectstore.AwaitLayout(t.Context(), store, 100*time.Millisecond)
	}()

	if _, err := client.CreateBucket(t.Context(), &s3.CreateBucketInput{
		Bucket: aws.String(missing),
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-awaited:
		if err != nil {
			t.Fatalf("AwaitLayout = %v", err)
		}
	case <-time.After(time.Minute):
		t.Fatal("AwaitLayout did not return after the bucket was created")
	}

	if got := objectstoretest.Read(t, store, objectstore.LayoutKey, nil); got != `{"version":1}`+"\n" {
		t.Errorf("the bucket records %q", got)
	}
}

func TestS3Store(t *testing.T) {
	t.Parallel()

	objectstoretest.RunContract(t, func(t *testing.T) objectstore.Store {
		t.Helper()

		return newStore(t)
	})
}

func TestS3StorePresignsGets(t *testing.T) {
	t.Parallel()

	s := newStore(t)
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

	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("presigned GET = %d", resp.StatusCode)
	}
}

func TestPutMemory(t *testing.T) { //nolint:paralleltest // measures the process's heap
	s := newStore(t)

	var base runtime.MemStats

	runtime.GC()
	runtime.ReadMemStats(&base)

	var peak atomic.Uint64

	done := make(chan struct{})
	sampled := make(chan struct{})

	go func() {
		defer close(sampled)

		var m runtime.MemStats

		for {
			runtime.ReadMemStats(&m)
			peak.Store(max(peak.Load(), m.HeapInuse))

			select {
			case <-done:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()

	body := io.LimitReader(mrand.NewChaCha8([32]byte{}), 64<<20)
	err := s.Put(t.Context(), objectstore.TemporaryKeyPrefix+"memory", body, -1)

	close(done)
	<-sampled

	if err != nil {
		t.Fatal(err)
	}

	if held := (peak.Load() - base.HeapInuse) >> 20; held > 40 {
		t.Errorf("a 64 MiB upload held %d MiB of heap at its peak, want at most 40", held)
	}
}

func TestPutConcurrencyBounded(t *testing.T) {
	t.Parallel()

	s := newStoreWith(t, 2)

	release := make(chan struct{})

	var started atomic.Int32

	errs := make(chan error, 3)

	for i := range 3 {
		go func() {
			body := &gatedReader{
				started: &started,
				release: release,
				r:       bytes.NewReader([]byte("content")),
			}
			errs <- s.Put(t.Context(), objectstore.TemporaryKeyPrefix+"bounded-"+strconv.Itoa(i), body, -1)
		}()
	}

	deadline := time.Now().Add(10 * time.Second)
	for started.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	time.Sleep(200 * time.Millisecond)

	if n := started.Load(); n != 2 {
		t.Fatalf("uploads reading their body = %d, want 2", n)
	}

	close(release)

	for range 3 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

type gatedReader struct {
	started *atomic.Int32
	release chan struct{}
	once    sync.Once
	r       io.Reader
}

func (g *gatedReader) Read(p []byte) (int, error) {
	g.once.Do(func() {
		g.started.Add(1)
		<-g.release
	})

	return g.r.Read(p)
}

// streamOnly hides io.Seeker, so the store cannot learn a body's length.
type streamOnly struct {
	io.Reader
}

func TestS3StoreUploadsMorePartsThanTheDefaultPartSizeAllows(t *testing.T) {
	t.Parallel()

	const size = 30 << 20

	tests := []struct {
		name string
		size int64
	}{
		{
			name: "known length",
			size: size,
		},
		{
			name: "unknown length",
			size: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newStore(t)
			s.SetUploadLimits(4, 40<<20)

			key := objectstore.TemporaryKeyPrefix + "parts"

			body := streamOnly{
				Reader: io.LimitReader(mrand.NewChaCha8([32]byte{}), size),
			}
			if err := s.Put(t.Context(), key, body, tt.size); err != nil {
				t.Fatalf("Put of %d bytes in at most 4 parts: %v", size, err)
			}

			info, err := s.Head(t.Context(), key)
			if err != nil || info.Size != size {
				t.Fatalf("Head = %+v, %v; want %d bytes", info, err, size)
			}
		})
	}
}

// stallingReader yields n bytes, then blocks until ctx is done.
type stallingReader struct {
	ctx  context.Context
	n    int64
	read atomic.Int64
}

func (r *stallingReader) Read(p []byte) (int, error) {
	left := r.n - r.read.Load()
	if left <= 0 {
		<-r.ctx.Done()

		return 0, r.ctx.Err()
	}

	n := int(min(int64(len(p)), left))
	clear(p[:n])
	r.read.Add(int64(n))

	return n, nil
}

func TestS3StoreAbortsTheMultipartUploadOfACancelledPut(t *testing.T) {
	t.Parallel()

	s, client, tmp := newStoreAndClient(t, 0)
	object := "cancelled"

	incomplete := func() int {
		out, err := client.ListMultipartUploads(t.Context(), &s3.ListMultipartUploadsInput{
			Bucket: aws.String(tmp),
			Prefix: aws.String(object),
		})
		if err != nil {
			t.Fatalf("ListMultipartUploads: %v", err)
		}

		return len(out.Uploads)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	body := &stallingReader{
		ctx: ctx,
		n:   12 << 20,
	}

	go func() {
		done <- s.Put(ctx, objectstore.TemporaryKeyPrefix+object, body, -1)
	}()

	deadline := time.Now().Add(30 * time.Second)
	for incomplete() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the multipart upload never started")
		}

		time.Sleep(50 * time.Millisecond)
	}

	cancel()

	if err := <-done; err == nil {
		t.Fatal("Put succeeded after its context was cancelled")
	}

	deadline = time.Now().Add(30 * time.Second)
	for incomplete() > 0 {
		if time.Now().After(deadline) {
			t.Fatal("the cancelled multipart upload was not aborted")
		}

		time.Sleep(100 * time.Millisecond)
	}
}

type recordingTransport struct {
	mu      sync.Mutex
	headers []http.Header
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPut {
		t.mu.Lock()
		t.headers = append(t.headers, req.Header.Clone())
		t.mu.Unlock()
	}

	return http.DefaultTransport.RoundTrip(req)
}

func (t *recordingTransport) checksummed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, h := range t.headers {
		for name := range h {
			if strings.HasPrefix(name, "X-Amz-Checksum-") || name == "X-Amz-Trailer" {
				return true
			}
		}
	}

	return false
}

func TestS3StoreChecksumSetting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		whenRequired bool
		want         bool
	}{
		{
			name: "when supported",
			want: true,
		},
		{
			name:         "when required",
			whenRequired: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, _, tmp := newStoreAndClient(t, 0)
			transport := &recordingTransport{}
			s := objectstore.NewS3WithHTTPClient(
				objectstore.S3Config{
					Endpoint:             endpoint,
					Region:               "us-east-1",
					AccessKey:            "any",
					SecretKey:            "any",
					ArtifactsBucket:      tmp,
					TemporaryBucket:      tmp,
					ChecksumWhenRequired: tt.whenRequired,
				},
				&http.Client{
					Transport: transport,
				},
			)

			key := objectstore.TemporaryKeyPrefix + "checksum"
			if err := s.Put(t.Context(), key, strings.NewReader("content"), 7); err != nil {
				t.Fatal(err)
			}

			if got := objectstoretest.Read(t, s, key, nil); got != "content" {
				t.Fatalf("read %q", got)
			}

			if got := transport.checksummed(); got != tt.want {
				t.Errorf("upload sent a checksum = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestS3StoreListsPastOnePage(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	s.SetListPageSize(2)

	for i := range 5 {
		key := objectstore.Key(fmt.Sprintf("%02x", i) + strings.Repeat("0", 62))
		if err := s.Put(t.Context(), key, strings.NewReader("x"), 1); err != nil {
			t.Fatal(err)
		}
	}

	n := 0

	for _, err := range s.List(t.Context(), "sha256/") {
		if err != nil {
			t.Fatal(err)
		}

		n++
	}

	if n != 5 {
		t.Errorf("listed %d objects over pages of 2, want 5", n)
	}
}
