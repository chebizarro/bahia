import { z } from 'zod';

function requiredString(message) {
  return z.string().trim().min(1, message);
}

function parseJsonString(value, invalidMessage) {
  const trimmed = (value || '').trim();
  if (!trimmed) {
    return {};
  }

  try {
    return JSON.parse(trimmed);
  } catch {
    throw new Error(invalidMessage);
  }
}

export const serviceFormSchema = z.object({
  name: requiredString('Name is required'),
  artifactRepo: requiredString('Artifact repository is required'),
  runtimeType: requiredString('Runtime type is required')
});

export const environmentFormSchema = z.object({
  name: requiredString('Name is required'),
  deploy_strategy: requiredString('Deploy strategy is required'),
  runtime_config: z.string().optional().default('')
}).superRefine((value, ctx) => {
  try {
    parseJsonString(value.runtime_config, 'Runtime config must be valid JSON');
  } catch (err) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, message: err.message, path: ['runtime_config'] });
  }
});

export const policyFormSchema = z.object({
  name: requiredString('Name is required'),
  enforcement: requiredString('Enforcement mode is required'),
  rules: z.string().min(1, 'Rules must be valid JSON')
}).superRefine((value, ctx) => {
  try {
    const parsed = JSON.parse(value.rules);
    if (!Array.isArray(parsed)) {
      ctx.addIssue({ code: z.ZodIssueCode.custom, message: 'Rules must be a JSON array', path: ['rules'] });
    }
  } catch {
    ctx.addIssue({ code: z.ZodIssueCode.custom, message: 'Rules must be valid JSON', path: ['rules'] });
  }
});

export const secretFormSchema = z.object({
  name: requiredString('Secret name is required'),
  value: z.string().min(1, 'Secret value is required')
});

export const secretValueSchema = z.object({
  value: z.string().min(1, 'Secret value is required')
});

export function validateForm(schema, data) {
  const result = schema.safeParse(data);
  if (result.success) {
    return { success: true, data: result.data, error: null, issues: [] };
  }

  return {
    success: false,
    data: null,
    error: result.error.issues[0]?.message || 'Invalid form input',
    issues: result.error.issues
  };
}

export function parseRuntimeConfig(runtimeConfig) {
  return parseJsonString(runtimeConfig, 'Runtime config must be valid JSON');
}
