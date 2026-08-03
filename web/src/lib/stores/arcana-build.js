import { publishCommand } from './public-controlplane.svelte.js';

export const ARCANA_REPOSITORY_URL = 'https://github.com/chebizarro/living-library-forge';
export const ARCANA_REPOSITORY_COORDINATE = 'chebizarro/living-library-forge';

export const ARCANA_PUBLIC_BUILD_ARGS = Object.freeze([
  'VITE_ARCANA_READ_RELAYS',
  'VITE_ARCANA_WRITE_RELAYS',
  'VITE_ARCANA_SIGNER_MODE',
  'VITE_ARCANA_SEARCH_DVM_PUBKEY',
  'VITE_BLOSSOM_URL',
  'VITE_ARCANA_INFERENCE_URL',
  'VITE_ARCANA_WORKFLOW_API_URL',
  'VITE_SUPABASE_URL',
  'VITE_SUPABASE_PUBLISHABLE_KEY'
]);

const allowedBuildArgs = new Set(ARCANA_PUBLIC_BUILD_ARGS);
const immutableDigest = /^sha256:[0-9a-f]{64}$/i;

export function isArcanaService(service) {
  const coordinate = String(service?.repository?.repo_coordinate || '').trim().toLowerCase();
  const repoUrl = String(service?.repo_url || service?.repository?.web_url || service?.repository?.clone_url || '')
    .trim().replace(/\.git$/i, '').toLowerCase();
  return coordinate === ARCANA_REPOSITORY_COORDINATE || repoUrl === ARCANA_REPOSITORY_URL;
}

export function publicArcanaBuildArgs(values = {}) {
  const result = {};
  for (const [name, rawValue] of Object.entries(values || {})) {
    if (!allowedBuildArgs.has(name)) throw new Error(`${name} is not an approved public Arcana build setting`);
    const value = String(rawValue ?? '').trim();
    if (value) result[name] = value;
  }
  const signerMode = result.VITE_ARCANA_SIGNER_MODE;
  if (signerMode && signerMode !== 'nip07' && signerMode !== 'nip46') {
    throw new Error('VITE_ARCANA_SIGNER_MODE must be nip07 or nip46');
  }
  return result;
}

export function arcanaBuildPayload({ service, gitRef, credentialRef, buildArgs }) {
  const ref = String(gitRef || '').trim();
  const secretId = String(credentialRef || '').trim();
  if (!service?.id) throw new Error('Select the Arcana service');
  if (!isArcanaService(service)) throw new Error('The selected service is not mapped to the Arcana repository');
  if (!ref) throw new Error('Git ref or full commit is required');
  if (!secretId) throw new Error('Select a protected repository credential reference');
  if (!service.artifact_repo) throw new Error('The service has no artifact repository');
  return {
    service_id: service.id,
    git_ref: ref,
    repository_credential_ref: secretId,
    artifact_repo: service.artifact_repo,
    build_args: publicArcanaBuildArgs(buildArgs),
    _meta: { progressToken: globalThis.crypto?.randomUUID?.() || `arcana-build-${Date.now()}` }
  };
}

export function requestArcanaBuild(payload) {
  return publishCommand({
    operation: 'build/request',
    tags: [
      ['service', payload?.service_id],
      ['repository', ARCANA_REPOSITORY_COORDINATE],
      ['git-ref', payload?.git_ref]
    ].filter((tag) => tag[1]),
    content: payload
  });
}

export function registerBuildResult(buildId) {
  const id = String(buildId || '').trim();
  if (!id) throw new Error('Build ID is required');
  return publishCommand({
    operation: 'artifact/register-build-result',
    tags: [['build', id]],
    content: {
      build_id: id,
      _meta: { progressToken: globalThis.crypto?.randomUUID?.() || `artifact-build-${Date.now()}` }
    }
  });
}

export function artifactCandidateForBuild(build, artifacts = []) {
  if (!build?.id || build.status !== 'succeeded') return null;
  const artifact = artifacts.find((candidate) => candidate.build_id === build.id);
  const digest = String(artifact?.image_digest || '').trim();
  const imageRepo = String(artifact?.image_repo || '').trim();
  if (!artifact || !imageRepo || !immutableDigest.test(digest)) return null;
  const verification = artifactVerificationState(artifact);
  if (verification.state !== 'verified' || verification.manifest_digest !== digest || verification.tag_resolved_digest !== digest) return null;
  return { ...artifact, immutable_ref: `${imageRepo}@${digest}` };
}

export function artifactVerificationState(artifact) {
  const metadata = artifact?.metadata && typeof artifact.metadata === 'object' ? artifact.metadata : {};
  const verification = metadata.verification && typeof metadata.verification === 'object' ? metadata.verification : {};
  const supplyChain = metadata.supply_chain && typeof metadata.supply_chain === 'object' ? metadata.supply_chain : {};
  const policy = metadata.policy && typeof metadata.policy === 'object' ? metadata.policy : {};
  return {
    manifest_digest: String(verification.manifest_digest || artifact?.image_digest || '').trim(),
    source: String(verification.source || '').trim(),
    state: String(verification.state || 'unverified').trim(),
    media_type: String(verification.media_type || artifact?.manifest_media_type || '').trim(),
    verified_at: String(verification.verified_at || '').trim(),
    tag_resolved_digest: String(verification.tag_resolved_digest || '').trim(),
    signature_state: String(supplyChain.signature_state || (artifact?.signature_ref ? 'present' : 'missing')).trim(),
    signature_refs: Array.isArray(supplyChain.signature_refs) ? supplyChain.signature_refs : [],
    sbom_state: String(supplyChain.sbom_state || (artifact?.sbom_url ? 'present' : 'missing')).trim(),
    sbom_ref: String(supplyChain.sbom_ref || artifact?.sbom_url || '').trim(),
    provenance_ref: String(supplyChain.provenance_ref || '').trim(),
    policy_state: String(policy.state || supplyChain.policy_state || 'unknown').trim(),
    policy_id: String(policy.policy_id || '').trim(),
    ci_publisher: String(policy.ci_publisher || '').trim(),
    referrer_discovery_state: String(supplyChain.referrer_discovery_state || 'not_reported').trim(),
    scan_status: String(artifact?.scan_status || 'unknown').trim()
  };
}

export function buildEvidence(build) {
  const metadata = build?.metadata && typeof build.metadata === 'object' ? build.metadata : {};
  const evidence = metadata.evidence && typeof metadata.evidence === 'object' ? metadata.evidence : {};
  return {
    log_url: String(metadata.log_url || metadata.logs_url || evidence.log_url || '').trim(),
    request_event_id: String(evidence.request_event_id || build?.source_event_id || '').trim(),
    run_event_id: String(evidence.run_event_id || metadata.run_event_id || '').trim(),
    result_event_id: String(evidence.result_event_id || metadata.result_event_id || '').trim(),
    failure_reason: String(metadata.failure_reason || metadata.error || evidence.failure_reason || '').trim()
  };
}
