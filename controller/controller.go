// Package controller implements the ratio enforcement logic.
// It reads network stats from the monitor, computes the TX deficit,
// accumulates it, and dispatches generation jobs to the worker pool.
package controller

import (
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/sudosz/neghab/generator"
	"github.com/sudosz/neghab/humanize"
	"github.com/sudosz/neghab/monitor"
)

// StatsReader is the interface for reading network interface stats.
// This enables testing with mock implementations.
type StatsReader interface {
	Stats() *monitor.Stats
}

// JobDispatcher is the interface for dispatching generation jobs.
// This enables testing with mock implementations.
type JobDispatcher interface {
	Dispatch(generator.Job) error
}

// Controller is the core decision-making component.
// It runs in a background goroutine and coordinates between a StatsReader
// and a JobDispatcher to enforce the target TX/RX ratio.
type Controller struct {
	targetRatio float64
	minTXBytes  uint64
	minTargetTX uint64
	smoothing   float64
	accumulator atomic.Uint64
	interval    time.Duration

	// Session baselines — captured on first tick to compute session-level
	// cumulative RX/TX totals rather than using absolute interface counters.
	// This prevents historically TX-heavy interfaces from permanently
	// blocking all generation.
	baselineRX  uint64
	baselineTX  uint64
	baselineSet bool

	mon    StatsReader
	gen    JobDispatcher
	stopCh chan struct{}
	doneCh chan struct{}
}

// New creates a new Controller with the given parameters.
//   - ratio: Upload/Download divisor. Formula: TargetTX = deltaRX / ratio.
//     ratio=0.1 → Upload=10×Download | ratio=1.0 → Upload=Download | ratio=10 → Upload=0.1×Download
//   - minTXBytes: minimum deficit before dispatching a job
//   - minTargetTX: minimum desiredTX per tick (floor, even when deltaRX is low/zero)
//   - smoothing: deficit accumulation dampener (0.0–1.0, 1.0 = no dampening)
//   - interval: how often to check stats
//   - mon: a started Monitor instance (or any StatsReader)
//   - gen: a started Generator pool (or any JobDispatcher)
func New(
	ratio float64,
	minTXBytes uint64,
	minTargetTX uint64,
	smoothing float64,
	interval time.Duration,
	mon StatsReader,
	gen JobDispatcher,
) *Controller {
	if smoothing <= 0 || smoothing > 1 {
		smoothing = 1.0
	}
	return &Controller{
		targetRatio: ratio,
		minTXBytes:  minTXBytes,
		minTargetTX: minTargetTX,
		smoothing:   smoothing,
		interval:    interval,
		mon:         mon,
		gen:         gen,
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
	}
}

// Start launches the controller loop in a background goroutine.
// It phase-offsets by half the interval to allow the monitor to produce
// the first reading before the controller attempts to use it.
func (c *Controller) Start() {
	go c.loop()
}

// Stop signals the controller to shut down and waits for it to finish.
func (c *Controller) Stop() {
	close(c.stopCh)
	<-c.doneCh
}

// Accumulator returns the current deficit accumulator value (bytes).
// Safe for concurrent access.
func (c *Controller) Accumulator() uint64 {
	return c.accumulator.Load()
}

// subAccumulator atomically subtracts n from the accumulator.
// Uses two's complement wraparound: Add(^(n-1)) is equivalent to Add(-n)
// because atomic.Uint64 has no Subtract method.
func (c *Controller) subAccumulator(n uint64) {
	c.accumulator.Add(^(n - 1))
}

func (c *Controller) loop() {
	defer close(c.doneCh)

	// Phase-offset: wait half an interval so the Monitor has time to
	// produce its first stats reading.
	time.Sleep(c.interval / 2)

	slog.Debug("controller: started",
		"ratio", c.targetRatio,
		"minTX", humanize.Bytes(c.minTXBytes),
		"minTargetTX", humanize.Bytes(c.minTargetTX),
		"smoothing", c.smoothing,
		"interval", c.interval)

	// Warn once at startup if the ratio is aggressive enough to saturate the link.
	if c.targetRatio <= 0.2 {
		slog.Warn("controller: target ratio <= 0.2 may saturate upstream link")
	}

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopCh:
			slog.Debug("controller: stopped",
				"accumulatorRemaining", humanize.Bytes(c.accumulator.Load()))
			return

		case <-ticker.C:
			c.tick()
		}
	}
}

// tick performs one control loop iteration.
func (c *Controller) tick() {
	stats := c.mon.Stats()
	if stats == nil {
		slog.Debug("controller: no stats yet (monitor may be initializing)")
		return
	}

	deltaRX := stats.DeltaRX
	deltaTX := stats.DeltaTX

	// ── Cumulative ratio check ────────────────────────────────────────
	// Capture session baseline on the first tick, then use session-level
	// cumulative totals to prevent ratio overshoot. Without this, the
	// per-tick approach would perpetually generate new TX whenever
	// deltaRX arrives, even when the session's overall TX/RX ratio is
	// already above target.
	//
	// Session totals are used instead of absolute /proc/net/dev counters
	// so that historically TX-heavy interfaces (e.g., backup servers)
	// don't permanently block all generation.
	if !c.baselineSet {
		c.baselineRX = stats.TotalRX
		c.baselineTX = stats.TotalTX
		c.baselineSet = true
		slog.Debug("controller: captured cumulative baseline",
			"totalRX", humanize.Bytes(stats.TotalRX),
			"totalTX", humanize.Bytes(stats.TotalTX))
		// Fall through to per-tick logic on first tick — the baseline
		// is zeroed so the check below won't trigger.
	}

	sessionRX := saturatingSub(stats.TotalRX, c.baselineRX)
	sessionTX := saturatingSub(stats.TotalTX, c.baselineTX)

	if sessionRX > 0 {
		cumulativeTarget := uint64(float64(sessionRX) / c.targetRatio)
		if sessionTX >= cumulativeTarget {
			if c.accumulator.Load() != 0 {
				c.accumulator.Store(0)
				slog.Debug("controller: cumulative ratio met — reset accumulator",
					"sessionRX", humanize.Bytes(sessionRX),
					"sessionTX", humanize.Bytes(sessionTX),
					"cumulativeTarget", humanize.Bytes(cumulativeTarget))
			}
			return
		}
	}

	// Compute desired TX based on the target ratio.
	// Formula: TargetTX = deltaRX / ratio.
	// ratio=0.1 → Upload=10×Download | ratio=1.0 → Upload=Download | ratio=10 → Upload=0.1×Download
	desiredTX := uint64(float64(deltaRX) / c.targetRatio)

	// Apply minimum target TX floor so generation doesn't stall when RX is low.
	if desiredTX < c.minTargetTX {
		desiredTX = c.minTargetTX
	}

	// If natural TX already meets or exceeds the target, no deficit exists.
	// Reset the accumulator to avoid carrying stale surplus from past intervals.
	if deltaTX >= desiredTX {
		if c.accumulator.Load() != 0 {
			c.accumulator.Store(0)
			slog.Debug("controller: reset accumulator — natural TX meets target",
				"deltaTX", humanize.Bytes(deltaTX),
				"desiredTX", humanize.Bytes(desiredTX))
		}
		return
	}

	// How much more TX do we need to generate?
	// We already know deltaTX < desiredTX, so missing > 0.
	missing := desiredTX - deltaTX

	// Add to the running deficit accumulator, dampened by smoothing.
	// This prevents a single large RX spike from triggering a massive generation burst.
	c.accumulator.Add(uint64(float64(missing) * c.smoothing))

	// Dispatch the full accumulated deficit (not just a smoothed fraction).
	// The minTXBytes threshold prevents tiny dispatches; the reset-on-overshoot
	// logic replaces smoothing as the anti-oscillation mechanism.
	acc := c.accumulator.Load()
	generateBytes := acc
	if generateBytes < c.minTXBytes {
		slog.Debug("controller: deficit below threshold",
			"accumulator", humanize.Bytes(acc),
			"threshold", humanize.Bytes(c.minTXBytes))
		return
	}

	// Dispatch a generation job to the worker pool.
	err := c.gen.Dispatch(generator.Job{
		TargetBytes: generateBytes,
	})
	if err != nil {
		slog.Warn("controller: failed to dispatch job, deferring",
			"error", err,
			"accumulator", humanize.Bytes(acc))
		// Do NOT reset accumulator — the deficit remains for the next tick.
		return
	}

	// Subtract what we dispatched from the accumulator.
	c.subAccumulator(generateBytes)

	slog.Info("controller: dispatched generation",
		"deltaRX", humanize.Bytes(deltaRX),
		"deltaTX", humanize.Bytes(deltaTX),
		"desiredTX", humanize.Bytes(desiredTX),
		"missing", humanize.Bytes(missing),
		"generated", humanize.Bytes(generateBytes),
		"accumulatorRemaining", humanize.Bytes(c.accumulator.Load()))
}

// saturatingSub returns a - b, or 0 if a < b (handles counter wrap).
func saturatingSub(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}
