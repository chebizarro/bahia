# Investigation: Swarmstr Tool Provisioning System

## Summary
Investigating a managed capability model for provisioning tools into swarmstr containers via Nostr events to Bahia, with immutable container rebuilds.

## Symptoms
- Agents need tools (jq, ripgrep, etc.) from various package managers (apt, pip, cargo, npm, bun, conan)
- Current approach: agents cannot self-provision (security risk)
- Need: Orchestrator (Bahia) to handle tool requests with audit trail
- Constraint: Immutable containers — rebuild and hot-swap, not runtime mutation

## Requirements (from interview)
1. **Authorization**: Orchestrator auto-approves from a global registry with audit trail
2. **Request interface**: Nostr events addressed to Bahia
3. **Provisioning**: Immutable approach — rebuild container image and hot-swap

## Background / Prior Research

### Existing Infrastructure Findings

**swarmstr Container Build System** (`scripts/docker/Dockerfile`):
- Already supports modular tool installation via build args:
  - `METIQ_INSTALL_PYTHON` — Python 3 + uv package manager
  - `METIQ_INSTALL_NODE` — Node.js 24.x
  - `METIQ_APT_PACKAGES` — Arbitrary apt packages
  - `METIQ_INSTALL_BROWSER` — Chromium + browser automation deps
  - `METIQ_INSTALL_DOCKER_CLI` — Docker CLI + compose plugin
- Base: Debian Bookworm (slim variant available)
- Uses multi-stage build with Go builder → runtime image

**Bahia Runtime Adapters** (`internal/adapters/runtime/`):
- Full Docker API integration: Deploy, Undeploy, Restart, Stop, StreamLogs
- Supports Docker, Podman, Kubernetes, Docker Compose
- `Deploy()` accepts `DeployOptions` with environment, labels, ports, volumes, command, entrypoint
- `stopAndRemove()` + `pullImage()` pattern for atomic updates

**Bahia Worker Domain** (`internal/domain/worker.go`):
- Workers advertise `Software []WorkerSoftware{Name, Version, Path}`
- `HasSoftware(name)` check for capability matching
- `RuntimeTarget` describes where Bahia deploys work

**swarmstr Plugin Manifest** (`internal/plugins/manifest/schema.go`):
- Rich schema: ID, Version, Runtime, Capabilities, Permissions
- Permissions model: Network, Filesystem, Exec, Secrets, Nostr, Agent, Storage
- Categories: Tools, Channels, Hooks, MCPServers, Skills, Providers
- Validation, parsing, compatibility checks

**swarmstr Plugin Installer** (`internal/plugins/installer/`):
- Handles npm packages and archives
- `ResolveManagedPath()` for safe path resolution
- Could be extended for other package managers

## Investigator Findings

### Key Insight: Reuse LLM Control-Plane Pattern

The existing Bahia LLM provisioning system provides the ideal foundation:
- `internal/controlplane/reactor.go` — Nostr request handling with authorization
- `internal/service/llm_registry.go` — DB-first desired state orchestration
- `internal/service/llm_provisioning_coordinator.go` — Queue claim → provision → observe → promote flow
- `internal/adapters/runtime/docker.go` — Deploy/undeploy/observe primitives

**Model tool provisioning as "toolset-derived OCI image deployment"**, not plugin installation.

## Investigation Log

### Phase 1 - Initial Assessment
**Hypothesis:** Bahia already has provisioning infrastructure we can extend
**Findings:** Confirmed. The LLM control-plane + runtime lifecycle + artifact system provides all necessary primitives.
**Evidence:**
- `llm_registry.go` manages intent → run → state transitions
- `runtime_lifecycle.go` handles deploy/undeploy/restart
- `pg_nostr_event.go` provides audit trail
- Dockerfile already supports modular build args
**Conclusion:** Extend existing patterns rather than building from scratch

---

## Root Cause

No root cause per se — this is a greenfield design. The investigation identifies that:
1. Current swarmstr containers can't receive runtime tool installation (security + immutability constraint)
2. Bahia has all the orchestration machinery, just needs tool-specific event kinds and registry

---

## Proposed Design

### 1. Architecture Overview

```
Agent Container                     Bahia Orchestrator
     │                                    │
     │ ─── Nostr Event (Kind 5976) ──────→│
     │     "Request tools: jq, ripgrep"   │
     │                                    ├── Validate against Global Tool Registry
     │                                    ├── Create Tool Provision Intent
     │                                    ├── Compute toolset hash
     │                                    ├── Check OCI cache for existing image
     │                                    │
     │                                    ├── If miss: Build derived image
     │                                    │   FROM base@sha256:...
     │                                    │   RUN apt-get install jq ripgrep
     │                                    │
     │                                    ├── Push to OCI registry
     │                                    ├── Hot-swap container (stop old, start new)
     │ ←── Nostr Event (Kind 7976) ───────┤
     │     "Complete: new image running"  │
```

### 2. Nostr Event Kinds

Add to `bahia/internal/controlplane/reactor.go`:

```go
const (
    KindToolProvisionRequest = 5976  // Agent → Bahia
    KindToolProvisionStatus  = 6976  // Bahia → Agent (progress)
    KindToolProvisionResult  = 7976  // Bahia → Agent (final)
    KindToolProfileState     = 31966 // Replaceable state record
)
```

**Request Payload:**
```json
{
  "service_id": "uuid",
  "environment_id": "uuid",
  "operation": "add",
  "tools": [
    { "tool_id": "ripgrep" },
    { "tool_id": "jq" },
    { "tool_id": "httpie", "version": "3.2.2" }
  ],
  "reason": "needed for log triage"
}
```

### 3. Security Model: Trust + Scan + Human Approval

**Problem with Allowlists**: Too restrictive, go stale, high maintenance burden.

**Alternative: Implicit Trust + Security Scan Gate + Human Approval Queue**

#### Trust Hierarchy (auto-approve if all pass)

| Source | Trust Level | Auto-Approve? |
|--------|-------------|---------------|
| Official distro repos (apt/Debian) | High | ✅ Yes |
| PyPI (pip) | Medium | ✅ If no CVEs |
| npmjs.com (npm) | Medium | ✅ If no CVEs |
| crates.io (cargo) | Medium | ✅ If no CVEs |
| bun.sh registry | Medium | ✅ If no CVEs |
| Conan Center | Medium | ✅ If no CVEs |
| Private/unknown sources | Low | ❌ Human approval |

#### Security Scan Gate

Leverage existing policy engine (`internal/domain/policy.go`):

```go
const (
    RuleMaxCriticalVulns  PolicyRuleType = "max_critical_vulns"  // existing
    RuleMaxHighVulns      PolicyRuleType = "max_high_vulns"      // existing
    RuleBlockPackage      PolicyRuleType = "block_package"       // existing (denylist)
    RuleRequireApproval   PolicyRuleType = "require_approval"    // existing
    
    // New rules for tool provisioning
    RulePackageMinAge     PolicyRuleType = "package_min_age"     // flag if < 7 days old
    RulePackageMinDownloads PolicyRuleType = "package_min_downloads" // flag if < threshold
    RuleTyposquatCheck    PolicyRuleType = "typosquat_check"     // check against popular names
)
```

#### Decision Flow

```
Request: "install httpie, jq, suspicious-pkg"
    │
    ├─→ httpie (pip)
    │     ├─ Source: PyPI ✓
    │     ├─ CVE scan: 0 critical, 0 high ✓
    │     ├─ Age: 10 years ✓
    │     └─ Result: AUTO-APPROVE
    │
    ├─→ jq (apt)
    │     ├─ Source: Debian official ✓
    │     └─ Result: AUTO-APPROVE (trusted source)
    │
    └─→ suspicious-pkg (npm)
          ├─ Source: npmjs ✓
          ├─ CVE scan: 0 ✓
          ├─ Age: 3 days ⚠️ (< 7 day threshold)
          ├─ Downloads: 12 ⚠️ (< 1000 threshold)
          └─ Result: QUEUE FOR HUMAN APPROVAL
```

#### Human Approval Queue

When auto-approve fails, use existing notification system:

```go
// Dispatch to configured channels (webhook, nostr_dm, or both)
dispatcher.Dispatch(ctx, "tool_provision.approval_required", map[string]any{
    "intent_id":     intent.ID,
    "service_name":  service.Name,
    "requester":     requesterPubkey,
    "tools":         pendingTools,
    "flags":         flags,  // why it needs approval
    "approve_url":   fmt.Sprintf("%s/tools/approve/%s", baseURL, intent.ID),
    "reject_url":    fmt.Sprintf("%s/tools/reject/%s", baseURL, intent.ID),
})
```

Channels (configurable per tenant via existing `NotificationChannel`):
- **Nostr DM** to operator pubkey
- **Webhook** to Slack/Discord/external system
- **Web UI** approval queue at `/tools/pending`

#### Approval Actions

```go
// Nostr event kinds for approval flow
const (
    KindToolApprovalRequest = 5977  // Bahia → Operator
    KindToolApprovalResponse = 7977 // Operator → Bahia (approve/reject)
)

// Or via existing MCP tools
bahia_tool_provision_approve(intent_id, reason)
bahia_tool_provision_reject(intent_id, reason)
```

#### Denylist (Explicit Blocks)

Instead of maintaining an allowlist, maintain a small **denylist** of known-bad packages:

```sql
CREATE TABLE tool_denylist (
    package_name TEXT NOT NULL,
    manager TEXT NOT NULL,
    reason TEXT NOT NULL,
    blocked_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    blocked_by TEXT,
    PRIMARY KEY (package_name, manager)
);
```

This is much lower maintenance — only add entries when threats are discovered.

#### Operator Policy Configuration

```yaml
tool_provisioning:
  # Trust levels
  trusted_sources:
    - apt:debian
    - apt:ubuntu
    - pip:pypi
    - npm:npmjs
    - cargo:cratesio
    - bun:bunsh
  
  # Security thresholds
  max_critical_vulns: 0      # block if > 0
  max_high_vulns: 3          # flag for approval if > 3
  
  # Heuristic flags (trigger human approval)
  min_package_age_days: 7
  min_downloads: 1000
  typosquat_check: true
  
  # Approval routing
  approval_channels:
    - type: nostr_dm
      pubkey: "npub1operator..."
    - type: webhook
      url: "https://slack.com/webhook/..."
```

### 4. Package Manager Support

| Manager | Install Method | Notes |
|---------|----------------|-------|
| **apt** | `apt-get install -y --no-install-recommends pkg=version` | Direct install |
| **pip** | `uv pip install --system package==version` | Requires Python base |
| **cargo** | Multi-stage: `cargo install --locked --version <v> <crate>` | Copy binaries only |
| **npm** | `npm install -g package@version` | Requires Node base |
| **bun** | `bun install -g package@version` | Requires Bun runtime |
| **conan** | Registry entry declares exact reference + deploy layout | Most complex |

### 5. Database Schema

```sql
-- Denylist (low-maintenance blocklist of known-bad packages)
CREATE TABLE tool_denylist (
    package_name TEXT NOT NULL,
    manager TEXT NOT NULL,
    reason TEXT NOT NULL,
    source TEXT,                    -- e.g. "cve-2024-1234", "typosquat", "malware"
    blocked_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    blocked_by TEXT,
    PRIMARY KEY (package_name, manager)
);

-- Tool provision intents with approval workflow
CREATE TABLE tool_provision_intents (
    id UUID PRIMARY KEY,
    service_id UUID NOT NULL REFERENCES services(id),
    environment_id UUID NOT NULL REFERENCES environments(id),
    requested_tools JSONB NOT NULL,           -- [{name, version?, manager}]
    resolved_tools JSONB,                     -- [{name, version, manager, source}]
    security_scan_results JSONB,              -- CVE findings per package
    toolset_hash TEXT,
    status TEXT NOT NULL DEFAULT 'pending',   -- pending, awaiting_approval, approved, rejected, building, completed, failed
    approval_required BOOLEAN NOT NULL DEFAULT false,
    approval_flags JSONB,                     -- why approval is needed
    approved_by TEXT,
    approved_at TIMESTAMPTZ,
    nostr_event_id TEXT,
    requester_pubkey TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Provision runs (build attempts)
CREATE TABLE tool_provision_runs (
    id UUID PRIMARY KEY,
    intent_id UUID NOT NULL REFERENCES tool_provision_intents(id),
    base_image_digest TEXT NOT NULL,
    built_image_digest TEXT,
    artifact_id UUID REFERENCES artifacts(id),
    build_log_url TEXT,
    status TEXT NOT NULL DEFAULT 'pending',
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    error_message TEXT
);

-- Current toolset state per service/environment
CREATE TABLE tool_profile_state (
    service_id UUID NOT NULL REFERENCES services(id),
    environment_id UUID NOT NULL REFERENCES environments(id),
    current_toolset_hash TEXT,
    current_image_digest TEXT,
    installed_tools JSONB NOT NULL DEFAULT '[]',
    previous_image_digest TEXT,               -- for rollback
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (service_id, environment_id)
);

-- Approval audit log
CREATE TABLE tool_approval_log (
    id UUID PRIMARY KEY,
    intent_id UUID NOT NULL REFERENCES tool_provision_intents(id),
    action TEXT NOT NULL,                     -- requested, approved, rejected
    actor_pubkey TEXT,
    reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

### 6. Build Strategy

Generate **derived Dockerfile** from base image:

```dockerfile
FROM ghcr.io/.../swarmstr-base@sha256:<current_digest>

COPY tools.lock.json /tmp/tools.lock.json
COPY install-tools.sh /usr/local/bin/install-tools
RUN /usr/local/bin/install-tools /tmp/tools.lock.json

LABEL io.bahia.toolset.hash="sha256:..."
LABEL io.bahia.source_event="<nostr_event_id>"
LABEL io.bahia.tools="jq,ripgrep,httpie"
```

**Toolset Hash** computed from:
- Base image digest
- Sorted resolved tool entries (tool_id + version + manager)
- Installer script schema version

**Caching**: If image with matching toolset hash exists, reuse it (no rebuild).

### 7. Implementation Layout

```
bahia/
  internal/
    domain/tool_provisioning.go          # New domain types
    service/tool_registry.go             # Registry validation
    service/tool_provisioning_coordinator.go  # Orchestration
    controlplane/tool_responder.go       # Nostr event handling
    adapters/build/docker.go             # Image building
    repository/pg_tool_provisioning.go   # Persistence
    db/migrations/000020_tool_provisioning.up.sql

  edits to existing files:
    internal/controlplane/reactor.go     # New event kinds
    internal/adapters/nostr/subscriber.go
    internal/adapters/nostr/publisher.go
    internal/repository/interfaces.go
    internal/service/runtime_lifecycle.go

swarmstr/
  scripts/docker/
    install-sh-common/install-tools.sh   # Main dispatcher
    install-sh-common/install-tools-apt.sh
    install-sh-common/install-tools-pip.sh
    install-sh-common/install-tools-cargo.sh
    install-sh-common/install-tools-npm.sh
    install-sh-common/install-tools-bun.sh
    install-sh-common/install-tools-conan.sh
```

---

## Recommendations

### Phase 1: Core Infrastructure (MVP)
1. Add tool request Nostr kinds to `reactor.go` (5976/6976/7976)
2. Add security scan integration (OSV database, npm audit, pip-audit)
3. Implement intent/run/state tables with approval workflow
4. Build `tool_provisioning_coordinator.go` following LLM pattern
5. Support **apt only** initially (inherently trusted, no CVE scan needed)
6. Add approval notification dispatch (reuse existing `notifications/dispatcher.go`)

### Phase 2: Security Scan + Package Manager Expansion
1. Integrate CVE scanning for pip/npm/cargo
2. Add heuristic checks (package age, download count)
3. Implement denylist table
4. Add pip/npm support with security gates
5. Build Web UI approval queue at `/tools/pending`

### Phase 3: Advanced Features
1. Add cargo/bun/conan support
2. Typosquatting detection
3. Image layer caching optimization
4. Blue/green deployment for zero-downtime swaps
5. SBOM generation for provisioned toolsets
6. Nostr-native approval flow (kind 5977/7977)

---

## Preventive Measures

### Security
- **No user-supplied shell commands** — registry-driven fixed installers only
- **Auto-approve only from global registry** — reject unknown tools
- **Audit trail** — all events persisted in `nostr_events`
- **Signature verification** — leverage existing cosign infrastructure

### Reliability
- **Atomic swaps** — old container runs until new is verified healthy
- **Rollback capability** — previous image digest stored in state
- **Idempotency** — toolset hash enables deduplication

### Maintainability
- **Schema versioning** — registry entries and installers versioned
- **Single-responsibility** — separate files per package manager
- **Test coverage** — installer scripts have verification commands
