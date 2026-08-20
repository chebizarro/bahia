# WS6 host exporters

Bahia fleet semantics come from signed Nostr observables. Direct scraping is restricted to machine and process facts that do not have useful domain-event meaning. This rollout installs:

- Ubuntu's `prometheus-node-exporter` package for filesystem, inode, memory, CPU, and host pressure.
- The checksum-pinned `utkuozdemir/nvidia_gpu_exporter` release on Lemmy for NVIDIA device, utilization, temperature, and VRAM facts.

Listeners bind only to each host's monitoring/Infra address. They do not bind to wildcard, Home, Compute, or public interfaces. The inventory is `deploy/observability/exporters/mvp-hosts.tsv`.

## Installation

Run the installer on each host as root, passing the address assigned to its Infra-facing interface:

```sh
sudo deploy/observability/exporters/install-node-exporter.sh <listen-address>
sudo deploy/observability/exporters/install-nvidia-gpu-exporter.sh <listen-address> # Lemmy only
```

The scripts are idempotent. They validate that the address exists locally, install or update the package, configure an explicit listener, restart the service, and require a successful local metrics scrape.

## Verification

From the approved Prometheus host, require HTTP 200 and representative source metrics:

```sh
curl --fail --max-time 10 http://<host>:9100/metrics | grep '^node_memory_MemAvailable_bytes'
curl --fail --max-time 15 http://<lemmy>:9835/metrics | grep '^nvidia_smi_'
```

Also verify `systemctl is-active prometheus-node-exporter` on every expected-up host and `systemctl is-active nvidia_gpu_exporter` on Lemmy. Roost is intentionally recorded as expected-down; it must not page until the host returns to service. An unreachable expected-up host is a rollout blocker, not a silently omitted target.

`btc-01` has a default-drop nftables input policy. Its durable `/etc/nftables.conf` must include TCP 9100 in the existing VLAN40 management allow-set; do not add a public rule or alter its Bitcoin, Lightning, or Docker forwarding rules.

## Rollback

Disable the corresponding service, then remove the package with the host package manager. Removing `prometheus-node-exporter` also removes the exporter listener. For the GPU exporter, remove `/etc/systemd/system/nvidia_gpu_exporter.service.d/listen-address.conf`, run `systemctl daemon-reload`, and remove `nvidia-gpu-exporter`.
