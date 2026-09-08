# BAHIA_GITEA_INITIATOR_SOURCE_PROVIDER_AUTH design decisions

- `hiveci.initiator.source_provider` is mandatory when the initiator is enabled. It accepts only `github` or `gitea`; Bahia does not inspect the clone host to choose a provider.
- GitHub retains the proven `service: github` plus `auth_token` API request and may derive `https://github.com/<owner>/<name>.git` when `source_clone_url` is empty.
- Private Gitea maps explicitly to provider-neutral `service: git`. The configured `source_auth_username` and resolved opaque service credential are sent as `auth_username` and `auth_password`, because mirror git transport does not authenticate with `auth_token`.
- Clone credentials are always separate from `clone_addr`. Gitea stores the submitted `clone_addr` as `original_url`, so validation compares the expected clean URL and rejects empty or mismatched metadata.
- GitHub rejects a configured source username rather than silently ignoring an ambiguous auth field.
