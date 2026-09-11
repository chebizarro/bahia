# Organizations

**Organizations** in Bahia provide team management, access control, and resource ownership.

## Overview

Organizations enable:
- **Team collaboration** — Multiple users working together
- **Access control** — Role-based permissions
- **Resource ownership** — Services, environments, policies belong to orgs

## Creating Organizations

### Web UI

The Organizations web route is protected by signer-first authentication. The browser performs every organization operation through encrypted ContextVM requests (see [Encrypted request/result facade](#encrypted-requestresult-facade)); if Bahia service pubkey discovery or standard Bahia relays are not configured, the page fails closed instead of falling back to REST.

1. Navigate to **Orgs** in the sidebar
2. Click **+ New Organization**
3. Fill in:
   - **Name**: Unique org identifier (e.g., `acme-corp`)
   - **Display Name**: Human-readable name (e.g., "ACME Corporation")
4. Click **Create Organization**

### CLI

```bash
bahia orgs create acme-corp --display-name "ACME Corporation"
```

Unlike most CLI mutations, the `orgs` commands call the REST compatibility API (`/api/v1/orgs`, NIP-98 authenticated) rather than publishing ContextVM requests.

### MCP

Bahia does not currently expose `bahia_org_*` MCP tools.

## Organization Roles

| Role | Permissions |
|------|-------------|
| **owner** | Full access, can delete org, manage all members |
| **admin** | Manage members, settings, all resources |
| **deployer** | Create and manage deployment-related resources |
| **viewer** | Read-only access to org resources |

### Role Hierarchy

```
owner > admin > deployer > viewer
```

Higher roles inherit all lower role permissions.

## Managing Members

### Adding Members

**Via Web UI:**
1. Go to organization detail
2. In the **Members** section, click **Invite Member**
3. Enter pubkey and role
4. Click **Send Invite**

**Via CLI:**
```bash
bahia orgs members add org-123 npub1newmember... --role deployer
```

### Accepting Invites

Members can accept pending encrypted invites from the Organizations page in the web UI. The current CLI does not expose an `orgs invites` subcommand.

### Updating Member Roles

The current CLI does not expose a dedicated member-role update subcommand. Role changes should use the encrypted web flow or the underlying encrypted request/result operations.

### Removing Members

```bash
bahia orgs members remove org-123 npub1member...
```

## Viewing Organizations

### Web UI

The **Organizations** page shows:
- Orgs you belong to
- Your role in each org
- Pending invites

Click an org to see:
- **Members**: Current members and roles, with **Invite Member**
- **Pending Invites**: Outstanding invitations (revocable)
- **Danger Zone**: Delete the organization (owner)

### CLI

```bash
# List orgs you belong to
bahia orgs list

# Get org details
bahia orgs get acme-corp

# List members
bahia orgs members list org-123
```

## Organization Resources

Resources can be scoped to organizations:

### Services

```bash
bahia services create \
  --name "payment-api" \
  --artifact-repo "ghcr.io/company/payment-api"
```

### Environments

```bash
bahia environments create \
  --org <org-uuid> \
  --name "production" \
  --strategy replace \
  --protected
```

Pass `--org <org-uuid>` to `bahia services create` as well to bind a service to an organization.

### Policies

Policy creation is signer-first. Publish a ContextVM `policy/create` command scoped to the organization (or `bahia policies create`); there are no REST policy mutation routes. Policy mutations require the `policies:write` permission, granted to admin and owner roles.

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
| Create services | deployer+ |
| Deploy | deployer+ |
| Manage policies | admin+ (`policies:write`) |
| Manage LLM routes | admin+ (`llm_routes:write`) |
| Manage members | admin+ |
| Delete org | owner |

## Deleting Organizations

Organization deletion is available from the org detail page's **Danger Zone** (encrypted `orgs.delete` operation). The current CLI does not expose `bahia orgs delete`.

## Encrypted Request/Result Facade

Web organization operations use the **encrypted request/result facade**: ContextVM kind `25910` messages wrapped in NIP-59 gift wraps (`1059`, or ephemeral `21059`). The legacy `5980`/`7980` encrypted request/result kinds are migration inputs only.

- The browser signs and encrypts a scoped org operation: `orgs.list`, `orgs.my_invites`, `orgs.detail`, `orgs.create`, `orgs.delete`, `orgs.create_invite`, `orgs.revoke_invite`, `orgs.accept_invite`, `orgs.update_member_role`, or `orgs.remove_member`.
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
