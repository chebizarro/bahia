# Tool provisioning

Tool provisioning and human approval are suspended. Existing PostgreSQL tool
intent, profile, and approval rows are derived records, not evidence that a
request was accepted or an effect was committed. The daemon does not use those
rows to authorize a tool image build or runtime deployment.

An operator-signed kind `30900` intent with `domain=tool`,
`op=approval-response`, `d=tool-approval:<provisioning-intent-id>`, and content
containing `intent_id`, `action` (`approve` or `reject`), and a non-empty
`reason` is refused with a service-signed rejected kind `30315` status after
relay quorum acceptance and durable local outbox recording. Publication failure
is reported to the caller, not treated as a successful refusal. The status
identifies the signed request; it does **not** approve the SQL row or run the
tool workflow. A locally constructed MCP event without the operator's Schnorr
signature is refused without an outcome event.

Read the rejection status for the reason. Do not infer approval from an MCP
tool response, a SQL approval row, or relay acceptance of the operator intent.
