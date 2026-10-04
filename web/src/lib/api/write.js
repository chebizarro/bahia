import { signHttpRequest } from '$lib/stores/auth.js';

// REST mutation compatibility until these operations have signed-intent handlers.
export async function authorizedWrite(path, method, payload) {
  const url = `/api/v1${path}`;
  const headers = { 'Content-Type': 'application/json' };
  const authorization = await signHttpRequest({ method, url });
  if (authorization) headers.Authorization = authorization;
  const response = await fetch(url, { method, headers, ...(payload === undefined ? {} : { body: JSON.stringify(payload) }) });
  const body = await response.json().catch(() => null);
  if (!response.ok || body?.error) throw new Error(body?.error || `HTTP ${response.status}: ${response.statusText}`);
  return body?.data ?? null;
}
