import { describe, it, expect, beforeEach } from 'vitest';

/**
 * Acceptance test for the per-worker Loom job projection.
 *
 * Regression context: the deployed worker detail page previously ignored Loom
 * job requests (kind 5100), status updates (kind 30100), and results
 * (kind 5101) even though those events arrived at the browser relay. These
 * tests replay a realistic failed-job event sequence (request -> failed
 * status -> result with exit code 1) and assert that both the subscription
 * filters and the projection surface the job.
 */
import { readModelFilters, applyControlplaneEvent, resetEventRouting } from '../../src/lib/stores/controlplane/events.svelte.js';
import {
  workerJobs,
  workerJobsForPubkey,
  isTerminalLoomJobStatus,
  resetWorkers,
  refreshWorkers
} from '../../src/lib/stores/collections/workers.svelte.js';
import {
  LOOM_JOB_REQUEST,
  LOOM_JOB_STATUS_UPDATE,
  LOOM_JOB_RESULT
} from '../../src/lib/nostr/kinds.gen.js';

const WORKER_PUBKEY = 'b'.repeat(64);
const CLIENT_PUBKEY = 'c'.repeat(64);
// Event ids mirror the relay evidence that motivated this projection.
const REQUEST_ID = '192f63e9'.padEnd(64, '0');
const STATUS_ID = '9b686ef2'.padEnd(64, '0');
const RESULT_ID = '5b479f7a'.padEnd(64, '0');

function relayEvent({ id, kind, pubkey, created_at, tags = [], content = '' }) {
  return { id, kind, pubkey, created_at, tags, content, sig: '0'.repeat(128) };
}

const jobRequestEvent = relayEvent({
  id: REQUEST_ID,
  kind: LOOM_JOB_REQUEST,
  pubkey: CLIENT_PUBKEY,
  created_at: 1770000000,
  tags: [
    ['cmd', 'bash'],
    ['args', '-c', 'set -e; echo deploy'],
    ['p', WORKER_PUBKEY]
  ]
});

const jobStatusEvent = relayEvent({
  id: STATUS_ID,
  kind: LOOM_JOB_STATUS_UPDATE,
  pubkey: WORKER_PUBKEY,
  created_at: 1770000010,
  tags: [
    ['d', REQUEST_ID],
    ['e', REQUEST_ID],
    ['p', CLIENT_PUBKEY],
    ['status', 'failed']
  ],
  content: 'job process exited with an error'
});

const jobResultEvent = relayEvent({
  id: RESULT_ID,
  kind: LOOM_JOB_RESULT,
  pubkey: WORKER_PUBKEY,
  created_at: 1770000012,
  tags: [
    ['e', REQUEST_ID],
    ['p', CLIENT_PUBKEY],
    ['success', 'false'],
    ['exit_code', '1'],
    ['duration', '12'],
    ['stdout', 'https://blossom.example/stdout-hash'],
    ['stderr', 'https://blossom.example/stderr-hash']
  ]
});

beforeEach(() => {
  resetEventRouting();
  resetWorkers();
});

describe('Loom job subscription filters', () => {
  it('subscribes to job requests, status updates, and results', () => {
    const filters = readModelFilters();
    const subscribedKinds = new Set(filters.flatMap((filter) => filter.kinds || []));
    expect(subscribedKinds.has(LOOM_JOB_REQUEST)).toBe(true);
    expect(subscribedKinds.has(LOOM_JOB_STATUS_UPDATE)).toBe(true);
    expect(subscribedKinds.has(LOOM_JOB_RESULT)).toBe(true);
  });
});

describe('per-worker Loom job projection', () => {
  it('projects a full request -> failed status -> result sequence onto the worker', () => {
    expect(applyControlplaneEvent(jobRequestEvent)).toBe(true);
    refreshWorkers();
    let jobs = workerJobsForPubkey(workerJobs, WORKER_PUBKEY);
    expect(jobs).toHaveLength(1);
    expect(jobs[0].status).toBe('queued');
    expect(jobs[0].client_pubkey).toBe(CLIENT_PUBKEY);

    expect(applyControlplaneEvent(jobStatusEvent)).toBe(true);
    refreshWorkers();
    jobs = workerJobsForPubkey(workerJobs, WORKER_PUBKEY);
    expect(jobs[0].status).toBe('failed');
    expect(jobs[0].message).toBe('job process exited with an error');

    expect(applyControlplaneEvent(jobResultEvent)).toBe(true);
    refreshWorkers();
    jobs = workerJobsForPubkey(workerJobs, WORKER_PUBKEY);
    expect(jobs).toHaveLength(1);
    const job = jobs[0];
    expect(job.job_id).toBe(REQUEST_ID);
    expect(job.result_event_id).toBe(RESULT_ID);
    expect(job.status).toBe('failed');
    expect(job.success).toBe(false);
    expect(job.exit_code).toBe(1);
    expect(job.duration_seconds).toBe(12);
    expect(job.stdout_url).toBe('https://blossom.example/stdout-hash');
    expect(job.stderr_url).toBe('https://blossom.example/stderr-hash');
    expect(isTerminalLoomJobStatus(job.status)).toBe(true);
  });

  it('projects out-of-order events (status before request) onto the same job', () => {
    expect(applyControlplaneEvent(jobStatusEvent)).toBe(true);
    expect(applyControlplaneEvent(jobRequestEvent)).toBe(true);
    refreshWorkers();
    const jobs = workerJobsForPubkey(workerJobs, WORKER_PUBKEY);
    expect(jobs).toHaveLength(1);
    expect(jobs[0].status).toBe('failed');
    expect(jobs[0].cmd).toBe('bash');
  });

  it('does not let a late status update regress a terminal result', () => {
    expect(applyControlplaneEvent(jobRequestEvent)).toBe(true);
    expect(applyControlplaneEvent(jobResultEvent)).toBe(true);
    const lateRunning = relayEvent({
      id: 'd'.repeat(64),
      kind: LOOM_JOB_STATUS_UPDATE,
      pubkey: WORKER_PUBKEY,
      created_at: 1770000005,
      tags: [
        ['d', REQUEST_ID],
        ['e', REQUEST_ID],
        ['p', CLIENT_PUBKEY],
        ['status', 'running']
      ]
    });
    applyControlplaneEvent(lateRunning);
    refreshWorkers();
    const jobs = workerJobsForPubkey(workerJobs, WORKER_PUBKEY);
    expect(jobs[0].status).toBe('failed');
    expect(jobs[0].exit_code).toBe(1);
  });

  it('ignores job events for other workers when filtering by pubkey', () => {
    applyControlplaneEvent(jobRequestEvent);
    applyControlplaneEvent(jobResultEvent);
    refreshWorkers();
    expect(workerJobsForPubkey(workerJobs, 'f'.repeat(64))).toHaveLength(0);
  });
});
