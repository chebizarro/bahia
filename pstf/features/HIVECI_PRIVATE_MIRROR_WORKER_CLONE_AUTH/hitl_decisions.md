# HIVECI_PRIVATE_MIRROR_WORKER_CLONE_AUTH design decisions

- Use a distinct `hiveci.initiator.mirror_read_credential_ref` and `mirror_read_username`, not the existing upstream mirror-provisioning credential and not the fleet Gitea admin token. The referenced Gitea identity must have read-only access to the private mirror namespace. This is the least-privilege option expressible by the current single-source initiator configuration.
- Require the resolved secret manifest to match both the configured UUID and the requested service. This retains the existing service-secret ownership boundary even though the reference is operator configuration.
- Reuse Loom's established `JobRequest.Secrets` path. Loom selects an allowlisted, capability-admitted worker first, then NIP-44 encrypts each value to that exact pubkey. There is no plaintext or wrong-recipient fallback.
- Pin the credential-consuming clone URL to the HTTPS origin from `hiveci.initiator.gitea_base_url` and to `/<mirror_owner>/<repository>.git`; do not trust URL shape alone.
- Preserve the existing exact-source-event replay guard, no-payment profile, Bahia service-key kind-5401 publisher, and trusted worker-signed kind-5402 divergence. Do not add `HIVE_CI_NSEC`.
