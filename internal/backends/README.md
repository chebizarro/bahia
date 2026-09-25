# Package backend transport and checksum contracts

Nexus and Pulp use operator-configured opaque `auth_secret_ref`, `tls_secret_ref`,
and `secret_refs` values. Production startup supplies the Bahia secrets adapter
to `factory.BuildRegistryWithSecrets`; no secret is resolved by a client or put
into a package record. The resolver is the access-control boundary: missing,
denied, undecryptable, or unauditable secrets abort construction. Credentials are
resolved at construction, so rebuild the registry when rotating them.

## Authentication and TLS

An auth secret contains either a raw bearer token, `{"token":"..."}`, or
`{"username":"...","password":"..."}`. Mixed, partial, malformed, and unknown
credential fields are rejected. Generic `secret_refs` are resolved for backend
extensions via `Secret(name)`; they do not implicitly become HTTP headers.

A TLS secret is JSON with `ca_cert` (PEM trust certificates) and/or both
`client_cert` (PEM certificate chain) and `client_key` (PEM private key). Custom
trust replaces system roots for that backend only. Without custom trust, system
roots remain in effect. Invalid configured material never falls back to another
trust source. Client keys must match their certificates. Certificate/hostname
verification is mandatory; `insecure_skip_verify: true` is rejected. Transports
require TLS 1.2 or later, and authenticated redirects are not followed.

Resolver failures are opaque, HTTP error bodies are omitted, and adapter errors
use `internal/redact` for known raw/encoded credentials. Credential-bearing
public fields and URLs are rejected; Nexus download URLs are constructed from
configured public endpoints rather than copied from server responses.

## Explicit checksum API opt-in

These values select REST contracts, **not product release versions**:

| Backend setting | Supported value | Authoritative evidence |
| --- | --- | --- |
| `nexus_api_version` | `v1` | Exact repository/path asset's `checksum.sha256`, after all search pages complete |
| `pulp_api_version` | `v3` | File content's `sha256`, scoped to the named repository's immutable `latest_version_href` and exact `relative_path` |

The contracts follow Sonatype's [Search API](https://help.sonatype.com/en/search-api.html)
and Pulp's [file-content API](https://docs.pulpproject.org/pulp_file_client/ContentFilesApi/),
[checksum schema](https://docs.pulpproject.org/pulp_file_client/FileFileContentResponse/),
and [repository schema](https://docs.pulpproject.org/pulp_file_client/FileFileRepositoryResponse/).
Enable only the contract deployed by the operator. Empty, unknown, or different
versions leave `CanObserveDrift` false and `ObserveArtifact` returns an error,
not an existence-only result masquerading as checksum verification.

Even with a supported version, every observation requires a valid expected
SHA-256 and a valid independently returned SHA-256 for existing content. Missing
fields, invalid digests, ambiguous matches, unsupported endpoints (including
404), authentication failures, incomplete pagination, and malformed JSON return
errors. Only a valid complete empty lookup proves absence. Requests never filter
by the expected digest, which would conceal mismatches. The package service
compares the returned digest with Bahia's expected digest and includes both in
its drift reason. Pulp evidence describes the repository version, not whether
a separate distribution has published that version. These are backend checksum
observations, not a download-and-rehash physical storage integrity check.

`factory/checksum_test.go` exercises the real package-service comparison with
local HTTP fixtures; `factory/security_test.go` and `factory/tls_test.go` cover
credential failure/redaction and verified CA/mTLS handshakes without a registry.
