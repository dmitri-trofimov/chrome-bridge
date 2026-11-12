#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_DIR="${HOME}/.local/bin"
LIB_DIR="${HOME}/.local/lib/chrome-bridge"
SYSTEMD_DIR="${HOME}/.config/systemd/user"

echo "==> Building chrome-bridge..."
cd "${SCRIPT_DIR}"
mkdir -p dist
GOOS=linux GOARCH=amd64 go build -o dist/chrome-bridge .
GOOS=windows GOARCH=amd64 go build -o dist/chrome-bridge.exe .

echo "==> Running tests..."
go test ./...

echo "==> Installing binaries..."
mkdir -p "${BIN_DIR}" "${LIB_DIR}"
install -m 755 dist/chrome-bridge "${BIN_DIR}/chrome-bridge"
install -m 755 dist/chrome-bridge.exe "${LIB_DIR}/chrome-bridge.exe"
install -m 755 dist/chrome-bridge.exe "${BIN_DIR}/chrome-bridge.exe"

echo "==> Cleaning up any legacy winpipe service..."
if systemctl --user is-active --quiet winpipe.service 2>/dev/null; then
    systemctl --user stop winpipe.service
fi
if systemctl --user is-enabled --quiet winpipe.service 2>/dev/null; then
    systemctl --user disable winpipe.service
fi
rm -f "${SYSTEMD_DIR}/winpipe.service"
rm -f "${BIN_DIR}/winpipe-bridge"

echo "==> Installing systemd user service..."
mkdir -p "${SYSTEMD_DIR}"
cat > "${SYSTEMD_DIR}/chrome-bridge.service" <<EOF
[Unit]
Description=Chrome DevTools Bridge (WSL to Windows)
After=network.target

[Service]
Type=simple
ExecStart=${BIN_DIR}/chrome-bridge
Restart=always
RestartSec=3
TimeoutStopSec=5

[Install]
WantedBy=default.target
EOF

systemctl --user daemon-reload
systemctl --user enable --now chrome-bridge.service

echo ""
echo "==> Installation complete! Service status:"
systemctl --user status chrome-bridge.service --no-pager
