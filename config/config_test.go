package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
)

// ═══════════════════════════════════════════════════════════════════════════
// Scenario Validation
// ═══════════════════════════════════════════════════════════════════════════

func TestSanitizeScenario_Valid(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"udp", "udp"},
		{"tcp-rst", "tcp-rst"},
		{"http-rst", "http-rst"},
		{"dns", "dns"},
		{"all", "all"},
		{"", "all"},
		{"udp,dns", "udp,dns"},
		{"udp, tcp-rst, dns", "udp, tcp-rst, dns"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := sanitizeScenario(tt.input)
			if err != nil {
				t.Errorf("sanitizeScenario(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("sanitizeScenario(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeScenario_Invalid(t *testing.T) {
	_, err := sanitizeScenario("quic")
	if err == nil {
		t.Error("expected error for unknown scenario")
	}
}

func TestSanitizeScenario_PartialInvalid(t *testing.T) {
	_, err := sanitizeScenario("udp,quic")
	if err == nil {
		t.Error("expected error when one of the scenarios is invalid")
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Viper Configuration Loading
// ═══════════════════════════════════════════════════════════════════════════

func TestViper_Defaults(t *testing.T) {
	v := viper.New()
	setDefaults(v)

	if v.GetString("interface") != "eth0" {
		t.Errorf("default interface = %q, want \"eth0\"", v.GetString("interface"))
	}
	if v.GetFloat64("ratio") != 0.1 {
		t.Errorf("default ratio = %v, want 0.1", v.GetFloat64("ratio"))
	}
	if v.GetInt("workers") != 4 {
		t.Errorf("default workers = %d, want 4", v.GetInt("workers"))
	}
}

func TestViper_YAMLConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "neghab.yaml")

	content := `
interface: ens160
ratio: 0.25
interval: 1s
scenario: dns
target: 10.0.0.1
port: 53
verbose: true
workers: 8
mix_interval: 60s
http_host: test.example.com
dns_resolvers:
  - 4.4.4.4
  - 8.8.8.8
min_tx: 4096
smoothing: 0.5
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		t.Fatalf("failed to read YAML config: %v", err)
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if cfg.Interface != "ens160" {
		t.Errorf("Interface = %q, want \"ens160\"", cfg.Interface)
	}
	if cfg.Ratio != 0.25 {
		t.Errorf("Ratio = %v, want 0.25", cfg.Ratio)
	}
	if cfg.Interval != 1*time.Second {
		t.Errorf("Interval = %v, want 1s", cfg.Interval)
	}
	if cfg.Scenario != "dns" {
		t.Errorf("Scenario = %q, want \"dns\"", cfg.Scenario)
	}
	if cfg.TargetIP != "10.0.0.1" {
		t.Errorf("TargetIP = %q, want \"10.0.0.1\"", cfg.TargetIP)
	}
	if cfg.TargetPort != 53 {
		t.Errorf("TargetPort = %d, want 53", cfg.TargetPort)
	}
	if !cfg.Verbose {
		t.Error("Verbose should be true")
	}
	if cfg.WorkerCount != 8 {
		t.Errorf("WorkerCount = %d, want 8", cfg.WorkerCount)
	}
	if cfg.MixInterval != 60*time.Second {
		t.Errorf("MixInterval = %v, want 60s", cfg.MixInterval)
	}
	if cfg.HTTPHost != "test.example.com" {
		t.Errorf("HTTPHost = %q, want \"test.example.com\"", cfg.HTTPHost)
	}
	if cfg.MinTXBytes != 4096 {
		t.Errorf("MinTXBytes = %d, want 4096", cfg.MinTXBytes)
	}
	if cfg.Smoothing != 0.5 {
		t.Errorf("Smoothing = %v, want 0.5", cfg.Smoothing)
	}
	if len(cfg.DNSResolvers) != 2 || cfg.DNSResolvers[0] != "4.4.4.4" {
		t.Errorf("DNSResolvers = %v", cfg.DNSResolvers)
	}
}

func TestViper_EnvVarOverride(t *testing.T) {
	// Set env var, create YAML config, verify env overrides config.
	dir := t.TempDir()
	path := filepath.Join(dir, "neghab.yaml")
	os.WriteFile(path, []byte("interface: file-iface\nratio: 0.5\n"), 0o644)

	t.Setenv("NEGHAB_INTERFACE", "env-iface")

	v := viper.New()
	v.SetConfigFile(path)
	v.ReadInConfig()

	v.SetEnvPrefix("NEGHAB")
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))

	// Env should override config file
	val := v.GetString("interface")
	if val == "file-iface" {
		t.Error("env var did not override config file; got file-iface, want env-iface")
	}
	// Ratio should come from config file (no env override)
	if v.GetFloat64("ratio") != 0.5 {
		t.Errorf("ratio from config file = %v, want 0.5", v.GetFloat64("ratio"))
	}
}

func TestViper_MissingConfigFile(t *testing.T) {
	v := viper.New()
	v.SetConfigFile("/nonexistent/path/config.yaml")
	err := v.ReadInConfig()
	if err == nil {
		t.Error("expected error for missing config file")
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Config → GeneratorConfig Conversion
// ═══════════════════════════════════════════════════════════════════════════

func TestGeneratorConfig(t *testing.T) {
	cfg := &Config{
		WorkerCount:  8,
		TargetIP:     "10.0.0.5",
		TargetPort:   8080,
		Scenario:     "udp,dns",
		MixInterval:  30 * time.Second,
		DNSResolvers: []string{"1.1.1.1"},
		HTTPHost:     "api.example.com",
	}

	gc := cfg.GeneratorConfig()

	if gc.WorkerCount != 8 {
		t.Errorf("WorkerCount = %d, want 8", gc.WorkerCount)
	}
	if gc.TargetIP != "10.0.0.5" {
		t.Errorf("TargetIP = %q, want \"10.0.0.5\"", gc.TargetIP)
	}
	if gc.Scenario != "udp,dns" {
		t.Errorf("Scenario = %q, want \"udp,dns\"", gc.Scenario)
	}
	if gc.MixInterval != 30*time.Second {
		t.Errorf("MixInterval = %v, want 30s", gc.MixInterval)
	}
	if len(gc.DNSResolvers) != 1 || gc.DNSResolvers[0] != "1.1.1.1" {
		t.Errorf("DNSResolvers = %v", gc.DNSResolvers)
	}
}
