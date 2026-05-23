export type ContinuityProfile = 'full' | 'degraded' | 'emergency' | 'offline' | string;

export type ContinuityOperationState =
  | 'steady'
  | 'failover_in_progress'
  | 'recovery_in_progress'
  | 'failed'
  | string;

export interface ContinuityRunDTO {
  id: string;
  step_index: number;
  step_count: number;
  step_action: string;
}

export interface ContinuityServiceStatusDTO {
  service_key: string;
  active_profile: ContinuityProfile;
  operation_state: ContinuityOperationState;
  primary_worker_pubkey: string;
  active_worker_pubkey: string;
  standby_worker_pubkey?: string;
  reason?: string;
  changed_at: string;
  current_run?: ContinuityRunDTO;
}

export type ContinuitySurvivability =
  | 'survivable'
  | 'degraded_only'
  | 'emergency_only'
  | 'unsatisfied'
  | string;

export interface ContinuityAssessmentDTO {
  service_key: string;
  survivability: ContinuitySurvivability;
  has_failover_recipe: boolean;
  has_recovery_recipe: boolean;
  standby_count: number;
  replication_configured: boolean;
  heartbeat_active: boolean;
}
