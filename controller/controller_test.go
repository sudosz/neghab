package controller

import (
	"testing"
	"time"

	"github.com/sudosz/neghab/generator"
	"github.com/sudosz/neghab/monitor"
)

// ── Mock StatsReader ───────────────────────────────────────────────────────

// mockStatsReader implements StatsReader for testing controller tick logic.
type mockStatsReader struct {
	stats *monitor.Stats
}

func (m *mockStatsReader) Stats() *monitor.Stats { return m.stats }

// ── Mock JobDispatcher ─────────────────────────────────────────────────────

// mockJobDispatcher implements JobDispatcher for testing job dispatch logic.
type mockJobDispatcher struct {
	dispatched []generator.Job
	failNext   bool
}

func (g *mockJobDispatcher) Dispatch(job generator.Job) error {
	if g.failNext {
		g.failNext = false
		return &mockError{"queue full"}
	}
	g.dispatched = append(g.dispatched, job)
	return nil
}

type mockError struct{ msg string }

func (e *mockError) Error() string { return e.msg }

// ── Helpers ────────────────────────────────────────────────────────────────

// makeTestController creates a Controller with mock dependencies for testing.
func makeTestController(ratio float64, minTX uint64, smoothing float64, stats *monitor.Stats) (*Controller, *mockJobDispatcher) {
	gen := &mockJobDispatcher{}
	mon := &mockStatsReader{stats: stats}

	ctrl := New(ratio, minTX, 0, smoothing, time.Second, mon, gen)
	return ctrl, gen
}

// ═══════════════════════════════════════════════════════════════════════════
// Constructor Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestNew_SmoothingClamp(t *testing.T) {
	tests := []struct {
		name  string
		input float64
		want  float64
	}{
		{"normal", 0.9, 0.9},
		{"zero clamped to 1", 0.0, 1.0},
		{"negative clamped to 1", -0.5, 1.0},
		{"over 1 ok", 1.0, 1.0},
		{"above 1 clamped", 2.0, 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl, _ := makeTestController(0.1, 1024, tt.input, nil)
			if ctrl.smoothing != tt.want {
				t.Errorf("New(smoothing=%v).smoothing = %v, want %v",
					tt.input, ctrl.smoothing, tt.want)
			}
		})
	}
}

func TestController_AccumulatorDefaultsZero(t *testing.T) {
	ctrl, _ := makeTestController(0.1, 1024, 0.9, nil)
	if acc := ctrl.Accumulator(); acc != 0 {
		t.Errorf("initial accumulator = %d, want 0", acc)
	}
}

func TestController_StartStop(t *testing.T) {
	ctrl, _ := makeTestController(0.1, 1024, 0.9, nil)
	ctrl.Start()
	time.Sleep(10 * time.Millisecond)
	ctrl.Stop()
	// Should not hang or panic.
}

// ═══════════════════════════════════════════════════════════════════════════
// Tick Logic Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestTick_NoStats(t *testing.T) {
	ctrl, gen := makeTestController(0.1, 1024, 0.9, nil)

	ctrl.tick()

	if ctrl.Accumulator() != 0 {
		t.Error("accumulator should not change when stats are nil")
	}
	if len(gen.dispatched) != 0 {
		t.Error("no jobs should be dispatched when stats are nil")
	}
}

func TestTick_NoTraffic(t *testing.T) {
	stats := &monitor.Stats{DeltaRX: 0, DeltaTX: 0}
	ctrl, gen := makeTestController(0.1, 1024, 0.9, stats)

	ctrl.tick()

	if ctrl.Accumulator() != 0 {
		t.Error("accumulator should not change when no traffic")
	}
	if len(gen.dispatched) != 0 {
		t.Error("no jobs should be dispatched when no traffic")
	}
}

func TestTick_BelowMinTX(t *testing.T) {
	// 100 bytes RX, ratio 0.1 → desiredTX = 100/0.1 = 1000 bytes.
	// After smoothing: acc = 1000 × 0.9 = 900 < minTX=1024, so no dispatch yet.
	stats := &monitor.Stats{DeltaRX: 100, DeltaTX: 0}
	ctrl, gen := makeTestController(0.1, 1024, 0.9, stats)

	ctrl.tick()

	if ctrl.Accumulator() == 0 {
		t.Error("accumulator should accumulate the missing TX deficit")
	}
	if len(gen.dispatched) != 0 {
		t.Error("should not dispatch when below min-tx threshold")
	}
}

func TestTick_Dispatches(t *testing.T) {
	// 1 MB RX, ratio 0.1 → desiredTX = 1MB/0.1 = 10MB. Dispatches all.
	stats := &monitor.Stats{DeltaRX: 1_000_000, DeltaTX: 0}
	ctrl, gen := makeTestController(0.1, 1024, 0.9, stats)

	ctrl.tick()

	if len(gen.dispatched) == 0 {
		t.Fatal("expected at least one dispatched job")
	}

	job := gen.dispatched[0]
	if job.TargetBytes == 0 {
		t.Error("dispatched job has zero TargetBytes")
	}
	// With 1MB RX at 0.1 ratio, desiredTX = 10MB.
	// Full accumulator dispatched → accumulator should be ~0 after.
	remaining := ctrl.Accumulator()
	t.Logf("dispatched %d bytes, accumulator remaining: %d", job.TargetBytes, remaining)
}

func TestTick_DispatchFailure(t *testing.T) {
	stats := &monitor.Stats{DeltaRX: 1_000_000, DeltaTX: 0}
	ctrl, gen := makeTestController(0.1, 1024, 1.0, stats)
	gen.failNext = true

	ctrl.tick()

	// Dispatch failed, accumulator should NOT have been reduced.
	if ctrl.Accumulator() == 0 {
		t.Error("accumulator should retain deficit after dispatch failure")
	}
	if len(gen.dispatched) != 0 {
		t.Error("no jobs should be accepted when queue is full")
	}
}

func TestTick_ExistingTX(t *testing.T) {
	// 1 MB RX, 50 KB TX, ratio 0.1 → desiredTX = 1MB/0.1 = 10MB.
	// missing = 9.95MB, acc = 9.95MB × 0.9 ≈ 8.96MB, dispatched in full.
	stats := &monitor.Stats{DeltaRX: 1_000_000, DeltaTX: 50_000}
	ctrl, gen := makeTestController(0.1, 1024, 0.9, stats)

	ctrl.tick()

	if len(gen.dispatched) == 0 {
		t.Fatal("expected dispatch for the ~9.95MB deficit")
	}
	job := gen.dispatched[0]
	t.Logf("dispatched %d bytes (expected ~10445056)", job.TargetBytes)
}

func TestTick_TXExceedsDesired(t *testing.T) {
	// TX already exceeds desired → no generation needed, accumulator resets.
	stats := &monitor.Stats{DeltaRX: 1_000, DeltaTX: 20_000}
	// ratio 0.1 → desiredTX = 1KB/0.1 = 10KB. Real TX = 20KB >= 10KB, no deficit.
	ctrl, gen := makeTestController(0.1, 1024, 1.0, stats)

	ctrl.tick()

	if len(gen.dispatched) != 0 {
		t.Error("should not dispatch when real TX already exceeds desired")
	}
	if ctrl.Accumulator() != 0 {
		t.Error("accumulator should reset to 0 when natural TX meets target")
	}
}

func TestTick_AccumulatesOverMultipleIntervals(t *testing.T) {
	// Small traffic each tick should accumulate until threshold is met.
	stats := &monitor.Stats{DeltaRX: 50, DeltaTX: 0}
	// ratio 0.1 → desiredTX = 50/0.1 = 500 per tick.
	// Needs 1024/500 ≈ 3 ticks to reach minTX threshold.
	ctrl, gen := makeTestController(0.1, 1024, 0.9, stats)

	// Run multiple ticks
	for i := 0; i < 5; i++ {
		ctrl.tick()
	}

	if len(gen.dispatched) == 0 {
		t.Error("expected dispatch after accumulating enough deficit")
	}
}

func TestTick_MinTargetTXFloor(t *testing.T) {
	// With minTargetTX=10MB, even zero RX produces 10MB desiredTX per tick.
	stats := &monitor.Stats{DeltaRX: 0, DeltaTX: 0}

	gen := &mockJobDispatcher{}
	mon := &mockStatsReader{stats: stats}
	ctrl := New(0.1, 1024, 10_000_000, 1.0, time.Second, mon, gen)

	ctrl.tick()

	// With 1.0 smoothing, 10MB > 1024 minTX — should dispatch the full amount.
	if len(gen.dispatched) == 0 {
		t.Fatal("expected dispatch even with zero RX when minTargetTX is set")
	}
	if gen.dispatched[0].TargetBytes != 10_000_000 {
		t.Errorf("expected 10MB dispatch, got %d", gen.dispatched[0].TargetBytes)
	}
	// After dispatching the full accumulator, it should be back to 0.
	if ctrl.Accumulator() != 0 {
		t.Errorf("accumulator should be 0 after full dispatch, got %d", ctrl.Accumulator())
	}
}

func TestTick_AccumulatorResetsOnSufficientTX(t *testing.T) {
	// Pre-load the accumulator with a deficit from two prior intervals.
	// Each tick: missing=500, accumulate 500*0.9=450.
	ctrl, gen := makeTestController(0.1, 1024, 0.9,
		&monitor.Stats{DeltaRX: 50, DeltaTX: 0})
	ctrl.tick() // accumulator now has 450 bytes
	ctrl.tick() // accumulator now has 900 bytes

	if ctrl.Accumulator() != 900 {
		t.Fatalf("expected accumulator=900, got %d", ctrl.Accumulator())
	}

	// Now natural TX exceeds the target → accumulator must reset to 0.
	// ratio 0.1 → desiredTX = 1KB/0.1 = 10KB. Real TX = 20KB >= 10KB.
	stats := &monitor.Stats{DeltaRX: 1_000, DeltaTX: 20_000}
	ctrl.mon = &mockStatsReader{stats: stats}

	ctrl.tick()

	if ctrl.Accumulator() != 0 {
		t.Errorf("accumulator should reset to 0 when natural TX meets target, got %d",
			ctrl.Accumulator())
	}
	if len(gen.dispatched) != 0 {
		t.Error("should not dispatch when natural TX already meets target")
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Cumulative Ratio Gating Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestCumulative_FirstTickCapturesBaseline(t *testing.T) {
	stats := &monitor.Stats{
		DeltaRX: 10_000,
		DeltaTX: 0,
		TotalRX: 1_000_000_000, // 1GB absolute counter
		TotalTX: 5_000_000_000, // 5GB absolute counter
	}
	ctrl, _ := makeTestController(0.1, 1024, 1.0, stats)

	if ctrl.baselineSet {
		t.Fatal("baseline must not be set before first tick")
	}

	ctrl.tick()

	if !ctrl.baselineSet {
		t.Fatal("baseline must be set after first tick")
	}
	if ctrl.baselineRX != 1_000_000_000 {
		t.Errorf("baselineRX = %d, want %d", ctrl.baselineRX, 1_000_000_000)
	}
	if ctrl.baselineTX != 5_000_000_000 {
		t.Errorf("baselineTX = %d, want %d", ctrl.baselineTX, 5_000_000_000)
	}
}

func TestCumulative_FirstTickAlwaysFallsThrough(t *testing.T) {
	// Even with massive TX already on the interface (historically TX-heavy),
	// the first tick should fall through to per-tick logic because
	// sessionRX=0 → cumulative check is a no-op.
	stats := &monitor.Stats{
		DeltaRX: 1_000_000,       // 1MB new RX this interval
		DeltaTX: 0,               // no TX this interval
		TotalRX: 1_000_000_000,   // 1GB historical RX
		TotalTX: 500_000_000_000, // 500GB historical TX (500:1 ratio!)
	}
	ctrl, gen := makeTestController(0.1, 1024, 1.0, stats)

	ctrl.tick() // first tick: baseline captured, session deltas=0, falls through

	// The 500:1 historical ratio should NOT block generation on the first tick.
	// desiredTX = 1MB / 0.1 = 10MB, so this should dispatch.
	if len(gen.dispatched) == 0 {
		t.Error("first tick should dispatch even on historically TX-heavy interface")
	}
}

func TestCumulative_RatioMetBlocksGeneration(t *testing.T) {
	ctrl, gen := makeTestController(0.1, 1024, 1.0, nil)

	// First tick: establish baseline at 1GB RX, 5GB TX
	ctrl.mon = &mockStatsReader{stats: &monitor.Stats{
		DeltaRX: 0, DeltaTX: 0,
		TotalRX: 1_000_000_000, // 1GB
		TotalTX: 5_000_000_000, // 5GB
	}}
	ctrl.tick() // captures baseline

	// Second tick: sessionRX=2GB-1GB=1GB, sessionTX=10.5GB-5GB=5.5GB.
	// cumulativeTarget = 1GB / 0.1 = 10GB.
	// sessionTX (5.5GB) < cumulativeTarget (10GB) → ratio NOT met → generate.
	ctrl.mon = &mockStatsReader{stats: &monitor.Stats{
		DeltaRX: 1_000_000_000, // 1GB new RX
		DeltaTX: 0,             // no natural TX
		TotalRX: 2_000_000_000, // 2GB total
		TotalTX: 5_500_000_000, // 5.5GB total
	}}
	gen.dispatched = nil
	ctrl.tick()
	if len(gen.dispatched) == 0 {
		t.Fatal("expected dispatch when cumulative ratio is below target")
	}

	// Third tick: sessionRX=2.5GB-1GB=1.5GB, sessionTX=16GB-5GB=11GB.
	// cumulativeTarget = 1.5GB / 0.1 = 15GB.
	// sessionTX (11GB) < 15GB → still below target → still generates.
	// Set DeltaTX=0 so the per-tick check doesn't independently block.
	ctrl.mon = &mockStatsReader{stats: &monitor.Stats{
		DeltaRX: 500_000_000,    // 500MB new RX
		DeltaTX: 0,              // no natural TX (per-tick allows generation)
		TotalRX: 2_500_000_000,  // 2.5GB total
		TotalTX: 16_000_000_000, // 16GB total (includes generated TX from previous tick)
	}}
	gen.dispatched = nil
	ctrl.tick()
	if len(gen.dispatched) == 0 {
		t.Fatal("expected dispatch when cumulative ratio is still below target")
	}

	// Fourth tick: sessionRX=3GB-1GB=2GB, sessionTX=25GB-5GB=20GB.
	// cumulativeTarget = 2GB / 0.1 = 20GB.
	// sessionTX (20GB) >= 20GB → ratio MET → block generation.
	ctrl.mon = &mockStatsReader{stats: &monitor.Stats{
		DeltaRX: 500_000_000,
		DeltaTX: 9_000_000_000,
		TotalRX: 3_000_000_000,  // 3GB total
		TotalTX: 25_000_000_000, // 25GB total
	}}
	gen.dispatched = nil
	ctrl.tick()
	if len(gen.dispatched) != 0 {
		t.Error("should NOT dispatch when cumulative ratio is met")
	}
}

func TestCumulative_ResetsAccumulatorWhenRatioMet(t *testing.T) {
	// Use minTX=2000 so the first tick accumulates 1KB (desiredTX=100*10=1KB)
	// without reaching the dispatch threshold. This leaves a non-zero
	// accumulator for the cumulative check to reset — a meaningful test.
	ctrl, gen := makeTestController(0.1, 2000, 1.0, nil)

	// First tick: captures baseline, accumulates deficit but does NOT dispatch.
	stats1 := &monitor.Stats{
		DeltaRX: 100, DeltaTX: 0,
		TotalRX: 100_000_000, TotalTX: 500_000_000,
	}
	ctrl.mon = &mockStatsReader{stats: stats1}
	ctrl.tick() // desiredTX=100/0.1=1KB, acc=1KB < minTX=2KB → no dispatch

	accBefore := ctrl.Accumulator()
	if accBefore == 0 {
		t.Fatal("accumulator should be non-zero before cumulative reset (deficit below minTX)")
	}

	// Tick 2: cumulative ratio is met → accumulator should reset to 0.
	// sessionRX=200MB-100MB=100MB, cumulativeTarget=100MB/0.1=1GB.
	// sessionTX=2GB-500MB=1.5GB >= 1GB → ratio met → block & reset.
	stats2 := &monitor.Stats{
		DeltaRX: 0, DeltaTX: 0,
		TotalRX: 200_000_000,   // +100MB since baseline
		TotalTX: 2_000_000_000, // +1.5GB since baseline (above 1GB target)
	}
	ctrl.mon = &mockStatsReader{stats: stats2}
	gen.dispatched = nil

	ctrl.tick() // cumulative check fires, resets non-zero accumulator

	if ctrl.Accumulator() != 0 {
		t.Errorf("accumulator should reset to 0 when cumulative ratio met, got %d (was %d before)",
			ctrl.Accumulator(), accBefore)
	}
	if len(gen.dispatched) != 0 {
		t.Error("should NOT dispatch when cumulative ratio is met, even with non-zero accumulator")
	}
}

func TestCumulative_ZeroSessionRXSkipsCheck(t *testing.T) {
	// When no new RX since baseline (sessionRX=0), the cumulative check
	// is skipped. The per-tick logic handles the delta-based check.
	ctrl, gen := makeTestController(0.1, 1024, 1.0, nil)

	// Establish baseline
	ctrl.mon = &mockStatsReader{stats: &monitor.Stats{
		DeltaRX: 0, DeltaTX: 0,
		TotalRX: 500_000_000, TotalTX: 500_000_000,
	}}
	ctrl.tick()

	// Next tick: same TotalRX/TotalTX = sessionRX=0, sessionTX=0.
	// No cumulative check should fire. Per-tick: deltaRX=1MB, desiredTX=10MB.
	ctrl.mon = &mockStatsReader{stats: &monitor.Stats{
		DeltaRX: 1_000_000, DeltaTX: 0,
		TotalRX: 500_000_000, TotalTX: 500_000_000, // no change
	}}
	gen.dispatched = nil
	ctrl.tick()

	if len(gen.dispatched) == 0 {
		t.Error("should dispatch based on per-tick delta even when session totals are stagnant")
	}
}

func TestCumulative_CounterWrap(t *testing.T) {
	// Counter wrap: TotalRX < baselineRX → saturatingSub returns 0.
	// sessionRX=0 → cumulative check skipped → per-tick logic runs.
	ctrl, gen := makeTestController(0.1, 1024, 1.0, nil)

	// Establish baseline near max uint64
	ctrl.mon = &mockStatsReader{stats: &monitor.Stats{
		DeltaRX: 0, DeltaTX: 0,
		TotalRX: 18_000_000_000_000_000_000, // near max
		TotalTX: 18_000_000_000_000_000_000,
	}}
	ctrl.tick() // captures baseline

	// Simulate counter wrap: TotalRX rolled over to a small value
	ctrl.mon = &mockStatsReader{stats: &monitor.Stats{
		DeltaRX: 1_000_000, DeltaTX: 0,
		TotalRX: 500_000, // rolled over (less than baseline)
		TotalTX: 500_000,
	}}
	gen.dispatched = nil
	ctrl.tick()

	// saturatingSub(500_000, 18EB) = 0 → cumulative check skipped.
	// Per-tick: desiredTX = 1MB/0.1 = 10MB → should dispatch.
	if len(gen.dispatched) == 0 {
		t.Error("should dispatch on per-tick delta when counter wraps (sessionRX=0)")
	}
}

func TestCumulative_BaselineNotResetOnSubsequentTicks(t *testing.T) {
	// Once the baseline is captured, it should stay fixed across all
	// subsequent ticks. Only the first tick sets it.
	ctrl, _ := makeTestController(0.1, 1024, 1.0, nil)

	stats1 := &monitor.Stats{TotalRX: 1_000_000, TotalTX: 5_000_000}
	ctrl.mon = &mockStatsReader{stats: stats1}
	ctrl.tick()

	baselineAfterFirst := ctrl.baselineRX

	stats2 := &monitor.Stats{TotalRX: 2_000_000, TotalTX: 6_000_000}
	ctrl.mon = &mockStatsReader{stats: stats2}
	ctrl.tick()

	if ctrl.baselineRX != baselineAfterFirst {
		t.Errorf("baselineRX changed after first tick: %d → %d",
			baselineAfterFirst, ctrl.baselineRX)
	}
}

func TestCumulative_ExistingTestsStillPass(t *testing.T) {
	// Regression guard: verify that existing tests work correctly with
	// the cumulative gating. Tests that don't set TotalRX/TotalTX should
	// still behave the same way (cumulative check is a no-op).
	stats := &monitor.Stats{DeltaRX: 1_000_000, DeltaTX: 0}
	ctrl, gen := makeTestController(0.1, 1024, 0.9, stats)

	ctrl.tick()

	if len(gen.dispatched) == 0 {
		t.Fatal("existing test pattern: expected dispatch with 1MB RX at ratio 0.1")
	}
	// After dispatch, accumulator should be near 0.
	if ctrl.Accumulator() > 10000 {
		t.Errorf("accumulator too high after dispatch: %d", ctrl.Accumulator())
	}
}
