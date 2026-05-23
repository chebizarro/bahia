import type { ContinuityAssessmentDTO, ContinuityServiceStatusDTO } from '$lib/types/continuity';

type Fetcher = typeof fetch;

type APIResponse<T> = {
  data?: T;
  error?: string;
  message?: string;
};

const CONTINUITY_STATUS_PATH = '/api/continuity/status';
const CONTINUITY_TOPOLOGY_PATH = '/api/continuity/topology';
const CONTINUITY_SIMULATE_PATH = '/api/continuity/simulate';

export async function fetchContinuityStatus(fetcher: Fetcher = fetch): Promise<ContinuityServiceStatusDTO[]> {
  return requestContinuity<ContinuityServiceStatusDTO[]>(CONTINUITY_STATUS_PATH, fetcher, 'Continuity status');
}

export async function fetchContinuityTopology(fetcher: Fetcher = fetch): Promise<ContinuityAssessmentDTO[]> {
  return requestContinuity<ContinuityAssessmentDTO[]>(CONTINUITY_TOPOLOGY_PATH, fetcher, 'Continuity topology');
}

export async function simulateWorkerFailure(
  workerPubKey: string,
  fetcher: Fetcher = fetch
): Promise<ContinuityAssessmentDTO[]> {
  return requestContinuity<ContinuityAssessmentDTO[]>(CONTINUITY_SIMULATE_PATH, fetcher, 'Continuity simulation', {
    method: 'POST',
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/json'
    },
    body: JSON.stringify({ worker_pubkey: workerPubKey })
  });
}

async function requestContinuity<T>(
  path: string,
  fetcher: Fetcher,
  label: string,
  init: RequestInit = { headers: { Accept: 'application/json' } }
): Promise<T> {
  const response = await fetcher(path, init);

  let body: APIResponse<T> | null = null;
  const contentType = response.headers.get('content-type') || '';
  if (contentType.includes('application/json')) {
    body = await response.json();
  }

  if (!response.ok) {
    throw new Error(body?.error || `${label} request failed with HTTP ${response.status}`);
  }
  if (body?.error) {
    throw new Error(body.error);
  }
  return Array.isArray(body?.data) ? (body.data as T) : ([] as T);
}
