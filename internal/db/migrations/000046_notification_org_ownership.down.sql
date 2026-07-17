DROP INDEX IF EXISTS idx_notification_channels_org_id;

ALTER TABLE notification_channels
    DROP CONSTRAINT IF EXISTS fk_notification_channels_org_id;

ALTER TABLE notification_channels
    DROP COLUMN IF EXISTS org_id;
