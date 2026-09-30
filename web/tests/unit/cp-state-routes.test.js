import { describe, it, expect } from 'vitest';
import { CP_STATE_SCHEMA_BY_LEGACY_KIND, controlStateSchema } from '../../src/lib/nostr/cp-state.js';
import { DNS_STATE_SCHEMA_BY_LEGACY_KIND } from '../../src/lib/nostr/kinds.gen.js';

// legacy_kind values are the internal/kinds catalog kinds the producers stamp
// (projector controlStateEnvelope, controlplane worker-state publisher).
const EXPECTED_ROUTES = {
  31961: 'bahia.state.service.v1',
  31962: 'bahia.registry.service.v1',
  31963: 'bahia.registry.environment.v1',
  31964: 'bahia.registry.llm-route.v1',
  31965: 'bahia.state.llm-route.v1',
  31966: 'bahia.registry.artifact.v1',
  31967: 'bahia.registry.deployment-intent.v1',
  31968: 'bahia.registry.deployment-run.v1',
  31969: 'bahia.registry.build.v1',
  31970: 'bahia.registry.policy.v1',
  31971: 'bahia.registry.package-repository.v1',
  31972: 'bahia.registry.package-artifact.v1',
  31973: 'bahia.registry.package-promotion.v1',
  31975: 'bahia.state.dns-zone.v1',
  31976: 'bahia.state.dns-endpoint.v1',
  31977: 'bahia.state.dns-policy.v1',
  31978: 'bahia.state.dns-backend.v1',
  31980: 'bahia.registry.ml-model.v1',
  31981: 'bahia.registry.ml-model-version.v1',
  31982: 'bahia.registry.ml-dataset.v1',
  31983: 'bahia.registry.ml-recipe.v1',
  31984: 'bahia.state.ml-recipe-run.v1',
  31985: 'bahia.registry.ml-inference-endpoint.v1',
  31986: 'bahia.state.ml-inference-endpoint.v1',
  31987: 'bahia.state.ml-evaluation.v1',
  31988: 'bahia.state.ml-provenance.v1',
  31989: 'bahia.state.ml-runtime-capability.v1',
  31990: 'bahia.state.assistant-session.v1',
  31991: 'bahia.registry.backup-definition.v1',
  31992: 'bahia.registry.backup-policy.v1',
  31993: 'bahia.registry.backup-repository.v1',
  31994: 'bahia.registry.backup-retention.v1',
  31995: 'bahia.registry.backup-recipe.v1',
  31996: 'bahia.state.backup-run.v1',
  31997: 'bahia.state.backup-verification.v1',
  31998: 'bahia.state.backup-restore.v1',
  31999: 'bahia.state.backup-observation.v1',
  32000: 'bahia.state.worker.v1',
  32001: 'bahia.state.worker-assignment.v1',
  32002: 'bahia.state.worker-drain.v1',
  32003: 'bahia.state.worker-eligibility.v1'
};

function cpState(tags) {
  return { kind: 30900, tags, content: '{}' };
}

describe('generated cp-state legacy_kind routes', () => {
  it('routes every producer catalog kind to its family schema', () => {
    for (const [legacyKind, schema] of Object.entries(EXPECTED_ROUTES)) {
      expect(CP_STATE_SCHEMA_BY_LEGACY_KIND[legacyKind], `legacy_kind ${legacyKind}`).toBe(schema);
    }
  });

  it('agrees with the DNS family table and never routes the wire kind itself', () => {
    for (const [legacyKind, schema] of Object.entries(DNS_STATE_SCHEMA_BY_LEGACY_KIND)) {
      expect(CP_STATE_SCHEMA_BY_LEGACY_KIND[legacyKind]).toBe(schema);
    }
    expect(CP_STATE_SCHEMA_BY_LEGACY_KIND['30900']).toBeUndefined();
    expect(new Set(Object.values(CP_STATE_SCHEMA_BY_LEGACY_KIND)).size).toBe(Object.keys(CP_STATE_SCHEMA_BY_LEGACY_KIND).length);
  });

  it('resolves envelope records through legacy_kind and per-family records to themselves', () => {
    expect(controlStateSchema(cpState([['schema', 'bahia.cp-state.v1'], ['legacy_kind', '32001'], ['deleted', 'false']]))).toBe('bahia.state.worker-assignment.v1');
    expect(controlStateSchema(cpState([['schema', 'bahia.state.worker.v1'], ['legacy_kind', '32000']]))).toBe('bahia.state.worker.v1');
    expect(controlStateSchema(cpState([['schema', 'bahia.cp-state.v1'], ['legacy_kind', '1']]))).toBe('');
    expect(controlStateSchema(cpState([['schema', 'bahia.cp-state.v1']]))).toBe('');
  });
});
