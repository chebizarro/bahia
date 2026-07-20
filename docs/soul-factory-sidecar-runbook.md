# Soul Factory sidecar deploy and first-provision runbook

This runbook is for operators deploying the Soul Factory runtime sidecar on `max`. Do not run these steps from a development agent session unless you are the human operator for `max`.

## 1. Build and place binaries

On a trusted build host for the Bahia repo:

```bash
go build -o ./bin/openclaw-soulfactory-control ./cmd/openclaw-soulfactory-control
go build -o ./bin/openclaw-soulfactory-sidecar ./cmd/openclaw-soulfactory-sidecar
```

Copy both binaries to `max` and install them in the chosen service directory, for example `/opt/bahia/soulfactory/`.

## 2. Generate and persist the sidecar runtime key

On `max`, create a dedicated runtime Nostr key for the sidecar. Store it in a root-readable service env file or secret manager entry owned by the `openclaw-soulfactory-sidecar` service.

```bash
install -d -m 0700 /etc/bahia/soulfactory
# Generate with the fleet-approved Nostr key tool, then write only the nsec/hex secret here.
install -m 0600 /dev/null /etc/bahia/soulfactory/sidecar.env
printf 'SOULFACTORY_SIDECAR_NSEC=%s\n' '<generated-sidecar-runtime-nsec>' >> /etc/bahia/soulfactory/sidecar.env
```

Record key ownership and rotation in the host secrets inventory. This key is only the sidecar runtime identity; agent signing remains Signet-held.

## 3. Configure the OpenClaw control wrapper

Use existing-container Docker mode and confirm the `openclaw` CLI is present. If it is not on `PATH`, set `OPENCLAW_SOULFACTORY_BIN` in the service environment.

Required control methods:

```text
soulfactory.provision,soulfactory.update,soulfactory.persona.update,soulfactory.revoke
```

## 4. Start the sidecar on the fleet relay

Run the sidecar with the reactor Signet/controller pubkey as the only trusted controller and the fleet relay for both discovery and control traffic:

```bash
/opt/bahia/soulfactory/openclaw-soulfactory-sidecar \
  -command /opt/bahia/soulfactory/openclaw-soulfactory-control \
  -methods soulfactory.provision,soulfactory.update,soulfactory.persona.update,soulfactory.revoke \
  -trusted-controller-pubkeys '<reactor-signet-pubkey>' \
  -relays wss://relay.sharegap.net \
  -control-relays wss://relay.sharegap.net
```

Deploy this as a supervised service on `max` using the host's standard service manager. Load the persisted sidecar runtime key from `/etc/bahia/soulfactory/sidecar.env` or the equivalent secret source.

## 5. Verify the published capability

Subscribe on `wss://relay.sharegap.net` for kind `30317` from the sidecar runtime pubkey. The latest capability event must include:

- `schema=soulfactory-runtime-capability/v1`
- `control_schema=soulfactory-runtime-control/v1`
- `runtime=openclaw`
- `methods` includes `soulfactory.provision`
- `controller_pubkeys` includes the reactor Signet/controller pubkey

If `souls/new` does not discover this capability, verify relay URL, sidecar runtime key, controller pubkey, and method list before provisioning.

## 6. Provision one agent end-to-end

1. Open `souls/new` in Bahia.
2. Confirm the OpenClaw `30317` capability is selectable.
3. Enter a brief-only draft and save the signed draft (`31952`).
4. Submit the provisioning request (`5950`).
5. Watch progress events through steps 1, 2, 4, and 8:
   - step 1: LLM soul generation
   - step 2: Signet `25910` `agent/provision` returns `pubkey` and `bunker_uri`
   - step 4: kind `0` profile is published using Signet-held signing
   - step 8: runtime binds through `38384` → sidecar → `38386`, then Bahia service/deployment-intent registration runs
6. Confirm an active `31951` soul appears in the UI with a Signet-held bunker URI and no agent `nsec` material.

## 7. Rollback

To stop provisioning new runtime bindings, stop `openclaw-soulfactory-sidecar` on `max`. Existing Signet-held agent identities remain managed by Signet; use the Soul Factory revoke action for agent-specific teardown.
