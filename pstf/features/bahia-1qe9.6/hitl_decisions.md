# bahia-1qe9.6 HITL Decisions

## Package and repository subject digest resolution

Observed: Bahia has package artifact projections with SHA-256 digests, but ContextVM `sbom/generate` / `sbom/import` requests in this slice carry only a generic `SBOMSubject`. Repository subjects require a concrete git commit digest (`git:<commit>`) or content digest, but no canonical service/repository commit resolver is available in the touched ContextVM request shape.

Decision needed: Define the canonical ContextVM subject locator shape for package artifact coordinates and repository commit resolution before automatic digest lookup is enabled.

Current behavior: The SBOM orchestrator accepts package/repository subjects when `subject.digest` is explicitly provided and rejects ambiguous package/repository requests without guessing.
