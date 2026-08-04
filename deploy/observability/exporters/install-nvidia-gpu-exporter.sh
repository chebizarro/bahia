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
if ! command -v nvidia-smi >/dev/null; then
  echo "nvidia-smi is required" >&2
  exit 69
fi

readonly exporter_version=1.13.1
readonly package_name="nvidia-gpu-exporter_${exporter_version}_linux_amd64.deb"
readonly package_sha256=ebe88ed4a5af816958898dd290b9c8bca67f9aa6d71fa3f732ecf503ae6e661f
readonly package_url="https://github.com/utkuozdemir/nvidia_gpu_exporter/releases/download/v${exporter_version}/${package_name}"

export DEBIAN_FRONTEND=noninteractive
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT
curl --fail --location --silent --show-error --output "$work_dir/$package_name" "$package_url"
printf '%s  %s\n' "$package_sha256" "$work_dir/$package_name" | sha256sum --check --strict
apt-get install -y "$work_dir/$package_name"

install -d -o root -g root -m 0755 /etc/systemd/system/nvidia_gpu_exporter.service.d
override_tmp=$(mktemp)
trap 'rm -rf "$work_dir"; rm -f "$override_tmp"' EXIT
cat >"$override_tmp" <<EOF
[Service]
ExecStart=
ExecStart=/usr/bin/nvidia_gpu_exporter --web.listen-address=${listen_address}:9835
EOF
install -o root -g root -m 0644 "$override_tmp" /etc/systemd/system/nvidia_gpu_exporter.service.d/listen-address.conf

systemctl daemon-reload
systemctl enable nvidia_gpu_exporter
systemctl restart nvidia_gpu_exporter
systemctl is-active --quiet nvidia_gpu_exporter
metrics_tmp=$(mktemp)
trap 'rm -rf "$work_dir"; rm -f "$override_tmp" "$metrics_tmp"' EXIT
curl --fail --silent --show-error --retry 5 --retry-connrefused --retry-delay 1 --max-time 15 --output "$metrics_tmp" "http://$listen_address:9835/metrics"
grep -q '^nvidia_smi_' "$metrics_tmp"
