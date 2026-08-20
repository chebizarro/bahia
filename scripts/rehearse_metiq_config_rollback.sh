#!/bin/sh
set -eu

if [ "$#" -ne 2 ]; then
  printf '%s\n' 'usage: rehearse_metiq_config_rollback.sh <captured-prior-config> <rendered-enabled-config>' >&2
  exit 64
fi

prior=$1
enabled=$2
work_dir=$(mktemp -d)
cleanup() {
  rm -rf -- "$work_dir"
}
trap cleanup EXIT INT TERM
umask 077

digest() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

test -f "$prior"
test -f "$enabled"
prior_digest=$(digest "$prior")
enabled_digest=$(digest "$enabled")
if [ "$prior_digest" = "$enabled_digest" ]; then
  printf '%s\n' 'enabled config must differ from captured prior config' >&2
  exit 1
fi

install -m 0600 "$prior" "$work_dir/active.yaml"
install -m 0600 "$enabled" "$work_dir/active.yaml"
test "$(digest "$work_dir/active.yaml")" = "$enabled_digest"
install -m 0600 "$prior" "$work_dir/active.yaml"
restored_digest=$(digest "$work_dir/active.yaml")
test "$restored_digest" = "$prior_digest"
cmp -s "$prior" "$work_dir/active.yaml"

printf '{"prior_config_digest":"sha256:%s","enabled_config_digest":"sha256:%s","restored_config_digest":"sha256:%s","rehearsed":true}\n' \
  "$prior_digest" "$enabled_digest" "$restored_digest"
