export const DEFAULT_DEPLOY_ESTIMATED_DURATION_SECS = 300;

function parsePositiveInteger(value, fallback = DEFAULT_DEPLOY_ESTIMATED_DURATION_SECS) {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) return fallback;
  return Math.round(parsed);
}

function isSatUnit(unit) {
  const value = String(unit || 'sat').toLowerCase();
  return value === 'sat' || value === 'sats';
}

function firstPricedTier(worker) {
  const pricing = Array.isArray(worker?.pricing) ? worker.pricing : [];
  return pricing.find((tier) => {
    const price = Number(tier?.price_per_second ?? tier?.price_per_sec);
    return Number.isFinite(price) && price >= 0 && isSatUnit(tier?.unit);
  }) || null;
}

export function isValidEstimatedDurationSecs(value) {
  const parsed = Number(value);
  return Number.isInteger(parsed) && parsed > 0;
}

export function normalizeEstimatedDurationSecs(value) {
  return parsePositiveInteger(value);
}

export function formatDurationSecs(value) {
  const seconds = parsePositiveInteger(value);
  if (seconds < 60) return `${seconds}s`;

  const minutes = Math.floor(seconds / 60);
  const remainder = seconds % 60;
  if (minutes < 60) return remainder ? `${minutes}m ${remainder}s` : `${minutes}m`;

  const hours = Math.floor(minutes / 60);
  const minuteRemainder = minutes % 60;
  return minuteRemainder ? `${hours}h ${minuteRemainder}m` : `${hours}h`;
}

export function formatSats(value) {
  const sats = Number(value);
  if (!Number.isFinite(sats)) return '—';
  return `${Math.round(sats).toLocaleString()} sat`;
}

export function formatPricePerSecond(value, unit = 'sat') {
  const price = Number(value);
  if (!Number.isFinite(price)) return 'Not available';
  return `${price.toLocaleString()} ${unit || 'sat'}/sec`;
}

export function workerDisplayName(worker) {
  if (!worker) return 'Unknown worker';
  if (worker.name) return worker.name;
  const pubkey = worker.pubkey || worker.pub_key;
  if (!pubkey) return 'Unnamed worker';
  return String(pubkey).length > 16 ? `${String(pubkey).slice(0, 12)}…${String(pubkey).slice(-6)}` : String(pubkey);
}

export function buildWorkerCostEstimates(workers, estimatedDurationSecs = DEFAULT_DEPLOY_ESTIMATED_DURATION_SECS) {
  const requestedSecs = normalizeEstimatedDurationSecs(estimatedDurationSecs);

  return (Array.isArray(workers) ? workers : [])
    .map((worker) => {
      const tier = firstPricedTier(worker);
      if (!tier) return null;

      const pricePerSecond = Number(tier.price_per_second ?? tier.price_per_sec);
      const unit = 'sat';
      const minDurationSecs = Number(worker?.min_duration_secs || 0);
      const billableSecs = Math.max(requestedSecs, Number.isFinite(minDurationSecs) ? minDurationSecs : 0);
      const estimatedCostSats = Math.round(pricePerSecond * billableSecs);

      return {
        worker_pubkey: worker.pubkey || worker.pub_key || '',
        worker_name: workerDisplayName(worker),
        mint_url: tier.mint_url || '',
        price_per_second: pricePerSecond,
        unit,
        estimated_secs: requestedSecs,
        billable_secs: billableSecs,
        min_duration_secs: Number.isFinite(minDurationSecs) ? minDurationSecs : 0,
        estimated_cost_sats: estimatedCostSats
      };
    })
    .filter(Boolean)
    .sort((a, b) => a.estimated_cost_sats - b.estimated_cost_sats);
}

export function summarizeDeploymentCostEstimates(workers, estimatedDurationSecs = DEFAULT_DEPLOY_ESTIMATED_DURATION_SECS) {
  const estimates = buildWorkerCostEstimates(workers, estimatedDurationSecs);
  if (estimates.length === 0) {
    return {
      estimated_secs: normalizeEstimatedDurationSecs(estimatedDurationSecs),
      available_workers: 0,
      estimates: [],
      cheapest: null,
      min_cost_sats: 0,
      max_cost_sats: 0,
      average_cost_sats: 0
    };
  }

  const costs = estimates.map((estimate) => estimate.estimated_cost_sats);
  const total = costs.reduce((sum, cost) => sum + cost, 0);

  return {
    estimated_secs: estimates[0].estimated_secs,
    available_workers: estimates.length,
    estimates,
    cheapest: estimates[0],
    min_cost_sats: costs[0],
    max_cost_sats: costs[costs.length - 1],
    average_cost_sats: Math.round(total / estimates.length)
  };
}
