# Verification Report — fp-obs.6

## Current state

Implementation and live verification are in progress. Six reachable expected-up hosts now run Ubuntu `prometheus-node-exporter` 1.7.0 bound to their declared Infra addresses. Lemmy also runs checksum-verified `nvidia_gpu_exporter` 1.13.1 bound to `192.168.40.110:9835`.

An independent probe from edge-01 returned the required memory and filesystem/inode series from edge-01, max, core-01, storage-01, btc-01, and Lemmy. Lemmy returned 187 `nvidia_smi_*` series and two per-device VRAM-used series for its two live Tesla P40 GPUs.

`btc-01`'s durable default-drop nftables policy was backed up to `/etc/nftables.conf.before-node-exporter-20260804T0040PDT`, updated only to allow TCP 9100 from VLAN40 in the existing management rule, validated with `nft -c`, reloaded, and successfully probed from edge-01.

## Source verification

- `bash -n deploy/observability/exporters/install-node-exporter.sh deploy/observability/exporters/install-nvidia-gpu-exporter.sh` — passed.
- `promtool check config deploy/observability/prometheus.yml` — passed with 13 rules.
- `promtool test rules deploy/observability/bahia-alerts.test.yml` — passed.
- PSTF JSON parsing and `git diff --check` — passed.

## Known rollout state

- Roost is explicitly expected-down.
- ai-02 is expected-up but was unreachable from the management plane during preflight; it remains a blocker until routed access or host-side execution is available.
- Prometheus aggregate scraping belongs to `fp-obs.1`; AC5 remains pending until that plane is deployed.
