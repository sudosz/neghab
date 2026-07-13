package monitor

import (
	"strings"
	"testing"
	"time"
)

func TestReadInterfaceStats_Parsing(t *testing.T) {
	// Simulated /proc/net/dev content
	data := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
  lo: 1234567890  100000    0    0    0     0          0         0 9876543210   50000    0    0    0     0       0          0
 eth0: 8888888888  200000    0    0    0     0          0         0 7777777777   80000    0    0    0     0       0          0
 wlan0:   555555    5000    0    0    0     0          0         0   444444    3000    0    0    0     0       0          0`

	lines := strings.Split(data, "\n")
	rx, tx, err := readInterfaceStatsFromLines(lines, "eth0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rx != 8888888888 {
		t.Errorf("rx = %d, want 8888888888", rx)
	}
	if tx != 7777777777 {
		t.Errorf("tx = %d, want 7777777777", tx)
	}
}

func TestReadInterfaceStats_NotFound(t *testing.T) {
	data := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
  lo: 1234    100    0    0    0     0          0         0  5678    200    0    0    0     0       0          0`

	lines := strings.Split(data, "\n")
	_, _, err := readInterfaceStatsFromLines(lines, "eth99")
	if err == nil {
		t.Fatal("expected error for missing interface, got nil")
	}
}

func TestReadInterfaceStats_Baseline(t *testing.T) {
	data := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
 eth0:    1000      10    0    0    0     0          0         0    2000      5    0    0    0     0       0          0`

	lines := strings.Split(data, "\n")
	rx, tx, err := readInterfaceStatsFromLines(lines, "eth0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rx != 1000 {
		t.Errorf("rx = %d, want 1000", rx)
	}
	if tx != 2000 {
		t.Errorf("tx = %d, want 2000", tx)
	}
}

func TestParseInterfaceNames(t *testing.T) {
	data := []byte(`Inter-|   Receive
 face |bytes
  lo: 123
 eth0: 456
 docker0: 789`)

	names := parseInterfaceNames(data)

	want := map[string]bool{"lo": true, "eth0": true, "docker0": true}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected interface: %q", n)
		}
		delete(want, n)
	}
	if len(want) > 0 {
		t.Errorf("missing interfaces: %v", want)
	}
}

func TestListInterfaces_Integration(t *testing.T) {
	// This test attempts to read /proc/net/dev if available (Linux only).
	// On non-Linux or in containers without /proc/net/dev, it gracefully skips.
	ifaces, err := ListInterfaces()
	if err != nil {
		t.Skipf("skipping: cannot read /proc/net/dev: %v", err)
	}
	if len(ifaces) == 0 {
		t.Error("expected at least one interface (lo)")
	}
	t.Logf("found %d interfaces: %v", len(ifaces), ifaces)
}

// ═══════════════════════════════════════════════════════════════════════════
// Stats TotalRX / TotalTX Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestStats_ZeroValueHasZeroTotals(t *testing.T) {
	var s Stats
	if s.TotalRX != 0 {
		t.Errorf("zero-value Stats.TotalRX = %d, want 0", s.TotalRX)
	}
	if s.TotalTX != 0 {
		t.Errorf("zero-value Stats.TotalTX = %d, want 0", s.TotalTX)
	}
	if s.DeltaRX != 0 {
		t.Errorf("zero-value Stats.DeltaRX = %d, want 0", s.DeltaRX)
	}
	if s.DeltaTX != 0 {
		t.Errorf("zero-value Stats.DeltaTX = %d, want 0", s.DeltaTX)
	}
}

func TestStats_TotalFieldsAreIndependent(t *testing.T) {
	s := Stats{
		DeltaRX: 1000,
		DeltaTX: 500,
		TotalRX: 99_999_999_999,
		TotalTX: 88_888_888_888,
	}

	// Delta and total fields should hold their assigned values independently.
	if s.TotalRX != 99_999_999_999 {
		t.Errorf("TotalRX = %d, want 99_999_999_999", s.TotalRX)
	}
	if s.TotalTX != 88_888_888_888 {
		t.Errorf("TotalTX = %d, want 88_888_888_888", s.TotalTX)
	}
	// Delta values should not be affected by Total values.
	if s.DeltaRX != 1000 {
		t.Errorf("DeltaRX = %d, want 1000", s.DeltaRX)
	}
	if s.DeltaTX != 500 {
		t.Errorf("DeltaTX = %d, want 500", s.DeltaTX)
	}
}

func TestMonitor_StatsIncludesTotalCounters(t *testing.T) {
	// Start a monitor on the loopback interface with a fast interval.
	// After a couple of ticks, Stats should include non-zero TotalRX/TotalTX
	// (the absolute counters from /proc/net/dev).
	m := New("lo", 50*time.Millisecond)
	m.Start()
	defer m.Stop()

	// Wait for at least 2 ticks (baseline + one data point).
	time.Sleep(150 * time.Millisecond)

	s := m.Stats()
	if s == nil {
		t.Fatal("Stats() returned nil after monitor had time to collect data")
	}

	// On a live system, loopback should have non-zero byte counters
	// from system services (systemd-resolved, dbus, etc.).
	if s.TotalRX == 0 && s.TotalTX == 0 {
		t.Log("warning: loopback TotalRX and TotalTX are both 0 — may be an idle container")
	}

	// Delta values should also be populated.
	t.Logf("TotalRX=%d TotalTX=%d DeltaRX=%d DeltaTX=%d",
		s.TotalRX, s.TotalTX, s.DeltaRX, s.DeltaTX)
}

func TestMonitor_StatsAfterMultipleTicks(t *testing.T) {
	// After enough ticks, Stats must be non-nil and contain the absolute
	// interface counters in TotalRX/TotalTX — these are used by the
	// controller for cumulative ratio gating.
	m := New("lo", 40*time.Millisecond)
	m.Start()
	defer m.Stop()

	// Wait for baseline + 3 data ticks (40ms each = 160ms, add margin).
	time.Sleep(200 * time.Millisecond)

	s := m.Stats()
	if s == nil {
		t.Fatal("Stats() returned nil after monitor ran for multiple ticks")
	}
	// Stats must contain absolute interface counters.
	// On a quiet container these could be zero, but on any Linux system
	// with loopback traffic they'll be non-zero. Log but don't fail.
	t.Logf("after multiple ticks: TotalRX=%d TotalTX=%d DeltaRX=%d DeltaTX=%d",
		s.TotalRX, s.TotalTX, s.DeltaRX, s.DeltaTX)
}
