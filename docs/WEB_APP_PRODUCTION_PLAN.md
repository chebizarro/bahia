# Bahia Web App Production Readiness Plan

> **⚠️ Historical planning document with a stale snapshot section**
>
> This file mixes a preserved web-app roadmap with a short 2026-04-29 status snapshot. It is **not** the source of truth for Bahia's current auth, transport, or control-plane behavior.
>
> Current authoritative docs:
> - `docs/control-planes.md`
> - `docs/architecture.md`
> - `docs/api.md`
> - `docs/web-app-setup.md`
> - `docs/web-testing.md`
>
> Historical terminology in this file such as **REST-first API-client assumptions**, **planned NIP-46**, or **missing Notifications UI** reflects the planning context at the time and may now be obsolete.
>
> **Status**: Historical planning artifact; not current product contract
> **Created**: 2026-04-29  
> **Updated**: 2026-05-05 (reclassified as historical / non-authoritative)
> **Scope**: Historical web planning, migration staging, and roadmap context

---

## Snapshot Note

The short status summary below captures what this document claimed on 2026-04-29. Treat it as a dated snapshot, not as current truth. Round 6 focused on hardening the then-current web UI with expanded test coverage, performance optimizations, and supporting documentation.

### ✅ Completed in Round 6

**Test Coverage**:
- ✅ **Unit Tests**: Extended coverage for API client methods, global stores, Soul Factory stores, and Nostr parsing utilities
- ✅ **E2E Smoke Tests**: Added coverage for environments CRUD, service secrets, workers/events pages, and dashboard
- ✅ **Test Frameworks**: Vitest (unit) and Playwright (E2E) fully configured

**Performance Optimizations**:
- ✅ **Dashboard**: Optimized pending deployments aggregation with caching, bounded concurrency, and store reuse
- ✅ **Global Stores**: Added in-flight request deduplication, `loadAll()` freshness guard, and relay-backed read-model refreshes

**Documentation**:
- ✅ **Setup Guide**: `docs/web-app-setup.md` - Prerequisites, running the app, auth, troubleshooting
- ✅ **API Client Guide**: `docs/web-api-client.md` - Client methods, Bahia envelope, error handling, examples
- ✅ **Component Library**: `docs/web-components.md` - Reusable components, props/events, accessibility
- ✅ **Testing Guide**: `docs/web-testing.md` - Vitest/Playwright commands, mocking patterns, conventions

### Production-Ready Features

**UI/UX**:
- ✅ Full CRUD for Services, Environments, Policies, Secrets
- ✅ Deployment workflow (create intent, approve/reject, view runs)
- ✅ Dashboard with real-time Nostr relay updates
- ✅ Soul Factory provisioning with NIP-07 Nostr signing
- ✅ Workers, events, and states views
- ✅ Comprehensive component library (forms, modals, feedback, tables)

**Backend Integration**:
- ✅ API client with 100% endpoint coverage
- ✅ Direct NIP-98 authentication with NIP-07 signing
- ✅ Bahia envelope unwrapping and error normalization
- ✅ Nostr sidecar read models for real-time updates

**Code Quality**:
- ✅ Test coverage for critical paths (unit + E2E smoke tests)
- ✅ Performance-optimized data loading and caching
- ✅ Accessibility-focused components (WCAG 2.1 AA)
- ✅ Production build pipeline (`vite build`)

### Snapshot Caveat

The limitation bullets that originally appeared here are not maintained as current product truth. Some of those items have since changed materially. Use the authoritative docs listed above for the current state of NIP-46 support, notification flows, auth behavior, and transport architecture.

---

## Historical Context: Original Production Plan

The sections below reflect the **original production plan** from before Round 6. They remain for historical reference and to guide future enhancements beyond the current production-ready state.

---

## Executive Summary (Original Plan)

The Bahia web app is currently a **read-only demo**. This plan outlines the work required to make it production-ready with full CRUD capabilities, proper error handling, authentication, and 100% API coverage.

**Current State:**
- ~4,000 lines of Svelte/JS code
- 16 route files, 7 components, 3 stores
- Zero tests
- Read-only views for all entities
- Simulated Soul Factory provisioning
- Hardcoded relay "Connected" status

**Target State:**
- Full CRUD UI for Services, Environments, Policies, Secrets
- Complete deployment workflow (create, approve, reject, rollback)
- Nostr relay realtime with proper connection state
- Authentication flow with Nostr (NIP-07/NIP-46)
- Real Soul Factory provisioning via Nostr
- Comprehensive test coverage
- 100% REST API client coverage

---

## Architecture Overview

### Current Stack
```
┌─────────────────────────────────────────────────────────────┐
│                     SvelteKit Web App                       │
│  ┌─────────────┐  ┌─────────────┐  ┌──────────────────────┐ │
│  │   Routes    │  │  Stores     │  │   API Client         │ │
│  │  (read-only)│  │  (basic)    │  │   (21 methods,       │ │
│  │             │  │             │  │    2 unused writes)  │ │
│  └─────────────┘  └─────────────┘  └──────────────────────┘ │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                   Go REST API (Chi Router)                  │
│  ┌─────────────────────────────────────────────────────────┐│
│  │  Full CRUD: Services, Environments, Policies, Secrets   ││
│  │  Deployments: Intents, Runs, Approve/Reject, Rollback   ││
│  │  Registry: Builds, Artifacts, SBOM, Signatures          ││
│  │  Workers, Payments, Notifications, OCI Registry         ││
│  └─────────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────┘
```

### Target Stack
```
┌─────────────────────────────────────────────────────────────┐
│                     SvelteKit Web App                       │
│  ┌─────────────┐  ┌─────────────┐  ┌──────────────────────┐ │
│  │   Routes    │  │  Stores     │  │   API Client         │ │
│  │  Full CRUD  │  │  + Auth     │  │   100% coverage      │ │
│  │  + Forms    │  │  + relay state│  │   + error handling   │ │
│  │  + Modals   │  │  + cache    │  │   + retry logic      │ │
│  └─────────────┘  └─────────────┘  └──────────────────────┘ │
│  ┌─────────────────────────────────────────────────────────┐│
│  │   Nostr Client (NIP-07/NIP-46 signing)                  ││
│  └─────────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────┘
```

---

## Epic 1: Infrastructure & Foundation

### 1.1 Error Handling & Boundaries
- Add global error boundary component
- Implement toast notification system for errors
- Add retry logic to API client with exponential backoff
- Create error state components for empty/error/loading states

### 1.2 Authentication System
- Implement Nostr NIP-07 browser extension detection
- Add NIP-46 (Nostr Connect/bunker) support for signing
- Create auth store with login/logout/session management
- Add protected route wrapper component
- Implement per-request NIP-98 signing for protected HTTP calls

### 1.3 Nostr Relay Connection Management
- Create relay connection state store (discovering/connecting/bootstrapping/live/error)
- Add automatic reconnection with backoff
- Surface connection status in UI (replace hardcoded "Connected")
- Add event filtering UI controls

### 1.4 Testing Infrastructure
- Set up Vitest for unit tests
- Add Playwright for E2E tests
- Create test utilities (mocks, fixtures)
- Add CI workflow for tests

### 1.5 Component Library
- Create form components: Input, Select, Textarea, Checkbox
- Create Modal/Dialog component with accessibility
- Create Confirmation dialog component
- Create LoadingButton component with spinner
- Add form validation utilities (Zod schema integration)

---

## Epic 2: API Client Completeness (100% Coverage)

### 2.1 Service Operations
```javascript
// Existing (read-only)
listServices()
getService(id)

// Missing (to add)
createService({ name, artifact_repo, runtime_type, default_branch, repo_url })
updateService(id, { name?, artifact_repo?, runtime_type?, default_branch?, repo_url? })
deleteService(id, force?)
```

### 2.2 Environment Operations
```javascript
// Existing
listEnvironments()
getEnvironment(id)

// Missing
createEnvironment({ name, loom_worker_selector, runtime_config, deploy_strategy, protected })
updateEnvironment(id, { name?, loom_worker_selector?, runtime_config?, deploy_strategy?, protected? })
deleteEnvironment(id)
```

### 2.3 Deployment Operations
```javascript
// Existing
listIntents(serviceId, envId)
getIntent(id)
createIntent(serviceId, envId, artifactId)  // exists but unused
approveIntent(id)  // exists but unused

// Missing
rejectIntent(id)
listRuns(intentId)
getRun(id)
completeRun(id, { status, exit_code })
rollback({ service_id, environment_id, requested_by })
```

### 2.4 Policy Operations
```javascript
// Existing
listPolicies()
getPolicy(id)

// Missing
createPolicy({ name, environment_id, rules, enforcement, enabled })
updatePolicy(id, { name?, rules?, enforcement?, enabled? })
deletePolicy(id)
evaluatePolicy({ artifact_id, environment_id })
```

### 2.5 Secret Operations
```javascript
// Existing
listSecrets(serviceId)

// Missing
createSecret(serviceId, { name, value })
updateSecret(serviceId, secretId, { value })
deleteSecret(serviceId, secretId)
```

### 2.6 Build & Artifact Operations
```javascript
// Existing
listBuilds(serviceId)
listArtifacts(serviceId)

// Missing
getBuild(id)
registerBuild({ service_id, git_sha, git_ref, ci_system, ci_run_id, status })
updateBuildStatus(id, { status })
getArtifact(id)
registerArtifact({ build_id, service_id, image_repo, image_tag, image_digest, ... })
```

### 2.7 Worker Operations
```javascript
// Existing
listWorkers()
getWorker(pubkey)

// Missing
getWorkerPricing(pubkey)
```

### 2.8 Notification Operations
```javascript
// Missing (all)
listChannels()
getChannel(id)
createChannel({ name, channel_type, config, event_filter, enabled })
updateChannel(id, { name?, config?, event_filter?, enabled? })
deleteChannel(id)
testChannel(id)
listNotificationLogs()
```

### 2.9 SBOM & Signature Operations
```javascript
// Missing (all)
getSBOM(artifactId)
getSBOMPackages(artifactId)
searchSBOMPackages(query)
ingestSBOM(artifactId, sbomData)
listSignatures(artifactId)
listVerifiedSignatures(artifactId)
hasVerifiedSignature(artifactId)
verifySignatures(artifactId)
```

### 2.10 Payment Operations
```javascript
// Missing (all)
estimateCost({ run_id, estimated_duration })
getRunCost(runId)
getPaymentHistory()
```

### 2.11 State & Observation Operations
```javascript
// Existing
listStates()
listDriftedStates()

// Missing
getStateByServiceEnv(serviceId, envId)
listStatesByEnvironment(envId)
recordObservation({ service_id, environment_id, observed_image_digest, health_status, ... })
```

---

## Epic 3: Services Management UI

### 3.1 Services List Page Enhancement
- Add "Create Service" button
- Add search/filter functionality
- Add bulk actions (delete selected)
- Add pagination

### 3.2 Create Service Form
- Form with fields: name, artifact_repo, runtime_type, default_branch, repo_url
- Runtime type dropdown (docker, etc.)
- Form validation with Zod
- Success/error toast notifications

### 3.3 Service Detail Page Enhancement
- Add "Edit" and "Delete" buttons
- Add inline editing for fields
- Show deployment history
- Add "Deploy" action button
- Add secrets management section

### 3.4 Edit Service Modal/Page
- Pre-populated form with current values
- Show what changed before saving
- Confirm unsaved changes on navigation

### 3.5 Delete Service Confirmation
- Confirmation dialog with service name
- Warning about cascading effects
- Force delete option for services with deployments

---

## Epic 4: Environments Management UI

### 4.1 Environments List Page Enhancement
- Add "Create Environment" button
- Add filter by protected status
- Show deployment status per environment

### 4.2 Create Environment Form
- Fields: name, loom_worker_selector, runtime_config, deploy_strategy, protected
- Deploy strategy dropdown (rolling, blue-green, canary)
- JSON editor for runtime_config

### 4.3 Environment Detail Page
- Create new route `/environments/[id]`
- Show services deployed to this environment
- Show deployment history
- Show current state (drifted/in-sync)

### 4.4 Edit/Delete Environment
- Edit form modal
- Delete confirmation with protection warning

---

## Epic 5: Deployment Workflow UI

### 5.1 Deployment Intent Creation
- "Deploy" button on service detail page
- Modal to select: environment, artifact
- Show estimated cost before confirming
- Display policy evaluation results

### 5.2 Deployment Approvals
- Pending approvals dashboard/list
- Approve/Reject buttons with confirmation
- Show who requested deployment
- Show what's changing (diff view)

### 5.3 Deployment History
- List of all deployment intents with status
- Filter by: service, environment, status
- Show run details (logs, duration, exit code)

### 5.4 Deployment Runs View
- Real-time log streaming via the log-follow endpoint
- Progress indicator
- Ability to view stdout/stderr

### 5.5 Rollback UI
- Rollback button on deployment history
- Confirmation with impact warning
- Target selection (previous version or specific artifact)

---

## Epic 6: Policies Management UI

### 6.1 Policies List Enhancement
- Add "Create Policy" button
- Filter by: enforcement level, enabled status
- Show rule count per policy

### 6.2 Create/Edit Policy Form
- Name, environment selector, enforcement level, enabled toggle
- Rule builder UI (visual or JSON)
- Test policy before saving

### 6.3 Policy Detail Page
- Show full rule configuration
- Show evaluation history
- Test against specific artifacts

### 6.4 Policy Evaluation UI
- Manual evaluation tool
- Show pass/fail with reasons
- Integration with deployment flow

---

## Epic 7: Secrets Management UI

### 7.1 Secrets Section in Service Detail
- List secrets (names only, values hidden)
- Create new secret form
- Update secret value
- Delete secret confirmation

### 7.2 Secret Value Security
- Reveal on click (with warning)
- Copy to clipboard functionality
- Audit log for secret access

---

## Epic 8: Workers & Payments UI

### 8.1 Workers List Enhancement
- Show pricing details
- Filter by capabilities
- Show availability status

### 8.2 Worker Detail Page
- Create `/workers/[pubkey]` route
- Show full worker profile
- Show job history
- Show pricing tiers

### 8.3 Payment History
- Create `/payments` route
- List all payments
- Filter by date, worker, service
- Export functionality

### 8.4 Cost Estimation
- Integrate cost estimates into deployment flow
- Show historical cost trends

---

## Epic 9: Notifications Management UI

### 9.1 Notifications Settings Page
- Create `/settings/notifications` route
- List notification channels
- Enable/disable toggles

### 9.2 Channel Management
- Create channel form (Slack, webhook, email)
- Test channel button
- Edit channel configuration

### 9.3 Notification Log
- View sent notifications
- Filter by channel, event type
- Resend failed notifications

---

## Epic 10: Soul Factory Production

### 10.1 NIP-07 Integration
- Detect browser extension (nos2x, Alby, etc.)
- Request signing permission
- Sign provisioning events

### 10.2 NIP-46 (Nostr Connect) Support
- Bunker connection flow
- Remote signing for provisioning
- Session management

### 10.3 Real Provisioning Flow
- Publish actual provisioning request events
- Listen for real progress events
- Handle errors from factory

### 10.4 Soul Management
- Edit soul details
- Suspend/reactivate souls
- View soul activity

---

## Epic 11: Dashboard Enhancement

### 11.1 Dashboard Widgets
- Deployment activity feed (real-time)
- Drift alerts with action buttons
- Resource utilization charts
- Cost summary widget

### 11.2 Quick Actions
- Deploy most recent artifact
- Approve pending deployments
- Fix drifted environments

### 11.3 Customizable Layout
- Drag-and-drop widget arrangement
- Save layout preferences
- Default layouts by role

---

## Epic 12: Testing & Quality

### 12.1 Unit Tests
- API client methods
- Store logic
- Utility functions
- Form validation

### 12.2 Component Tests
- Form components
- Modal interactions
- Table sorting/filtering
- Nostr relay connection handling

### 12.3 Integration Tests
- Auth flow
- CRUD operations
- Deployment workflow
- Error handling

### 12.4 E2E Tests
- Happy path: create service → deploy → approve
- Error scenarios
- Auth protected routes
- Real-time updates

---

## Implementation Phases

### Phase 1: Foundation (2 weeks)
- Epic 1: Infrastructure & Foundation
- Epic 2: API Client Completeness
- Basic unit test setup

### Phase 2: Core CRUD (3 weeks)
- Epic 3: Services Management
- Epic 4: Environments Management
- Epic 7: Secrets Management

### Phase 3: Deployment Workflow (2 weeks)
- Epic 5: Deployment Workflow
- Epic 6: Policies Management

### Phase 4: Extended Features (2 weeks)
- Epic 8: Workers & Payments
- Epic 9: Notifications
- Epic 11: Dashboard Enhancement

### Phase 5: Soul Factory (2 weeks)
- Epic 10: Soul Factory Production

### Phase 6: Quality & Polish (2 weeks)
- Epic 12: Testing & Quality
- Performance optimization
- Accessibility audit
- Documentation

---

## API Coverage Checklist

| Domain | Backend Routes | API Client | UI | MCP Tool |
|--------|---------------|------------|-----|----------|
| **Services** | | | | |
| List | ✅ | ✅ | ✅ | ✅ `bahia_list_services` |
| Get | ✅ | ✅ | ✅ | ✅ `bahia_get_service` |
| Create | signer-first Nostr only | ❌ REST mutation removed | signer-first Nostr | deprecated `bahia_create_service` returns migration error |
| Update | signer-first Nostr only | ❌ REST mutation removed | signer-first Nostr | deprecated `bahia_update_service` returns migration error |
| Delete | signer-first Nostr only | ❌ REST mutation removed | signer-first Nostr | deprecated `bahia_delete_service` returns migration error |
| **Environments** | | | | |
| List | ✅ | ✅ | ✅ | ✅ `bahia_list_environments` |
| Get | ✅ | ✅ | ❌ | ✅ `bahia_get_environment` |
| Create | signer-first Nostr only | ❌ REST mutation removed | signer-first Nostr | deprecated `bahia_create_environment` returns migration error |
| Update | signer-first Nostr only | ❌ REST mutation removed | signer-first Nostr | deprecated `bahia_update_environment` returns migration error |
| Delete | signer-first Nostr only | ❌ REST mutation removed | signer-first Nostr | deprecated `bahia_delete_environment` returns migration error |
| **Deployments** | | | | |
| Create Intent | ✅ | ✅ | ❌ | ✅ `bahia_deploy` |
| Get Intent | ✅ | ✅ | ❌ | ✅ `bahia_get_deployment_status` |
| List Intents | ✅ | ✅ | ❌ | ✅ `bahia_list_intents` |
| Approve | ✅ | ✅ | ❌ | ✅ `bahia_approve_deployment` |
| Reject | ✅ | ❌ | ❌ | ✅ `bahia_reject_deployment` |
| Create Run | ✅ | ❌ | ❌ | ❌ |
| Get Run | ✅ | ✅ | ❌ | ❌ |
| List Runs | ✅ | ❌ | ❌ | ✅ `bahia_list_runs` |
| Complete Run | ✅ | ❌ | ❌ | ❌ |
| Rollback | ✅ | ❌ | ❌ | ✅ `bahia_rollback` |
| **Policies** | | | | |
| List | ✅ | ✅ | ✅ | ❌ |
| Get | ✅ | ✅ | ❌ | ❌ |
| Create | ✅ | ❌ | ❌ | ❌ |
| Update | ✅ | ❌ | ❌ | ❌ |
| Delete | ✅ | ❌ | ❌ | ❌ |
| Evaluate | ✅ | ❌ | ❌ | ❌ |
| **Secrets** | | | | |
| List | ✅ | ✅ | ✅ | ❌ |
| Create | ✅ | ❌ | ❌ | ❌ |
| Update | ✅ | ❌ | ❌ | ❌ |
| Delete | ✅ | ❌ | ❌ | ❌ |
| **Builds** | | | | |
| List | ✅ | ✅ | ✅ | ✅ `bahia_list_builds` |
| Get | ✅ | ❌ | ❌ | ✅ `bahia_get_build` |
| Register | ✅ | ❌ | ❌ | ❌ |
| Update Status | ✅ | ❌ | ❌ | ❌ |
| **Artifacts** | | | | |
| List | ✅ | ✅ | ✅ | ✅ `bahia_list_artifacts` |
| Get | ✅ | ❌ | ❌ | ✅ `bahia_get_artifact` |
| Register | ✅ | ❌ | ❌ | ❌ |
| **Workers** | | | | |
| List | ✅ | ✅ | ✅ | ❌ |
| Get | ✅ | ✅ | ❌ | ❌ |
| Pricing | ✅ | ❌ | ❌ | ❌ |
| **Notifications** | | | | |
| List Channels | ✅ | ❌ | ❌ | ❌ |
| Get Channel | ✅ | ❌ | ❌ | ❌ |
| Create Channel | ✅ | ❌ | ❌ | ❌ |
| Update Channel | ✅ | ❌ | ❌ | ❌ |
| Delete Channel | ✅ | ❌ | ❌ | ❌ |
| Test Channel | ✅ | ❌ | ❌ | ❌ |
| List Logs | ✅ | ❌ | ❌ | ❌ |
| **Payments** | | | | |
| Estimate Cost | ✅ | ❌ | ❌ | ❌ |
| Get Run Cost | ✅ | ❌ | ❌ | ❌ |
| History | ✅ | ❌ | ❌ | ❌ |
| **SBOM** | | | | |
| Get SBOM | ✅ | ❌ | ❌ | ❌ |
| Get Packages | ✅ | ❌ | ❌ | ❌ |
| Search | ✅ | ❌ | ❌ | ❌ |
| Ingest | ✅ | ❌ | ❌ | ❌ |
| **Signatures** | | | | |
| List | ✅ | ❌ | ❌ | ❌ |
| List Verified | ✅ | ❌ | ❌ | ❌ |
| Has Verified | ✅ | ❌ | ❌ | ❌ |
| Verify | ✅ | ❌ | ❌ | ❌ |
| **State** | | | | |
| List All | ✅ | ✅ | ✅ | ✅ `bahia_list_states` |
| List Drifted | ✅ | ✅ | ❌ | ✅ `bahia_list_drifted` |
| By Environment | ✅ | ❌ | ❌ | ❌ |
| Get State | ✅ | ❌ | ❌ | ❌ |
| Record Observation | ✅ | ❌ | ❌ | ✅ `bahia_get_observation` |

---

## MCP Tool Coverage Gaps

The following operations exist in the REST API but lack MCP tools, or intentionally require signer-first Nostr flows instead of direct MCP registry writes:

1. **Service Create/Update/Delete** - Direct MCP registry writes are deprecated; use signed ContextVM/Nostr `service/*` commands.
2. **Environment Create/Update/Delete** - Direct MCP registry writes are deprecated; use signed ContextVM/Nostr `environment/*` commands.
3. **Policy CRUD** - No MCP tools for policies
4. **Secrets CRUD** - No MCP tools for secrets
5. **Notification CRUD** - No MCP tools for notifications
6. **Payment Operations** - No MCP tools for payments
7. **SBOM Operations** - No MCP tools for SBOM
8. **Signature Operations** - No MCP tools for signatures
9. **Build Registration** - No MCP tool to register builds
10. **Artifact Registration** - No MCP tool to register artifacts
11. **Run Management** - No tools to create/complete runs

### Recommended New MCP Tools

```go
// Service/environment mutations are intentionally omitted from new direct-write recommendations.
// Use signer-first ContextVM/Nostr methods: service/create, service/update, service/delete,
// environment/create, environment/update, environment/delete.

// Policy operations
"bahia_list_policies"
"bahia_get_policy"
"bahia_create_policy"
"bahia_update_policy"
"bahia_delete_policy"
"bahia_evaluate_policy"

// Secret operations
"bahia_list_secrets"
"bahia_create_secret"
"bahia_update_secret"
"bahia_delete_secret"

// Build/Artifact operations
"bahia_register_build"
"bahia_register_artifact"

// Run operations
"bahia_create_run"
"bahia_complete_run"
"bahia_get_run_logs"

// Notification operations
"bahia_list_notification_channels"
"bahia_create_notification_channel"
"bahia_test_notification_channel"
```

---

## Success Metrics

1. **API Coverage**: 100% of REST endpoints exposed in web UI
2. **MCP Coverage**: 100% of operations available as MCP tools
3. **Test Coverage**: >80% for critical paths
4. **Performance**: <200ms page load, <100ms API response
5. **Accessibility**: WCAG 2.1 AA compliance
6. **Error Rate**: <0.1% unhandled errors

---

## Dependencies & Prerequisites

1. **Backend Auth**: NIP-98-only HTTP auth enabled for protected API/MCP routes
2. **Nostr Relays**: Reliable relay infrastructure for Soul Factory
3. **Nostr sidecar stability**: relay read-model bootstrap reaches EOSE and live subscriptions stay connected
4. **Design System**: Finalize color palette, typography, spacing

---

## Open Questions

1. Which Nostr signing methods beyond NIP-07/NIP-46 should the web app support for operator workflows?
2. What level of RBAC is needed (admin, developer, viewer)?
3. Should secrets have rotation/expiration policies?
4. Do we need audit logging in the UI?
5. Should MCP tools require different auth than REST API?

---

## Appendix: File Structure Target

```
web/src/
├── lib/
│   ├── api/
│   │   ├── client.js          # Full API client
│   │   ├── types.ts           # TypeScript types
│   │   └── errors.js          # Error classes
│   ├── components/
│   │   ├── forms/             # Form components
│   │   │   ├── Input.svelte
│   │   │   ├── Select.svelte
│   │   │   ├── Textarea.svelte
│   │   │   └── FormField.svelte
│   │   ├── modals/            # Modal components
│   │   │   ├── Modal.svelte
│   │   │   ├── ConfirmDialog.svelte
│   │   │   └── SlideOver.svelte
│   │   ├── feedback/          # Feedback components
│   │   │   ├── Toast.svelte
│   │   │   ├── LoadingSpinner.svelte
│   │   │   └── ErrorBoundary.svelte
│   │   └── data/              # Data display
│   │       ├── Table.svelte
│   │       ├── Card.svelte
│   │       ├── Badge.svelte
│   │       └── EmptyState.svelte
│   ├── stores/
│   │   ├── auth.js            # Authentication state
│   │   ├── controlplane.svelte.js # Nostr read-model connection state
│   │   ├── services.js        # Service CRUD
│   │   ├── environments.js    # Environment CRUD
│   │   ├── deployments.js     # Deployment workflow
│   │   ├── policies.js        # Policy management
│   │   └── notifications.js   # Toast notifications
│   ├── nostr/
│   │   ├── client.js          # Nostr relay client
│   │   ├── nip07.js           # Browser extension
│   │   └── nip46.js           # Nostr Connect
│   └── utils/
│       ├── validation.js      # Zod schemas
│       └── formatting.js      # Date, number formatters
├── routes/
│   ├── (auth)/                # Auth-protected routes
│   │   ├── services/
│   │   │   ├── +page.svelte   # List + Create
│   │   │   ├── [id]/
│   │   │   │   ├── +page.svelte    # Detail + Edit
│   │   │   │   └── deploy/+page.svelte
│   │   ├── environments/
│   │   │   ├── +page.svelte
│   │   │   └── [id]/+page.svelte
│   │   ├── deployments/
│   │   │   ├── +page.svelte   # History
│   │   │   ├── pending/+page.svelte
│   │   │   └── [id]/+page.svelte
│   │   ├── policies/
│   │   │   ├── +page.svelte
│   │   │   └── [id]/+page.svelte
│   │   ├── workers/
│   │   │   ├── +page.svelte
│   │   │   └── [pubkey]/+page.svelte
│   │   ├── settings/
│   │   │   ├── notifications/+page.svelte
│   │   │   └── +page.svelte
│   │   └── souls/
│   │       ├── +page.svelte
│   │       ├── new/+page.svelte
│   │       └── [id]/+page.svelte
│   ├── login/+page.svelte
│   ├── +page.svelte           # Dashboard
│   └── +layout.svelte
└── tests/
    ├── unit/
    ├── integration/
    └── e2e/
```
