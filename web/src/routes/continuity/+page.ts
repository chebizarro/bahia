import { fetchContinuityStatus, fetchContinuityTopology } from '$lib/api/continuity';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ fetch }) => {
  const [statusResult, topologyResult] = await Promise.allSettled([
    fetchContinuityStatus(fetch),
    fetchContinuityTopology(fetch)
  ]);

  const statuses = statusResult.status === 'fulfilled' ? statusResult.value : [];
  const assessments = topologyResult.status === 'fulfilled' ? topologyResult.value : [];
  const errors = [statusResult, topologyResult]
    .filter((result) => result.status === 'rejected')
    .map((result) => (result.reason instanceof Error ? result.reason.message : 'Failed to load continuity data'));

  return {
    statuses,
    assessments,
    error: errors.length > 0 ? errors.join('; ') : null
  };
};
