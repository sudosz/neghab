// Neghab — Advanced Network Traffic Simulator
//
// Monitors real network traffic on a Linux interface and injects synthetic
// upload traffic to enforce a user-defined TX/RX ratio, making the traffic
// profile appear as a legitimate application (video streaming, web browsing)
// to evade ISP traffic shaping and DPI.
//
// Usage:
//
//	sudo neghab --interface eth0 --ratio 0.1 --scenario udp
package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/sudosz/neghab/config"
	"github.com/sudosz/neghab/controller"
	"github.com/sudosz/neghab/generator"
	"github.com/sudosz/neghab/logger"
	"github.com/sudosz/neghab/monitor"
)

// Build information — set via ldflags at compile time.
var (
	Version   = "dev"
	BuildDate = "unknown"
)

func main() {
	// ---------- Parse configuration ----------
	cfg := config.Parse()

	// ---------- Initialize logging ----------
	logger.Init(cfg.Verbose)

	// ---------- Startup banner ----------
	logger.Banner(Version, BuildDate)
	logger.PrintStatus("Interface", cfg.Interface)
	logger.PrintStatus("Scenario", cfg.Scenario)
	logger.PrintStatus("Ratio", formatRatio(cfg.Ratio))
	logger.PrintStatus("Interval", cfg.Interval.String())
	logger.PrintStatus("Workers", fmt.Sprintf("%d", cfg.WorkerCount))
	if cfg.TargetIP != "" {
		logger.PrintStatus("Target", fmt.Sprintf("%s:%d", cfg.TargetIP, cfg.TargetPort))
	}
	if cfg.MixInterval > 0 {
		logger.PrintStatus("Mix", cfg.MixInterval.String())
	}
	if cfg.ConfigFile != "" {
		logger.PrintStatus("Config", cfg.ConfigFile)
	}
	os.Stdout.WriteString("\n")

	// ---------- Sanity checks ----------
	// Note: ratio warnings are emitted by controller.New() based on actual value.
	if cfg.Scenario == "tcp-rst" || cfg.Scenario == "all" {
		slog.Info("tcp-rst scenario requires CAP_NET_RAW; run with sudo if not already")
	}

	// ---------- Runtime info ----------
	slog.Debug("runtime info",
		"goVersion", runtime.Version(),
		"goOS", runtime.GOOS,
		"goArch", runtime.GOARCH,
		"numCPU", runtime.NumCPU(),
		"numGoroutine", runtime.NumGoroutine(),
	)

	// ---------- Start monitor ----------
	mon := monitor.New(cfg.Interface, cfg.Interval)
	mon.Start()

	// ---------- Start generator pool ----------
	gen := generator.NewPool(cfg.GeneratorConfig())

	// ---------- Start controller ----------
	ctrl := controller.New(
		cfg.Ratio,
		cfg.MinTXBytes,
		cfg.MinTargetTX,
		cfg.Smoothing,
		cfg.Interval,
		mon,
		gen,
	)
	ctrl.Start()

	// ---------- Log scenario rotation if enabled ----------
	if cfg.MixInterval > 0 {
		slog.Info("scenario rotation enabled",
			"interval", cfg.MixInterval,
		)
	}

	// ---------- Ready signal ----------
	logger.Success("Neghab is running — press Ctrl+C to stop")
	os.Stdout.WriteString("\n")

	// ---------- Wait for shutdown signal ----------
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	sig := <-sigCh
	slog.Info("shutting down", "signal", sig.String())

	// Graceful shutdown: stop components in reverse order with a timeout.
	// Controller → Generator → Monitor (defers run in reverse).
	done := make(chan struct{})
	go func() {
		ctrl.Stop()
		gen.Stop()
		mon.Stop()
		close(done)
	}()

	select {
	case <-done:
		logger.Success("Neghab stopped gracefully")
	case <-time.After(5 * time.Second):
		slog.Warn("graceful shutdown timed out, forcing exit")
	}
}

// formatRatio returns a human-readable ratio string, e.g., "1:10 (0.1)".
func formatRatio(r float64) string {
	reciprocal := 1.0 / r
	return fmt.Sprintf("1:%.0f (%.2f)", reciprocal, r)
}
