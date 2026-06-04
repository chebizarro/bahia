import { loadContinuityDashboardFromNostr } from '$lib/nostr/continuity';
import type { PageLoad } from './$types';

export const ssr = false;

export const load: PageLoad = async () => {
  try {
    const { statuses, assessments, events } = await loadContinuityDashboardFromNostr();
    return {
      statuses,
      assessments,
      continuityEvents: events,
      error: null
    };
  } catch (caught) {
    return {
      statuses: [],
      assessments: [],
      continuityEvents: [],
      error: caught instanceof Error ? caught.message : 'Failed to load continuity data from Nostr relays'
    };
  }
};
