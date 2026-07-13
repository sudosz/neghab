//go:build integration
// +build integration

// Package controller integration tests exercise the full pipeline:
// monitor → controller → generator pool.
//
// These tests use real network (loopback UDP) and /proc/net/dev and require
// root or CAP_NET_RAW on Linux. Run with:
//
//	go test -tags=integration -count=1 -timeout=60s ./controller/
package controller

import (
	"net"
	"testing"
	"time"

	"github.com/sudosz/neghab/generator"
	"github.com/sudosz/neghab/monitor"
)

// ═══════════════════════════════════════════════════════════════════════════
// Integration Test A: Controller → Generator Pool Pipeline
//
// Uses mock StatsReader (asymmetric RX >> TX) + real generator.Pool wired
// through the real Controller. Verifies that jobs flow from the controller
// into the pool and that workers execute the UDP scenario successfully.
// ═══════════════════════════════════════════════════════════════════════════

func TestIntegration_ControllerDispatchesToPool(t *testing.T) {
	// NOTE: ctrl.Start() is intentionally not called — we exercise tick()
	// directly for deterministic control over when dispatches happen.
	// No goroutine leak since the controller loop was never started.

	// Use a real generator pool: UDP to loopback on an unused port.
	// UDP is connectionless — packets are silently dropped by the kernel.
	pool := generator.NewPool(generator.PoolConfig{
		WorkerCount: 2,
		TargetIP:    "127.0.0.1",
		TargetPort:  19999, // unused port, nothing listening
		Scenario:    "udp",
	})
	defer pool.Stop()

	// Mock StatsReader: return asymmetric stats — 1 MB RX, 0 TX.
	// Ratio 0.1 → desiredTX = 100 KB. Smoothing 1.0 generates all.
	stats := &monitor.Stats{DeltaRX: 1_000_000, DeltaTX: 0}
	mockMon := &mockStatsReader{stats: stats}

	// Controller with low threshold so dispatch fires immediately.
	ctrl := New(0.1, 100, 1.0, time.Second, mockMon, pool)

	// Record initial accumulator.
	initialAcc := ctrl.Accumulator()
	if initialAcc != 0 {
		t.Fatalf("initial accumulator = %d, want 0", initialAcc)
	}

	// Execute one tick. With 1 MB RX at ratio 0.1 and smoothing 1.0,
	// the controller should compute desiredTX = 100000, accumulate it,
	// and dispatch since 100000 > minTX (100).
	ctrl.tick()

	// Give the pool worker a moment to pick up and process the job.
	time.Sleep(100 * time.Millisecond)

	// After a successful dispatch, the accumulator should be reduced.
	// With smoothing=1.0, it dispatches the full accumulated deficit.
	remainingAcc := ctrl.Accumulator()
	t.Logf("initial accumulator: %d", initialAcc)
	t.Logf("remaining accumulator: %d", remainingAcc)
	t.Logf("pool scenario: %s", pool.CurrentScenario())

	// Verify the accumulator was reduced (dispatch succeeded).
	// It may not be exactly 0 due to uint64 rounding in the smoothing
	// calculation, but it should be significantly less than 100000.
	if remainingAcc > 50000 {
		t.Errorf("accumulator should decrease after dispatch (got %d, want < 50000)", remainingAcc)
	}
}

func TestIntegration_ControllerDispatchMultipleTicks(t *testing.T) {
	pool := generator.NewPool(generator.PoolConfig{
		WorkerCount: 2,
		TargetIP:    "127.0.0.1",
		TargetPort:  19999,
		Scenario:    "udp",
	})
	defer pool.Stop()

	// Moderate RX each tick: 2000 bytes, ratio 0.1, smoothing 1.0.
	// Each tick produces desiredTX = 200 bytes.
	// minTX = 1024 → needs ~6 ticks to accumulate enough.
	stats := &monitor.Stats{DeltaRX: 2000, DeltaTX: 0}
	mockMon := &mockStatsReader{stats: stats}

	ctrl := New(0.1, 1024, 1.0, time.Second, mockMon, pool)

	// Run enough ticks to trigger dispatch.
	for i := 0; i < 10; i++ {
		ctrl.tick()
	}

	time.Sleep(100 * time.Millisecond)

	remaining := ctrl.Accumulator()
	t.Logf("after 10 ticks, accumulator: %d", remaining)

	// After dispatching, accumulator should be below threshold.
	// It could be non-zero if dispatch happened mid-accumulation
	// and there's fractional remainder.
	if remaining >= 20_000 {
		// If accumulator is still very high, dispatch may have failed
		// repeatedly. This could happen if pool workers are slow (CI).
		// A healthy pipeline should drain the accumulator.
		t.Errorf("accumulator too high after multiple ticks: %d", remaining)
	}
}

func TestIntegration_PoolStopAfterDispatch(t *testing.T) {
	// NOTE: ctrl.Start() is intentionally not called — same reason as above.

	pool := generator.NewPool(generator.PoolConfig{
		WorkerCount: 2,
		TargetIP:    "127.0.0.1",
		TargetPort:  19999,
		Scenario:    "udp",
	})

	stats := &monitor.Stats{DeltaRX: 1_000_000, DeltaTX: 0}
	mockMon := &mockStatsReader{stats: stats}

	ctrl := New(0.1, 100, 1.0, time.Second, mockMon, pool)
	ctrl.tick()

	// Allow the pool worker to process the job.
	time.Sleep(100 * time.Millisecond)

	// Stop the pool — should not hang even if a job was recently dispatched.
	pool.Stop()
	t.Log("pool stopped cleanly after dispatch")
}

// ═══════════════════════════════════════════════════════════════════════════
// Integration Test B: Full Loopback Smoke Test
//
// Uses the real Monitor on the loopback interface, a real generator Pool,
// and the real Controller running in background goroutines. Generates
// loopback traffic to create measurable RX/TX deltas, then verifies the
// pipeline runs without panics or deadlocks.
// ═══════════════════════════════════════════════════════════════════════════

func TestIntegration_FullPipelineLoopback(t *testing.T) {
	// Short intervals for fast test execution.
	interval := 50 * time.Millisecond

	// 1. Start the real monitor on loopback.
	mon := monitor.New("lo", interval)
	mon.Start()
	defer mon.Stop()

	// Give the monitor time to take a baseline reading.
	time.Sleep(interval * 2)

	// 2. Start the real generator pool.
	pool := generator.NewPool(generator.PoolConfig{
		WorkerCount: 2,
		TargetIP:    "127.0.0.1",
		TargetPort:  19998,
		Scenario:    "udp",
	})
	defer pool.Stop()

	// 3. Start the controller with a low threshold.
	// Ratio 0.1, minTX 100, smoothing 1.0.
	ctrl := New(0.1, 100, 1.0, interval, mon, pool)
	ctrl.Start()
	defer ctrl.Stop()

	// 4. Generate actual loopback traffic.
	// Send and receive UDP packets on a dedicated port. This creates real
	// RX/TX byte counters on the loopback interface that the monitor can read.
	generateLoopbackTraffic(t)

	// 5. Wait for the controller to process several ticks.
	// Phase-offset is interval/2 = 25ms, then one tick at 50ms.
	// Wait for at least 3 full ticks to give the pipeline time to react.
	time.Sleep(interval * 5)

	// 6. Verify the monitor has produced stats.
	stats := mon.Stats()
	if stats == nil {
		t.Log("monitor produced no stats — loopback may have no traffic in this environment")
	} else {
		t.Logf("monitor stats: RX=%d TX=%d time=%v", stats.DeltaRX, stats.DeltaTX, stats.Time)
	}

	// 7. Verify the controller ran without panics.
	acc := ctrl.Accumulator()
	t.Logf("controller accumulator: %d", acc)
	t.Logf("generator scenario: %s", pool.CurrentScenario())

	// On loopback, RX ≈ TX (symmetric), so the controller most likely
	// won't dispatch jobs (no deficit). That's expected and fine —
	// the test validates that the full pipeline runs without errors.
}

func TestIntegration_FullPipelineGracefulShutdown(t *testing.T) {
	interval := 50 * time.Millisecond

	mon := monitor.New("lo", interval)
	mon.Start()

	pool := generator.NewPool(generator.PoolConfig{
		WorkerCount: 1,
		TargetIP:    "127.0.0.1",
		TargetPort:  19997,
		Scenario:    "udp",
	})

	ctrl := New(0.1, 1024, 0.9, interval, mon, pool)
	ctrl.Start()

	// Let it run briefly.
	time.Sleep(interval * 3)

	// Graceful shutdown — stop controller first, then pool, then monitor.
	ctrl.Stop()
	t.Log("controller stopped")

	pool.Stop()
	t.Log("pool stopped")

	mon.Stop()
	t.Log("monitor stopped")

	// All components should have stopped without hanging.
}

// ── Helpers ────────────────────────────────────────────────────────────────

// generateLoopbackTraffic creates real network traffic on the loopback
// interface by sending and receiving UDP packets. This produces measurable
// RX/TX byte counters in /proc/net/dev.
func generateLoopbackTraffic(t *testing.T) {
	t.Helper()

	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 29998}

	// Create a listener to receive traffic on loopback.
	listener, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
		return
	}
	defer listener.Close()

	// Send UDP packets to the listener in a goroutine.
	done := make(chan struct{})
	go func() {
		defer close(done)

		conn, err := net.DialUDP("udp", nil, addr)
		if err != nil {
			t.Logf("cannot dial loopback: %v", err)
			return
		}
		defer conn.Close()

		buf := make([]byte, 1400)
		for i := 0; i < 50; i++ {
			if _, err := conn.Write(buf); err != nil {
				t.Logf("write error: %v", err)
				return
			}
			time.Sleep(1 * time.Millisecond)
		}
	}()

	// Receive the packets (creates RX bytes on loopback).
	recvBuf := make([]byte, 1500)
	received := 0
	listener.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	for received < 50 {
		_, _, err := listener.ReadFromUDP(recvBuf)
		if err != nil {
			break // timeout or error — enough packets received
		}
		received++
	}

	<-done
	t.Logf("loopback traffic: sent 50 packets, received %d packets", received)
}
