# Shared assistant batch approval hash vectors

`batch_approval_hash_vectors.json` is the cross-language contract for
`domain.AssistantBatchApprovalHashInput`. Each `input` object is the complete
normalized JSON envelope; `canonical` is the exact UTF-8 RFC 8785 output, and
`sha256` is lowercase hex over those bytes. Browser tests should compare both
canonical bytes and digest, not only a server-supplied hash.

The input always contains `version`, `session_id`, `run_id`, `workflow`,
`proposal_id`, `revision`, `scope`, and `plan`. `scope.allowed_tools` is always
present (`null` unrestricted; `[]` no tools). Optional scope fields are omitted
when absent; `scope.arguments_digest`, when present, is the RFC 8785 SHA-256
hash of private command-scope arguments. The public session projection exposes
that digest but never the private arguments. The plan retains step order and
uses executable input only; previews and model-supplied idempotency keys are
removed before hashing. Empty step arguments are `{}`, not `null`.

Canonicalize objects recursively by UTF-16 property-name order, preserve array
order, and use ECMAScript `JSON.stringify` for primitive values, following
[RFC 8785](https://www.rfc-editor.org/rfc/rfc8785.html). Reject duplicate JSON
keys and invalid Unicode before decoding. Numeric boundaries include the
2^53-1 integer boundary, fixed/scientific notation cutovers and negative zero.
