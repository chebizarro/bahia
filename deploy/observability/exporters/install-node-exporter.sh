#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <listen-address>" >&2
  exit 64
fi

listen_address=$1
if [[ ! $listen_address =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
  echo "listen-address must be an IPv4 address" >&2
  exit 64
fi
if ! ip -4 address show | grep -Fq " $listen_address/"; then
  echo "listen-address is not assigned to this host: $listen_address" >&2
  exit 65
fi

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends prometheus-node-exporter

config_tmp=$(mktemp)
trap 'rm -f "$config_tmp"' EXIT
printf 'ARGS="--web.listen-address=%s:9100"\n' "$listen_address" >"$config_tmp"
install -o root -g root -m 0644 "$config_tmp" /etc/default/prometheus-node-exporter

systemctl enable prometheus-node-exporter
systemctl restart prometheus-node-exporter
systemctl is-active --quiet prometheus-node-exporter
curl --fail --silent --show-error --retry 5 --retry-connrefused --retry-delay 1 --max-time 10 "http://$listen_address:9100/metrics" >/dev/null
