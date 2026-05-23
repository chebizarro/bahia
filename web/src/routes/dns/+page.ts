import type { PageLoad } from './$types';

type APIResponse<T> = {
  data?: T;
  error?: string;
  message?: string;
};

const DNS_PATHS = {
  zones: '/api/v1/dns/zones',
  endpoints: '/api/v1/dns/catalog',
  drift: '/api/v1/dns/drift',
  policies: '/api/v1/dns/policies'
};

async function requestList<T>(fetcher: typeof fetch, path: string, label: string, listKey: string): Promise<T[]> {
  const response = await fetcher(path, { headers: { Accept: 'application/json' } });

  let body: APIResponse<T[] | Record<string, T[]>> | null = null;
  const contentType = response.headers.get('content-type') || '';
  if (contentType.includes('application/json')) {
    body = await response.json();
  }

  if (!response.ok) {
    throw new Error(body?.error || body?.message || `${label} request failed with HTTP ${response.status}`);
  }
  if (body?.error) {
    throw new Error(body.error);
  }

  const payload = body?.data ?? body;
  if (Array.isArray(payload)) return payload as T[];
  if (payload && typeof payload === 'object' && Array.isArray((payload as Record<string, T[]>)[listKey])) {
    return (payload as Record<string, T[]>)[listKey];
  }
  return [];
}

export const load: PageLoad = async ({ fetch }) => {
  const [zonesResult, endpointsResult, driftResult, policiesResult] = await Promise.allSettled([
    requestList<Record<string, unknown>>(fetch, DNS_PATHS.zones, 'DNS zones', 'zones'),
    requestList<Record<string, unknown>>(fetch, DNS_PATHS.endpoints, 'DNS catalog', 'endpoints'),
    requestList<Record<string, unknown>>(fetch, DNS_PATHS.drift, 'DNS drift', 'drift'),
    requestList<Record<string, unknown>>(fetch, DNS_PATHS.policies, 'DNS policies', 'policies')
  ]);

  const errors = [zonesResult, endpointsResult, driftResult, policiesResult]
    .filter((result) => result.status === 'rejected')
    .map((result) => (result.reason instanceof Error ? result.reason.message : 'Failed to load DNS data'));

  return {
    zones: zonesResult.status === 'fulfilled' ? zonesResult.value : [],
    endpoints: endpointsResult.status === 'fulfilled' ? endpointsResult.value : [],
    driftEvents: driftResult.status === 'fulfilled' ? driftResult.value : [],
    policies: policiesResult.status === 'fulfilled' ? policiesResult.value : [],
    error: errors.length > 0 ? errors.join('; ') : null
  };
};
