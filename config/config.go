// Package config handles multi-source configuration for Neghab using Viper.
//
// Priority (highest to lowest):
//  1. CLI flags (--interface, --ratio, etc.)
//  2. Environment variables (NEGHAB_INTERFACE, NEGHAB_RATIO, etc.)
//  3. Config file (YAML, JSON, or TOML — auto-detected)
//  4. Built-in defaults
//
// Config files are searched at:
//   - --config <path> (explicit)
//   - /etc/neghab/config.yaml (system)
//   - ./neghab.yaml (local fallback)
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/sudosz/neghab/generator"
	"github.com/sudosz/neghab/monitor"
)

// Config holds all configuration for the Neghab traffic simulator.
// Fields use mapstructure tags for Viper unmarshaling.
type Config struct {
	// Interface is the network interface to monitor (e.g., "eth0").
	Interface string `mapstructure:"interface"`

	// Ratio controls Upload vs Download: TargetTX = deltaRX / ratio.
	// ratio=0.1 → Upload=10×Download | ratio=1.0 → Upload=Download | ratio=10 → Upload=0.1×Download
	Ratio float64 `mapstructure:"ratio"`

	// Interval is the monitoring interval for reading /proc/net/dev stats.
	Interval time.Duration `mapstructure:"interval"`

	// Scenario is the traffic generation scenario(s) to use.
	Scenario string `mapstructure:"scenario"`

	// TargetIP is the destination IP for generated traffic.
	TargetIP string `mapstructure:"target"`

	// TargetPort is the destination port for generated traffic.
	TargetPort int `mapstructure:"port"`

	// Verbose enables debug-level logging.
	Verbose bool `mapstructure:"verbose"`

	// WorkerCount is the number of concurrent generator workers.
	WorkerCount int `mapstructure:"workers"`

	// MinTXBytes is the minimum deficit threshold before generating traffic.
	MinTXBytes uint64 `mapstructure:"min_tx"`

	// MinTargetTX is a floor for desiredTX per tick. When deltaRX/ratio
	// produces less than this, the floor is used instead. Set to 0 to disable.
	MinTargetTX uint64 `mapstructure:"min_target_tx"`

	// Smoothing dampens deficit accumulation (0.0–1.0). Each tick, only
	// smoothing × missing is added to the accumulator. 1.0 = no dampening.
	Smoothing float64 `mapstructure:"smoothing"`

	// MixInterval is how often to rotate scenarios (0 = no rotation).
	MixInterval time.Duration `mapstructure:"mix_interval"`

	// HTTPHost is the target hostname for the http-rst scenario.
	HTTPHost string `mapstructure:"http_host"`

	// HTTPRSTPath is the URL path for the http-rst scenario (e.g., /upload).
	HTTPRSTPath string `mapstructure:"http_rst_path"`

	// UploadPath is the URL path for the upload scenario (e.g., /speedtest/upload.php).
	UploadPath string `mapstructure:"upload_path"`

	// UploadBufferSize is the body chunk size for upload in bytes (default 131072 = 128KB).
	UploadBufferSize int `mapstructure:"upload_buffer_size"`

	// UploadStreams is the number of parallel HTTP streams per upload job (default 1).
	// Speedtest-cli uses 4-8 streams for maximum throughput.
	UploadStreams int `mapstructure:"upload_streams"`

	// UploadTargets is a list of upload servers as "host:port" (e.g., "server1:8080").
	// When set, streams are distributed round-robin across all targets. When empty,
	// falls back to the single TargetIP:TargetPort.
	UploadTargets []string `mapstructure:"upload_targets"`

	// DNSResolvers is the list of DNS resolver addresses.
	DNSResolvers []string `mapstructure:"dns_resolvers"`

	// ConfigFile is the path to the loaded configuration file (empty if none).
	ConfigFile string
}

// Parse reads configuration from CLI flags, environment variables, config
// files, and built-in defaults via Viper. Returns a validated *Config.
func Parse() *Config {
	v := viper.New()

	// ── 1. Defaults ────────────────────────────────────────────────────
	setDefaults(v)

	// ── 2. Config file paths ───────────────────────────────────────────
	// Resolved in order: --config > /etc/neghab/config.yaml > ./neghab.yaml.
	v.SetConfigType("yaml")

	// ── 3. Environment variables ───────────────────────────────────────
	v.SetEnvPrefix("NEGHAB")
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))

	// ── 4. CLI flags (pflag) ───────────────────────────────────────────
	configPath, listIfaces := registerFlags()

	pflag.Parse()

	// Handle --list-interfaces immediately (before any config validation).
	if *listIfaces {
		printInterfaces()
		os.Exit(0)
	}

	// ── 5. Read config file ────────────────────────────────────────────
	if *configPath != "" {
		v.SetConfigFile(*configPath)
		if err := v.ReadInConfig(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot read config file %q: %v\n", *configPath, err)
		}
	} else {
		// Try system path first: /etc/neghab/config.yaml
		v.SetConfigFile("/etc/neghab/config.yaml")
		if err := v.ReadInConfig(); err != nil {
			// Fall back to local: ./neghab.yaml
			v.SetConfigFile("./neghab.yaml")
			_ = v.ReadInConfig() // ignore error — no config is OK
		}
	}
	if cfgFile := v.ConfigFileUsed(); cfgFile != "" {
		fmt.Fprintf(os.Stderr, "info: using config file: %s\n", cfgFile)
	}

	// ── 6. Bind pflags to Viper (CLI overrides env/config) ────────────
	if err := v.BindPFlags(pflag.CommandLine); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to bind CLI flags: %v\n", err)
	}

	// ── 7. Unmarshal into Config struct ────────────────────────────────
	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to parse configuration: %v\n", err)
		os.Exit(1)
	}

	// Record which config file was used.
	cfg.ConfigFile = v.ConfigFileUsed()

	// ── 8. Post-process ────────────────────────────────────────────────
	// DNS resolvers: Viper stores CLI/env string values as a single-element
	// slice (e.g., ["8.8.8.8,1.1.1.1"]). YAML arrays come through correctly.
	// Normalize by splitting comma-separated values.
	if len(cfg.DNSResolvers) == 1 && strings.Contains(cfg.DNSResolvers[0], ",") {
		parts := strings.Split(cfg.DNSResolvers[0], ",")
		cfg.DNSResolvers = nil
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				cfg.DNSResolvers = append(cfg.DNSResolvers, p)
			}
		}
	}
	if len(cfg.DNSResolvers) == 0 {
		cfg.DNSResolvers = []string{"8.8.8.8", "1.1.1.1"}
	}

	// Upload targets: same normalization — CLI/env provides comma-separated
	// string, YAML provides native array. Also filter the default empty string
	// that Viper stores when the --upload-targets flag is not set.
	if len(cfg.UploadTargets) == 1 && cfg.UploadTargets[0] == "" {
		cfg.UploadTargets = nil
	}
	if len(cfg.UploadTargets) == 1 && strings.Contains(cfg.UploadTargets[0], ",") {
		parts := strings.Split(cfg.UploadTargets[0], ",")
		cfg.UploadTargets = nil
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				cfg.UploadTargets = append(cfg.UploadTargets, p)
			}
		}
	}

	// ── 9. Validate ────────────────────────────────────────────────────
	validateConfig(cfg)

	return cfg
}

// setDefaults registers all built-in defaults with Viper.
func setDefaults(v *viper.Viper) {
	v.SetDefault("interface", "eth0")
	v.SetDefault("ratio", 0.1)
	v.SetDefault("interval", "500ms")
	v.SetDefault("scenario", "udp")
	v.SetDefault("target", "192.168.1.254")
	v.SetDefault("port", 443)
	v.SetDefault("verbose", false)
	v.SetDefault("workers", 4)
	v.SetDefault("mix_interval", "0s")
	v.SetDefault("http_host", "example.com")
	v.SetDefault("http_rst_path", "/upload")
	v.SetDefault("upload_path", "/upload")
	v.SetDefault("upload_buffer_size", 131072)
	v.SetDefault("upload_streams", 1)
	v.SetDefault("upload_targets", []string{})
	v.SetDefault("dns_resolvers", []string{"8.8.8.8", "1.1.1.1"})
	v.SetDefault("min_tx", 1024)
	v.SetDefault("min_target_tx", 0)
	v.SetDefault("smoothing", 0.9)
}

// registerFlags defines all CLI flags via pflag and returns pointers to
// the --config and --list-interfaces values (needed before config load).
func registerFlags() (configPath *string, listIfaces *bool) {
	configPath = pflag.String("config", "",
		"Path to configuration file (YAML, JSON, or TOML)")

	listIfaces = pflag.Bool("list-interfaces", false,
		"List available network interfaces and exit")

	pflag.String("interface", "eth0",
		"Network interface to monitor (e.g., eth0, ens160)")
	pflag.Float64("ratio", 0.1,
		"Upload/Download multiplier: TargetTX = deltaRX / ratio (0.1 = Upload 10×Download)")
	pflag.Duration("interval", 500*time.Millisecond,
		"Monitoring interval (e.g., 500ms, 1s)")
	pflag.String("scenario", "udp",
		"Traffic scenario(s): udp, tcp-rst, http-rst, dns, all, or comma-separated")
	pflag.String("target", "192.168.1.254",
		"Target IP address for generated traffic")
	pflag.Int("port", 443,
		"Target port for generated traffic")
	pflag.Bool("verbose", false,
		"Enable debug-level logging")
	pflag.Int("workers", 4,
		"Number of concurrent generator workers")
	pflag.Duration("mix-interval", 0,
		"Scenario rotation interval (e.g., 60s, 5m). 0 = no rotation")
	pflag.String("http-host", "example.com",
		"Target hostname for the http-rst scenario")
	pflag.String("http-rst-path", "/upload",
		"URL path for the http-rst scenario (e.g., /upload)")
	pflag.String("upload-path", "/upload",
		"URL path for the upload scenario (e.g., /speedtest/upload.php)")
	pflag.Int("upload-buffer-size", 131072,
		"Upload body buffer size in bytes (default 128KB, e.g., 262144 for 256KB)")
	pflag.Int("upload-streams", 1,
		"Parallel HTTP streams per upload job (default 1, speedtest-cli uses 4-8)")
	pflag.String("upload-targets", "",
		"Comma-separated upload servers as host:port or host:port/path (e.g., srv1:8080/up,srv2:8080)")
	pflag.String("dns-resolvers", "8.8.8.8,1.1.1.1",
		"Comma-separated DNS resolver addresses for dns scenario")
	pflag.Int("min-tx", 1024,
		"Minimum TX deficit in bytes before generating traffic")
	pflag.Int("min-target-tx", 0,
		"Minimum desired TX per tick in bytes (floor, 0 = disabled)")
	pflag.Float64("smoothing", 0.9,
		"Deficit accumulation dampener (0.0–1.0, 1.0 = no dampening)")

	pflag.Usage = usage

	return
}

// ── Validation ────────────────────────────────────────────────────────────

func validateConfig(cfg *Config) {
	if cfg.MinTXBytes == 0 {
		fmt.Fprintf(os.Stderr, "error: --min-tx must be positive\n")
		os.Exit(1)
	}
	if cfg.Smoothing <= 0 || cfg.Smoothing > 1 {
		fmt.Fprintf(os.Stderr, "error: --smoothing must be between 0.0 and 1.0 (got %.2f)\n", cfg.Smoothing)
		os.Exit(1)
	}
	if cfg.Ratio <= 0 || cfg.Ratio > 10 {
		fmt.Fprintf(os.Stderr, "error: --ratio must be between 0.0 and 10.0 (got %.2f)\n", cfg.Ratio)
		os.Exit(1)
	}
	if cfg.Interval < 100*time.Millisecond {
		fmt.Fprintf(os.Stderr, "warning: --interval < 100ms may cause high CPU usage\n")
	}
	if cfg.WorkerCount < 1 {
		fmt.Fprintf(os.Stderr, "error: --workers must be at least 1 (got %d)\n", cfg.WorkerCount)
		os.Exit(1)
	}

	validScenarios, err := sanitizeScenario(cfg.Scenario)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid scenario %q. Valid: udp, tcp-rst, http-rst, dns, upload, download, all\n", cfg.Scenario)
		os.Exit(1)
	}
	cfg.Scenario = validScenarios
}

// ── Helpers ───────────────────────────────────────────────────────────────

// GeneratorConfig converts the Config to a generator.PoolConfig.
func (c *Config) GeneratorConfig() generator.PoolConfig {
	return generator.PoolConfig{
		WorkerCount:      c.WorkerCount,
		TargetIP:         c.TargetIP,
		TargetPort:       c.TargetPort,
		Scenario:         c.Scenario,
		MixInterval:      c.MixInterval,
		DNSResolvers:     c.DNSResolvers,
		HTTPHost:         c.HTTPHost,
		HTTPRSTPath:      c.HTTPRSTPath,
		UploadPath:       c.UploadPath,
		UploadBufferSize: c.UploadBufferSize,
		UploadStreams:    c.UploadStreams,
		UploadTargets:    c.UploadTargets,
	}
}

// sanitizeScenario validates and normalizes the scenario string.
func sanitizeScenario(s string) (string, error) {
	valid := map[string]bool{
		"udp": true, "tcp-rst": true, "http-rst": true, "dns": true,
		"upload": true, "download": true,
	}

	if s == "" || s == "all" {
		return "all", nil
	}

	parts := strings.Split(s, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if !valid[p] {
			return "", fmt.Errorf("unknown scenario: %s", p)
		}
	}
	return s, nil
}

// printInterfaces reads /proc/net/dev and prints available interfaces.
func printInterfaces() {
	ifaces, err := monitor.ListInterfaces()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot read /proc/net/dev: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Available network interfaces:")
	for _, iface := range ifaces {
		fmt.Printf("  %s\n", iface)
	}
}

// usage prints the CLI help text.
func usage() {
	fmt.Fprintf(os.Stderr, "Neghab — Advanced Network Traffic Simulator\n\n")
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "  neghab [flags]\n\n")
	fmt.Fprintf(os.Stderr, "Flags:\n")
	pflag.PrintDefaults()
	fmt.Fprintf(os.Stderr, "\nConfiguration:\n")
	fmt.Fprintf(os.Stderr, "  Config files (YAML, JSON, TOML) searched at:\n")
	fmt.Fprintf(os.Stderr, "    1. --config <path>\n")
	fmt.Fprintf(os.Stderr, "    2. /etc/neghab/config.yaml (system)\n")
	fmt.Fprintf(os.Stderr, "    3. ./neghab.yaml (local fallback)\n")
	fmt.Fprintf(os.Stderr, "  Environment variables: NEGHAB_<KEY> (e.g., NEGHAB_INTERFACE=eth0)\n")
	fmt.Fprintf(os.Stderr, "  Priority: CLI flags > env vars > config file > defaults\n")
	fmt.Fprintf(os.Stderr, "\nScenarios:\n")
	fmt.Fprintf(os.Stderr, "  udp        UDP Dead-End — QUIC-like packets to silent IP (default)\n")
	fmt.Fprintf(os.Stderr, "  tcp-rst    TCP RST Flood — raw RST/ACK packets (requires CAP_NET_RAW)\n")
	fmt.Fprintf(os.Stderr, "  http-rst   HTTP POST + RST — abort upload with connection reset\n")
	fmt.Fprintf(os.Stderr, "  dns        DNS Query Spam — random lookups to public resolvers\n")
	fmt.Fprintf(os.Stderr, "  upload     HTTP POST upload — works with speedtest.net servers and any HTTP endpoint\n")
	fmt.Fprintf(os.Stderr, "  download   HTTP GET download — generates RX to feed the ratio formula\n")
	fmt.Fprintf(os.Stderr, "  all        All available scenarios in rotation\n\n")
	fmt.Fprintf(os.Stderr, "Examples:\n")
	fmt.Fprintf(os.Stderr, "  sudo neghab --interface eth0 --ratio 0.1 --scenario udp\n")
	fmt.Fprintf(os.Stderr, "  sudo neghab --config /etc/neghab/config.yaml\n")
	fmt.Fprintf(os.Stderr, "  NEGHAB_SCENARIO=dns sudo -E neghab --interface eth0\n")
	fmt.Fprintf(os.Stderr, "  sudo neghab --interface eth0 --scenario all --mix-interval 60s\n")
}
