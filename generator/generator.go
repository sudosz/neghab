// Package generator manages a pool of workers that execute traffic generation
// scenarios. Each worker picks up jobs from a buffered channel and runs the
// configured scenario to produce the required amount of upload traffic.
package generator

import (
	"fmt"
	"log/slog"
	mathrand "math/rand/v2"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sudosz/neghab/humanize"
)

// ScenarioName constants for the built-in scenarios.
const (
	ScenarioUDP      = "udp"
	ScenarioTCPRST   = "tcp-rst"
	ScenarioHTTPRS   = "http-rst"
	ScenarioDNS      = "dns"
	ScenarioUpload   = "upload"
	ScenarioDownload = "download"
)

// Job describes a traffic generation task.
type Job struct {
	// TargetBytes is the number of upload bytes to generate.
	TargetBytes uint64

	// ResultChan is an optional channel for receiving the job result.
	// If nil, the result is only logged.
	ResultChan chan<- JobResult
}

// JobResult reports the outcome of a completed generation job.
type JobResult struct {
	ActualBytes uint64
	Scenario    string
	Err         error
}

// PoolConfig holds configuration for the generator pool.
type PoolConfig struct {
	WorkerCount      int
	TargetIP         string
	TargetPort       int
	Scenario         string        // single scenario name, or comma-separated list
	MixInterval      time.Duration // scenario rotation interval (0 = no rotation)
	DNSResolvers     []string      // DNS resolvers for dns scenario
	HTTPHost         string        // target hostname for http-rst scenario
	HTTPRSTPath      string        // URL path for http-rst scenario (e.g., /upload)
	UploadPath       string        // URL path for upload scenario (e.g., /speedtest/upload.php)
	UploadBufferSize int           // upload body buffer size in bytes (default 131072 = 128KB)
	UploadStreams    int           // parallel HTTP streams per job (default 1, speedtest-cli uses 4-8)
	UploadTargets    []string      // upload servers as "host:port" or "host:port/path" (empty = use TargetIP:TargetPort)
}

// Pool manages a fixed set of worker goroutines that execute generation jobs.
type Pool struct {
	jobs          chan Job
	cfg           PoolConfig
	scenarios     []Scenario
	mixer         *Mixer
	activeFunc    func(uint64) (uint64, error) // currently active generation function
	httpPool      *connPool                    // TCP pool for download scenario (primary target)
	uploadTargets []uploadTarget               // upload targets with per-server pools
	wg            sync.WaitGroup
	stopCh        chan struct{}
	doneCh        chan struct{}
}

// NewPool creates a pool with the specified configuration.
func NewPool(cfg PoolConfig) *Pool {
	// Parse upload targets: if UploadTargets is set, create a connPool per
	// server. Otherwise fall back to single TargetIP:TargetPort (backward compat).
	uploadTargets := parseUploadTargets(cfg)

	// The download scenario always uses the primary (first) target's pool.
	var httpPool *connPool
	if len(uploadTargets) > 0 {
		httpPool = uploadTargets[0].pool
	}

	// Create an upload buffer pool only when the upload or download scenario
	// is active. This avoids unnecessary memory allocation for UDP/DNS/etc.
	var uploadBufPool *sync.Pool
	needsUploadBuf := strings.Contains(cfg.Scenario, "upload") ||
		strings.Contains(cfg.Scenario, "download") ||
		cfg.Scenario == "all"
	if needsUploadBuf {
		bufSize := cfg.UploadBufferSize
		if bufSize < 4096 {
			bufSize = 131072
		}
		uploadBufPool = &sync.Pool{
			New: func() any {
				buf := make([]byte, bufSize)
				for i := range buf {
					buf[i] = byte(mathrand.IntN(256))
				}
				return buf
			},
		}
	}

	// Pre-warm connection pools so the first jobs don't pay TCP handshake cost.
	// Pre-warm at least 1 connection per pool even with a single worker.
	prewarmCount := cfg.WorkerCount / 2
	if prewarmCount < 1 {
		prewarmCount = 1
	}
	for _, t := range uploadTargets {
		t.pool.prewarm(prewarmCount)
	}

	// Start with the default scenarios (udp, tcp-rst, dns)
	allScenarios := DefaultScenarios()

	// Replace the default DNS scenario with one using the configured resolvers
	dnsScenario := Scenario{
		Name: "dns",
		Func: func(targetBytes uint64, _ string, _ int) (uint64, error) {
			return generateDNSQuerySpam(targetBytes, cfg.DNSResolvers)
		},
	}
	for i, s := range allScenarios {
		if s.Name == "dns" {
			allScenarios[i] = dnsScenario
			break
		}
	}

	// Add the http-rst scenario (needs host and path from PoolConfig)
	allScenarios = append(allScenarios, Scenario{
		Name: "http-rst",
		Func: func(targetBytes uint64, _ string, _ int) (uint64, error) {
			return generateHTTPPostRST(targetBytes, cfg.HTTPHost, cfg.TargetPort, cfg.HTTPRSTPath)
		},
	})

	// Add the http-upload scenario (proper HTTP POST, works with speedtest servers).
	// Only registered when a buffer pool is available (i.e., upload/download is configured).
	if uploadBufPool != nil {
		allScenarios = append(allScenarios, Scenario{
			Name: "upload",
			Func: func(targetBytes uint64, _ string, _ int) (uint64, error) {
				return generateHTTPUpload(targetBytes, uploadTargets, uploadBufPool, cfg.UploadStreams)
			},
		})
	}

	// Add the http-download scenario (HTTP GET, generates RX traffic).
	// In multi-server mode, download hits the first upload target for
	// consistent Host header matching the actual connection pool.
	downloadIP, downloadPort := cfg.TargetIP, cfg.TargetPort
	if len(uploadTargets) > 0 {
		downloadIP, downloadPort = parseHostPort(uploadTargets[0].hostPort)
		if downloadPort == 0 {
			downloadIP, downloadPort = cfg.TargetIP, cfg.TargetPort
		}
	}
	if httpPool != nil {
		allScenarios = append(allScenarios, Scenario{
			Name: "download",
			Func: func(targetBytes uint64, _ string, _ int) (uint64, error) {
				return generateHTTPDownload(targetBytes, downloadIP, downloadPort, cfg.UploadPath, httpPool)
			},
		})
	}

	// Filter scenarios based on user configuration
	scenarios := FilterScenarios(cfg.Scenario, allScenarios)

	// Set up the active generation function based on the first (or only) scenario
	scenarioName := scenarios[0].Name
	var activeFunc func(uint64) (uint64, error)

	switch scenarioName {
	case ScenarioUDP:
		ip, port := cfg.TargetIP, cfg.TargetPort
		activeFunc = func(bytes uint64) (uint64, error) {
			return generateUDP(bytes, ip, port)
		}
	case ScenarioTCPRST:
		ip, port := cfg.TargetIP, cfg.TargetPort
		activeFunc = func(bytes uint64) (uint64, error) {
			return generateTCPRST(bytes, ip, port)
		}
	case ScenarioHTTPRS:
		activeFunc = func(bytes uint64) (uint64, error) {
			return generateHTTPPostRST(bytes, cfg.HTTPHost, cfg.TargetPort, cfg.HTTPRSTPath)
		}
	case ScenarioUpload:
		if uploadBufPool != nil {
			activeFunc = func(bytes uint64) (uint64, error) {
				return generateHTTPUpload(bytes, uploadTargets, uploadBufPool, cfg.UploadStreams)
			}
		}
	case ScenarioDownload:
		if httpPool != nil {
			activeFunc = func(bytes uint64) (uint64, error) {
				return generateHTTPDownload(bytes, downloadIP, downloadPort, cfg.UploadPath, httpPool)
			}
		}
	case ScenarioDNS:
		activeFunc = func(bytes uint64) (uint64, error) {
			return generateDNSQuerySpam(bytes, cfg.DNSResolvers)
		}
	default:
		// Fallback to UDP
		ip, port := cfg.TargetIP, cfg.TargetPort
		activeFunc = func(bytes uint64) (uint64, error) {
			return generateUDP(bytes, ip, port)
		}
	}

	p := &Pool{
		jobs:          make(chan Job, cfg.WorkerCount*2),
		cfg:           cfg,
		scenarios:     scenarios,
		activeFunc:    activeFunc,
		httpPool:      httpPool,
		uploadTargets: uploadTargets,
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
	}

	for i := 0; i < cfg.WorkerCount; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}

	go func() {
		p.wg.Wait()
		close(p.doneCh)
	}()

	// Start scenario mixer whenever multiple scenarios are configured.
	if len(scenarios) > 1 {
		interval := cfg.MixInterval
		if interval <= 0 {
			interval = 30 * time.Second // default rotation
		}
		p.mixer = NewMixer(scenarios, interval)
		p.mixer.Start()
		slog.Info("generator: scenario rotation enabled",
			"scenarios", formatScenarioNames(scenarios),
			"interval", interval)
	} else {
		attrs := []any{
			"workers", cfg.WorkerCount,
			"scenario", formatScenarioNames(scenarios),
		}
		if len(cfg.UploadTargets) > 0 {
			attrs = append(attrs, "uploadTargets", cfg.UploadTargets)
			attrs = append(attrs, "uploadStreams", cfg.UploadStreams)
		} else {
			attrs = append(attrs, "target", fmt.Sprintf("%s:%d", cfg.TargetIP, cfg.TargetPort))
		}
		if cfg.UploadBufferSize > 0 && uploadBufPool != nil {
			attrs = append(attrs, "bufferSize", humanize.Bytes(uint64(cfg.UploadBufferSize)))
		}
		slog.Info("generator pool started", attrs...)
	}

	return p
}

// Stop signals all workers to shut down and waits for them to finish.
func (p *Pool) Stop() {
	if p.mixer != nil {
		p.mixer.Stop()
	}
	close(p.stopCh)
	<-p.doneCh
	// Close all upload target pools (dedup: httpPool is uploadTargets[0].pool).
	seen := make(map[*connPool]bool)
	for _, t := range p.uploadTargets {
		if !seen[t.pool] {
			t.pool.close()
			seen[t.pool] = true
		}
	}
	if p.httpPool != nil && !seen[p.httpPool] {
		p.httpPool.close()
	}
	slog.Debug("generator pool stopped")
}

// Dispatch submits a job to the pool. Returns an error if the job queue is full.
func (p *Pool) Dispatch(job Job) error {
	select {
	case p.jobs <- job:
		return nil
	default:
		return fmt.Errorf("job queue full (%d pending, %d workers busy)",
			len(p.jobs), p.cfg.WorkerCount)
	}
}

// CurrentScenario returns the name of the currently active scenario.
// The mixer (if active) is only written during NewPool and is never modified
// after construction, so this is safe for concurrent reads without a lock.
func (p *Pool) CurrentScenario() string {
	if p.mixer != nil {
		return p.mixer.Current().Name
	}
	if len(p.scenarios) > 0 {
		return p.scenarios[0].Name
	}
	return "none"
}

// worker is the main loop for a single generator goroutine.
func (p *Pool) worker(id int) {
	defer p.wg.Done()

	slog.Debug("generator worker started", "workerID", id)

	for {
		select {
		case <-p.stopCh:
			slog.Debug("generator worker stopped", "workerID", id)
			return

		case job := <-p.jobs:
			p.executeJob(id, job)
		}
	}
}

// executeJob runs a single generation job using the currently active scenario.
func (p *Pool) executeJob(workerID int, job Job) {
	scenarioName := p.CurrentScenario()
	logger := slog.With(
		"workerID", workerID,
		"scenario", scenarioName,
		"targetBytes", job.TargetBytes,
	)

	logger.Debug("worker: executing job")

	var actualBytes uint64
	var err error

	// Use the current scenario function
	if p.mixer != nil {
		scenario := p.mixer.Current()
		actualBytes, err = scenario.Func(job.TargetBytes, p.cfg.TargetIP, p.cfg.TargetPort)
		scenarioName = scenario.Name
	} else {
		actualBytes, err = p.activeFunc(job.TargetBytes)
	}

	result := JobResult{
		ActualBytes: actualBytes,
		Scenario:    scenarioName,
		Err:         err,
	}

	if err != nil {
		logger.Warn("worker: scenario failed",
			"error", err,
			"actualBytes", humanize.Bytes(actualBytes))
	} else {
		logger.Debug("worker: job complete",
			"actualBytes", humanize.Bytes(actualBytes))
	}

	if job.ResultChan != nil {
		job.ResultChan <- result
	}
}

// formatScenarioNames formats a list of scenarios for logging.
func formatScenarioNames(scenarios []Scenario) string {
	if len(scenarios) == 0 {
		return "none"
	}
	s := ""
	for i, sc := range scenarios {
		if i > 0 {
			s += ", "
		}
		s += sc.Name
	}
	return s
}

// parseHostPort splits a "host:port" string into host and port components.
// Falls back to returning the full string as host with port 0 on parse error.
func parseHostPort(addr string) (host string, port int) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, 0
	}
	port, _ = strconv.Atoi(p)
	return h, port
}

// parseUploadTargets builds the upload target list from PoolConfig.
// Each entry can be "host:port" (uses global UploadPath) or "host:port/path"
// (per-target path). If UploadTargets is empty, falls back to a single target
// from TargetIP:TargetPort.
func parseUploadTargets(cfg PoolConfig) []uploadTarget {
	if len(cfg.UploadTargets) > 0 {
		targets := make([]uploadTarget, 0, len(cfg.UploadTargets))
		for _, t := range cfg.UploadTargets {
			hostPort, path := t, cfg.UploadPath
			// Parse "host:port/path" format — the first '/' separates
			// the host:port from the URL path.
			if idx := strings.IndexByte(t, '/'); idx >= 0 {
				hostPort = strings.TrimSpace(t[:idx])
				path = strings.TrimSpace(t[idx:]) // preserve leading /
				if path == "" {
					path = cfg.UploadPath
				}
			}
			if hostPort == "" {
				continue
			}
			targets = append(targets, uploadTarget{
				hostPort: hostPort,
				path:     path,
				pool:     newConnPool(hostPort, cfg.WorkerCount),
			})
		}
		if len(targets) > 0 {
			return targets
		}
	}

	// Backward-compatible single-target fallback
	addr := fmt.Sprintf("%s:%d", cfg.TargetIP, cfg.TargetPort)
	return []uploadTarget{{
		hostPort: addr,
		path:     cfg.UploadPath,
		pool:     newConnPool(addr, cfg.WorkerCount),
	}}
}
