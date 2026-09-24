#!/bin/sh
set -eu

INDEX_HTML=/usr/share/nginx/html/index.html

if [ ! -f "$INDEX_HTML" ]; then
  exit 0
fi

# Build-time substitution already supplied bootstrap configuration.
if ! grep -Eq '__PUBLIC_BAHIA_BOOTSTRAP_RELAYS__|__PUBLIC_BAHIA_SERVICE_PUBKEYS__' "$INDEX_HTML"; then
  exit 0
fi

BOOT_RELAYS="${PUBLIC_BAHIA_BOOTSTRAP_RELAYS:-}"
SERVICE_PUBKEYS="${PUBLIC_BAHIA_SERVICE_PUBKEYS:-${PUBLIC_BAHIA_SERVICE_PUBKEY:-}}"

if [ -z "$BOOT_RELAYS" ] || [ -z "$SERVICE_PUBKEYS" ]; then
  echo "bahia-web bootstrap env missing: PUBLIC_BAHIA_BOOTSTRAP_RELAYS and PUBLIC_BAHIA_SERVICE_PUBKEYS must be set" >&2
  exit 1
fi

BOOT_RELAYS_ESCAPED=$(printf '%s' "$BOOT_RELAYS" | sed 's/[\\&|]/\\&/g')
SERVICE_PUBKEYS_ESCAPED=$(printf '%s' "$SERVICE_PUBKEYS" | sed 's/[\\&|]/\\&/g')

TEMP_INDEX=$(mktemp "${INDEX_HTML}.XXXXXX")
trap 'rm -f "$TEMP_INDEX"' 0

sed \
  -e "s|__PUBLIC_BAHIA_BOOTSTRAP_RELAYS__|$BOOT_RELAYS_ESCAPED|g" \
  -e "s|__PUBLIC_BAHIA_SERVICE_PUBKEYS__|$SERVICE_PUBKEYS_ESCAPED|g" \
  "$INDEX_HTML" > "$TEMP_INDEX"
cat "$TEMP_INDEX" > "$INDEX_HTML"
