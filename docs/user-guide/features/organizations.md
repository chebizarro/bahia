# Organizations

**Organizations** in Bahia provide team management, access control, and resource ownership.

## Overview

Organizations enable:
- **Team collaboration** — Multiple users working together
- **Access control** — Role-based permissions
- **Resource ownership** — Services, environments, policies belong to orgs

## Creating Organizations

### Web UI

The Organizations web route is protected by signer-first authentication and currently requires backend REST compatibility auth. If the backend does not advertise `direct_nostr_http_auth`, the page fails closed with a compatibility message and does not issue organization REST requests.

1. Navigate to **Organizations** in the sidebar
2. Click **New Organization**
3. Fill in:
   - **Name**: Unique org identifier (e.g., `acme-corp`)
   - **Display Name**: Human-readable name (e.g., "ACME Corporation")
4. Click **Create**

### CLI

```bash
bahia orgs create \
  --name "acme-corp" \
  --display-name "ACME Corporation"
```

### MCP Tool (Encrypted)

Organization operations use encrypted Nostr:

```json
{
  "tool": "bahia_org_create",
  "arguments": {
    "name": "acme-corp",
    "display_name": "ACME Corporation"
  }
}
```

## Organization Roles

| Role | Permissions |
|------|-------------|
| **owner** | Full access, can delete org, manage all members |
| **admin** | Manage members, settings, all resources |
| **editor** | Create/modify services, deployments, etc. |
| **viewer** | Read-only access to org resources |

### Role Hierarchy

```
owner > admin > editor > viewer
```

Higher roles inherit all lower role permissions.

## Managing Members

### Adding Members

**Via Web UI:**
1. Go to organization detail
2. Click **Members** tab
3. Click **Invite Member**
4. Enter pubkey and role
5. Click **Send Invite**

**Via CLI:**
```bash
bahia orgs invite acme-corp \
  --pubkey "npub1newmember..." \
  --role editor
```

### Accepting Invites

Members must accept invites:

**Via Web UI:**
1. Go to **My Invites** (in user menu)
2. Review invite details
3. Click **Accept** or **Decline**

**Via CLI:**
```bash
bahia orgs invites accept invite-123
```

### Updating Member Roles

```bash
bahia orgs members update acme-corp \
  --pubkey "npub1member..." \
  --role admin
```

### Removing Members

```bash
bahia orgs members remove acme-corp \
  --pubkey "npub1member..."
```

## Viewing Organizations

### Web UI

The **Organizations** page shows:
- Orgs you belong to
- Your role in each org
- Pending invites

Click an org to see:
- **Overview**: Org info and stats
- **Members**: Current members and roles
- **Invites**: Pending invitations
- **Settings**: Edit org (admins+)

### CLI

```bash
# List orgs you belong to
bahia orgs list

# Get org details
bahia orgs get acme-corp

# List members
bahia orgs members list acme-corp

# List your invites
bahia orgs invites list
```

## Organization Resources

Resources can be scoped to organizations:

### Services

```bash
bahia services create \
  --name "payment-api" \
  --org-id "org-123"
```

### Environments

```bash
bahia environments create \
  --name "Production" \
  --org-id "org-123"
```

### Policies

Policy creation is signer-first. Publish a signed Nostr `PolicyCreate` event scoped to the organization instead of using the deprecated REST-backed policy mutation command path.

## Access Control

### Resource Visibility

| Resource | Visibility |
|----------|------------|
| Services | Org members only |
| Environments | Org members only |
| Artifacts | Service org members |
| Deployments | Service org members |
| Policies | Org members only |
| Notifications | Org admins+ |

### Action Permissions

| Action | Required Role |
|--------|---------------|
| View resources | viewer+ |
| Create services | editor+ |
| Deploy | editor+ |
| Manage policies | admin+ |
| Manage members | admin+ |
| Delete org | owner |

## Deleting Organizations

Only owners can delete organizations:

### Web UI

1. Go to org **Settings**
2. Scroll to **Danger Zone**
3. Click **Delete Organization**
4. Confirm deletion

### CLI

```bash
bahia orgs delete acme-corp
```

**Warning**: Deleting an org:
- Removes all org-scoped resources
- Revokes all member access
- Cannot be undone

## Encrypted Request/Result Facade

Organization operations use the **encrypted request/result facade** over Nostr events (`5980` requests and `7980` terminal results):

- The browser signs and encrypts a scoped org operation such as `orgs.create`, `orgs.list`, or `orgs.delete`.
- Bahia decrypts the request, validates the requester, applies RBAC/repository changes, and publishes an encrypted terminal result correlated to the request event id.
- Member lists, invites, and org CRUD responses are not public Nostr read models; durable org state remains repository-backed and is returned only through encrypted request/result responses.
- The UI treats relay `OK`, `AUTH`, `CLOSED`, and encrypted terminal result outcomes according to the shared request/result lifecycle contract.

This requires a NIP-44 capable signer and configured encrypted relay/service pubkey settings.

## Best Practices

1. **Use meaningful names** — Help members identify orgs
2. **Principle of least privilege** — Start with viewer, elevate as needed
3. **Regular audits** — Review member list periodically
4. **Document roles** — Clarify who can do what
5. **Don't over-share ownership** — Limit owners to trusted admins

## Troubleshooting

### Can't Create Org

- Verify your pubkey is in `auth.bootstrap_owner_pubkeys`
- Check NIP-44 signer capability

### Invite Not Received

- Invites are encrypted — check encrypted relay connectivity
- Verify pubkey is correct

### Permission Denied

- Check your role in the org
- Verify resource belongs to your org

## Related

- [Services](services.md) — Org-owned resources
- [Policies](policies.md) — Org access policies
- [Notifications](notifications.md) — Org alerts
