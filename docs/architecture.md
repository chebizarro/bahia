# Bahia Architecture

## Overview

Bahia is the central coordinator for your deployment pipeline. It tracks builds, artifacts, deployments, and monitors what's actually running to detect when things drift out of sync.

**Core responsibilities:**
- Register and track builds and artifacts from CI systems
- Manage deployment intents (requests to deploy) with optional approvals
- Dispatch deployment jobs to Loom workers
- Observe running containers and detect drift
- Publish signed events to Nostr for audit trail

## Components

### API Server
REST API built with chi router. Handles all CRUD operations for services, environments, builds, artifacts, deployments, and state queries.

- JSON request/response format
- Health (`/health`) and readiness (`/ready`) endpoints
- Middleware: logging, recovery, CORS

### Service Layer
The core business logic:

- **Artifact Service** — Registers builds and artifacts, tracks metadata
- **Deployment Service** — Creates intents, manages approval workflows, triggers runs
- **State Service** — Tracks desired vs observed state, calculates drift
- **Rollback Service** — Creates rollback intents from successful prior deployments

### Workflow Coordinator
Bridges Bahia to Loom workers via Nostr:

- Publishes job requests (Kind 5100) to deploy containers
- Subscribes to job status updates (Kind 30100) and results (Kind 5101)
- Supports job cancellation (Kind 5102)
- Maps Loom job states to internal deployment run status

### Adapters

| Adapter | Purpose |
|---------|---------|
| **Harbor** | Resolves image tags to digests, verifies image existence |
| **Nostr** | Publishes audit events, subscribes to job updates |
| **Docker** | Queries running containers for drift detection |
| **Cashu** | Handles payments to workers (in development) |
| **Signet** | NIP-46 bunker integration for key management |

### Reconciler
Background worker that continuously compares desired state (what should be running) with observed state (what is running):

- Queries Docker Engine API on target hosts
- Updates drift status in the database
- Emits drift detection events

### Persistence
- PostgreSQL 16+ via pgx connection pool
- Embedded SQL migrations (run on server start)
- Repository pattern for data access

## Data Flow

```
1. Build completes in CI
   ↓
2. CI registers build + artifact in Bahia
   ↓
3. User/automation creates deployment intent
   ↓
4. (Optional) Intent approved
   ↓
5. Bahia creates deployment run, publishes job to Loom
   ↓
6. Loom worker executes: docker pull && docker run
   ↓
7. Worker publishes result, Bahia marks run complete
   ↓
8. Bahia updates desired state
   ↓
9. Reconciler observes actual containers
   ↓
10. Drift detected if mismatch
```

## Source of Truth

| Concern | Where it lives |
|---------|----------------|
| Container images | Harbor registry |
| Desired state | PostgreSQL |
| Observed state | PostgreSQL (from Docker queries) |
| Workflow history | PostgreSQL |
| Audit trail | Nostr relays (signed events) |

## Key Design Decisions

1. **Nostr for coordination** — Job dispatch and audit use Nostr events, making the system observable and decentralized
2. **Intent-based deployments** — Separates "I want to deploy X" from "actually deploy X", enabling approvals and auditability
3. **Drift detection** — Continuously verifies actual matches desired, catching manual changes or failures
4. **Rollback as first-class** — Rollbacks create new intents referencing prior successful artifacts, maintaining full history
