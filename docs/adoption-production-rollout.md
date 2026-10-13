# Docker workload adoption

Bahia can scan existing Docker endpoints and import selected containers as
services, environments, builds, artifacts and canonical adoption bindings. The
operator surface is the signed `adoption/scan` and `adoption/import` intent path
used by `bahia adopt`; there is no HTTP adoption route.

## Enablement

Adoption is disabled by default. A daemon that enables it requires HTTP auth,
a service signing key and at least one authorized operator pubkey:

```yaml
auth:
  enabled: true
nostr:
  private_key: ${BAHIA_NOSTR_PRIVATE_KEY}
adoption:
  enabled: true
  allowed_pubkeys:
    - <64-hex-operator-pubkey>
  allow_compose_takeover: false
runtime:
  endpoints:
    prod-docker:
      docker_host: tcp://docker.example:2376
      ca_cert_file: /run/secrets/docker-ca.pem
      client_cert_file: /run/secrets/docker-cert.pem
      client_key_file: /run/secrets/docker-key.pem
```

`adoption.allowed_subjects` and `allowed_emails` do not authorize signed
requests. Configuration fails when adoption is enabled without
`auth.enabled`, a service key, or an `allowed_pubkeys` entry.

Use named `runtime.endpoints`. Signed adoption intents accept only endpoint
references; raw Docker hosts are not a CLI target and must not be placed in an
intent or relay-visible event.

## Scan

Sign with an authorized local key file or NIP-46 bunker, then request a bounded
preview:

```bash
bahia --relay wss://relay.example \
  --service-pubkey <bahia-service-pubkey> \
  --nostr-key-file /run/secrets/operator.nsec \
  adopt scan \
  --target prod=prod-docker \
  --environment prod=production \
  --limit 20
```

Use `--offset` for the next page. `--idempotency-key <uuidv7>` makes a scan
safe to replay. The preview reports whether each container is adoptable and
lists warnings; sensitive environment and label values are omitted and only
their key names are reported.

Compose-owned containers are refused unless
`adoption.allow_compose_takeover: true`. Enabling that option authorizes Bahia
to manage the selected containers directly rather than their Compose project,
so review the preview before import.

## Import

Import explicit container IDs from the preview:

```bash
bahia --relay wss://relay.example \
  --service-pubkey <bahia-service-pubkey> \
  --nostr-key-file /run/secrets/operator.nsec \
  adopt import \
  --org <organization-uuid> \
  --target prod=prod-docker \
  --environment prod=production \
  --select prod/<container-id>=api \
  --idempotency-key <uuidv7>
```

`--all` imports every adoptable candidate from the named targets; it is
mutually exclusive with an empty selection only in the sense that one of
`--all` or `--select` is required. Repeat `--select` for multiple containers.

### SF-04: inline environment secrets

Select a server-managed endpoint with `--target alias=endpointRef` before
reviewing sensitive environment keys; a raw Docker host is not a signer-first
input.

Before importing a container with inline secrets, attempt the import once
without references. The refused candidate status names the sensitive keys and
its deterministic `service_id`; no service is published. Import each value
into Signet as a credential labelled `<org-id>/<service-id>/<name>`. Use
`--secret-ref KEY=credential-id:version` once per key (or the intent's
`secret_refs` map with `{store:"signet",credential_id,version}`). The daemon
checks the exact label, active status and pinned version; it never reads the
value. The adopted service keeps no inline copy. A missing or invalid reference
refuses the candidate, naming keys but not values. For a key that is genuinely
non-secret, use `--non-secret-env KEY` instead; never use both for one key.

```bash
bahia adopt import --org "$ORG" --target prod=prod-docker \
  --select prod/<container-id>=api \
  --secret-ref DB_PASSWORD=cred_<id>:1
```

The daemon publishes canonical service/environment/build/artifact records and
an `adoption-binding` record at
`adoption:binding:<service-id>:<environment-id>`. A binding moves from
`in_progress` to `complete` only after its canonical publications succeed. If
a relay publication interrupts the request, repeat the same import with the
same idempotency key; the service resumes from the binding rather than
repeating completed work.

## Verification

1. Confirm the CLI receives an accepted `30315` intent status.
2. REQ the service pubkey for `30900` topics `service-registry`,
   `environment-registry`, `build-registry`, `artifact-registry` and
   `adoption-binding`.
3. Confirm the binding is `complete` and its fingerprints match the selected
   runtime workload.
4. Inspect `GET /ready`; an enabled adoption service contributes the
   `adoption` check.
5. Run a second scan. The imported workload must resolve to its existing
   binding rather than appear as a new unbound candidate.

A failed or incomplete import is not evidence of success. Preserve the intent
ID and sanitized CLI output, correct the endpoint or relay failure, and replay
the same request.
