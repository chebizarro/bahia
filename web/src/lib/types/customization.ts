export type SoulDraftSchema = 'soulfactory-draft/v2';

export type SoulTier = 'lightweight' | 'standard' | 'heavy' | string;

export interface SoulIdentitySpec {
  name?: string;
  purpose?: string;
  tier?: SoulTier;
  nip05?: string;
  theme?: string;
  emoji?: string;
}

export interface SoulPersonaSpec {
  traits?: string[];
  style?: string;
  tone?: string;
  constraints?: string[];
  system_prompt_sections?: Record<string, string>;
}

export interface SoulAvatarGenerationSpec {
  prompt?: string;
  style_preset?: string;
  seed?: string;
  width?: number;
  height?: number;
  provider?: string;
}

export type SoulAvatarCurrent = 'generated' | 'uploaded' | string;

export interface SoulAvatarSpec {
  generation?: SoulAvatarGenerationSpec;
  uploaded_ref?: string;
  generated_ref?: string;
  current?: SoulAvatarCurrent;
}

export interface SoulVoicePersonaSpec {
  label?: string;
  profile?: string;
  style?: string;
  accent?: string;
  pacing?: string;
  [providerConfig: string]: unknown;
}

export type SoulVoiceAutoMode = 'off' | 'always' | 'tagged' | string;

export interface SoulVoiceSpec {
  provider?: string;
  persona_id?: string;
  persona?: SoulVoicePersonaSpec;
  auto_mode?: SoulVoiceAutoMode;
  sample_text?: string;
  providers?: Record<string, Record<string, unknown>>;
}

export interface SoulMemorySearchSpec {
  top_k?: number;
  score_threshold?: number;
  rerank?: boolean;
  rerank_model?: string;
}

export interface SoulMemorySpec {
  embedding_provider?: string;
  embedding_model?: string;
  search?: SoulMemorySearchSpec;
  strategy?: string;
  auto_index?: boolean;
  retention_days?: number;
}

export interface SoulRuntimeSpec {
  target?: string;
  runtime_pubkey?: string;
  capability_ref?: string;
  runtime_binding?: string;
  state?: string;
}

export interface SoulPermissionsSpec {
  allowed_kinds?: number[];
  tool_grants?: string[];
  approval_policy?: string;
}

export interface SoulRelayPolicySpec {
  read?: string[];
  write?: string[];
  control?: string[];
  nip65_discovery?: boolean;
}

export interface SoulWorkspaceSpec {
  repo?: string;
  branch?: string;
  environment?: string;
}

export interface SoulAssetRefsSpec {
  avatar_ref?: string;
  voice_ref?: string;
}

export interface SoulDraftContentV2 {
  schema: SoulDraftSchema;
  brief?: string;
  template_ref?: string;
  agent_id?: string;
  spec_hash?: string;
  previous_spec_hash?: string;
  identity?: SoulIdentitySpec;
  persona?: SoulPersonaSpec;
  avatar?: SoulAvatarSpec;
  voice?: SoulVoiceSpec;
  memory?: SoulMemorySpec;
  runtime?: SoulRuntimeSpec;
  permissions?: SoulPermissionsSpec;
  relay_policy?: SoulRelayPolicySpec;
  workspace?: SoulWorkspaceSpec;
  assets?: SoulAssetRefsSpec;
  [key: string]: unknown;
}

export interface CustomizationProviderOption {
  id: string;
  label: string;
  description?: string;
  models?: string[];
  presets?: string[];
}

export interface SoulDraftDiffEntry {
  path: string;
  before: unknown;
  after: unknown;
  type: 'added' | 'removed' | 'changed';
}
