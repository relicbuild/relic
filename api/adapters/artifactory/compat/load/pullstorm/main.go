// Command pullstorm measures what happens when many pods pull the same
// image through a Relic remote repository at once.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	tag         = "1.0"
	mediaType   = "application/vnd.docker.distribution.manifest.v2+json"
	layerType   = "application/vnd.docker.image.rootfs.diff.tar.gzip"
	configType  = "application/vnd.docker.container.image.v1+json"
	parallelism = 3
)

type config struct {
	relic, user, password string
	listen, upstreamURL   string
	pods, images, layers  int
	sharedLayers          int
	layerSize             int64
	upstreamRate          int64
	out                   string
}

// Result is one round of pulls.
type Result struct {
	Round            string   `json:"round"`
	Pods             int      `json:"pods"`
	Images           int      `json:"images"`
	Succeeded        int      `json:"succeeded"`
	Errors           int      `json:"errors"`
	FirstErrors      []string `json:"firstErrors,omitempty"`
	WallSeconds      float64  `json:"wallSeconds"`
	PullP50Ms        int64    `json:"pullP50Ms"`
	PullP95Ms        int64    `json:"pullP95Ms"`
	PullMaxMs        int64    `json:"pullMaxMs"`
	MBps             float64  `json:"mbPerSecond"`
	UpstreamManifest int64    `json:"upstreamManifestRequests"`
	UpstreamBlobs    int64    `json:"upstreamBlobRequests"`
	UpstreamMinimum  int64    `json:"upstreamMinimum"`
}

func main() {
	var cfg config

	var layerSize, rate string

	flag.StringVar(&cfg.relic, "relic", "http://localhost:28093", "Relic base URL")
	flag.StringVar(&cfg.user, "user", "admin", "Relic user")
	flag.StringVar(&cfg.password, "password", os.Getenv("RELIC_PASSWORD"), "Relic password (default $RELIC_PASSWORD)")
	flag.StringVar(&cfg.listen, "listen", ":28094", "where the upstream registry listens")
	flag.StringVar(&cfg.upstreamURL, "upstream-url", "", "the upstream registry's URL as Relic reaches it")
	flag.IntVar(&cfg.pods, "pods", 100, "concurrent pulls")
	flag.IntVar(&cfg.images, "images", 1, "different images the pods pull")
	flag.IntVar(&cfg.layers, "layers", 5, "layers in each image")
	flag.IntVar(&cfg.sharedLayers, "shared-layers", 2, "of those, base layers every image shares")
	flag.StringVar(&layerSize, "layer-size", "20MiB", "size of each layer")
	flag.StringVar(&rate, "upstream-rate", "50MiB", "per-connection upstream bandwidth (0: unlimited)")
	flag.StringVar(&cfg.out, "out", "", "write results as JSON to this file")
	flag.Parse()

	var err error
	if cfg.layerSize, err = parseSize(layerSize); err != nil {
		log.Fatal(err)
	}

	if cfg.upstreamRate, err = parseSize(rate); err != nil {
		log.Fatal(err)
	}

	if cfg.upstreamURL == "" {
		log.Fatal("-upstream-url is required")
	}

	if err := run(context.Background(), cfg); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, cfg config) error {
	log.Printf("prepare %d images of %d layers of %s", cfg.images, cfg.layers, fmtSize(cfg.layerSize))

	up, err := newUpstream(ctx, cfg.images, cfg.layers, cfg.sharedLayers, cfg.layerSize, cfg.upstreamRate)
	if err != nil {
		return err
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	hs := &http.Server{
		Handler:           up,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		_ = hs.Serve(ln)
	}()

	defer func() {
		_ = hs.Close()
	}()

	repo := "storm-" + strconv.FormatInt(time.Now().Unix(), 36)
	body := `{"rclass":"remote","packageType":"docker","url":"` + cfg.upstreamURL + `"}`

	if err := relicDo(ctx, cfg, http.MethodPut, "/artifactory/api/repositories/"+repo, body); err != nil {
		return fmt.Errorf("create %s: %w", repo, err)
	}
	defer func() {
		if err := relicDo(
			context.WithoutCancel(ctx),
			cfg,
			http.MethodDelete,
			"/artifactory/api/repositories/"+repo,
			"",
		); err != nil {
			log.Printf("delete %s: %v", repo, err)
		}
	}()

	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConnsPerHost: cfg.pods * parallelism,
		},
	}

	var results []Result

	for _, round := range []string{
		"cold",
		"warm",
	} {
		up.manifestHits.Store(0)
		up.blobHits.Store(0)
		log.Printf("%s: %d pods pull %d images", round, cfg.pods, cfg.images)

		r := storm(ctx, cfg, client, repo)
		r.Round = round
		r.UpstreamManifest, r.UpstreamBlobs = up.manifestHits.Load(), up.blobHits.Load()
		r.Images = cfg.images
		images := min(cfg.images, cfg.pods)
		r.UpstreamMinimum = int64(images*(1+cfg.layers-cfg.sharedLayers) + 1 + cfg.sharedLayers)

		if round == "warm" {
			r.UpstreamMinimum = 0
		}

		log.Printf(
			"  %d/%d succeeded in %.1fs, pull p95 %d ms, upstream requests %d (minimum %d)",
			r.Succeeded,
			r.Pods,
			r.WallSeconds,
			r.PullP95Ms,
			r.UpstreamManifest+r.UpstreamBlobs,
			r.UpstreamMinimum,
		)
		results = append(results, r)
	}

	printTable(results)

	if cfg.out != "" {
		b, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(cfg.out, b, 0o600); err != nil {
			return fmt.Errorf("write results: %w", err)
		}
	}

	return nil
}

func storm(ctx context.Context, cfg config, client *http.Client, repo string) Result {
	var (
		mu    sync.Mutex
		times []time.Duration
		errs  []string
		bytes atomic.Int64
	)

	start := time.Now()

	var wg sync.WaitGroup
	for i := range cfg.pods {
		wg.Go(func() {
			t0 := time.Now()
			n, err := pull(ctx, cfg, client, repo, imageName(i%cfg.images))
			bytes.Add(n)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				errs = append(errs, err.Error())
				return
			}

			times = append(times, time.Since(t0))
		})
	}

	wg.Wait()

	wall := time.Since(start)

	slices.Sort(times)

	r := Result{
		Pods:        cfg.pods,
		Succeeded:   len(times),
		Errors:      len(errs),
		WallSeconds: wall.Seconds(),
		MBps:        float64(bytes.Load()) / 1e6 / wall.Seconds(),
	}

	if len(times) > 0 {
		r.PullP50Ms = times[len(times)/2].Milliseconds()
		r.PullP95Ms = times[len(times)*95/100].Milliseconds()
		r.PullMaxMs = times[len(times)-1].Milliseconds()
	}

	slices.Sort(errs)
	errs = slices.Compact(errs)
	r.FirstErrors = errs[:min(5, len(errs))]

	return r
}

func pull(ctx context.Context, cfg config, client *http.Client, repo, image string) (int64, error) {
	var manifest bytes.Buffer
	if _, err := get(ctx, cfg, client, "/v2/"+repo+"/"+image+"/manifests/"+tag, mediaType, &manifest); err != nil {
		return 0, fmt.Errorf("manifest: %w", err)
	}

	var m struct {
		Config descriptor   `json:"config"`
		Layers []descriptor `json:"layers"`
	}
	if err := json.Unmarshal(manifest.Bytes(), &m); err != nil {
		return 0, fmt.Errorf("manifest: %w", err)
	}

	var n atomic.Int64

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(parallelism)

	for _, d := range append([]descriptor{
		m.Config,
	}, m.Layers...) {
		g.Go(func() error {
			h := sha256.New()

			read, err := get(ctx, cfg, client, "/v2/"+repo+"/"+image+"/blobs/"+d.Digest, "", h)
			n.Add(read)

			if err != nil {
				return fmt.Errorf("blob %s: %w", d.Digest[:19], err)
			}

			if "sha256:"+hex.EncodeToString(h.Sum(nil)) != d.Digest {
				return fmt.Errorf("blob %s: digest mismatch", d.Digest[:19])
			}

			return nil
		})
	}

	return n.Load() + int64(manifest.Len()), g.Wait()
}

func get(ctx context.Context, cfg config, client *http.Client, path, accept string, w io.Writer) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.relic+path, nil)
	if err != nil {
		return 0, err
	}

	req.SetBasicAuth(cfg.user, cfg.password)

	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("status %d", resp.StatusCode)
	}

	return io.Copy(w, resp.Body)
}

func relicDo(ctx context.Context, cfg config, method, path, body string) error {
	req, err := http.NewRequestWithContext(ctx, method, cfg.relic+path, strings.NewReader(body))
	if err != nil {
		return err
	}

	req.SetBasicAuth(cfg.user, cfg.password)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: time.Minute,
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, b)
	}

	return nil
}

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

func imageName(i int) string { return "storm/app" + strconv.Itoa(i) }

type upstream struct {
	manifests    map[string][]byte
	blobs        map[string]blob
	rate         int64
	manifestHits atomic.Int64
	blobHits     atomic.Int64
}

type blob struct {
	seed  [32]byte
	size  int64
	fixed []byte
}

func (b blob) reader() io.Reader {
	if b.fixed != nil {
		return bytes.NewReader(b.fixed)
	}

	return io.LimitReader(rand.NewChaCha8(b.seed), b.size)
}

func newUpstream(ctx context.Context, images, layers, shared int, size, rate int64) (*upstream, error) {
	u := &upstream{
		manifests: map[string][]byte{},
		blobs:     map[string]blob{},
		rate:      rate,
	}
	cfgBlob := blob{
		fixed: []byte(`{"architecture":"amd64","os":"linux"}`),
	}
	cfgDesc := describe(cfgBlob, configType)
	u.blobs[cfgDesc.Digest] = cfgBlob

	descs := make([][]descriptor, images)

	g, _ := errgroup.WithContext(ctx)
	g.SetLimit(runtime.GOMAXPROCS(0))

	for i := range images {
		descs[i] = make([]descriptor, layers)
		for j := range layers {
			g.Go(func() error {
				descs[i][j] = describe(blob{
					seed: seedOf(i, j, shared),
					size: size,
				}, layerType)

				return nil
			})
		}
	}

	_ = g.Wait()

	for i := range images {
		for j, d := range descs[i] {
			u.blobs[d.Digest] = blob{
				seed: seedOf(i, j, shared),
				size: size,
			}
		}

		m, err := json.Marshal(map[string]any{
			"schemaVersion": 2,
			"mediaType":     mediaType,
			"config":        cfgDesc,
			"layers":        descs[i],
		})
		if err != nil {
			return nil, err
		}

		u.manifests[imageName(i)] = m
	}

	return u, nil
}

func seedOf(i, j, shared int) [32]byte {
	if j < shared {
		i = -1
	}

	var seed [32]byte
	copy(seed[:], strconv.Itoa(i)+"/"+strconv.Itoa(j))

	return seed
}

func describe(b blob, mt string) descriptor {
	h := sha256.New()
	n, _ := io.Copy(h, b.reader())

	return descriptor{
		MediaType: mt,
		Digest:    "sha256:" + hex.EncodeToString(h.Sum(nil)),
		Size:      n,
	}
}

// ServeHTTP answers /v2/<image>/manifests/<tag> and /v2/<image>/blobs/<digest>.
func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/v2/")
	if p == "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if image, ok := strings.CutSuffix(p, "/manifests/"+tag); ok {
		m, found := u.manifests[image]
		if !found {
			http.NotFound(w, r)
			return
		}

		u.manifestHits.Add(1)
		w.Header().Set("Content-Type", mediaType)
		u.send(w, r, bytes.NewReader(m), int64(len(m)))

		return
	}

	if _, digest, ok := strings.Cut(p, "/blobs/"); ok {
		b, found := u.blobs[digest]
		if !found {
			http.NotFound(w, r)
			return
		}

		u.blobHits.Add(1)

		size := b.size
		if b.fixed != nil {
			size = int64(len(b.fixed))
		}

		u.send(w, r, b.reader(), size)

		return
	}

	http.NotFound(w, r)
}

func (u *upstream) send(w http.ResponseWriter, r *http.Request, body io.Reader, size int64) {
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))

	if r.Method == http.MethodHead {
		return
	}

	if u.rate <= 0 {
		_, _ = io.Copy(w, body)
		return
	}

	buf := make([]byte, 64<<10)
	start := time.Now()

	var sent int64

	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}

			sent += int64(n)

			due := time.Duration(float64(sent) / float64(u.rate) * float64(time.Second))
			if d := due - time.Since(start); d > 0 {
				time.Sleep(d)
			}
		}

		if err != nil {
			return
		}
	}
}

func printTable(rs []Result) {
	fmt.Println(
		"| round | pods | images | succeeded | errors | wall s | pull p50 ms | pull p95 ms | pull max ms | MB/s | upstream requests (minimum) |",
	)
	fmt.Println("| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |")

	for _, r := range rs {
		fmt.Printf(
			"| %s | %d | %d | %d | %d | %.1f | %d | %d | %d | %.0f | %d manifest + %d blobs (%d) |\n",
			r.Round,
			r.Pods,
			r.Images,
			r.Succeeded,
			r.Errors,
			r.WallSeconds,
			r.PullP50Ms,
			r.PullP95Ms,
			r.PullMaxMs,
			r.MBps,
			r.UpstreamManifest,
			r.UpstreamBlobs,
			r.UpstreamMinimum,
		)
	}

	for _, r := range rs {
		for _, e := range r.FirstErrors {
			fmt.Printf("%s error: %s\n", r.Round, e)
		}
	}
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
		{
			"B",
			1,
		},
	}
	for _, u := range units {
		if n, ok := strings.CutSuffix(s, u.suffix); ok {
			v, err := strconv.ParseInt(n, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("size %q: %w", s, err)
			}

			return v * u.mult, nil
		}
	}

	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, errors.New("size " + strconv.Quote(s) + ": want a number with GiB, MiB, KiB or B")
	}

	return v, nil
}

func fmtSize(n int64) string {
	switch {
	case n >= 1<<30 && n%(1<<30) == 0:
		return strconv.FormatInt(n>>30, 10) + "GiB"
	case n >= 1<<20 && n%(1<<20) == 0:
		return strconv.FormatInt(n>>20, 10) + "MiB"
	}

	return strconv.FormatInt(n, 10) + "B"
}
