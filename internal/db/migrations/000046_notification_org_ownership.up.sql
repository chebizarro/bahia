-- Attach notification channels to tenant organizations. Notification logs are
-- scoped transitively through their channel_id foreign key.
ALTER TABLE notification_channels ADD COLUMN IF NOT EXISTS org_id UUID;

UPDATE notification_channels
SET org_id = NULL
WHERE org_id = '00000000-0000-0000-0000-000000000000'::uuid;

WITH single_org AS (
    SELECT id
    FROM organizations
    ORDER BY created_at, id
    LIMIT 1
), org_count AS (
    SELECT count(*) AS n
    FROM organizations
)
UPDATE notification_channels
SET org_id = (SELECT id FROM single_org)
WHERE org_id IS NULL
  AND (SELECT n FROM org_count) = 1;

UPDATE notification_channels c
SET org_id = NULL
WHERE org_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM organizations o WHERE o.id = c.org_id
  );

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'fk_notification_channels_org_id'
          AND conrelid = 'notification_channels'::regclass
    ) THEN
        ALTER TABLE notification_channels
            ADD CONSTRAINT fk_notification_channels_org_id
            FOREIGN KEY (org_id) REFERENCES organizations(id) ON DELETE CASCADE;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_notification_channels_org_id
    ON notification_channels(org_id);
