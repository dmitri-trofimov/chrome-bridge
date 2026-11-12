# chrome-bridge

Zero-configuration Chrome DevTools bridge from WSL to Windows over stdio.

Enables AI coding agents (such as Gemini CLI / Antigravity, Claude, Cursor) and developer tools running in WSL to seamlessly connect to your everyday Google Chrome browser on Windows without modifying shortcuts, changing firewall rules, or manually updating ports.

```text
WSL DevTools client (127.0.0.1:9222)
       │
       ▼
Linux chrome-bridge
       │
       ▼ (stdin/stdout via WSL interop)
Windows chrome-bridge.exe
       │
       ▼
Windows Chrome (dynamic ephemeral port)
```

## Features

- **Zero Arguments:** Run `chrome-bridge` or enable the systemd service. No flags or configuration required.
- **Port Auto-detection:** Dynamically discovers Windows Chrome's active DevTools port from `DevToolsActivePort`.
- **Dynamic Routing:** If Chrome restarts and picks a new port, `chrome-bridge` detects the change on the next connection without restarting the service.
- **Auto-Sync:** Keeps `~/.config/google-chrome/DevToolsActivePort` synchronized so MCP tools (`chrome-devtools-mcp`), Puppeteer, and Playwright connect automatically.

## Quick Start / Installation

Clone the repository and run the install script:

```bash
git clone https://github.com/dmitri-trofimov/chrome-bridge.git
cd chrome-bridge
./install.sh
```

`install.sh` will:
1. Build both `dist/chrome-bridge` (Linux) and `dist/chrome-bridge.exe` (Windows).
2. Run test suites.
3. Install binaries to `~/.local/bin/` and `~/.local/lib/chrome-bridge/`.
4. Install, enable, and start the systemd user service `chrome-bridge.service`.

### Uninstallation

To remove the service and binaries:

```bash
./uninstall.sh
```

## How to Use with Chrome

1. In Windows Chrome, open `chrome://inspect/#remote-debugging` and ensure the remote debugging server is running.
2. Any WSL tool connecting to `127.0.0.1:9222` (or using `chrome-devtools-mcp --autoConnect`) connects directly to your active Windows Chrome tabs.
3. Chrome may display a one-time approval prompt: click **Allow**.

## Manual Build & Run

```bash
# Build
GOOS=linux GOARCH=amd64 go build -o dist/chrome-bridge .
GOOS=windows GOARCH=amd64 go build -o dist/chrome-bridge.exe .

# Run directly
./dist/chrome-bridge
```
