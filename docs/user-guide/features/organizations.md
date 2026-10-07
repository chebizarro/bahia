# Organizations

Organizations are Bahia's tenancy boundary. Services, environments, deployments, secrets, notification channels, and their confidential state belong to an organization.

## Roles

| Role | Access |
|---|---|
| `viewer` | Read organization resources |
| `deployer` | Viewer access plus deployment submission and approval |
| `admin` | Deployer access plus resource, secret, policy, and channel administration |
| `owner` | Admin access plus membership and organization settings |

The daemon authorizes the verified signer for every action. UI visibility does not grant a role.

## Create and manage

Open **Orgs** (`/orgs`) or use:

```bash
bahia orgs list
bahia orgs get <id-or-name>
bahia orgs create acme --display-name "ACME"
bahia orgs members list <org-id>
bahia orgs members add <org-id> <pubkey> --role deployer
bahia orgs members remove <org-id> <pubkey>
bahia orgs invites create <org-id> <pubkey> --role viewer --expires-in 72
bahia orgs invites delete <org-id> <invite-id>
bahia orgs delete <org-id>
```

Creating an organization requires a fleet operator or its configured bootstrap owner. Membership and invite records are signed canonical state; invite acceptance and role changes are checked against the current organization.

## Confidential state and OCK

Bahia encrypts organization-confidential records with an organization content key (OCK) using the NIP-CAS-0011 envelope. The daemon wraps the current OCK to each authorized member's pubkey. A signer without a valid envelope sees **not readable with this key** rather than plaintext or an empty substitute.

Membership removal rotates the key while excluding the removed member. **Refounding** republishes current confidential records under the current key epoch so authorized members converge on one readable version.

`strict_revocation` defaults to `false`. When enabled, member removal or role downgrade rotates and refounds before the membership change is committed. If refounding fails, the membership change fails and can be retried. Without strict revocation, rotation protects newly published state while existing ciphertext remains encrypted under its recorded epoch.

Fleet-confidential state uses the fleet OCK. Scoped operator allowlists are encrypted canonical records addressed as `operators:<scope>`, such as `operators:continuity` and `operators:soul-factory`.

## Safety

- Use owner sparingly and admin for routine administration.
- Verify a member's hex pubkey out of band.
- Enable strict revocation where removing historical read access is required.
- Wait for the new membership and key-envelope records before assuming a role change is visible on every relay.
- Retry with the same intent ID after a timeout.

## Related

- [Services](services.md)
- [Notifications](notifications.md)
- [Policies](policies.md)
- [Nostr Integration](../nostr-integration.md)
