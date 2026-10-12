import { describe, expect, it } from 'vitest';
import {
  environmentFormSchema,
  parseRuntimeConfig,
  policyFormSchema,
  secretFormSchema,
  secretReferenceSchema,
  serviceFormSchema,
  validateForm
} from '../../src/lib/validation/forms.js';

describe('form validation schemas', () => {
  it('validates service form required fields', () => {
    expect(validateForm(serviceFormSchema, { name: '', artifactRepo: 'a/b', runtimeType: 'docker' }).error).toBe('Name is required');
    expect(validateForm(serviceFormSchema, { name: 'api', artifactRepo: '', runtimeType: 'docker' }).error).toBe('Artifact repository is required');
    expect(validateForm(serviceFormSchema, { name: 'api', artifactRepo: 'a/b', runtimeType: '' }).error).toBe('Runtime type is required');
  });

  it('validates environment runtime config JSON', () => {
    expect(validateForm(environmentFormSchema, { name: 'prod', deploy_strategy: 'rolling', runtime_config: '{}' }).success).toBe(true);
    expect(validateForm(environmentFormSchema, { name: 'prod', deploy_strategy: 'rolling', runtime_config: '{bad json}' }).error).toBe('Runtime config must be valid JSON');
  });

  it('validates policy rules JSON array', () => {
    expect(validateForm(policyFormSchema, { name: 'p', enforcement: 'warn', rules: '[]' }).success).toBe(true);
    expect(validateForm(policyFormSchema, { name: 'p', enforcement: 'warn', rules: '{}' }).error).toBe('Rules must be a JSON array');
    expect(validateForm(policyFormSchema, { name: 'p', enforcement: 'warn', rules: 'not-json' }).error).toBe('Rules must be valid JSON');
  });

  it('validates secret forms', () => {
    const credential_id = `cred_${'a'.repeat(64)}`;
    expect(validateForm(secretFormSchema, { name: '', credential_id, credential_version: 1 }).error).toBe('Secret name is required');
    expect(validateForm(secretFormSchema, { name: 'TOKEN', credential_id: '', credential_version: 1 }).error).toMatch(/Signet credential id/);
    expect(validateForm(secretReferenceSchema, { credential_id, credential_version: 0 }).error).toBe('Credential version must be at least 1');
    expect(validateForm(secretReferenceSchema, { credential_id, credential_version: 1.5 }).error).toBe('Credential version must be a whole number');
    expect(validateForm(secretFormSchema, { name: 'TOKEN', credential_id, credential_version: '2' }).data).toEqual({ name: 'TOKEN', credential_id, credential_version: 2 });
  });

  it('parses runtime config with empty fallback', () => {
    expect(parseRuntimeConfig('')).toEqual({});
    expect(parseRuntimeConfig('{"a":1}')).toEqual({ a: 1 });
  });
});
