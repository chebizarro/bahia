import type { ContinuityAssessmentDTO, ContinuityServiceStatusDTO } from '$lib/types/continuity';

type Fetcher = typeof fetch;

const CONTINUITY_STATUS_PATH = '/api/continuity/status';
const CONTINUITY_TOPOLOGY_PATH = '/api/continuity/topology';
const CONTINUITY_SIMULATE_PATH = '/api/continuity/simulate';

export async function fetchContinuityStatus(fetcher: Fetcher = fetch): Promise<ContinuityServiceStatusDTO[]> {
  const response = await fetcher(CONTINUITY_STATUS_PATH, { headers: { Accept: 'application/json' } });
  if (!response.ok) throw new Error(`Continuity status request failed: HTTP ${response.status}`);
  const body = await response.json();
  return Array.isArray(body?.data) ? body.data : [];
}

export async function fetchContinuityTopology(fetcher: Fetcher = fetch): Promise<ContinuityAssessmentDTO[]> {
  const response = await fetcher(CONTINUITY_TOPOLOGY_PATH, { headers: { Accept: 'application/json' } });
  if (!response.ok) throw new Error(`Continuity topology request failed: HTTP ${response.status}`);
  const body = await response.json();
  return Array.isArray(body?.data) ? body.data : [];
}

export async function simulateWorkerFailure(
  workerPubKey: string,
  fetcher: Fetcher = fetch
): Promise<ContinuityAssessmentDTO[]> {
  const response = await fetcher(CONTINUITY_SIMULATE_PATH, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify({ worker_pubkey: workerPubKey })
  });
  if (!response.ok) throw new Error(`Continuity simulation request failed: HTTP ${response.status}`);
  const body = await response.json();
  return Array.isArray(body?.data) ? body.data : [];
}
