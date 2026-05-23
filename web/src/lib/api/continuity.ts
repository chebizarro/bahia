import type { ContinuityServiceStatusDTO } from '$lib/types/continuity';

type Fetcher = typeof fetch;

type APIResponse<T> = {
  data?: T;
  error?: string;
  message?: string;
};

const CONTINUITY_STATUS_PATH = '/api/continuity/status';

export async function fetchContinuityStatus(fetcher: Fetcher = fetch): Promise<ContinuityServiceStatusDTO[]> {
  const response = await fetcher(CONTINUITY_STATUS_PATH, {
    headers: { Accept: 'application/json' }
  });

  let body: APIResponse<ContinuityServiceStatusDTO[]> | null = null;
  const contentType = response.headers.get('content-type') || '';
  if (contentType.includes('application/json')) {
    body = await response.json();
  }

  if (!response.ok) {
    throw new Error(body?.error || `Continuity status request failed with HTTP ${response.status}`);
  }
  if (body?.error) {
    throw new Error(body.error);
  }
  return Array.isArray(body?.data) ? body.data : [];
}
