# Adoption

**Adoption** brings containers that already run on a configured runtime target under Bahia management without redeploying them.

## Scanning

1. Open **Adoption**, choose the organization that will own the adopted resources, and enter a target as `alias=endpointRef` (a runtime endpoint the daemon has configured).
2. Select **Scan target**. The browser signs an `adoption` `scan` intent; the daemon inspects the target and answers with a bounded, redacted findings page in the intent status.
3. Review each finding: target, container, image, proposed service name, adoptability, and warnings. Use **Load more findings** while the status reports `truncated` with a `next_offset`.

A scan is a preview. It never creates a service, and its status data omits environment values and other runtime secrets. A target reporting `scan_failed` needs its connectivity or permissions fixed before a retry.

From the CLI: `bahia adopt scan --target prod=prod-docker [--offset 0 --limit 20]`.

## Importing

An `adoption` `import` intent (`bahia adopt import --org "$ORG" --target prod=prod-docker --all`, or `--select alias/containerID[=serviceName]`) creates the service, environment, deployment unit, build, and artifact records for each adoptable container and publishes an **adoption binding** that ties the container's runtime fingerprints to those records. The accepted status returns the intent ID and candidate count; the imported services then appear in **Services** as canonical records with `ownership_mode: adopted`.

An interrupted import resumes from its binding records after a daemon restart; retrying with the same `--idempotency-key` is safe.

Adoption never copies a secret value. A container whose environment carries secret values inline is refused, and the result names the keys, never the values. A key is a secret by whole name tokens (`*_PASSWORD`, `*_PASSWD`, `*_SECRET`, `*_TOKEN`, `*_CREDENTIALS`, `*_API_KEY`, `*_PRIVATE_KEY`, `*_ACCESS_KEY`, `*_SIGNING_KEY`, ...) or, for a connection string (`*_URL`, `*_URI`, `*_DSN`, `CONNECTION_STRING`), when its value carries a password; `COMPASS_URL`, `SESSION_TIMEOUT` or `SORT_KEY` are not secrets. Move secret values out of the container (for example into mounted files), or import each into the [secret store](../../runbooks/signet-secret-store.md) and deploy the service through Bahia instead of adopting it. A key flagged by name that is not a secret can be declared with `--non-secret-env KEY` (intent field `non_secret_env_keys`). Non-secret environment values are kept on the adopted service.

```bash
bahia adopt import --org "$ORG" --target prod=prod-docker --all --non-secret-env PASSWORD_MIN_LENGTH
```

## Configuration

```yaml
adoption:
  enabled: true
  allowed_pubkeys: ["<operator-hex-pubkey>"]   # required; scans and imports are refused otherwise
  allow_raw_docker_hosts: false                # permit --raw-target alias=dockerHost
```

The signer must be listed in `adoption.allowed_pubkeys`. Server-managed endpoint references are the normal target form; raw Docker hosts are accepted only when `allow_raw_docker_hosts` is set. Adoption works with any service signer (`nostr.signer`: local, NIP-46 or NIP-55L).

## Related

- [Services](services.md) — the records an import creates
- [Artifacts](artifacts.md) — importing a single already-running image (`artifacts import-observed`)
