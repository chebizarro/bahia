# Adoption

The **Adoption** page scans configured runtime targets for containers that Bahia could bring under management.

1. Choose the organization that will own adopted resources.
2. Enter the configured runtime target name and endpoint reference.
3. Select **Scan target**. Bahia signs an `adoption/scan` kind-`30900` intent; no ContextVM request or REST mutation is sent.
4. Review the bounded findings returned in the matching kind-`30315` intent status event. Each row shows the target, container, image, proposed service, adoptability, and warning count. Use **Load more findings** when the status reports another page.

The scan result is a preview, not an import. Do not treat a finding as an adopted service until a separate `adoption/import` intent is accepted and the canonical service, environment, and lineage projections appear.

Scan status data is intentionally bounded and omits runtime secrets. If a target reports `scan_failed`, inspect target connectivity and permissions before retrying. Bahia never infers adoption from a request acknowledgement alone.
