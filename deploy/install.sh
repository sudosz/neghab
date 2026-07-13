#!/bin/bash
#
# Neghab — One-Line Installation Script
#
# Usage:
#   curl -sSL https://raw.githubusercontent.com/sudosz/neghab/main/deploy/install.sh | sudo bash
#
# This script:
#   1. Detects the system architecture
#   2. Downloads the latest Neghab release binary from GitHub
#   3. Installs it to /usr/local/bin
#   4. Creates a systemd service unit
#   5. Enables and starts the service
#
set -euo pipefail

# ── Colors ────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
BOLD='\033[1m'
DIM='\033[2m'
NC='\033[0m' # No Color

log_info()  { echo -e "${BLUE}[*]${NC} $1"; }
log_ok()    { echo -e "${GREEN}[+]${NC} $1"; }
log_warn()  { echo -e "${YELLOW}[!]${NC} $1"; }
log_error() { echo -e "${RED}[-]${NC} $1" >&2; }

# ── Configuration ─────────────────────────────────────────────────
REPO="sudosz/neghab"
BINARY="neghab"
INSTALL_DIR="/usr/local/bin"
SERVICE_FILE="/etc/systemd/system/${BINARY}.service"
CONFIG_DIR="/etc/neghab"
CONFIG_FILE="${CONFIG_DIR}/config.yaml"

# ── Banner ────────────────────────────────────────────────────────
echo ""
echo -e "${CYAN}  ███╗   ██╗███████╗ ██████╗ ██╗  ██╗ █████╗ ██████╗  ${NC}"
echo -e "${CYAN}  ████╗  ██║██╔════╝██╔════╝ ██║  ██║██╔══██╗██╔══██╗ ${NC}"
echo -e "${CYAN}  ██╔██╗ ██║█████╗  ██║  ███╗███████║███████║██████╔╝ ${NC}"
echo -e "${CYAN}  ██║╚██╗██║██╔══╝  ██║   ██║██╔══██║██╔══██║██╔══██╗ ${NC}"
echo -e "${CYAN}  ██║ ╚████║███████╗╚██████╔╝██║  ██║██║  ██║██████╔╝ ${NC}"
echo -e "${CYAN}  ╚═╝  ╚═══╝╚══════╝ ╚═════╝ ╚═╝  ╚═╝╚═╝  ╚═╝╚═════╝  ${NC}"
echo ""
echo -e "${DIM}  Advanced Network Traffic Simulator — Installer${NC}"
echo ""

# ── Root Check ────────────────────────────────────────────────────
if [[ $EUID -ne 0 ]]; then
    log_error "This script must be run as root. Please use sudo."
    exit 1
fi

# ── Detect Architecture ────────────────────────────────────────────
ARCH=$(uname -m)
case "$ARCH" in
    x86_64|amd64)
        ARCH="amd64"
        ;;
    aarch64|arm64)
        ARCH="arm64"
        ;;
    *)
        log_error "Unsupported architecture: $ARCH"
        exit 1
        ;;
esac
log_info "Detected architecture: ${BOLD}${ARCH}${NC}"

# ── Download Binary ───────────────────────────────────────────────
log_info "Downloading latest ${BINARY} binary for linux/${ARCH}..."
DOWNLOAD_URL="https://github.com/${REPO}/releases/latest/download/${BINARY}-linux-${ARCH}"

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

if command -v curl &> /dev/null; then
    curl -fsSL "$DOWNLOAD_URL" -o "${TMP_DIR}/${BINARY}"
elif command -v wget &> /dev/null; then
    wget -q "$DOWNLOAD_URL" -O "${TMP_DIR}/${BINARY}"
else
    log_error "Neither curl nor wget found. Please install one of them."
    exit 1
fi

chmod +x "${TMP_DIR}/${BINARY}"

# ── Verify Binary ─────────────────────────────────────────────────
if ! file "${TMP_DIR}/${BINARY}" | grep -q "ELF"; then
    log_error "Downloaded file is not a valid ELF binary."
    log_info "Check that a release exists at: https://github.com/${REPO}/releases"
    exit 1
fi

log_ok "Binary verified"

# ── Install Binary ────────────────────────────────────────────────
log_info "Installing binary to ${INSTALL_DIR}..."
mv "${TMP_DIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
log_ok "Binary installed: ${INSTALL_DIR}/${BINARY}"

# ── Create Config Directory ───────────────────────────────────────
log_info "Creating configuration directory..."
mkdir -p "${CONFIG_DIR}"

# ── Create Default Config ─────────────────────────────────────────
if [[ ! -f "${CONFIG_FILE}" ]]; then
    log_info "Creating default YAML configuration..."
    cat > "${CONFIG_FILE}" << 'YAMLEOF'
# Neghab Configuration
# See: https://github.com/sudosz/neghab
#
# Priority: CLI flags > NEGHAB_* env vars > this config file > defaults

interface: eth0
ratio: 0.1
interval: 500ms
scenario: udp
target: 192.168.1.254
port: 443
verbose: false
workers: 4
mix_interval: 0s
http_host: example.com
dns_resolvers:
  - 8.8.8.8
  - 1.1.1.1
min_tx: 1024
min_target_tx: 0
smoothing: 0.9
YAMLEOF
    log_ok "Default config created: ${CONFIG_FILE}"
fi

# ── Create systemd Service ────────────────────────────────────────
log_info "Setting up systemd service..."

# Detect the primary network interface
DEFAULT_IFACE=$(ip route show default 2>/dev/null | awk '{print $5}' | head -1)
if [[ -z "$DEFAULT_IFACE" ]]; then
    DEFAULT_IFACE="eth0"
    log_warn "Could not detect default interface, using 'eth0'"
else
    log_info "Detected default interface: ${BOLD}${DEFAULT_IFACE}${NC}"
fi

cat > "${SERVICE_FILE}" << SERVICEEOF
[Unit]
Description=Neghab — Advanced Network Traffic Simulator
Documentation=https://github.com/sudosz/neghab
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=60
StartLimitBurst=3

[Service]
Type=simple
User=root
Group=root
ExecStart=${INSTALL_DIR}/${BINARY} --config ${CONFIG_FILE}
Environment=NO_COLOR=1
Restart=always
RestartSec=10
CPUQuota=50%
MemoryMax=256M
MemoryHigh=128M
TasksMax=32
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=no
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_NETLINK AF_UNIX
RestrictNamespaces=yes
LockPersonality=yes
RestrictRealtime=yes
CapabilityBoundingSet=CAP_NET_RAW
AmbientCapabilities=CAP_NET_RAW
StandardOutput=journal
StandardError=journal
SyslogIdentifier=neghab
KillSignal=SIGTERM
TimeoutStopSec=10

[Install]
WantedBy=multi-user.target
SERVICEEOF

log_ok "Service file created: ${SERVICE_FILE}"

# ── Enable and Start Service ──────────────────────────────────────
log_info "Enabling and starting service..."
systemctl daemon-reload
systemctl enable "${BINARY}" 2>/dev/null || true
systemctl start "${BINARY}" 2>/dev/null || true

# Wait for service to start
sleep 2
if systemctl is-active --quiet "${BINARY}" 2>/dev/null; then
    log_ok "Service is running!"
else
    log_warn "Service may not have started. Check: sudo systemctl status ${BINARY}"
fi

# ── Summary ───────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}${BOLD}════════════════════════════════════════════════${NC}"
echo -e "${GREEN}${BOLD}  Neghab Installation Complete!${NC}"
echo -e "${GREEN}${BOLD}════════════════════════════════════════════════${NC}"
echo ""
echo -e "  ${DIM}Binary:${NC}  ${INSTALL_DIR}/${BINARY}"
echo -e "  ${DIM}Config:${NC}  ${CONFIG_FILE}"
echo -e "  ${DIM}Service:${NC}  ${BINARY}"
echo -e "  ${DIM}Env vars:${NC} NEGHAB_INTERFACE NEGHAB_RATIO NEGHAB_SCENARIO ..."
echo ""
echo -e "  ${BOLD}Commands:${NC}"
echo -e "    ${CYAN}Check status${NC}    systemctl status ${BINARY}"
echo -e "    ${CYAN}View logs${NC}       journalctl -u ${BINARY} -f"
echo -e "    ${CYAN}Stop service${NC}    systemctl stop ${BINARY}"
echo -e "    ${CYAN}Start service${NC}   systemctl start ${BINARY}"
echo -e "    ${CYAN}Manual run${NC}      sudo ${BINARY} --interface ${DEFAULT_IFACE} --ratio 0.1"
echo ""
log_warn "Edit ${CONFIG_FILE} or set NEGHAB_* env vars to match your network setup."
log_info "Installation finished."
echo ""
