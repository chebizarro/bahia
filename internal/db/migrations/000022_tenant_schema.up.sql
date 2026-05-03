CREATE TABLE IF NOT EXISTS organizations (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL,
  owner_pubkey TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS org_members (
  org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  pubkey TEXT NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('viewer', 'deployer', 'admin', 'owner')),
  nip05 TEXT NOT NULL DEFAULT '',
  joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (org_id, pubkey)
);

CREATE INDEX IF NOT EXISTS idx_org_members_pubkey ON org_members(pubkey);

CREATE TABLE IF NOT EXISTS org_invites (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  pubkey TEXT NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('viewer', 'deployer', 'admin', 'owner')),
  invited_by TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_org_invites_org_id ON org_invites(org_id);
CREATE INDEX IF NOT EXISTS idx_org_invites_pubkey ON org_invites(pubkey);
CREATE INDEX IF NOT EXISTS idx_org_invites_expires_at ON org_invites(expires_at);

UPDATE services s
SET org_id = NULL
WHERE org_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM organizations o WHERE o.id = s.org_id
  );

UPDATE environments e
SET org_id = NULL
WHERE org_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM organizations o WHERE o.id = e.org_id
  );

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conname = 'fk_services_org_id'
      AND conrelid = 'services'::regclass
  ) THEN
    ALTER TABLE services
      ADD CONSTRAINT fk_services_org_id
      FOREIGN KEY (org_id) REFERENCES organizations(id) ON DELETE SET NULL;
  END IF;

  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conname = 'fk_environments_org_id'
      AND conrelid = 'environments'::regclass
  ) THEN
    ALTER TABLE environments
      ADD CONSTRAINT fk_environments_org_id
      FOREIGN KEY (org_id) REFERENCES organizations(id) ON DELETE SET NULL;
  END IF;
END $$;
