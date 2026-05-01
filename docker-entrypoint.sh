#!/bin/sh
set -e

# Resolve the config path. Order of precedence:
#   1. CLI_PROXY_CONFIG_FILE env (explicit override, used by the dokploy compose)
#   2. /CLIProxyAPI/config.yaml as a regular file (bind-mounted from host)
#   3. /CLIProxyAPI/config.yaml as a directory (Docker auto-creates an empty
#      directory at the bind path when the host source doesn't exist — common
#      on Dokploy when the local docker-compose.yml binds ./config.yaml but the
#      git clone doesn't include it). In that case we put config.yaml *inside*.
#   4. Fall back to writing /CLIProxyAPI/config.yaml fresh.
CONFIG_FILE="${CLI_PROXY_CONFIG_FILE:-}"

if [ -z "$CONFIG_FILE" ]; then
  if [ -d /CLIProxyAPI/config.yaml ]; then
    CONFIG_FILE=/CLIProxyAPI/config.yaml/config.yaml
    echo "[entrypoint] /CLIProxyAPI/config.yaml is a directory — using $CONFIG_FILE inside it"
  else
    CONFIG_FILE=/CLIProxyAPI/config.yaml
  fi
fi

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

# When CLI_PROXY_API_KEYS is set (comma-separated), replace the api-keys list
# in the config. Makes the env var the source of truth — useful for Dokploy
# where you don't want secrets living in committed YAML.
if [ -n "${CLI_PROXY_API_KEYS:-}" ]; then
  if command -v yq >/dev/null 2>&1; then
    KEYS_YAML=$(printf '%s' "$CLI_PROXY_API_KEYS" | awk -v RS=',' '
      NR==1 { printf "[\"%s\"", $0; next }
      { printf ",\"%s\"", $0 }
      END { print "]" }')
    export KEYS_YAML
    yq -i '.api-keys = env(KEYS_YAML)' "$CONFIG_FILE"
    echo "[entrypoint] replaced api-keys from CLI_PROXY_API_KEYS env"
  else
    echo "[entrypoint] WARN: yq not installed; skipping CLI_PROXY_API_KEYS injection" >&2
  fi
fi

# Pass -config explicitly so we never rely on the default working-directory
# lookup (which fails when /CLIProxyAPI/config.yaml is a directory).
exec "$@" -config "$CONFIG_FILE"
