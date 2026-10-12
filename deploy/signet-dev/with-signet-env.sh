#!/bin/sh
# Dev compose only: hands the disposable Signet's pubkey and the requester key
# that deploy/signet-dev wrote to the shared volume to a Bahia process, then
# runs it. The requester key is copied into an owner-only file, as
# secret_store.signet.client_key_file requires.
set -eu
umask 077
cat /run/signet-dev/requester.key > /tmp/bahia-secret-store.key
BAHIA_SECRET_STORE_SIGNET_PUBKEY=$(cat /run/signet-dev/signet.pub)
export BAHIA_SECRET_STORE_SIGNET_PUBKEY
exec "$@"
