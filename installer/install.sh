#!/usr/bin/env bash
# MCP Gateway installer for Linux and macOS.
# Downloads pre-built binaries from GitHub Releases with checksum verification,
# installs to ~/.local/bin, and registers a user-level service.
#
# Security note: checksums provide integrity verification (download corruption).
# Release checksums are signed with Sigstore cosign (keyless). To verify:
#   cosign verify-blob --bundle checksums.txt.bundle \
#     --certificate-identity-regexp 'https://github.com/blicksten/mcp-gateway/.github/workflows/release.yml@refs/tags/v.*' \
#     --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' checksums.txt
# For production deployments, pin a version: MCP_GATEWAY_VERSION=v1.0.0 ./install.sh
set -euo pipefail

# Guard against unset HOME
[ -n "${HOME:-}" ] || { echo "ERROR: HOME is unset"; exit 1; }

# Check required dependencies
for dep in curl tar; do
    command -v "$dep" >/dev/null 2>&1 || { echo "ERROR: Required tool '$dep' not found."; exit 1; }
done

REPO="github.com/blicksten/mcp-gateway"
INSTALL_DIR="$HOME/.local/bin"
CONFIG_DIR="$HOME/.mcp-gateway"

# --- Detect OS and architecture ---
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
    linux)  OS="linux" ;;
    darwin) OS="darwin" ;;
    *)      echo "ERROR: Unsupported OS: $OS"; exit 1 ;;
esac

ARCH="$(uname -m)"
case "$ARCH" in
    x86_64|amd64)   ARCH="amd64" ;;
    aarch64|arm64)   ARCH="arm64" ;;
    *)               echo "ERROR: Unsupported architecture: $ARCH (only amd64 and arm64 are supported)"; exit 1 ;;
esac

# --- Determine latest version ---
VERSION="${MCP_GATEWAY_VERSION:-latest}"
if [ "$VERSION" = "latest" ]; then
    VERSION="$(curl -fsSL "https://api.${REPO%/*}/repos/${REPO#*/}/releases/latest" \
        | grep '"tag_name"' | sed -E 's/.*"tag_name":\s*"([^"]+)".*/\1/')"
    if [ -z "$VERSION" ]; then
        echo "ERROR: Could not determine latest version."
        exit 1
    fi
fi
echo "Installing mcp-gateway $VERSION for $OS/$ARCH..."

# --- Download binary archive and checksums ---
ARCHIVE_NAME="mcp-gateway_${VERSION#v}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://${REPO}/releases/download/${VERSION}"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

echo "Downloading $ARCHIVE_NAME..."
curl -fsSL --proto '=https' -o "$WORK_DIR/$ARCHIVE_NAME" "$BASE_URL/$ARCHIVE_NAME"
curl -fsSL --proto '=https' -o "$WORK_DIR/checksums.txt" "$BASE_URL/checksums.txt"

# --- Checksum verification (MANDATORY) ---
echo "Verifying checksum..."
EXPECTED="$(grep -F "$ARCHIVE_NAME" "$WORK_DIR/checksums.txt" | awk '{print $1}')"
if [ -z "$EXPECTED" ]; then
    echo "ERROR: Archive not found in checksums.txt"
    exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
    ACTUAL="$(sha256sum "$WORK_DIR/$ARCHIVE_NAME" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
    ACTUAL="$(shasum -a 256 "$WORK_DIR/$ARCHIVE_NAME" | awk '{print $1}')"
else
    echo "ERROR: Neither sha256sum nor shasum found. Cannot verify checksum."
    exit 1
fi

if [ "$EXPECTED" != "$ACTUAL" ]; then
    echo "ERROR: Checksum mismatch!"
    echo "  Expected: $EXPECTED"
    echo "  Actual:   $ACTUAL"
    exit 1
fi
echo "Checksum OK."

# --- Extract and install ---
mkdir -p "$INSTALL_DIR"
tar -xzf "$WORK_DIR/$ARCHIVE_NAME" -C "$WORK_DIR"
cp "$WORK_DIR/mcp-gateway" "$INSTALL_DIR/mcp-gateway"
cp "$WORK_DIR/mcp-ctl" "$INSTALL_DIR/mcp-ctl"
chmod 755 "$INSTALL_DIR/mcp-gateway" "$INSTALL_DIR/mcp-ctl"

# --- PATH idempotency ---
PATH_LINE='export PATH="$HOME/.local/bin:$PATH"'
for rc in "$HOME/.bashrc" "$HOME/.zshrc"; do
    if [ -f "$rc" ] && ! grep -qxF "$PATH_LINE" "$rc"; then
        echo "$PATH_LINE" >> "$rc"
        echo "Added ~/.local/bin to PATH in $(basename "$rc")"
    fi
done

# --- Default config ---
if [ ! -f "$CONFIG_DIR/config.json" ]; then
    mkdir -p "$CONFIG_DIR"
    cat > "$CONFIG_DIR/config.json" << 'CONF'
{
  "backends": [],
  "listen": "127.0.0.1:8100"
}
CONF
    chmod 600 "$CONFIG_DIR/config.json"
    echo "Created default config at $CONFIG_DIR/config.json"
fi

# --- Service registration ---
case "$OS" in
    darwin)
        PLIST_DIR="$HOME/Library/LaunchAgents"
        PLIST="$PLIST_DIR/dev.mcp-gateway.plist"
        mkdir -p "$PLIST_DIR"
        # Unload existing service before overwriting plist
        if [ -f "$PLIST" ]; then
            launchctl unload "$PLIST" 2>/dev/null || true
        fi
        cat > "$PLIST" << PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>dev.mcp-gateway</string>
    <key>ProgramArguments</key>
    <array>
        <string>${INSTALL_DIR}/mcp-gateway</string>
        <string>-config</string>
        <string>${CONFIG_DIR}/config.json</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>${CONFIG_DIR}/gateway.log</string>
    <key>StandardErrorPath</key>
    <string>${CONFIG_DIR}/gateway.err</string>
</dict>
</plist>
PLIST
        echo "Created LaunchAgent at $PLIST"
        echo "  Start: launchctl load $PLIST"
        echo "  Stop:  launchctl unload $PLIST"
        ;;
    linux)
        SERVICE_DIR="$HOME/.config/systemd/user"
        SERVICE="$SERVICE_DIR/mcp-gateway.service"
        mkdir -p "$SERVICE_DIR"
        cat > "$SERVICE" << SERVICE
[Unit]
Description=MCP Gateway daemon
After=network.target

[Service]
ExecStart=${INSTALL_DIR}/mcp-gateway -config ${CONFIG_DIR}/config.json
Restart=always
RestartSec=5
StandardOutput=append:${CONFIG_DIR}/gateway.log
StandardError=append:${CONFIG_DIR}/gateway.err

[Install]
WantedBy=default.target
SERVICE
        systemctl --user daemon-reload 2>/dev/null || true
        echo "Created systemd user service at $SERVICE"
        echo "  Start:  systemctl --user start mcp-gateway"
        echo "  Enable: systemctl --user enable mcp-gateway"
        echo "  Logs:   journalctl --user -u mcp-gateway"
        ;;
esac

# --- Summary ---
echo ""
echo "=== MCP Gateway $VERSION installed ==="
echo "  Binaries: $INSTALL_DIR/mcp-gateway, $INSTALL_DIR/mcp-ctl"
echo "  Config:   $CONFIG_DIR/config.json"
if [ "$OS" = "darwin" ]; then
    echo ""
    echo "NOTE: macOS Gatekeeper may block unsigned binaries. If so, run:"
    echo "  xattr -d com.apple.quarantine $INSTALL_DIR/mcp-gateway"
    echo "  xattr -d com.apple.quarantine $INSTALL_DIR/mcp-ctl"
fi
