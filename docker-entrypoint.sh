#!/bin/sh
set -e

# Resolve the config path. CLI_PROXY_CONFIG_FILE lets you override (Dokploy can
# point this at /CLIProxyAPI/config/config.yaml on a persistent volume so edits
# made via the management UI survive container recreation).
CONFIG_FILE="${CLI_PROXY_CONFIG_FILE:-/CLIProxyAPI/config.yaml}"
CONFIG_DIR=$(dirname "$CONFIG_FILE")

mkdir -p "$CONFIG_DIR" /CLIProxyAPI/auths /CLIProxyAPI/logs

# Seed config on first start. Idempotent — never overwrites an existing file.
if [ ! -f "$CONFIG_FILE" ]; then
  if [ -f /CLIProxyAPI/config.production.yaml ]; then
    cp /CLIProxyAPI/config.production.yaml "$CONFIG_FILE"
    echo "[entrypoint] seeded $CONFIG_FILE from config.production.yaml"
  elif [ -f /CLIProxyAPI/config.example.yaml ]; then
    cp /CLIProxyAPI/config.example.yaml "$CONFIG_FILE"
    echo "[entrypoint] seeded $CONFIG_FILE from config.example.yaml"
  fi
fi

# Make /CLIProxyAPI/config.yaml resolve to the real (possibly volume-backed)
# file so the binary's default lookup keeps working without a -config flag.
if [ "$CONFIG_FILE" != "/CLIProxyAPI/config.yaml" ]; then
  ln -sf "$CONFIG_FILE" /CLIProxyAPI/config.yaml
fi

exec "$@"
