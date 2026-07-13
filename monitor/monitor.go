// Package monitor reads network interface byte counters from /proc/net/dev
// and provides thread-safe access to RX/TX deltas.
package monitor

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sudosz/neghab/humanize"
)

// Stats holds the delta RX/TX values from the most recent monitoring interval
// plus the absolute interface counters (for cumulative ratio tracking).
type Stats struct {
	DeltaRX uint64
	DeltaTX uint64
	TotalRX uint64 // absolute RX byte counter from /proc/net/dev
	TotalTX uint64 // absolute TX byte counter from /proc/net/dev
	Time    time.Time
}

// Monitor periodically reads /proc/net/dev and computes RX/TX deltas.
type Monitor struct {
	iface    string
	interval time.Duration
	stats    atomic.Pointer[Stats]
	lastRX   uint64
	lastTX   uint64
	first    bool
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// New creates a new Monitor for the given interface and polling interval.
func New(iface string, interval time.Duration) *Monitor {
	return &Monitor{
		iface:    iface,
		interval: interval,
		first:    true,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// Start launches the monitoring loop in a background goroutine.
func (m *Monitor) Start() {
	go m.loop()
}

// Stop signals the monitoring loop to exit and waits for it to finish.
func (m *Monitor) Stop() {
	close(m.stopCh)
	<-m.doneCh
}

// Stats returns the most recent RX/TX deltas, or nil if no data is available yet.
func (m *Monitor) Stats() *Stats {
	return m.stats.Load()
}

// ListInterfaces returns the names of all network interfaces found in /proc/net/dev.
func ListInterfaces() ([]string, error) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return nil, fmt.Errorf("reading /proc/net/dev: %w", err)
	}
	return parseInterfaceNames(data), nil
}

// Interface returns the monitored interface name.
func (m *Monitor) Interface() string {
	return m.iface
}

func (m *Monitor) loop() {
	defer close(m.doneCh)

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	slog.Debug("monitor: started",
		"interface", m.iface,
		"interval", m.interval)

	for {
		select {
		case <-m.stopCh:
			slog.Debug("monitor: stopped")
			return

		case t := <-ticker.C:
			rx, tx, err := readInterfaceStats(m.iface)
			if err != nil {
				slog.Warn("monitor: failed to read stats", "error", err)
				continue
			}

			if m.first {
				m.lastRX = rx
				m.lastTX = tx
				m.first = false
				slog.Debug("monitor: initial baseline",
					"totalRX", humanize.Bytes(rx),
					"totalTX", humanize.Bytes(tx))
				continue
			}

			// Handle counter overflow: /proc/net/dev counters wrap at 2^64
			if rx < m.lastRX || tx < m.lastTX {
				slog.Warn("monitor: counter wrap detected, resetting baseline",
					"lastRX", m.lastRX, "currentRX", rx,
					"lastTX", m.lastTX, "currentTX", tx)
				m.lastRX = rx
				m.lastTX = tx
				continue
			}

			deltaRX := rx - m.lastRX
			deltaTX := tx - m.lastTX
			m.lastRX = rx
			m.lastTX = tx

			stats := &Stats{
				DeltaRX: deltaRX,
				DeltaTX: deltaTX,
				TotalRX: rx,
				TotalTX: tx,
				Time:    t,
			}
			m.stats.Store(stats)

			slog.Debug("monitor: interface stats",
				"deltaRX", humanize.Bytes(deltaRX),
				"deltaTX", humanize.Bytes(deltaTX),
				"totalRX", humanize.Bytes(rx),
				"totalTX", humanize.Bytes(tx))
		}
	}
}

// readInterfaceStats parses /proc/net/dev and returns RX and TX byte counters
// for the specified interface. Format of /proc/net/dev:
//
//	Inter-|   Receive                                                |  Transmit
//	 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
//	 eth0:  123456   1000    0    0    0     0          0         0  654321    500    0    0    0     0       0          0
func readInterfaceStats(iface string) (rx, tx uint64, err error) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0, fmt.Errorf("reading /proc/net/dev: %w", err)
	}

	return readInterfaceStatsFromLines(strings.Split(string(data), "\n"), iface)
}

// readInterfaceStatsFromLines parses interface stats from pre-split lines.
// Exported for testing.
func readInterfaceStatsFromLines(lines []string, iface string) (rx, tx uint64, err error) {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, iface+":") {
			continue
		}

		fields := strings.Fields(trimmed)
		if len(fields) < 10 {
			return 0, 0, fmt.Errorf(
				"unexpected field count for %q: got %d, want >=10",
				iface, len(fields))
		}

		// Field 1: RX bytes (index 1 after splitting by whitespace)
		rx, err = strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("parsing RX bytes for %q: %w", iface, err)
		}

		// Field 9: TX bytes (index 9 after splitting by whitespace)
		tx, err = strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("parsing TX bytes for %q: %w", iface, err)
		}

		return rx, tx, nil
	}

	// Interface not found — list available from the lines we already parsed
	avail := parseInterfaceNamesFromLines(lines)
	return 0, 0, fmt.Errorf("interface %q not found in /proc/net/dev; available: %v",
		iface, avail)
}

// parseInterfaceNames extracts interface names from /proc/net/dev data.
func parseInterfaceNames(data []byte) []string {
	return parseInterfaceNamesFromLines(strings.Split(string(data), "\n"))
}

// parseInterfaceNamesFromLines extracts interface names from pre-split lines.
func parseInterfaceNamesFromLines(lines []string) []string {
	var ifaces []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if idx := strings.IndexByte(trimmed, ':'); idx > 0 {
			ifaces = append(ifaces, trimmed[:idx])
		}
	}
	return ifaces
}
