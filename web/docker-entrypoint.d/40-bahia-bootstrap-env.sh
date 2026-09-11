#!/bin/sh
# Substitute the discovery bootstrap seed into the prebuilt SPA shell at
# container start (web/src/app.html carries __PUBLIC_BAHIA_*__ placeholders).
#
# Inputs (runtime environment):
#   PUBLIC_BAHIA_BOOTSTRAP_RELAYS   comma-separated relay URLs (required)
#   PUBLIC_BAHIA_SERVICE_PUBKEYS    comma-separated trusted service pubkeys
#                                   (PUBLIC_BAHIA_SERVICE_PUBKEY accepted too)
#   BAHIA_WEB_SERVICE_PUBKEY_NIP11_URL
#                                   optional, local-stack only: when no pubkey
#                                   is configured, read the `pubkey` field of the
#                                   Bahia relay sidecar's NIP-11 document at this
#                                   URL (e.g. http://relay:3334/relay). The
#                                   sidecar derives it from BAHIA_NOSTR_PRIVATE_KEY.
#                                   Leave unset in production so the trust anchor
#                                   is always pinned explicitly.
#   BAHIA_WEB_SERVICE_PUBKEY_NIP11_ATTEMPTS  NIP-11 fetch attempts (default 30,
#                                   one second apart).
#
# The script fails closed (exit 1, which aborts the nginx entrypoint) when the
# placeholders are present and no relay or pubkey can be resolved. Once
# substituted, restarts are a no-op.
set -eu

INDEX_HTML=/usr/share/nginx/html/index.html

if [ ! -f "$INDEX_HTML" ]; then
  exit 0
fi

if ! grep -q '__PUBLIC_BAHIA_BOOTSTRAP_RELAYS__\|__PUBLIC_BAHIA_SERVICE_PUBKEYS__' "$INDEX_HTML"; then
  echo "bahia-web bootstrap: seed already substituted in $INDEX_HTML; skipping" >&2
  exit 0
fi

BOOT_RELAYS="${PUBLIC_BAHIA_BOOTSTRAP_RELAYS:-}"
SERVICE_PUBKEYS="${PUBLIC_BAHIA_SERVICE_PUBKEYS:-${PUBLIC_BAHIA_SERVICE_PUBKEY:-}}"
NIP11_URL="${BAHIA_WEB_SERVICE_PUBKEY_NIP11_URL:-}"

if [ -z "$SERVICE_PUBKEYS" ] && [ -n "$NIP11_URL" ]; then
  attempts="${BAHIA_WEB_SERVICE_PUBKEY_NIP11_ATTEMPTS:-30}"
  i=0
  while [ "$i" -lt "$attempts" ]; do
    i=$((i + 1))
    doc=$(wget -q -T 3 -O - --header='Accept: application/nostr+json' "$NIP11_URL" 2>/dev/null || true)
    pubkey=$(printf '%s' "$doc" | tr -d '\n' | sed -n 's/.*"pubkey"[[:space:]]*:[[:space:]]*"\([0-9a-fA-F]\{64\}\)".*/\1/p')
    if [ -n "$pubkey" ]; then
      SERVICE_PUBKEYS=$(printf '%s' "$pubkey" | tr 'A-F' 'a-f')
      echo "bahia-web bootstrap: using service pubkey $SERVICE_PUBKEYS from NIP-11 at $NIP11_URL" >&2
      break
    fi
    sleep 1
  done
  if [ -z "$SERVICE_PUBKEYS" ]; then
    echo "bahia-web bootstrap: no pubkey in NIP-11 document at $NIP11_URL after $attempts attempts" >&2
  fi
fi

if [ -z "$BOOT_RELAYS" ] || [ -z "$SERVICE_PUBKEYS" ]; then
  echo "bahia-web bootstrap env missing: PUBLIC_BAHIA_BOOTSTRAP_RELAYS and PUBLIC_BAHIA_SERVICE_PUBKEYS must be set (or BAHIA_WEB_SERVICE_PUBKEY_NIP11_URL must resolve a relay pubkey)" >&2
  exit 1
fi

BOOT_RELAYS_ESCAPED=$(printf '%s' "$BOOT_RELAYS" | sed 's/[\\&|]/\\&/g')
SERVICE_PUBKEYS_ESCAPED=$(printf '%s' "$SERVICE_PUBKEYS" | sed 's/[\\&|]/\\&/g')

sed -i \
  -e "s|__PUBLIC_BAHIA_BOOTSTRAP_RELAYS__|$BOOT_RELAYS_ESCAPED|g" \
  -e "s|__PUBLIC_BAHIA_SERVICE_PUBKEYS__|$SERVICE_PUBKEYS_ESCAPED|g" \
  "$INDEX_HTML"
