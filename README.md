<p align="center">
  <img src="https://img.shields.io/badge/Neghab-Traffic%20Simulator-6366f1?style=for-the-badge&logo=go&logoColor=white" alt="Neghab">
</p>

<p align="center">
  <img src="https://img.shields.io/github/actions/workflow/status/sudosz/neghab/release.yml?style=flat-square&label=ci" alt="CI">
  <img src="https://img.shields.io/github/go-mod/go-version/sudosz/neghab?style=flat-square" alt="Go Version">
  <img src="https://img.shields.io/github/license/sudosz/neghab?style=flat-square" alt="License">
  <img src="https://img.shields.io/github/v/release/sudosz/neghab?style=flat-square" alt="Release">
</p>

<p align="center">
  <strong>
    Real-time network traffic shaper that monitors interface byte counters<br>
    and injects synthetic traffic to enforce a precise TX/RX ratio —<br>
    making your traffic profile indistinguishable from legitimate applications.
  </strong>
</p>

---

## 📖 Table of Contents

- [How It Works](#-how-it-works)
- [Features](#-features)
- [Quick Start](#-quick-start)
- [Traffic Scenarios](#-traffic-scenarios)
- [Installation](#-installation)
- [CLI Usage](#-cli-usage)
- [Configuration Reference](#-configuration-reference)
- [systemd Service](#-systemd-service)
- [Building from Source](#-building-from-source)
- [Project Structure](#-project-structure)
- [Security](#-security)
- [Legal Disclaimer](#-legal-disclaimer)

---

## 🔬 How It Works

```
                          ┌─────────────────────────────────┐
                          │         Neghab Daemon            │
                          │                                  │
  /proc/net/dev ─────────▶│  ┌──────────┐    ┌───────────┐  │   Generated
                          │  │ Monitor  │───▶│Controller │  │   Traffic
                          │  │ 500ms    │    │           │  │──▶──────▶
                          │  └──────────┘    └─────┬─────┘  │
                          │                        │        │
                          │       accumulator      │dispatch│
                          │       + cumulative     │        │
                          │       ratio gating     ▼        │
                          │                ┌───────────┐    │
                          │                │ Generator │    │
                          │                │   Pool    │    │
                          │                │ (N workers│    │
                          │                │  + mixer) │    │
                          │                └───────────┘    │
                          └─────────────────────────────────┘
```

**Three goroutine pipeline:**

1. **Monitor** — polls `/proc/net/dev` every `interval` (default 500ms), computes RX/TX deltas and exposes absolute interface counters for cumulative ratio tracking.

2. **Controller** — the decision engine. On each tick it:
   - Captures a **session baseline** on the first tick (prevents historical traffic from skewing the ratio)
   - Checks the **cumulative ratio**: if `sessionTX ≥ sessionRX / ratio`, generation is blocked — even if per-tick deltas show a deficit
   - Computes per-tick deficit: `desiredTX = deltaRX / ratio`, accumulates with configurable smoothing
   - Dispatches generation jobs when the deficit exceeds `--min-tx`

3. **Generator Pool** — N concurrent workers execute the active traffic scenario. Multiple scenarios rotate via the built-in mixer. HTTP scenarios use persistent connection pools with pre-warmed connections for zero cold-start latency.

### Ratio Formula

```
desiredTX = deltaRX / ratio

ratio=0.1  →  TX = 10 × RX   (heavy upload)
ratio=0.5  →  TX =  2 × RX   (moderate upload)
ratio=1.0  →  TX =  1 × RX   (symmetric)
ratio=10   →  TX = 0.1 × RX  (heavy download)
```

**Cumulative gating** prevents ratio overshoot: even if per-tick deltas need TX, the controller won't dispatch if the session's overall TX already exceeds the target. This is what stops neghab from perpetually generating new TX when the cumulative ratio is already met.

---

## ✨ Features

| Category | Capability |
|----------|-----------|
| **Monitoring** | Sub-second `/proc/net/dev` polling with counter wrap detection |
| **Ratio Control** | Session-based cumulative gating + per-tick deficit accumulation with configurable smoothing |
| **Scenarios** | 6 built-in: UDP, TCP RST, HTTP-RST, DNS, Upload (speedtest-style), Download (RX generator) |
| **Upload Engine** | Persistent connection pools, N parallel streams per job, multi-server round-robin, configurable buffer sizes, per-target URL paths |
| **Connection Pool** | Pre-warmed TCP connections, lock-free `get`/`put`, idle timeout — congestion windows stay hot |
| **Scenario Mixer** | Automatic rotation through any combination of scenarios at configurable intervals |
| **Performance** | Zero-allocation hot paths (`sync.Pool` buffers), chunked writes (no OOM on large jobs), persistent UDP connections |
| **DX** | ANSI-colored structured logging, journald-compatible, startup banner with build info |
| **Dependencies** | Zero — standard library only; single statically-linked binary |
| **Deployment** | Hardened systemd unit, one-line installer, multi-arch binaries (`amd64` + `arm64`) |

---

## 🚀 Quick Start

```bash
# One-line install
curl -sSL https://raw.githubusercontent.com/sudosz/neghab/main/deploy/install.sh | sudo bash

# List available interfaces
sudo neghab --list-interfaces

# Basic UDP dead-end (default scenario, ~100% TX efficiency)
sudo neghab --interface eth0 --ratio 0.1

# Speedtest-style upload — saturates your upstream
sudo neghab --interface eth0 --scenario upload \
  --target mashhad1.irancell.ir --port 8080 \
  --upload-path /speedtest/upload.php \
  --upload-streams 8 --workers 16

# Stealth HTTP-RST — looks like aborted uploads
sudo neghab --interface eth0 --scenario http-rst \
  --http-host your-server.com --port 443

# All scenarios rotating every 60 seconds
sudo neghab --interface eth0 --scenario all --mix-interval 60s
```

---

## 🎭 Traffic Scenarios

| Scenario | TX Efficiency | Pooling | Parallel Streams | Multi-Server | Best For |
|----------|:------------:|:--------:|:----------------:|:------------:|----------|
| **UDP Dead-End** | ~100% | — | — | — | Maximum TX, zero RX |
| **TCP RST Flood** | ~100% | — | — | — | Raw packet injection |
| **HTTP-RST** | ~99.9% | — | — | — | Stealth TX (aborted uploads) |
| **DNS Query Spam** | ~95% | ✅ | — | — | Blending with normal DNS |
| **Upload** ⭐ | ~99.5% | ✅ | ✅ | ✅ | Maximum throughput |
| **Download** ⭐ | — (generates RX) | ✅ | — | — | Feeds the ratio loop |

### UDP Dead-End *(Default)*

Sends UDP packets with QUIC-like headers to a non-responsive target IP. Packet sizes range 800–1500 bytes with burst patterns mimicking real QUIC/WebRTC streams. The target never responds — near-perfect TX efficiency.

```bash
sudo neghab --interface eth0 --ratio 0.1
```

### TCP RST Flood

Sends raw TCP RST+ACK packets from spoofed random source IPs via raw sockets. Requires `CAP_NET_RAW` (root). The remote host drops them silently.

```bash
sudo neghab --interface eth0 --scenario tcp-rst --target 10.0.0.1 --port 443
```

### HTTP-RST — Stealth TX

Establishes a TCP connection, sends a complete HTTP POST with realistic body content (80% printable ASCII, 20% binary), then forces a connection reset via `SO_LINGER` before the server can reply. On the wire, it looks exactly like an interrupted upload — extremely effective for evading simple traffic shaping.

```bash
sudo neghab --interface eth0 --scenario http-rst \
  --http-host your-server.com --port 443 --http-rst-path /upload
```

### DNS Query Spam

Sends DNS AAAA queries for random subdomains to public resolvers through persistent UDP connections (no dial-per-query overhead). Responses are NXDOMAIN (small). Mimics ad-tracking or malware beacon traffic.

```bash
sudo neghab --interface eth0 --scenario dns \
  --dns-resolvers "8.8.8.8,1.1.1.1,9.9.9.9"
```

### Upload ⭐ — Speedtest-Style Throughput

Full HTTP POST engine with **connection pooling**, **parallel streams**, and **multi-server round-robin**. Each target server gets a dedicated connection pool — TCP congestion windows stay warm across jobs. Pre-warming eliminates cold-start handshake overhead.

- **Connection pooling** — connections reused across jobs; no TCP handshake per dispatch
- **Parallel streams** — configurable N streams per job (speedtest-cli uses 4–8)
- **Multi-server** — round-robin distribution across any number of servers
- **Per-target paths** — `server:8080/speedtest/upload.php` — each server gets its own URL endpoint
- **Configurable buffers** — body chunk size from 4 KB to multiple MB

```bash
# Single server — maximum throughput
sudo neghab --interface eth0 --scenario upload \
  --target speedtest.example.com --port 8080 \
  --upload-path /upload --upload-streams 8 --workers 16 \
  --upload-buffer-size 524288 --min-target-tx 10485760

# Multi-server with per-target paths — speedtest.net style
sudo neghab --interface eth0 --scenario upload \
  --upload-targets "srv1:8080/speedtest/upload.php,srv2:8080/upload" \
  --upload-streams 8 --workers 16
```

```yaml
# YAML equivalent — multi-server speedtest configuration
interface: eth0
ratio: 0.1
scenario: upload
upload_targets:
  - mashhad1.irancell.ir:8080/speedtest/upload.php
  - speedtest1.pishgaman.net:8080/upload
upload_streams: 8
upload_buffer_size: 524288
workers: 16
min_target_tx: 10485760
smoothing: 1.0
```

### Download — RX Generator

HTTP GET requests that read back the response body, generating **RX traffic** on the interface. The RX feeds the controller's ratio formula (`desiredTX = deltaRX / ratio`), creating a self-reinforcing bidirectional traffic loop. Uses the same connection pool as upload — best deployed together.

```bash
sudo neghab --interface eth0 --scenario upload,download --workers 8
```

---

## 📦 Installation

### One-Line Installer

```bash
curl -sSL https://raw.githubusercontent.com/sudosz/neghab/main/deploy/install.sh | sudo bash
```

Detects your architecture, downloads the latest release, installs to `/usr/local/bin`, creates and enables a systemd service.

### Manual Binary Download

```bash
# Linux amd64
sudo curl -fsSL -o /usr/local/bin/neghab \
  https://github.com/sudosz/neghab/releases/latest/download/neghab-linux-amd64
sudo chmod +x /usr/local/bin/neghab

# Linux arm64 (Raspberry Pi)
sudo curl -fsSL -o /usr/local/bin/neghab \
  https://github.com/sudosz/neghab/releases/latest/download/neghab-linux-arm64
sudo chmod +x /usr/local/bin/neghab
```

### Build from Source

```bash
git clone https://github.com/sudosz/neghab.git
cd neghab
make build
sudo make install
```

---

## ⌨️ CLI Usage

```bash
# Basic: UDP dead-end with 10:1 TX/RX ratio
sudo neghab --interface eth0 --ratio 0.1

# HTTP-RST stealth mode
sudo neghab --interface eth0 --scenario http-rst \
  --http-host speedtest.example.com --port 443

# Upload + Download loop (bidirectional traffic)
sudo neghab --interface eth0 --scenario upload,download \
  --target speedtest.example.com --port 8080 \
  --upload-streams 4 --workers 8

# Multi-server upload with per-target paths
sudo neghab --interface eth0 --scenario upload \
  --upload-targets "srv1:8080/up,srv2:8080/up,srv3:8080" \
  --upload-streams 8 --workers 16 --upload-buffer-size 524288

# All scenarios rotating every 60s
sudo neghab --interface eth0 --scenario all --mix-interval 60s --ratio 0.15

# From config file
sudo neghab --config /etc/neghab/config.yaml
```

### All Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--interface` | `eth0` | Network interface to monitor and transmit through |
| `--ratio` | `0.1` | TX/RX target: `desiredTX = deltaRX / ratio` (0.1 = TX 10× RX) |
| `--interval` | `500ms` | Monitoring interval for `/proc/net/dev` polling |
| `--scenario` | `udp` | `udp`, `tcp-rst`, `http-rst`, `dns`, `upload`, `download`, `all`, or comma-separated |
| `--target` | `192.168.1.254` | Target IP for UDP, TCP-RST, and HTTP-RST scenarios |
| `--port` | `443` | Target port |
| `--workers` | `4` | Concurrent generator workers |
| `--mix-interval` | `0s` | Scenario rotation interval (0 = disabled) |
| `--http-host` | `example.com` | `Host` header for HTTP-RST |
| `--http-rst-path` | `/upload` | URL path for HTTP-RST POST requests |
| `--upload-path` | `/upload` | URL path for upload/download scenarios |
| `--upload-streams` | `1` | Parallel HTTP streams per upload job (4–8 for throughput) |
| `--upload-buffer-size` | `131072` | Upload body buffer size in bytes (128 KB default) |
| `--upload-targets` | `""` | Comma-separated `host:port/path` servers for multi-server upload |
| `--dns-resolvers` | `8.8.8.8,1.1.1.1` | DNS resolver addresses for dns scenario |
| `--min-tx` | `1024` | Minimum accumulated deficit before dispatching a job (bytes) |
| `--min-target-tx` | `0` | Per-tick desired TX floor (0 = disabled) |
| `--smoothing` | `0.9` | Deficit dampener (0.0–1.0; 1.0 = no dampening) |
| `--verbose` | `false` | Enable debug-level logging |
| `--config` | `""` | Path to YAML/JSON/TOML config file |
| `--list-interfaces` | — | List available interfaces and exit |

---

## 📝 Configuration Reference

Priority: **CLI flags → `NEGHAB_*` env vars → config file → defaults**.

Config files searched at:
1. `--config <path>` (explicit)
2. `/etc/neghab/config.yaml` (system-wide)
3. `./neghab.yaml` (local fallback)

Formats: YAML (primary), JSON, or TOML.

### Minimal

```yaml
interface: eth0
ratio: 0.1
scenario: udp
```

### Speedtest-Style Multi-Server Upload

```yaml
interface: eth0
ratio: 0.1
scenario: upload

upload_targets:
  - mashhad1.irancell.ir:8080/speedtest/upload.php
  - speedtest1.pishgaman.net:8080/upload

upload_streams: 8
upload_buffer_size: 524288
workers: 16
min_target_tx: 10485760
smoothing: 1.0
```

### Bidirectional Upload + Download Loop

```yaml
interface: eth0
ratio: 0.1
scenario: upload,download
target: speedtest.example.com
port: 8080
upload_path: /upload
upload_streams: 4
workers: 8
mix_interval: 30s
```

### Stealth HTTP-RST

```yaml
interface: eth0
ratio: 0.1
scenario: http-rst
target: your-internal-server.com
port: 443
http_host: your-internal-server.com
http_rst_path: /upload
workers: 8
smoothing: 0.5
```

### Full Field Reference

| Field | Type | Default | CLI Flag | Env Var |
|-------|------|---------|----------|---------|
| `interface` | string | `eth0` | `--interface` | `NEGHAB_INTERFACE` |
| `ratio` | float | `0.1` | `--ratio` | `NEGHAB_RATIO` |
| `interval` | duration | `500ms` | `--interval` | `NEGHAB_INTERVAL` |
| `scenario` | string | `udp` | `--scenario` | `NEGHAB_SCENARIO` |
| `target` | string | `192.168.1.254` | `--target` | `NEGHAB_TARGET` |
| `port` | int | `443` | `--port` | `NEGHAB_PORT` |
| `workers` | int | `4` | `--workers` | `NEGHAB_WORKERS` |
| `mix_interval` | duration | `0s` | `--mix-interval` | `NEGHAB_MIX_INTERVAL` |
| `http_host` | string | `example.com` | `--http-host` | `NEGHAB_HTTP_HOST` |
| `http_rst_path` | string | `/upload` | `--http-rst-path` | `NEGHAB_HTTP_RST_PATH` |
| `upload_path` | string | `/upload` | `--upload-path` | `NEGHAB_UPLOAD_PATH` |
| `upload_streams` | int | `1` | `--upload-streams` | `NEGHAB_UPLOAD_STREAMS` |
| `upload_buffer_size` | int | `131072` | `--upload-buffer-size` | `NEGHAB_UPLOAD_BUFFER_SIZE` |
| `upload_targets` | []string | `[]` | `--upload-targets` | `NEGHAB_UPLOAD_TARGETS` |
| `dns_resolvers` | []string | `[8.8.8.8, 1.1.1.1]` | `--dns-resolvers` | `NEGHAB_DNS_RESOLVERS` |
| `min_tx` | int | `1024` | `--min-tx` | `NEGHAB_MIN_TX` |
| `min_target_tx` | int | `0` | `--min-target-tx` | `NEGHAB_MIN_TARGET_TX` |
| `smoothing` | float | `0.9` | `--smoothing` | `NEGHAB_SMOOTHING` |
| `verbose` | bool | `false` | `--verbose` | `NEGHAB_VERBOSE` |

---

## ⚙️ systemd Service

```bash
sudo systemctl status neghab
sudo journalctl -u neghab -f         # live logs
sudo systemctl restart neghab        # apply config changes
```

The included unit applies strict sandboxing: `NoNewPrivileges=true`, `ProtectSystem=strict`, `PrivateTmp=yes`, `PrivateDevices=true`, and only `CAP_NET_RAW` capability.

---

## 🔨 Building from Source

```bash
git clone https://github.com/sudosz/neghab.git
cd neghab

make build            # Build for current platform
make build-all        # Cross-compile linux/amd64 + linux/arm64
make test             # Run all tests
make lint             # golangci-lint static analysis
sudo make install     # Install to /usr/local/bin
```

---

## 📁 Project Structure

```
neghab/
├── main.go                    # Entry point, signal handling, startup banner
├── Makefile                   # Build, test, lint, release targets
├── .goreleaser.yaml           # GoReleaser multi-arch build config
│
├── config/
│   └── config.go              # Viper multi-source config (CLI, env, YAML, defaults)
│
├── controller/
│   └── controller.go          # Ratio enforcement engine with session-based cumulative gating
│
├── generator/
│   ├── generator.go           # Worker pool, job dispatch, target parsing, scenario wiring
│   ├── upload.go              # HTTP POST upload (pool, parallel streams, multi-server)
│   ├── download.go            # HTTP GET download (RX generator, shares upload pool)
│   ├── connpool.go            # Persistent TCP connection pool with pre-warming
│   ├── http_rst.go            # HTTP POST + RST abort (chunked 8KB writes, 80/20 body)
│   ├── udp.go                 # UDP dead-end (QUIC-like packet sizes, burst patterns)
│   ├── tcp_rst.go             # TCP RST/ACK raw socket flood (spoofed source IPs)
│   ├── dns.go                 # DNS query spam (persistent UDP, round-robin resolvers)
│   └── mixer.go               # Scenario rotation with random start
│
├── monitor/
│   └── monitor.go             # /proc/net/dev interface stats (deltas + absolute counters)
│
├── logger/
│   └── logger.go              # ANSI-colored slog handler with journald compat
│
├── humanize/
│   └── humanize.go            # IEC byte formatting (B, KiB, MiB, GiB, TiB)
│
├── deploy/
│   ├── neghab.service         # Hardened systemd unit
│   ├── neghab.yaml            # Annotated example config with all scenarios
│   └── install.sh             # One-line installer (arch detection, systemd setup)
│
└── .github/workflows/
    └── release.yml            # CI: test → lint → goreleaser → GitHub Release
```

---

## 🔒 Security

Neghab requires root to read `/proc/net/dev` and open raw sockets. The systemd service enforces:

- **Least privilege** — only `CAP_NET_RAW` granted; all other capabilities dropped
- **Read-only filesystem** — `ProtectSystem=strict` with read-only `/etc` exceptions
- **No privilege escalation** — `NoNewPrivileges=true`
- **No external dependencies** — statically-linked binary, no shared libraries, no runtime downloads
- **No user input in payloads** — all traffic body content is internally generated; no injection surface

---

## ⚠️ Legal Disclaimer

**This tool is for educational and research purposes on networks you own** or have explicit written permission to test.

- **ISP Terms of Service** — Traffic profile manipulation may violate your ISP's ToS
- **Local Laws** — Verify your jurisdiction's regulations on network traffic manipulation
- **Authorized Targets Only** — Only send traffic to IPs you own or have permission to target

**The authors assume no liability for misuse. You are solely responsible for compliance with all applicable laws.**

---

## 📄 License

MIT — see [LICENSE](LICENSE) for details.

---

<p align="center">
  <sub>Built with ❤️ by <a href="https://github.com/sudosz">sudosz</a></sub>
</p>
