# HIVECI_PRIVATE_BUILD_CONTEXT_STAGING design decisions

- Authorize dependencies on `hiveci.policies[].build_dependencies`, because that fleet-owned policy already binds a repository workflow to a Bahia service and environment. Workflow files and kind-5401 tags cannot add or redirect dependencies.
- Resolve each configured repository's current default-branch head through fleet Gitea at dispatch time, then place only the returned lowercase 40-hex SHA in the signed kind-5100. This avoids stale static config while making each individual job reproducible.
- Do not accept a configured branch, tag, or static revision. The service policy contains only the name and credential-free HTTPS clone URL; Gitea chooses the default branch and Bahia resolves its current head.
- Require one dependency set per service across enabled policies. Conflicting service policies fail configuration validation rather than selecting by order.
- Reuse the existing private-mirror read credential only inside Loom's selected-worker NIP-44 secret path. Dependency URLs remain credential-free and on the fleet Gitea origin; repository-controlled workflow code never receives the credential.
- Resolve the full set before publishing. A partial result is discarded, and exact initiation replay returns before dependency resolution or dispatch so it cannot produce a duplicate job.
