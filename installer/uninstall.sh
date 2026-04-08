#!/usr/bin/env bash
# MCP Gateway uninstaller for Linux and macOS.
# Stops the service, removes binaries and service registration.
# Config directory is preserved.
set -euo pipefail

INSTALL_DIR="$HOME/.local/bin"
CONFIG_DIR="$HOME/.mcp-gateway"
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"

# --- Stop and remove service ---
case "$OS" in
    darwin)
        PLIST="$HOME/Library/LaunchAgents/dev.mcp-gateway.plist"
        if [ -f "$PLIST" ]; then
            launchctl unload "$PLIST" 2>/dev/null || true
            rm -f "$PLIST"
            echo "Removed LaunchAgent."
        fi
        ;;
    linux)
        if systemctl --user is-active mcp-gateway >/dev/null 2>&1; then
            systemctl --user stop mcp-gateway
        fi
        if systemctl --user is-enabled mcp-gateway >/dev/null 2>&1; then
            systemctl --user disable mcp-gateway
        fi
        SERVICE="$HOME/.config/systemd/user/mcp-gateway.service"
        if [ -f "$SERVICE" ]; then
            rm -f "$SERVICE"
            systemctl --user daemon-reload 2>/dev/null || true
            echo "Removed systemd user service."
        fi
        ;;
esac

# --- Remove binaries ---
for bin in mcp-gateway mcp-ctl; do
    if [ -f "$INSTALL_DIR/$bin" ]; then
        rm -f "$INSTALL_DIR/$bin"
        echo "Removed $INSTALL_DIR/$bin"
    fi
done

# --- Summary ---
echo ""
echo "=== MCP Gateway uninstalled ==="
echo "Config preserved at $CONFIG_DIR/"
echo "To remove config: rm -rf $CONFIG_DIR"
