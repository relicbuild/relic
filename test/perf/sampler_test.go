//go:build perf

package perf

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
)

const (
	metricsInterval   = 500 * time.Millisecond
	processesInterval = 2 * time.Second
	peakGrowth        = 1.05
)

type cgroupSample struct {
	at    time.Time
	usage uint64
	anon  uint64
	file  uint64
}

type processSample struct {
	at      time.Time
	rss     uint64
	command string
}

type runtimeSample struct {
	at   time.Time
	heap uint64
	rss  uint64
}

type sampler struct {
	t      *testing.T
	server server
	docker *testcontainers.DockerClient

	mu        sync.Mutex
	cgroup    []cgroupSample
	processes []processSample
	runtime   []runtimeSample
	phase     string
	peak      uint64
	peaks     int
}

func startSampler(ctx context.Context, t *testing.T, s server) *sampler {
	t.Helper()

	docker, err := testcontainers.NewDockerClientWithOpts(ctx)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = docker.Close()
	})

	recorder := &sampler{
		t:      t,
		server: s,
		docker: docker,
	}

	go recorder.streamStats(ctx)
	go recorder.every(ctx, processesInterval, recorder.sampleProcesses)
	go recorder.every(ctx, metricsInterval, recorder.sampleRuntime)

	return recorder
}

func (s *sampler) every(ctx context.Context, interval time.Duration, sample func(context.Context)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sample(ctx)
		}
	}
}

func (s *sampler) streamStats(ctx context.Context) {
	stats, err := s.docker.ContainerStats(ctx, s.server.id, client.ContainerStatsOptions{
		Stream: true,
	})
	if err != nil {
		s.t.Logf("container stats: %v", err)

		return
	}

	defer func() {
		_ = stats.Body.Close()
	}()

	decoder := json.NewDecoder(stats.Body)

	for {
		var response container.StatsResponse
		if err := decoder.Decode(&response); err != nil {
			return
		}

		memory := response.MemoryStats

		s.mu.Lock()
		s.cgroup = append(s.cgroup, cgroupSample{
			at:    time.Now(),
			usage: memory.Usage,
			anon:  memory.Stats["anon"],
			file:  memory.Stats["file"],
		})
		s.mu.Unlock()
	}
}

func (s *sampler) sampleProcesses(ctx context.Context) {
	top, err := s.docker.ContainerTop(ctx, s.server.id, client.ContainerTopOptions{
		Arguments: []string{
			"-o",
			"pid,rss,args",
		},
	})
	if err != nil {
		return
	}

	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, process := range top.Processes {
		if len(process) < 3 {
			continue
		}

		rss, ok := parseRSS(process[1])
		if !ok {
			continue
		}

		s.processes = append(s.processes, processSample{
			at:      now,
			rss:     rss,
			command: process[2],
		})
	}
}

func (s *sampler) sampleRuntime(ctx context.Context) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.server.metrics+"/metrics", nil)
	if err != nil {
		return
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return
	}

	defer func() {
		_ = response.Body.Close()
	}()

	sample := runtimeSample{
		at: time.Now(),
	}

	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		name, value, ok := strings.Cut(scanner.Text(), " ")
		if !ok {
			continue
		}

		switch name {
		case "go_memstats_heap_inuse_bytes":
			sample.heap = parseMetric(value)
		case "process_resident_memory_bytes":
			sample.rss = parseMetric(value)
		}
	}

	s.mu.Lock()
	s.runtime = append(s.runtime, sample)

	newPeak := float64(sample.heap) > float64(s.peak)*peakGrowth
	if newPeak {
		s.peak = sample.heap
		s.peaks++
	}

	name := fmt.Sprintf("%s-peak-%d-heap.pb.gz", s.phase, s.peaks)
	s.mu.Unlock()

	if newPeak {
		s.profile(ctx, name, "heap")
	}
}

func parseRSS(value string) (uint64, bool) {
	shift := 10

	switch {
	case strings.HasSuffix(value, "g"):
		shift = 30
	case strings.HasSuffix(value, "m"):
		shift = 20
	case strings.HasSuffix(value, "k"):
		shift = 10
	}

	amount, err := strconv.ParseFloat(strings.TrimRight(value, "gmk"), 64)
	if err != nil || amount < 0 {
		return 0, false
	}

	return uint64(amount * float64(uint64(1)<<shift)), true
}

func parseMetric(value string) uint64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || f < 0 {
		return 0
	}

	return uint64(f)
}

func (s *sampler) profile(ctx context.Context, name, profile string) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.server.metrics+"/debug/pprof/"+profile, nil)
	if err != nil {
		return
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return
	}

	defer func() {
		_ = response.Body.Close()
	}()

	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK {
		return
	}

	writeArtifact(s.t, name, body)
}

func (s *sampler) begin(ctx context.Context, phase string) {
	s.mu.Lock()
	s.phase = phase
	s.peak = 0
	s.peaks = 0
	s.mu.Unlock()

	s.profile(ctx, phase+"-start-heap.pb.gz", "heap?gc=1")
}

func (s *sampler) end(ctx context.Context, phase string) {
	s.profile(ctx, phase+"-end-allocs.pb.gz", "allocs")
	s.profile(ctx, phase+"-end-heap.pb.gz", "heap?gc=1")
}

type peaks struct {
	cgroup       uint64
	anon         uint64
	relicRSS     uint64
	relicHeap    uint64
	otherRSS     uint64
	otherProcess string
}

func (s *sampler) peaksBetween(start, end time.Time) peaks {
	s.mu.Lock()
	defer s.mu.Unlock()

	within := func(at time.Time) bool {
		return !at.Before(start) && !at.After(end)
	}

	var p peaks

	for _, c := range s.cgroup {
		if within(c.at) {
			p.cgroup = max(p.cgroup, c.usage)
			p.anon = max(p.anon, c.anon)
		}
	}

	for _, r := range s.runtime {
		if within(r.at) {
			p.relicRSS = max(p.relicRSS, r.rss)
			p.relicHeap = max(p.relicHeap, r.heap)
		}
	}

	for _, process := range s.processes {
		if within(process.at) && !strings.HasPrefix(process.command, "/relic") && process.rss > p.otherRSS {
			p.otherRSS = process.rss
			p.otherProcess = process.command
		}
	}

	return p
}
