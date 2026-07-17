CREATE TABLE backup_operation_checkpoints (
    operation_type TEXT NOT NULL CHECK (operation_type IN ('snapshot', 'restore')),
    operation_id UUID NOT NULL,
    token UUID NOT NULL UNIQUE,
    status TEXT NOT NULL CHECK (status IN ('executing', 'executed')),
    result JSONB NOT NULL DEFAULT 'null'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    executed_at TIMESTAMPTZ,
    PRIMARY KEY (operation_type, operation_id),
    CHECK ((status = 'executing' AND executed_at IS NULL) OR (status = 'executed' AND executed_at IS NOT NULL))
);
