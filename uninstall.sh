#!/usr/bin/env bash
set -euo pipefail

BIN_DIR="${HOME}/.local/bin"
LIB_DIR="${HOME}/.local/lib/chrome-bridge"
SYSTEMD_DIR="${HOME}/.config/systemd/user"

echo "==> Stopping and disabling chrome-bridge.service..."
if systemctl --user is-active --quiet chrome-bridge.service 2>/dev/null; then
    systemctl --user stop chrome-bridge.service
fi
if systemctl --user is-enabled --quiet chrome-bridge.service 2>/dev/null; then
    systemctl --user disable chrome-bridge.service
fi

rm -f "${SYSTEMD_DIR}/chrome-bridge.service"
systemctl --user daemon-reload

echo "==> Removing installed binaries..."
rm -f "${BIN_DIR}/chrome-bridge" "${BIN_DIR}/chrome-bridge.exe"
rm -rf "${LIB_DIR}"

echo "==> chrome-bridge uninstalled successfully."
