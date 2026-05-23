import { fetchContinuityStatus } from '$lib/api/continuity';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ fetch }) => {
  try {
    return {
      statuses: await fetchContinuityStatus(fetch),
      error: null
    };
  } catch (error) {
    return {
      statuses: [],
      error: error instanceof Error ? error.message : 'Failed to load continuity status'
    };
  }
};
