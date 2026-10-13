# Adoption

**Adoption** brings containers that already run on a configured runtime target under Bahia management without redeploying them.

## Scanning

1. Open **Adoption**, choose the organization that will own the adopted resources, and enter a target as `alias=endpointRef` (a runtime endpoint the daemon has configured).
2. Select **Scan target**. The browser signs an `adoption` `scan` intent; the daemon inspects the target and answers with a bounded, redacted findings page in the intent status.
3. Review each finding: target, container, image, proposed service name, adoptability, and warnings. Use **Load more findings** while the status reports `truncated` with a `next_offset`.

A scan is a preview. It never creates a service, and its status data omits environment values and other runtime secrets. A target reporting `scan_failed` needs its connectivity or permissions fixed before a retry.

From the CLI: `bahia adopt scan --target prod=prod-docker [--offset 0 --limit 20]`.

## Importing

An `adoption` `import` intent selects each workload and carries fixed
operator-minted UUIDv7 IDs for its service and target environment. The signed
handler uses these IDs on create; `--all` is not supported because unknown
workloads cannot have author-fixed IDs. The import creates the service,
environment, deployment unit, build, and artifact records and publishes an
**adoption binding** tying runtime fingerprints to those records. The accepted
status returns the intent ID and candidate count; canonical services then
appear in **Services** with `ownership_mode: adopted`. If a matching
UUIDv5-identified adoption was already published, Bahia reads and retains its
existing coordinates instead of rekeying it.

Keep `--service-id`, `--environment-id`, and `--idempotency-key` fixed for every
retry, including after a timeout or interrupted publish. An interrupted import
resumes from its canonical binding. Use the scan findings to select each
container explicitly.

Adoption never copies a secret value. For each inline secret, provide
`--secret-ref KEY=credential-id:version`. A first import without references
refuses the candidate but returns its signed `service_id` and the key names,
without publishing service state. Import each value into
[Signet](../../runbooks/signet-secret-store.md) with label
`<org-id>/<service-id>/<name>`; the credential must be active at the pinned
version. The intent carries only credential references, never values. Bahia
publishes a reference-only service secret and removes the inline key from the
adopted service environment. `--non-secret-env KEY` declares a name that is
not secret; it cannot also have a reference.

```bash
bahia adopt scan --org "$ORG" --target prod=prod-docker
bahia adopt import --org "$ORG" --target prod=prod-docker \
  --select prod/<container-id>=payment-api \
  --environment-id prod=<environment-uuidv7> \
  --service-id prod/<container-id>=<service-uuidv7> \
  --idempotency-key <attempt-uuidv7> \
  --secret-ref DB_PASSWORD=cred_<id>:1
```

## Configuration

```yaml
adoption:
  enabled: true
  allowed_pubkeys: ["<operator-hex-pubkey>"]   # required; scans and imports are refused otherwise
```

The signer must be listed in `adoption.allowed_pubkeys`. Signed adoption uses
server-managed endpoint references; raw Docker hosts are not accepted. Adoption
works with any service signer (`nostr.signer`: local, NIP-46 or NIP-55L).

## Related

- [Services](services.md) — the records an import creates
- [Artifacts](artifacts.md) — importing a single already-running image (`artifacts import-observed`)
