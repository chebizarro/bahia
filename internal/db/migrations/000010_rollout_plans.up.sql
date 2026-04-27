CREATE TABLE IF NOT EXISTS rollout_plans (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    deployment_intent_id UUID NOT NULL REFERENCES deployment_intents(id) ON DELETE CASCADE,
    strategy VARCHAR(20) NOT NULL,
    current_step INT NOT NULL DEFAULT 0,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_rollout_plans_intent ON rollout_plans(deployment_intent_id);
CREATE INDEX idx_rollout_plans_status ON rollout_plans(status) WHERE status IN ('pending', 'running');

CREATE TABLE IF NOT EXISTS rollout_steps (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    rollout_plan_id UUID NOT NULL REFERENCES rollout_plans(id) ON DELETE CASCADE,
    step_order INT NOT NULL,
    action VARCHAR(30) NOT NULL,
    config JSONB DEFAULT '{}',
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    health_result JSONB,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    UNIQUE (rollout_plan_id, step_order)
);

CREATE INDEX idx_rollout_steps_plan ON rollout_steps(rollout_plan_id);
