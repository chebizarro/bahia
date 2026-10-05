#!/bin/sh
set -eu
set -f

SEED_SCRIPT=${BAHIA_BOOTSTRAP_SCRIPT_PATH:-/usr/share/nginx/html/bahia-bootstrap.js}
BOOT_RELAYS=${PUBLIC_BAHIA_BOOTSTRAP_RELAYS:-}
SERVICE_PUBKEYS=${PUBLIC_BAHIA_SERVICE_PUBKEYS:-${PUBLIC_BAHIA_SERVICE_PUBKEY:-}}

if [ -z "$BOOT_RELAYS" ] || [ -z "$SERVICE_PUBKEYS" ]; then
  echo 'bahia-web bootstrap env missing: PUBLIC_BAHIA_BOOTSTRAP_RELAYS and PUBLIC_BAHIA_SERVICE_PUBKEYS must be set' >&2
  exit 1
fi

seed_array() {
  values=$1
  kind=$2
  case "$values" in ,*|*,|*,,*) echo "bahia-web invalid $kind: empty list entry" >&2; return 1;; esac
  old_ifs=$IFS
  IFS=,
  first=1
  for value in $values; do
    value=$(printf '%s' "$value" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')
    if [ "$(printf '%s' "$value" | tr -d '\r\n')" != "$value" ]; then
      echo "bahia-web invalid $kind: control character" >&2; IFS=$old_ifs; return 1
    fi
    if [ "$kind" = relay ]; then
      if ! printf '%s\n' "$value" | grep -Eq '^wss?://([A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\])(:[0-9]+)?(/[A-Za-z0-9._~:/?&=%+@#-]*)?$'; then
        echo "bahia-web invalid relay URL: $value" >&2; IFS=$old_ifs; return 1
      fi
    elif ! printf '%s\n' "$value" | grep -Eiq '^[0-9a-f]{64}$'; then
      echo "bahia-web invalid service pubkey: $value" >&2; IFS=$old_ifs; return 1
    fi
    if [ "$first" -eq 0 ]; then printf ','; fi
    first=0
    printf '"%s"' "$value"
  done
  IFS=$old_ifs
}

# Validate both lists before replacing the existing script, so a failed restart
# cannot leave a partial seed behind.
RELAYS_JSON=$(seed_array "$BOOT_RELAYS" relay)
KEYS_JSON=$(seed_array "$SERVICE_PUBKEYS" pubkey)
TEMP_SCRIPT=$(mktemp "${SEED_SCRIPT}.XXXXXX")
trap 'rm -f "$TEMP_SCRIPT"' EXIT
printf 'window.__BAHIA_BOOTSTRAP__ = {schema:"bahia.bootstrap.v1",relay_urls:[%s],service_pubkeys:[%s]};\n' "$RELAYS_JSON" "$KEYS_JSON" > "$TEMP_SCRIPT"
mv "$TEMP_SCRIPT" "$SEED_SCRIPT"
