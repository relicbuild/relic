// Command bandwidth measures how many bytes per second Relic moves under
// concurrent downloads and uploads, and what proxying through Relic costs
// against the object store read directly.
package main

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"uuid"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"relic/internal/platform/objectstore"
)

const repo = "bandwidth-local"

type config struct {
	relic, user, password    string
	s3, region, bucket, temp string
	accessKey, secretKey     string
	downloadSizes            []int64
	downloadConcurrency      []int
	uploadSizes              []int64
	uploadConcurrency        []int
	step                     time.Duration
	containers               []string
	out                      string
}

// Result is one measured step.
type Result struct {
	Op          string  `json:"op"`
	Target      string  `json:"target"`
	Size        int64   `json:"size"`
	Concurrency int     `json:"concurrency"`
	Seconds     float64 `json:"seconds"`
	Bytes       int64   `json:"bytes"`
	Requests    int     `json:"requests"`
	Errors      int     `json:"errors"`
	MBps        float64 `json:"mbps"`
	TTFBp50     float64 `json:"ttfbP50Ms"`
	TTFBp95     float64 `json:"ttfbP95Ms"`
	Totalp95    float64 `json:"totalP95Ms"`
	Stats       []Stats `json:"stats"`
}

// Stats is a container's resource use during a step.
type Stats struct {
	Container string  `json:"container"`
	CPUAvg    float64 `json:"cpuAvgPercent"`
	CPUMax    float64 `json:"cpuMaxPercent"`
	MemMaxMiB float64 `json:"memMaxMiB"`
}

func main() {
	cfg := parseFlags()

	ctx := context.Background()
	b := newBench(cfg)

	if err := b.run(ctx); err != nil {
		log.Fatal(err)
	}
}

func parseFlags() config {
	var cfg config

	var dsizes, dconc, usizes, uconc, containers string

	flag.StringVar(&cfg.relic, "relic", "http://localhost:28092", "Relic base URL")
	flag.StringVar(&cfg.user, "user", "admin", "Relic user")
	flag.StringVar(&cfg.password, "password", os.Getenv("RELIC_PASSWORD"), "Relic password (default $RELIC_PASSWORD)")
	flag.StringVar(&cfg.s3, "s3", "http://localhost:18335", "object store endpoint, as clients reach it")
	flag.StringVar(&cfg.region, "region", "us-east-1", "object store region")
	flag.StringVar(&cfg.bucket, "bucket", "artifacts", "Relic's artifacts bucket")
	flag.StringVar(&cfg.temp, "temporary-bucket", "temporary", "Relic's temporary bucket (direct uploads go there)")
	flag.StringVar(&dsizes, "download-sizes", "10MiB,100MiB,1GiB", "file sizes to download")
	flag.StringVar(&dconc, "download-concurrency", "1,8,32,128,256", "concurrent downloaders")
	flag.StringVar(&usizes, "upload-sizes", "10MiB,100MiB", "file sizes to upload")
	flag.StringVar(&uconc, "upload-concurrency", "1,8,32", "concurrent uploaders")
	flag.DurationVar(&cfg.step, "step", 15*time.Second, "how long each step runs")
	flag.StringVar(&containers, "containers", "", "comma-separated containers to sample with docker stats")
	flag.StringVar(&cfg.out, "out", "", "write the results as JSON here")
	flag.Parse()

	cfg.accessKey = os.Getenv("RELIC_OBJECT_STORAGE_ACCESS_KEY")
	cfg.secretKey = os.Getenv("RELIC_OBJECT_STORAGE_SECRET_KEY")

	var err error
	if cfg.downloadSizes, err = sizes(dsizes); err == nil {
		if cfg.uploadSizes, err = sizes(usizes); err == nil {
			if cfg.downloadConcurrency, err = ints(dconc); err == nil {
				cfg.uploadConcurrency, err = ints(uconc)
			}
		}
	}

	if err != nil {
		log.Fatal(err)
	}

	if containers != "" {
		cfg.containers = strings.Split(containers, ",")
	}

	return cfg
}

type bench struct {
	cfg     config
	http    *http.Client
	s3      *s3.Client
	presign *s3.PresignClient
	results []Result
}

func newBench(cfg config) *bench {
	client := s3.New(s3.Options{
		Region:       cfg.region,
		BaseEndpoint: aws.String(cfg.s3),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.accessKey, cfg.secretKey, ""),
	})

	return &bench{
		cfg: cfg,
		http: &http.Client{
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 512,
				DisableCompression:  true,
			},
		},
		s3:      client,
		presign: s3.NewPresignClient(client),
	}
}

func (b *bench) run(ctx context.Context) error {
	if err := b.relicDo(ctx, http.MethodPut, "/artifactory/api/repositories/"+repo,
		strings.NewReader(`{"rclass":"local","packageType":"generic"}`), -1, "application/json"); err != nil {
		return fmt.Errorf("create repository: %w", err)
	}
	defer func() {
		err := b.relicDo(
			context.WithoutCancel(ctx),
			http.MethodDelete,
			"/artifactory/api/repositories/"+repo,
			nil,
			-1,
			"",
		)
		if err != nil {
			log.Printf("delete repository: %v", err)
		}
	}()

	for _, size := range b.cfg.downloadSizes {
		if err := b.downloads(ctx, size); err != nil {
			return err
		}
	}

	for _, size := range b.cfg.uploadSizes {
		if err := b.uploads(ctx, size); err != nil {
			return err
		}
	}

	b.report()

	if b.cfg.out == "" {
		return nil
	}

	doc, err := json.MarshalIndent(b.results, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(b.cfg.out, doc, 0o600)
}

func (b *bench) downloads(ctx context.Context, size int64) error {
	path := "seed/" + human(size) + ".bin"
	log.Printf("seed %s (%s)", path, human(size))

	if err := b.relicDo(ctx, http.MethodPut, "/artifactory/"+repo+"/"+path, newRandom(size), size, ""); err != nil {
		return fmt.Errorf("seed %s: %w", path, err)
	}

	sha, err := b.sha256Of(ctx, path)
	if err != nil {
		return err
	}

	direct, err := b.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.cfg.bucket),
		Key:    aws.String(objectstore.Key(sha)),
	}, s3.WithPresignExpires(24*time.Hour))
	if err != nil {
		return fmt.Errorf("presign: %w", err)
	}

	targets := []struct {
		name string
		req  func(ctx context.Context) (*http.Request, error)
	}{
		{
			"relic",
			func(ctx context.Context) (*http.Request, error) {
				r, err := http.NewRequestWithContext(
					ctx,
					http.MethodGet,
					b.cfg.relic+"/artifactory/"+repo+"/"+path,
					nil,
				)
				if err == nil {
					r.SetBasicAuth(b.cfg.user, b.cfg.password)
				}

				return r, err
			},
		},
		{
			"direct",
			func(ctx context.Context) (*http.Request, error) {
				return http.NewRequestWithContext(ctx, http.MethodGet, direct.URL, nil)
			},
		},
	}

	for _, c := range b.cfg.downloadConcurrency {
		for _, t := range targets {
			b.step(ctx, "download", t.name, size, c, func(ctx context.Context, _ int) (int64, time.Duration, error) {
				r, err := t.req(ctx)
				if err != nil {
					return 0, 0, err
				}

				return b.get(r)
			}, nil)
		}
	}

	return nil
}

func (b *bench) uploads(ctx context.Context, size int64) error {
	for _, c := range b.cfg.uploadConcurrency {
		var n atomic.Int64

		b.step(ctx, "upload", "relic", size, c, func(ctx context.Context, w int) (int64, time.Duration, error) {
			p := fmt.Sprintf("/artifactory/%s/up/%d/%d.bin", repo, w, n.Add(1))
			return b.put(ctx, b.cfg.relic+p, newRandom(size), size, true)
		}, func(ctx context.Context) {
			if err := b.relicDo(ctx, http.MethodDelete, "/artifactory/"+repo+"/up", nil, -1, ""); err != nil {
				log.Printf("clean up uploads: %v", err)
			}
		})

		var (
			mu   sync.Mutex
			keys []string
		)

		b.step(ctx, "upload", "direct", size, c, func(ctx context.Context, _ int) (int64, time.Duration, error) {
			key := "bandwidth/" + uuid.NewV7().String()

			req, err := b.presign.PresignPutObject(ctx, &s3.PutObjectInput{
				Bucket:        aws.String(b.cfg.temp),
				Key:           aws.String(key),
				ContentLength: aws.Int64(size),
			})
			if err != nil {
				return 0, 0, err
			}

			mu.Lock()

			keys = append(keys, key)
			mu.Unlock()

			return b.put(ctx, req.URL, newRandom(size), size, false)
		}, func(ctx context.Context) {
			for _, k := range keys {
				if _, err := b.s3.DeleteObject(
					ctx,
					&s3.DeleteObjectInput{
						Bucket: aws.String(b.cfg.temp),
						Key:    aws.String(k),
					},
				); err != nil {
					log.Printf("clean up %s: %v", k, err)
				}
			}
		})
	}

	return nil
}

func (b *bench) step(
	ctx context.Context,
	op, target string,
	size int64,
	c int,
	do func(ctx context.Context, worker int) (int64, time.Duration, error),
	cleanup func(ctx context.Context),
) {
	log.Printf("%s %s %s x%d", op, target, human(size), c)

	stepCtx, cancel := context.WithTimeout(ctx, b.cfg.step)
	defer cancel()

	stats := b.sample(stepCtx)

	var (
		bytes    atomic.Int64
		mu       sync.Mutex
		ttfb     []time.Duration
		total    []time.Duration
		errCount int
		errMsgs  = map[string]bool{}
		wg       sync.WaitGroup
	)

	start := time.Now()

	for w := range c {
		wg.Go(func() {
			for stepCtx.Err() == nil {
				t0 := time.Now()
				n, first, err := do(stepCtx, w)

				if stepCtx.Err() != nil {
					bytes.Add(n)
					return
				}

				mu.Lock()
				if err != nil {
					errCount++

					if len(errMsgs) < 5 {
						errMsgs[err.Error()] = true
					}
				} else {
					bytes.Add(n)

					ttfb = append(ttfb, first)
					total = append(total, time.Since(t0))
				}
				mu.Unlock()

				if err != nil {
					time.Sleep(100 * time.Millisecond)
				}
			}
		})
	}

	wg.Wait()

	elapsed := time.Since(start)

	r := Result{
		Op:          op,
		Target:      target,
		Size:        size,
		Concurrency: c,
		Seconds:     elapsed.Seconds(),
		Bytes:       bytes.Load(),
		Requests:    len(total),
		Errors:      errCount,
		MBps:        float64(bytes.Load()) / elapsed.Seconds() / 1e6,
		TTFBp50:     ms(percentile(ttfb, 50)),
		TTFBp95:     ms(percentile(ttfb, 95)),
		Totalp95:    ms(percentile(total, 95)),
		Stats:       <-stats,
	}
	b.results = append(b.results, r)
	log.Printf("  %.0f MB/s, %d requests, %d errors", r.MBps, r.Requests, r.Errors)

	for msg := range errMsgs {
		log.Printf("  error: %s", msg)
	}

	if cleanup != nil {
		cleanup(ctx)
	}
}

func (b *bench) get(r *http.Request) (int64, time.Duration, error) {
	t0 := time.Now()

	resp, err := b.http.Do(r)
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	first := time.Since(t0)

	n, err := io.Copy(io.Discard, resp.Body)
	if err == nil && resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("status %d", resp.StatusCode)
	}

	return n, first, err
}

func (b *bench) put(
	ctx context.Context,
	url string,
	body io.Reader,
	size int64,
	auth bool,
) (int64, time.Duration, error) {
	counted := &countingReader{
		r: body,
	}

	r, err := http.NewRequestWithContext(ctx, http.MethodPut, url, counted)
	if err != nil {
		return 0, 0, err
	}

	r.ContentLength = size
	if auth {
		r.SetBasicAuth(b.cfg.user, b.cfg.password)
	}

	t0 := time.Now()

	resp, err := b.http.Do(r)
	if err != nil {
		return counted.n, 0, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return counted.n, 0, fmt.Errorf("status %d", resp.StatusCode)
	}

	return counted.n, time.Since(t0), nil
}

func (b *bench) relicDo(
	ctx context.Context,
	method, path string,
	body io.Reader,
	size int64,
	contentType string,
) error {
	r, err := http.NewRequestWithContext(ctx, method, b.cfg.relic+path, body)
	if err != nil {
		return err
	}

	r.SetBasicAuth(b.cfg.user, b.cfg.password)

	if size >= 0 {
		r.ContentLength = size
	}

	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}

	resp, err := b.http.Do(r)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, msg)
	}

	return nil
}

func (b *bench) sha256Of(ctx context.Context, path string) (string, error) {
	r, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		b.cfg.relic+"/artifactory/api/storage/"+repo+"/"+path,
		nil,
	)
	if err != nil {
		return "", err
	}

	r.SetBasicAuth(b.cfg.user, b.cfg.password)

	resp, err := b.http.Do(r)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	var info struct {
		Checksums struct {
			SHA256 string `json:"sha256"`
		} `json:"checksums"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil || info.Checksums.SHA256 == "" {
		return "", errors.Join(fmt.Errorf("storage info of %s: status %d", path, resp.StatusCode), err)
	}

	return info.Checksums.SHA256, nil
}

func (b *bench) sample(ctx context.Context) <-chan []Stats {
	out := make(chan []Stats, 1)
	if len(b.cfg.containers) == 0 {
		out <- nil
		return out
	}

	go func() {
		cpu := map[string][]float64{}
		mem := map[string]float64{}

		for ctx.Err() == nil {
			args := append(
				[]string{
					"stats",
					"--no-stream",
					"--format",
					"{{.Name}} {{.CPUPerc}} {{.MemUsage}}",
				},
				b.cfg.containers...)

			lines, err := exec.CommandContext(ctx, "docker", args...).Output()
			if err != nil {
				select {
				case <-ctx.Done():
				case <-time.After(time.Second):
				}

				continue
			}

			for line := range strings.Lines(string(lines)) {
				f := strings.Fields(line)
				if len(f) < 3 {
					continue
				}

				c, _ := strconv.ParseFloat(strings.TrimSuffix(f[1], "%"), 64)
				cpu[f[0]] = append(cpu[f[0]], c)
				mem[f[0]] = max(mem[f[0]], mib(f[2]))
			}
		}

		var s []Stats

		for _, name := range b.cfg.containers {
			v := cpu[name]
			if len(v) == 0 {
				continue
			}

			var sum float64
			for _, x := range v {
				sum += x
			}

			s = append(
				s,
				Stats{
					Container: name,
					CPUAvg:    sum / float64(len(v)),
					CPUMax:    slices.Max(v),
					MemMaxMiB: mem[name],
				},
			)
		}

		out <- s
	}()

	return out
}

func (b *bench) report() {
	fmt.Println(
		"| op | target | size | clients | MB/s | requests | errors | TTFB p50 ms | TTFB p95 ms | request p95 ms | CPU avg/max % (MiB) |",
	)
	fmt.Println("| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |")

	for _, r := range b.results {
		st := make([]string, 0, len(r.Stats))
		for _, s := range r.Stats {
			st = append(st, fmt.Sprintf("%s %.0f/%.0f (%.0f)", short(s.Container), s.CPUAvg, s.CPUMax, s.MemMaxMiB))
		}

		fmt.Printf(
			"| %s | %s | %s | %d | %.0f | %d | %d | %.0f | %.0f | %.0f | %s |\n",
			r.Op,
			r.Target,
			human(r.Size),
			r.Concurrency,
			r.MBps,
			r.Requests,
			r.Errors,
			r.TTFBp50,
			r.TTFBp95,
			r.Totalp95,
			strings.Join(st, ", "),
		)
	}
}

type random struct {
	rng  *rand.ChaCha8
	left int64
}

func newRandom(size int64) io.Reader {
	var seed [32]byte

	_, _ = crand.Read(seed[:])

	return &random{
		rng:  rand.NewChaCha8(seed),
		left: size,
	}
}

func (r *random) Read(p []byte) (int, error) {
	if r.left <= 0 {
		return 0, io.EOF
	}

	if int64(len(p)) > r.left {
		p = p[:r.left]
	}

	n, _ := r.rng.Read(p)
	r.left -= int64(n)

	return n, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)

	return n, err
}

func percentile(d []time.Duration, p int) time.Duration {
	if len(d) == 0 {
		return 0
	}

	slices.Sort(d)

	return d[min(len(d)-1, len(d)*p/100)]
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func mib(s string) float64 {
	units := []struct {
		suffix string
		mib    float64
	}{
		{
			"GiB",
			1024,
		},
		{
			"MiB",
			1,
		},
		{
			"KiB",
			1.0 / 1024,
		},
		{
			"B",
			1.0 / 1024 / 1024,
		},
	} // longest suffix first
	for _, u := range units {
		if v, ok := strings.CutSuffix(s, u.suffix); ok {
			n, _ := strconv.ParseFloat(v, 64)
			return n * u.mib
		}
	}

	return 0
}

func short(name string) string {
	name = strings.TrimSuffix(name, "-1")
	if i := strings.Index(name, "-"); i >= 0 {
		return name[i+1:]
	}

	return name
}

func sizes(s string) ([]int64, error) {
	var out []int64

	for f := range strings.SplitSeq(s, ",") {
		n, err := parseSize(strings.TrimSpace(f))
		if err != nil {
			return nil, err
		}

		out = append(out, n)
	}

	return out, nil
}

func parseSize(s string) (int64, error) {
	units := []struct {
		suffix string
		mult   int64
	}{
		{
			"GiB",
			1 << 30,
		},
		{
			"MiB",
			1 << 20,
		},
		{
			"KiB",
			1 << 10,
		},
	}
	for _, u := range units {
		if v, ok := strings.CutSuffix(s, u.suffix); ok {
			n, err := strconv.ParseInt(v, 10, 64)
			return n * u.mult, err
		}
	}

	return strconv.ParseInt(s, 10, 64)
}

func human(n int64) string {
	switch {
	case n >= 1<<30 && n%(1<<30) == 0:
		return fmt.Sprintf("%dGiB", n>>30)
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%dMiB", n>>20)
	default:
		return strconv.FormatInt(n, 10)
	}
}

func ints(s string) ([]int, error) {
	var out []int

	for f := range strings.SplitSeq(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil {
			return nil, err
		}

		out = append(out, n)
	}

	return out, nil
}
